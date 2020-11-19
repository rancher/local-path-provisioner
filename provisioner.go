package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	pvController "sigs.k8s.io/sig-storage-lib-external-provisioner/controller"
)

type ActionType string

const (
	ActionTypeCreate = "create"
	ActionTypeDelete = "delete"
)

const (
	KeyNode = "kubernetes.io/hostname"

	NodeDefaultNonListedNodes = "DEFAULT_PATH_FOR_NON_LISTED_NODES"
)

var (
	CmdTimeoutCounts = 120

	ConfigFileCheckInterval = 30 * time.Second

	HelperPodNameMaxLength = 128
)

type LocalPathProvisioner struct {
	stopCh             chan struct{}
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

type NodePathMapData struct {
	Node  string   `json:"node,omitempty"`
	Paths []string `json:"paths,omitempty"`
}

type ConfigData struct {
	NodePathMap []*NodePathMapData `json:"nodePathMap,omitempty"`
}

type NodePathMap struct {
	Paths map[string]struct{}
}

type Config struct {
	NodePathMap map[string]*NodePathMap
}

func NewProvisioner(stopCh chan struct{}, kubeClient *clientset.Clientset,
	configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml string) (*LocalPathProvisioner, error) {
	p := &LocalPathProvisioner{
		stopCh: stopCh,

		kubeClient:         kubeClient,
		namespace:          namespace,
		helperImage:        helperImage,
		serviceAccountName: serviceAccountName,

		// config will be updated shortly by p.refreshConfig()
		config:        nil,
		configFile:    configFile,
		configData:    nil,
		configMapName: configMapName,
		configMutex:   &sync.RWMutex{},
	}
	var err error
	p.helperPod, err = loadHelperPodFile(helperPodYaml)
	if err != nil {
		return nil, err
	}
	if err := p.refreshConfig(); err != nil {
		return nil, err
	}
	p.watchAndRefreshConfig()
	return p, nil
}

func (p *LocalPathProvisioner) refreshConfig() error {
	p.configMutex.Lock()
	defer p.configMutex.Unlock()

	configData, err := loadConfigFile(p.configFile)
	if err != nil {
		return err
	}
	// no need to update
	if reflect.DeepEqual(configData, p.configData) {
		return nil
	}
	config, err := canonicalizeConfig(configData)
	if err != nil {
		return err
	}
	// only update the config if the new config file is valid
	p.configData = configData
	p.config = config

	output, err := json.Marshal(p.configData)
	if err != nil {
		return err
	}
	logrus.Debugf("Applied config: %v", string(output))

	return err
}

func (p *LocalPathProvisioner) watchAndRefreshConfig() {
	go func() {
		ticker := time.NewTicker(ConfigFileCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := p.refreshConfig(); err != nil {
					logrus.Errorf("failed to load the new config file: %v", err)
				}
			case <-p.stopCh:
				logrus.Infof("stop watching config file")
				return
			}
		}
	}()
}

func (p *LocalPathProvisioner) getRandomPathOnNode(node string) (string, error) {
	p.configMutex.RLock()
	defer p.configMutex.RUnlock()

	if p.config == nil {
		return "", fmt.Errorf("no valid config available")
	}

	c := p.config
	npMap := c.NodePathMap[node]
	if npMap == nil {
		npMap = c.NodePathMap[NodeDefaultNonListedNodes]
		if npMap == nil {
			return "", fmt.Errorf("config doesn't contain node %v, and no %v available", node, NodeDefaultNonListedNodes)
		}
		logrus.Debugf("config doesn't contain node %v, use %v instead", node, NodeDefaultNonListedNodes)
	}
	paths := npMap.Paths
	if len(paths) == 0 {
		return "", fmt.Errorf("no local path available on node %v", node)
	}
	path := ""
	for path = range paths {
		break
	}
	return path, nil
}

func (p *LocalPathProvisioner) Provision(opts pvController.ProvisionOptions) (*v1.PersistentVolume, error) {
	pvc := opts.PVC
	if pvc.Spec.Selector != nil {
		return nil, fmt.Errorf("claim.Spec.Selector is not supported")
	}
	for _, accessMode := range pvc.Spec.AccessModes {
		if accessMode != v1.ReadWriteOnce {
			return nil, fmt.Errorf("Only support ReadWriteOnce access mode")
		}
	}
	node := opts.SelectedNode
	if opts.SelectedNode == nil {
		return nil, fmt.Errorf("configuration error, no node was specified")
	}

	basePath, err := p.getRandomPathOnNode(node.Name)
	if err != nil {
		return nil, err
	}

	name := opts.PVName
	folderName := strings.Join([]string{name, opts.PVC.Namespace, opts.PVC.Name}, "_")

	path := filepath.Join(basePath, folderName)
	logrus.Infof("Creating volume %v at %v:%v", name, node.Name, path)

	storage := pvc.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	volMode := string(*pvc.Spec.VolumeMode)

	createCmdsForPath := []string{
		"/bin/sh",
		"/script/setup",
	}
	if err := p.createHelperPod(ActionTypeCreate, createCmdsForPath, name, path, node.Name, volMode, storage.Value()); err != nil {
		return nil, err
	}

	fs := v1.PersistentVolumeFilesystem
	hostPathType := v1.HostPathDirectoryOrCreate
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *opts.StorageClass.ReclaimPolicy,
			AccessModes:                   pvc.Spec.AccessModes,
			VolumeMode:                    &fs,
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): pvc.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)],
			},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: path,
					Type: &hostPathType,
				},
			},
			NodeAffinity: &v1.VolumeNodeAffinity{
				Required: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: []v1.NodeSelectorRequirement{
								{
									Key:      KeyNode,
									Operator: v1.NodeSelectorOpIn,
									Values: []string{
										node.Name,
									},
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

func (p *LocalPathProvisioner) Delete(pv *v1.PersistentVolume) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()
	path, node, err := p.getPathAndNodeForPV(pv)
	if err != nil {
		return err
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != v1.PersistentVolumeReclaimRetain {
		logrus.Infof("Deleting volume %v at %v:%v", pv.Name, node, path)
		storage := pv.Spec.Capacity[v1.ResourceName(v1.ResourceStorage)]
		volMode := string(*pv.Spec.VolumeMode)
		cleanupCmdsForPath := []string{"/bin/sh", "/script/teardown"}
		if err := p.createHelperPod(ActionTypeDelete, cleanupCmdsForPath, pv.Name, path, node, volMode, storage.Value()); err != nil {
			logrus.Infof("clean up volume %v failed: %v", pv.Name, err)
			return err
		}
		return nil
	}
	logrus.Infof("Retained volume %v", pv.Name)
	return nil
}

func (p *LocalPathProvisioner) getPathAndNodeForPV(pv *v1.PersistentVolume) (path, node string, err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()

	hostPath := pv.Spec.PersistentVolumeSource.HostPath
	if hostPath == nil {
		return "", "", fmt.Errorf("no HostPath set")
	}
	path = hostPath.Path

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
		if node != "" {
			break
		}
	}
	if node == "" {
		return "", "", fmt.Errorf("cannot find affinited node")
	}
	return path, node, nil
}

func (p *LocalPathProvisioner) createHelperPod(action ActionType, cmdsForPath []string, name, path, node, volumeMode string, sizeInBytes int64) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to %v volume %v", action, name)
	}()
	if name == "" || path == "" || node == "" {
		return fmt.Errorf("invalid empty name or path or node")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	path = strings.TrimSuffix(path, "/")
	parentDir, volumeDir := filepath.Split(path)
	parentDir = strings.TrimSuffix(parentDir, "/")
	volumeDir = strings.TrimSuffix(volumeDir, "/")
	if parentDir == "" || volumeDir == "" {
		// it covers the `/` case
		return fmt.Errorf("invalid path %v for %v: cannot find parent dir or volume dir", action, path)
	}
	hostPathType := v1.HostPathDirectoryOrCreate
	lpvVolumes := []v1.Volume{
		{
			Name: "data",
			VolumeSource: v1.VolumeSource{
				HostPath: &v1.HostPathVolumeSource{
					Path: parentDir,
					Type: &hostPathType,
				},
			},
		},
		{
			Name: "script",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: v1.LocalObjectReference{
						Name: p.configMapName,
					},
					Items: []v1.KeyToPath{
						{
							Key:  "setup",
							Path: "setup",
						},
						{
							Key:  "teardown",
							Path: "teardown",
						},
					},
				},
			},
		},
	}
	lpvVolumeMounts := []v1.VolumeMount{
		{
			Name:      "data",
			ReadOnly:  false,
			MountPath: parentDir,
		},
		{
			Name:      "script",
			ReadOnly:  false,
			MountPath: "/script",
		},
	}
	lpvTolerations := []v1.Toleration{
		{
			Operator: v1.TolerationOpExists,
		},
	}
	helperPod := p.helperPod.DeepCopy()

	// use different name for helper pods
	// https://github.com/rancher/local-path-provisioner/issues/154
	helperPod.Name = (helperPod.Name + "-" + string(action) + "-" + name)
	if len(helperPod.Name) > HelperPodNameMaxLength {
		helperPod.Name = helperPod.Name[:HelperPodNameMaxLength]
	}
	helperPod.Namespace = p.namespace
	helperPod.Spec.NodeName = node
	helperPod.Spec.ServiceAccountName = p.serviceAccountName
	helperPod.Spec.RestartPolicy = v1.RestartPolicyNever
	helperPod.Spec.Tolerations = append(helperPod.Spec.Tolerations, lpvTolerations...)
	helperPod.Spec.Volumes = append(helperPod.Spec.Volumes, lpvVolumes...)
	helperPod.Spec.Containers[0].VolumeMounts = append(helperPod.Spec.Containers[0].VolumeMounts, lpvVolumeMounts...)
	helperPod.Spec.Containers[0].Command = cmdsForPath
	helperPod.Spec.Containers[0].Args = []string{"-p", filepath.Join(parentDir, volumeDir),
		"-s", strconv.FormatInt(sizeInBytes, 10),
		"-m", volumeMode}

	// If it already exists due to some previous errors, the pod will be cleaned up later automatically
	// https://github.com/rancher/local-path-provisioner/issues/27
	logrus.Infof("create the helper pod %s into %s", helperPod.Name, p.namespace)
	_, err = p.kubeClient.CoreV1().Pods(p.namespace).Create(helperPod)
	if err != nil && !k8serror.IsAlreadyExists(err) {
		return err
	}

	defer func() {
		e := p.kubeClient.CoreV1().Pods(p.namespace).Delete(helperPod.Name, &metav1.DeleteOptions{})
		if e != nil {
			logrus.Errorf("unable to delete the helper pod: %v", e)
		}
	}()

	completed := false
	for i := 0; i < CmdTimeoutCounts; i++ {
		if pod, err := p.kubeClient.CoreV1().Pods(p.namespace).Get(helperPod.Name, metav1.GetOptions{}); err != nil {
			return err
		} else if pod.Status.Phase == v1.PodSucceeded {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		return fmt.Errorf("create process timeout after %v seconds", CmdTimeoutCounts)
	}

	logrus.Infof("Volume %v has been %vd on %v:%v", name, action, node, path)
	return nil
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
	defer f.Close()

	var data ConfigData
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}
	return &data, nil
}

func canonicalizeConfig(data *ConfigData) (cfg *Config, err error) {
	defer func() {
		err = errors.Wrapf(err, "config canonicalization failed")
	}()
	cfg = &Config{}
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
