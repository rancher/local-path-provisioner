package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
	"github.com/urfave/cli"

	pvController "github.com/kubernetes-incubator/external-storage/lib/controller"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	VERSION = "0.0.1"

	FlagConfigFile         = "config"
	FlagProvisionerName    = "provisioner-name"
	EnvProvisionerName     = "PROVISIONER_NAME"
	DefaultProvisionerName = "rancher.io/local-path"
	FlagNamespace          = "namespace"
	EnvNamespace           = "POD_NAMESPACE"
	DefaultNamespace       = "local-path-storage"
	FlagHelperImage        = "helper-image"
	EnvHelperImage         = "HELPER_IMAGE"
	DefaultHelperImage     = "busybox"
)

func cmdNotFound(c *cli.Context, command string) {
	panic(fmt.Errorf("Unrecognized command: %s", command))
}

func onUsageError(c *cli.Context, err error, isSubcommand bool) error {
	panic(fmt.Errorf("Usage error, please check your command"))
}

func RegisterShutdownChannel(done chan struct{}) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigs
		logrus.Infof("Receive %v to exit", sig)
		close(done)
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
		},
		Action: func(c *cli.Context) {
			if err := startDaemon(c); err != nil {
				logrus.Fatalf("Error starting daemon: %v", err)
			}
		},
	}
}

func startDaemon(c *cli.Context) error {
	stopCh := make(chan struct{})
	RegisterShutdownChannel(stopCh)

	config, err := rest.InClusterConfig()
	if err != nil {
		return errors.Wrap(err, "unable to get client config")
	}

	kubeClient, err := clientset.NewForConfig(config)
	if err != nil {
		return errors.Wrap(err, "unable to get k8s client")
	}

	serverVersion, err := kubeClient.Discovery().ServerVersion()
	if err != nil {
		return errors.Wrap(err, "Cannot start Provisioner: failed to get Kubernetes server version")
	}

	configFile := c.String(FlagConfigFile)
	if configFile == "" {
		return fmt.Errorf("invalid empty flag %v", FlagConfigFile)
	}
	provisionerName := c.String(FlagProvisionerName)
	if provisionerName == "" {
		return fmt.Errorf("invalid empty flag %v", FlagProvisionerName)
	}
	namespace := c.String(FlagNamespace)
	if namespace == "" {
		return fmt.Errorf("invalid empty flag %v", FlagNamespace)
	}
	helperImage := c.String(FlagHelperImage)
	if helperImage == "" {
		return fmt.Errorf("invalid empty flag %v", FlagHelperImage)
	}

	provisioner, err := NewProvisioner(stopCh, kubeClient, configFile, namespace, helperImage)
	if err != nil {
		return err
	}
	pc := pvController.NewProvisionController(
		kubeClient,
		provisionerName,
		provisioner,
		serverVersion.GitVersion,
	)
	logrus.Debug("Provisioner started")
	pc.Run(stopCh)
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
