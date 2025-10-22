package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

type ActionType string

const (
	ActionTypeCreate = "create"
	ActionTypeDelete = "delete"
)

const (
	KeyNode      = "kubernetes.io/hostname"
	KeyNodeField = "metadata.name"

	NodeDefaultNonListedNodes = "DEFAULT_PATH_FOR_NON_LISTED_NODES"

	helperScriptDir     = "/script"
	helperDataVolName   = "data"
	helperScriptVolName = "script"

	envVolDir  = "VOL_DIR"
	envVolMode = "VOL_MODE"
	envVolSize = "VOL_SIZE_BYTES"
)

const (
	defaultCmdTimeoutSeconds = 120
	defaultVolumeType        = "hostPath"
)

const (
	nodeNameAnnotationKey = "local.path.provisioner/selected-node"
)

var (
	ConfigFileCheckInterval = 30 * time.Second

	HelperPodNameMaxLength = 128
)

type LocalPathCsiController struct {
	ctx                context.Context
	kubeClient         *clientset.Clientset
	namespace          string
	helperImage        string
	serviceAccountName string

	config        *Config
	configData    *ConfigData
	configFile    string
	configMapName string
	configMutex   *sync.RWMutex
	helperPod     *v1.Pod
}

type Config struct {
	CmdTimeoutSeconds int
	SetupCommand      string
	TeardownCommand   string
	ResizeCommand     string
	StorageClassConfig
	StorageClassConfigs map[string]StorageClassConfig
}

type NodePathMapData struct {
	Node  string   `json:"node,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type StorageClassConfigData struct {
	NodePathMap          []*NodePathMapData `json:"nodePathMap,omitempty"`
	SharedFileSystemPath string             `json:"sharedFileSystemPath,omitempty"`
}

type ConfigData struct {
	CmdTimeoutSeconds int    `json:"cmdTimeoutSeconds,omitempty"`
	SetupCommand      string `json:"setupCommand,omitempty"`
	TeardownCommand   string `json:"teardownCommand,omitempty"`
	ResizeCommand     string `json:"resizeCommand,omitempty"`
	StorageClassConfigData
	StorageClassConfigs map[string]StorageClassConfigData `json:"storageClassConfigs"`
}

type StorageClassConfig struct {
	NodePathMap          map[string]*NodePathMap
	SharedFileSystemPath string
}

type NodePathMap struct {
	Paths map[string]struct{}
}

type volumeOptions struct {
	Name        string
	Path        string
	Mode        v1.PersistentVolumeMode
	SizeInBytes int64
	Node        string
}

func NewCsiController(ctx context.Context, kubeClient *clientset.Clientset,
	configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml string) (*LocalPathCsiController, error) {
	csiController := &LocalPathCsiController{
		ctx: ctx,

		kubeClient:         kubeClient,
		namespace:          namespace,
		helperImage:        helperImage,
		serviceAccountName: serviceAccountName,

		// config will be updated shortly by c.refreshConfig()
		config:        nil,
		configFile:    configFile,
		configData:    nil,
		configMapName: configMapName,
		configMutex:   &sync.RWMutex{},
	}

	var err error
	csiController.helperPod, err = loadHelperPodFile(helperPodYaml)
	if err != nil {
		return nil, err
	}
	if err := csiController.refreshConfig(); err != nil {
		return nil, err
	}
	csiController.watchAndRefreshConfig()
	return csiController, nil
}

func (c *LocalPathCsiController) refreshConfig() error {
	c.configMutex.Lock()
	defer c.configMutex.Unlock()

	configData, err := loadConfigFile(c.configFile)
	if err != nil {
		return err
	}
	// no need to update
	if reflect.DeepEqual(configData, c.configData) {
		return nil
	}
	config, err := canonicalizeConfig(configData)
	if err != nil {
		return err
	}
	// only update the config if the new config file is valid
	c.configData = configData
	c.config = config

	output, err := json.Marshal(c.configData)
	if err != nil {
		return err
	}
	logrus.Debugf("Applied config: %v", string(output))

	return err
}

func (c *LocalPathCsiController) refreshHelperPod() error {
	c.configMutex.Lock()
	defer c.configMutex.Unlock()

	helperPodFile, envExists := os.LookupEnv(EnvConfigMountPath)
	if !envExists {
		return nil
	}

	helperPodFile = filepath.Join(helperPodFile, DefaultHelperPodFile)
	newHelperPod, err := loadFile(helperPodFile)
	if err != nil {
		return err
	}

	c.helperPod, err = loadHelperPodFile(newHelperPod)
	if err != nil {
		return err
	}

	return nil
}

func (c *LocalPathCsiController) watchAndRefreshConfig() {
	go func() {
		ticker := time.NewTicker(ConfigFileCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.refreshConfig(); err != nil {
					logrus.Errorf("failed to load the new config file: %v", err)
				}
				if err := c.refreshHelperPod(); err != nil {
					logrus.Errorf("failed to load the new helper pod manifest: %v", err)
				}
			case <-c.ctx.Done():
				logrus.Infof("stop watching config file")
				return
			}
		}
	}()
}

func (c *LocalPathCsiController) isSharedFilesystem(config *StorageClassConfig) (bool, error) {
	c.configMutex.RLock()
	defer c.configMutex.RUnlock()

	if c.config == nil {
		return false, fmt.Errorf("no valid config available")
	}

	if (config.SharedFileSystemPath != "") && (len(config.NodePathMap) != 0) {
		return false, fmt.Errorf("both nodePathMap and sharedFileSystemPath are defined. Please make sure only one is in use")
	}

	if len(config.NodePathMap) != 0 {
		return false, nil
	}

	if config.SharedFileSystemPath != "" {
		return true, nil
	}

	return false, fmt.Errorf("both nodePathMap and sharedFileSystemPath are unconfigured")
}

func (c *LocalPathCsiController) createHelperPod(action ActionType, cmd []string, o volumeOptions, cfg *StorageClassConfig) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to %v volume %v", action, o.Name)
	}()

	if cfg != nil {
		sharedFS, err := c.isSharedFilesystem(cfg)
		if err != nil {
			return err
		}
		if o.Name == "" || o.Path == "" || (!sharedFS && o.Node == "") {
			return fmt.Errorf("invalid empty name or path or node")
		}
	}

	if !filepath.IsAbs(o.Path) {
		return fmt.Errorf("volume path %s is not absolute", o.Path)
	}
	o.Path = filepath.Clean(o.Path)
	parentDir, volumeDir := filepath.Split(o.Path)
	hostPathType := v1.HostPathDirectoryOrCreate
	lpvVolumes := []v1.Volume{
		{
			Name: helperDataVolName,
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: parentDir,
					Type: &hostPathType,
				},
			},
		},
	}
	lpvTolerations := []v1.Toleration{
		{
			Key:      v1.TaintNodeDiskPressure,
			Operator: v1.TolerationOpExists,
			Effect:   v1.TaintEffectNoSchedule,
		},
	}
	helperPod := c.helperPod.DeepCopy()

	keyToPathItems := make([]v1.KeyToPath, 0, 2)

	if c.config.SetupCommand == "" {
		keyToPathItems = append(keyToPathItems, v1.KeyToPath{
			Key:  "setup",
			Path: "setup",
		})
	}

	if c.config.TeardownCommand == "" {
		keyToPathItems = append(keyToPathItems, v1.KeyToPath{
			Key:  "teardown",
			Path: "teardown",
		})
	}

	if c.config.ResizeCommand == "" {
		keyToPathItems = append(keyToPathItems, v1.KeyToPath{
			Key:  "resize",
			Path: "resize",
		})
	}

	if len(keyToPathItems) > 0 {
		lpvVolumes = append(lpvVolumes, v1.Volume{
			Name: "script",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: c.configMapName,
					},
					Items: keyToPathItems,
				},
			},
		})

		scriptMount := addVolumeMount(&helperPod.Spec.Containers[0].VolumeMounts, helperScriptVolName, helperScriptDir)
		scriptMount.MountPath = helperScriptDir
	}

	dataMount := addVolumeMount(&helperPod.Spec.Containers[0].VolumeMounts, helperDataVolName, parentDir)
	parentDir = dataMount.MountPath
	parentDir = strings.TrimSuffix(parentDir, string(filepath.Separator))
	volumeDir = strings.TrimSuffix(volumeDir, string(filepath.Separator))
	if parentDir == "" || volumeDir == "" || !filepath.IsAbs(parentDir) {
		// it covers the `/` case
		return fmt.Errorf("invalid path %v for %v: cannot find parent dir or volume dir or parent dir is relative", action, o.Path)
	}
	env := []v1.EnvVar{
		{Name: envVolDir, Value: filepath.Join(parentDir, volumeDir)},
		{Name: envVolMode, Value: string(o.Mode)},
		{Name: envVolSize, Value: strconv.FormatInt(o.SizeInBytes, 10)},
	}

	// use different name for helper pods
	// https://github.com/rancher/local-path-provisioner/issues/154
	helperPod.Name = (helperPod.Name + "-" + string(action) + "-" + o.Name)
	if len(helperPod.Name) > HelperPodNameMaxLength {
		helperPod.Name = helperPod.Name[:HelperPodNameMaxLength]
	}
	helperPod.Namespace = c.namespace
	if helperPod.Spec.NodeName == "" && o.Node != "" {
		helperPod.Spec.NodeName = o.Node
	}
	helperPod.Spec.ServiceAccountName = c.serviceAccountName
	helperPod.Spec.RestartPolicy = v1.RestartPolicyNever
	if helperPod.Spec.Tolerations == nil {
		helperPod.Spec.Tolerations = append(helperPod.Spec.Tolerations, lpvTolerations...)
	}
	helperPod.Spec.Volumes = append(helperPod.Spec.Volumes, lpvVolumes...)
	helperPod.Spec.Containers[0].Command = cmd
	helperPod.Spec.Containers[0].Env = append(helperPod.Spec.Containers[0].Env, env...)
	helperPod.Spec.Containers[0].Args = []string{"-p", filepath.Join(parentDir, volumeDir),
		"-s", strconv.FormatInt(o.SizeInBytes, 10),
		"-m", string(o.Mode),
		"-a", string(action)}

	// If it already exists due to some previous errors, the pod will be cleaned up later automatically
	// https://github.com/rancher/local-path-provisioner/issues/27
	logrus.Infof("create the helper pod %s into %s", helperPod.Name, c.namespace)
	pod, err := c.kubeClient.CoreV1().Pods(c.namespace).Create(context.TODO(), helperPod, metav1.CreateOptions{})
	if err != nil && !k8serror.IsAlreadyExists(err) {
		return err
	}

	defer func() {
		// log helper pod logs to the controller
		if err := c.saveHelperPodLogs(pod); err != nil {
			logrus.Error(err.Error())
		}
		e := c.kubeClient.CoreV1().Pods(c.namespace).Delete(context.TODO(), helperPod.Name, metav1.DeleteOptions{})
		if e != nil {
			logrus.Errorf("unable to delete the helper pod: %v", e)
		}
	}()

	completed := false
	for i := 0; i < c.config.CmdTimeoutSeconds; i++ {
		if pod, err := c.kubeClient.CoreV1().Pods(c.namespace).Get(context.TODO(), helperPod.Name, metav1.GetOptions{}); err != nil {
			return err
		} else if pod.Status.Phase == v1.PodSucceeded {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		return fmt.Errorf("create process timeout after %v seconds", c.config.CmdTimeoutSeconds)
	}

	if o.Node == "" {
		logrus.Infof("Volume %v has been %vd on %v", o.Name, action, o.Path)
	} else {
		logrus.Infof("Volume %v has been %vd on %v:%v", o.Name, action, o.Node, o.Path)
	}
	return nil
}

func (c *LocalPathCsiController) getPathOnNode(node string, requestedPath string, config *StorageClassConfig) (string, error) {
	c.configMutex.RLock()
	defer c.configMutex.RUnlock()

	if c.config == nil {
		return "", fmt.Errorf("no valid config available")
	}

	sharedFS, err := c.isSharedFilesystem(config)
	if err != nil {
		return "", err
	}
	if sharedFS {
		// we are ignoring 'node' and returning shared FS path
		return config.SharedFileSystemPath, nil
	}
	// we are working with local FS
	npMap := config.NodePathMap[node]
	if npMap == nil {
		npMap = config.NodePathMap[NodeDefaultNonListedNodes]
		if npMap == nil {
			return "", fmt.Errorf("config doesn't contain node %v, and no %v available", node, NodeDefaultNonListedNodes)
		}
		logrus.Debugf("config doesn't contain node %v, use %v instead", node, NodeDefaultNonListedNodes)
	}
	paths := npMap.Paths
	if len(paths) == 0 {
		return "", fmt.Errorf("no local path available on node %v", node)
	}
	// if a particular path was requested by storage class
	if requestedPath != "" {
		if _, ok := paths[requestedPath]; !ok {
			return "", fmt.Errorf("config doesn't contain path %v on node %v", requestedPath, node)
		}
		return requestedPath, nil
	}
	// if no particular path was requested, choose a random one
	i := rand.IntN(len(paths))
	for p := range paths {
		if i == 0 {
			return p, nil
		}
		i--
	}
	return "", fmt.Errorf("failed to select a random path: no path selected from %d candidates", len(paths))
}

func (c *LocalPathCsiController) pickConfig(storageClassName string) (*StorageClassConfig, error) {
	if len(c.config.StorageClassConfigs) == 0 {
		return &c.config.StorageClassConfig, nil
	}
	cfg, ok := c.config.StorageClassConfigs[storageClassName]
	if !ok {
		return nil, fmt.Errorf("BUG: Got request for unexpected storage class %s", storageClassName)
	}
	return &cfg, nil
}

func (c *LocalPathCsiController) getPathAndNodeForPV(pv *v1.PersistentVolume, cfg *StorageClassConfig) (path, node string, err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()

	volumeSource := pv.Spec.PersistentVolumeSource
	if volumeSource.HostPath != nil && volumeSource.Local == nil {
		path = volumeSource.HostPath.Path
	} else if volumeSource.Local != nil && volumeSource.HostPath == nil {
		path = volumeSource.Local.Path
	} else {
		return "", "", fmt.Errorf("no path set")
	}

	if cfg != nil {
		sharedFS, err := c.isSharedFilesystem(cfg)
		if err != nil {
			return "", "", err
		}

		if sharedFS {
			// We don't have affinity and can use any node
			return path, "", nil
		}
	}

	// Dealing with local filesystem

	nodeAffinity := pv.Spec.NodeAffinity
	if nodeAffinity == nil {
		return "", "", fmt.Errorf("no NodeAffinity set")
	}
	required := nodeAffinity.Required
	if required == nil {
		return "", "", fmt.Errorf("no NodeAffinity.Required set")
	}

	node = ""
	for _, selectorTerm := range required.NodeSelectorTerms {
		for _, expression := range selectorTerm.MatchExpressions {
			if expression.Key == KeyNode && expression.Operator == v1.NodeSelectorOpIn {
				if len(expression.Values) != 1 {
					return "", "", fmt.Errorf("multiple values for the node affinity")
				}
				node = expression.Values[0]
				break
			}
		}
		// fallback
		for _, field := range selectorTerm.MatchFields {
			if field.Key == KeyNodeField && field.Operator == v1.NodeSelectorOpIn {
				if len(field.Values) != 1 {
					return "", "", fmt.Errorf("multiple values for the node affinity")
				}
				node = field.Values[0]
				break
			}
		}
		if node != "" {
			break
		}
	}
	if node == "" {
		return "", "", fmt.Errorf("cannot find affinited node")
	}
	return path, node, nil
}

func addVolumeMount(mounts *[]v1.VolumeMount, name, mountPath string) *v1.VolumeMount {
	for i, m := range *mounts {
		if m.Name == name {
			if m.MountPath == "" {
				(*mounts)[i].MountPath = mountPath
			}
			return &(*mounts)[i]
		}
	}
	*mounts = append(*mounts, v1.VolumeMount{Name: name, MountPath: mountPath})
	return &(*mounts)[len(*mounts)-1]
}

func isJSONFile(configFile string) bool {
	return strings.HasSuffix(configFile, ".json")
}

func unmarshalFromString(configFile string) (*ConfigData, error) {
	var data ConfigData
	if err := json.Unmarshal([]byte(configFile), &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func loadConfigFile(configFile string) (cfgData *ConfigData, err error) {
	defer func() {
		err = errors.Wrapf(err, "fail to load config file %v", configFile)
	}()

	if !isJSONFile(configFile) {
		return unmarshalFromString(configFile)
	}

	f, err := os.Open(configFile)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	var data ConfigData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func canonicalizeConfig(data *ConfigData) (cfg *Config, err error) {
	cfg = &Config{}
	if len(data.StorageClassConfigs) == 0 {
		defaultConfig, err := canonicalizeStorageClassConfig(&data.StorageClassConfigData)
		if err != nil {
			return nil, err
		}
		cfg.StorageClassConfig = *defaultConfig
	} else {
		cfg.StorageClassConfigs = make(map[string]StorageClassConfig, len(data.StorageClassConfigs))
		for name, classData := range data.StorageClassConfigs {
			classCfg, err := canonicalizeStorageClassConfig(&classData)
			if err != nil {
				return nil, errors.Wrap(err, fmt.Sprintf("config for class %s is invalid", name))
			}
			cfg.StorageClassConfigs[name] = *classCfg
		}
	}
	cfg.SetupCommand = data.SetupCommand
	cfg.TeardownCommand = data.TeardownCommand
	if data.CmdTimeoutSeconds > 0 {
		cfg.CmdTimeoutSeconds = data.CmdTimeoutSeconds
	} else {
		cfg.CmdTimeoutSeconds = defaultCmdTimeoutSeconds
	}
	return cfg, nil
}

func canonicalizeStorageClassConfig(data *StorageClassConfigData) (cfg *StorageClassConfig, err error) {
	defer func() {
		err = errors.Wrapf(err, "StorageClass config canonicalization failed")
	}()
	cfg = &StorageClassConfig{}
	cfg.SharedFileSystemPath = data.SharedFileSystemPath
	cfg.NodePathMap = map[string]*NodePathMap{}
	for _, n := range data.NodePathMap {
		if cfg.NodePathMap[n.Node] != nil {
			return nil, fmt.Errorf("duplicate node %v", n.Node)
		}
		npMap := &NodePathMap{Paths: map[string]struct{}{}}
		cfg.NodePathMap[n.Node] = npMap
		for _, p := range n.Paths {
			if p[0] != '/' {
				return nil, fmt.Errorf("path must start with / for path %v on node %v", p, n.Node)
			}
			path, err := filepath.Abs(p)
			if err != nil {
				return nil, err
			}
			if path == "/" {
				return nil, fmt.Errorf("cannot use root ('/') as path on node %v", n.Node)
			}
			if _, ok := npMap.Paths[path]; ok {
				return nil, fmt.Errorf("duplicate path %v on node %v", p, n.Node)
			}
			npMap.Paths[path] = struct{}{}
		}
	}

	return cfg, nil
}

// saveHelperPodLogs takes what is in stdout/stderr from the helper
// pod and logs it to the provisioner's logs. Returns an error if we
// can't retrieve the helper pod logs.
func (c *LocalPathCsiController) saveHelperPodLogs(pod *v1.Pod) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to save %s logs", pod.Name)
	}()

	// save helper pod logs
	podLogOpts := v1.PodLogOptions{
		Container: "helper-pod",
	}
	req := c.kubeClient.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &podLogOpts)
	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		return fmt.Errorf("error in opening stream: %s", err.Error())
	}

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return fmt.Errorf("error in copying information from podLogs to buf: %s", err.Error())
	}
	_ = podLogs.Close()

	// log all messages from the helper pod to the controller
	logrus.Infof("Start of %s logs", pod.Name)
	bufferStr := buf.String()
	if len(bufferStr) > 0 {
		helperPodLogs := strings.Split(strings.Trim(bufferStr, "\n"), "\n")
		for _, log := range helperPodLogs {
			logrus.Info(log)
		}
	}
	logrus.Infof("End of %s logs", pod.Name)
	return nil
}
