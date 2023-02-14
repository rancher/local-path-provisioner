package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
	"github.com/urfave/cli"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	pvController "sigs.k8s.io/sig-storage-lib-external-provisioner/v8/controller"
)

var (
	VERSION                       = "0.0.1"
	FlagConfigFile                = "config"
	FlagProvisionerName           = "provisioner-name"
	EnvProvisionerName            = "PROVISIONER_NAME"
	DefaultProvisionerName        = "rancher.io/local-path"
	FlagNamespace                 = "namespace"
	EnvNamespace                  = "POD_NAMESPACE"
	DefaultNamespace              = "local-path-storage"
	FlagHelperImage               = "helper-image"
	EnvHelperImage                = "HELPER_IMAGE"
	DefaultHelperImage            = "rancher/library-busybox:1.32.1"
	FlagServiceAccountName        = "service-account-name"
	DefaultServiceAccount         = "local-path-provisioner-service-account"
	EnvServiceAccountName         = "SERVICE_ACCOUNT_NAME"
	FlagKubeconfig                = "kubeconfig"
	DefaultConfigFileKey          = "config.json"
	DefaultConfigMapName          = "local-path-config"
	FlagConfigMapName             = "configmap-name"
	FlagHelperPodFile             = "helper-pod-file"
	DefaultHelperPodFile          = "helperPod.yaml"
	FlagWorkerThreads             = "worker-threads"
	DefaultWorkerThreads          = pvController.DefaultThreadiness
	FlagProvisioningRetryCount    = "provisioning-retry-count"
	DefaultProvisioningRetryCount = pvController.DefaultFailedProvisionThreshold
	FlagDeletionRetryCount        = "deletion-retry-count"
	DefaultDeletionRetryCount     = pvController.DefaultFailedDeleteThreshold
)

func cmdNotFound(c *cli.Context, command string) {
	panic(fmt.Errorf("Unrecognized command: %s", command))
}

func onUsageError(c *cli.Context, err error, isSubcommand bool) error {
	panic(fmt.Errorf("Usage error, please check your command"))
}

func RegisterShutdownChannel(cancelFn context.CancelFunc) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		klog.Infof("Receive %v to exit", sig)
		cancelFn()
	}()
}

func StartCmd() cli.Command {
	return cli.Command{
		Name: "start",
		Flags: []cli.Flag{
			cli.StringFlag{
				Name:  FlagConfigFile,
				Usage: "Required. Provisioner configuration file.",
				Value: "",
			},
			cli.StringFlag{
				Name:   FlagProvisionerName,
				Usage:  "Required. Specify Provisioner name.",
				EnvVar: EnvProvisionerName,
				Value:  DefaultProvisionerName,
			},
			cli.StringFlag{
				Name:   FlagNamespace,
				Usage:  "Required. The namespace that Provisioner is running in",
				EnvVar: EnvNamespace,
				Value:  DefaultNamespace,
			},
			cli.StringFlag{
				Name:   FlagHelperImage,
				Usage:  "Required. The helper image used for create/delete directories on the host",
				EnvVar: EnvHelperImage,
				Value:  DefaultHelperImage,
			},
			cli.StringFlag{
				Name:  FlagKubeconfig,
				Usage: "Paths to a kubeconfig. Only required when it is out-of-cluster.",
				Value: "",
			},
			cli.StringFlag{
				Name:  FlagConfigMapName,
				Usage: "Required. Specify configmap name.",
				Value: DefaultConfigMapName,
			},
			cli.StringFlag{
				Name:   FlagServiceAccountName,
				Usage:  "Required. The ServiceAccountName for deployment",
				EnvVar: EnvServiceAccountName,
				Value:  DefaultServiceAccount,
			},
			cli.StringFlag{
				Name:  FlagHelperPodFile,
				Usage: "Paths to the Helper pod yaml file",
				Value: "",
			},
			cli.IntFlag{
				Name:  FlagWorkerThreads,
				Usage: "Number of provisioner worker threads.",
				Value: DefaultWorkerThreads,
			},
			cli.IntFlag{
				Name:  FlagProvisioningRetryCount,
				Usage: "Number of retries of failed volume provisioning. 0 means retry indefinitely.",
				Value: DefaultProvisioningRetryCount,
			},
			cli.IntFlag{
				Name:  FlagDeletionRetryCount,
				Usage: "Number of retries of failed volume deletion. 0 means retry indefinitely.",
				Value: DefaultDeletionRetryCount,
			},
		},
		Action: func(c *cli.Context) {
			if err := startDaemon(c); err != nil {
				logrus.Fatalf("Error starting daemon: %v", err)
			}
		},
	}
}

func homeDir() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return os.Getenv("USERPROFILE") // windows
}

func loadConfig(kubeconfig string) (*rest.Config, error) {
	if len(kubeconfig) > 0 {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}

	kubeconfigPath := os.Getenv(clientcmd.RecommendedConfigPathEnvVar)
	if len(kubeconfigPath) > 0 {
		envVarFiles := filepath.SplitList(kubeconfigPath)
		for _, f := range envVarFiles {
			if _, err := os.Stat(f); err == nil {
				return clientcmd.BuildConfigFromFlags("", f)
			}
		}
	}

	if c, err := rest.InClusterConfig(); err == nil {
		return c, nil
	}

	kubeconfig = filepath.Join(homeDir(), clientcmd.RecommendedHomeDir, clientcmd.RecommendedFileName)
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}

func findConfigFileFromConfigMap(kubeClient clientset.Interface, namespace, configMapName, key string) (string, error) {
	cm, err := kubeClient.CoreV1().ConfigMaps(namespace).Get(context.TODO(), configMapName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}
	value, ok := cm.Data[key]
	if !ok {
		return "", fmt.Errorf("%v is not exist in local-path-config ConfigMap", key)
	}
	return value, nil
}

func startDaemon(c *cli.Context) error {
	ctx, cancelFn := context.WithCancel(context.TODO())
	RegisterShutdownChannel(cancelFn)

	config, err := loadConfig(c.String(FlagKubeconfig))
	if err != nil {
		return errors.Wrap(err, "unable to get client config")
	}

	kubeClient, err := clientset.NewForConfig(config)
	if err != nil {
		return errors.Wrap(err, "unable to get k8s client")
	}

	provisionerName := c.String(FlagProvisionerName)
	if provisionerName == "" {
		return fmt.Errorf("invalid empty flag %v", FlagProvisionerName)
	}
	namespace := c.String(FlagNamespace)
	if namespace == "" {
		return fmt.Errorf("invalid empty flag %v", FlagNamespace)
	}
	configMapName := c.String(FlagConfigMapName)
	if configMapName == "" {
		return fmt.Errorf("invalid empty flag %v", FlagConfigMapName)
	}
	configFile := c.String(FlagConfigFile)
	if configFile == "" {
		configFile, err = findConfigFileFromConfigMap(kubeClient, namespace, configMapName, DefaultConfigFileKey)
		if err != nil {
			return fmt.Errorf("invalid empty flag %v and it also does not exist at ConfigMap %v/%v with err: %v", FlagConfigFile, namespace, configMapName, err)
		}
	}
	helperImage := c.String(FlagHelperImage)
	if helperImage == "" {
		return fmt.Errorf("invalid empty flag %v", FlagHelperImage)
	}

	serviceAccountName := c.String(FlagServiceAccountName)
	if serviceAccountName == "" {
		return fmt.Errorf("invalid empty flag %v", FlagServiceAccountName)
	}

	// if helper pod file is not specified, then find the helper pod by configmap with key = helperPod.yaml
	// if helper pod file is specified with flag FlagHelperPodFile, then load the file
	helperPodFile := c.String(FlagHelperPodFile)
	helperPodYaml := ""
	if helperPodFile == "" {
		helperPodYaml, err = findConfigFileFromConfigMap(kubeClient, namespace, configMapName, DefaultHelperPodFile)
		if err != nil {
			return fmt.Errorf("invalid empty flag %v and it also does not exist at ConfigMap %v/%v with err: %v", FlagHelperPodFile, namespace, configMapName, err)
		}
	} else {
		helperPodYaml, err = loadFile(helperPodFile)
		if err != nil {
			return fmt.Errorf("could not open file %v with err: %v", helperPodFile, err)
		}
	}

	provisioningRetryCount := c.Int(FlagProvisioningRetryCount)
	if provisioningRetryCount < 0 {
		return fmt.Errorf("invalid negative integer flag %v", FlagProvisioningRetryCount)
	}

	deletionRetryCount := c.Int(FlagDeletionRetryCount)
	if deletionRetryCount < 0 {
		return fmt.Errorf("invalid negative integer flag %v", FlagDeletionRetryCount)
	}

	workerThreads := c.Int(FlagWorkerThreads)
	if workerThreads <= 0 {
		return fmt.Errorf("invalid zero or negative integer flag %v", FlagWorkerThreads)
	}

	provisioner, err := NewProvisioner(ctx, kubeClient, configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml)
	if err != nil {
		return err
	}
	pc := pvController.NewProvisionController(
		kubeClient,
		provisionerName,
		provisioner,
		pvController.LeaderElection(false),
		pvController.FailedProvisionThreshold(provisioningRetryCount),
		pvController.FailedDeleteThreshold(deletionRetryCount),
		pvController.Threadiness(workerThreads),
	)
	logrus.Debug("Provisioner started")
	pc.Run(ctx)
	logrus.Debug("Provisioner stopped")
	return nil
}

func main() {
	logrus.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})

	a := cli.NewApp()
	a.Version = VERSION
	a.Usage = "Local Path Provisioner"

	a.Before = func(c *cli.Context) error {
		if c.GlobalBool("debug") {
			logrus.SetLevel(logrus.DebugLevel)
		}
		return nil
	}

	a.Flags = []cli.Flag{
		cli.BoolFlag{
			Name:   "debug, d",
			Usage:  "enable debug logging level",
			EnvVar: "RANCHER_DEBUG",
		},
	}
	a.Commands = []cli.Command{
		StartCmd(),
	}
	a.CommandNotFound = cmdNotFound
	a.OnUsageError = onUsageError

	if err := a.Run(os.Args); err != nil {
		logrus.Fatalf("Critical error: %v", err)
	}
}
