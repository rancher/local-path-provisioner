package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	k8serror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"

	pvController "sigs.k8s.io/sig-storage-lib-external-provisioner/v11/controller"
)

type LocalPathProvisioner struct {
	name string
	LocalPathCsiController
}

func NewProvisioner(ctx context.Context, kubeClient *clientset.Clientset,
	configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml string, name string) (*LocalPathProvisioner, error) {
	var err error
	csiController, err := NewCsiController(ctx, kubeClient, configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml)
	if err != nil {
		return nil, err
	}
	p := &LocalPathProvisioner{
		name:                   name,
		LocalPathCsiController: *csiController,
	}
	return p, nil
}

type pvMetadata struct {
	PVName string
	PVC    metav1.ObjectMeta
}

func pathFromPattern(pattern string, opts pvController.ProvisionOptions, allowUnsafePath bool) (string, error) {
	metadata := pvMetadata{
		PVName: opts.PVName,
		PVC:    opts.PVC.ObjectMeta,
	}

	tpl, err := template.New("pathPattern").Parse(pattern)
	if err != nil {
		return "", err
	}

	buf := new(bytes.Buffer)
	err = tpl.Execute(buf, metadata)
	if err != nil {
		return "", err
	}

	if allowUnsafePath {
		return buf.String(), nil
	}

	path := buf.String()
	fixedBasePathPrefix := filepath.Join(opts.PVC.Namespace, opts.PVC.Name) + string(filepath.Separator)
	if !strings.HasPrefix(path, fixedBasePathPrefix) {
		return "", fmt.Errorf("pathPattern must start with {{ .PVC.Namespace }}/{{ .PVC.Name }}/: %s", path)
	}

	return path, nil
}

func (p *LocalPathProvisioner) Provision(_ context.Context, opts pvController.ProvisionOptions) (*v1.PersistentVolume, pvController.ProvisioningState, error) {
	cfg, err := p.pickConfig(opts.StorageClass.Name)
	if err != nil {
		return nil, pvController.ProvisioningFinished, err
	}
	return p.provisionFor(opts, cfg)
}

func (p *LocalPathProvisioner) provisionFor(opts pvController.ProvisionOptions, c *StorageClassConfig) (*v1.PersistentVolume, pvController.ProvisioningState, error) {
	pvc := opts.PVC
	node := opts.SelectedNode
	storageClass := opts.StorageClass
	sharedFS, err := p.isSharedFilesystem(c)
	if err != nil {
		return nil, pvController.ProvisioningFinished, err
	}
	if !sharedFS {
		if pvc.Spec.Selector != nil {
			return nil, pvController.ProvisioningFinished, fmt.Errorf("claim.Spec.Selector is not supported")
		}
		for _, accessMode := range pvc.Spec.AccessModes {
			if accessMode != v1.ReadWriteOnce && accessMode != v1.ReadWriteOncePod {
				return nil, pvController.ProvisioningFinished, fmt.Errorf("NodePath only supports ReadWriteOnce and ReadWriteOncePod (1.22+) access modes")
			}
		}
		if node == nil {
			return nil, pvController.ProvisioningFinished, fmt.Errorf("configuration error, no node was specified")
		}
	}

	nodeName := ""
	if node != nil {
		// This clause works only with sharedFS
		nodeName = node.Name
	}
	var requestedPath string
	if storageClass.Parameters != nil {
		if _, ok := storageClass.Parameters["nodePath"]; ok {
			requestedPath = storageClass.Parameters["nodePath"]
		}
	}
	basePath, err := p.getPathOnNode(nodeName, requestedPath, c)
	if err != nil {
		return nil, pvController.ProvisioningFinished, err
	}

	name := opts.PVName
	folderName := strings.Join([]string{name, opts.PVC.Namespace, opts.PVC.Name}, "_")

	pathPattern, exists := opts.StorageClass.Parameters["pathPattern"]
	if exists {
		allowUnsafePath := false
		allowUnsafePathPattern, exists := opts.StorageClass.Parameters["allowUnsafePathPattern"]
		if exists {
			allowUnsafePath, err = strconv.ParseBool(allowUnsafePathPattern)
			if err != nil {
				logrus.Warnf("failed to parse allowUnsafePathPattern %v, defaulting to false: %v", allowUnsafePathPattern, err)
				allowUnsafePath = false
			}
		} else {
			// Read from storageclass annotation for backward compatibility
			allowUnsafePathAnnotation, exists := opts.StorageClass.GetAnnotations()["allowUnsafePathPattern"]
			if exists {
				allowUnsafePath, err = strconv.ParseBool(allowUnsafePathAnnotation)
				if err != nil {
					logrus.Warnf("failed to parse allow-unsafe-path-pattern annotation %v, defaulting to false: %v", allowUnsafePathAnnotation, err)
					allowUnsafePath = false
				}
			}
		}
		folderName, err = pathFromPattern(pathPattern, opts, allowUnsafePath)
		if err != nil {
			err = errors.Wrapf(err, "failed to create path from pattern %v", pathPattern)
			return nil, pvController.ProvisioningFinished, err
		}
		// Check for directory traversal attempts in the path.
		if !allowUnsafePath && !filepath.IsLocal(folderName) {
			return nil, pvController.ProvisioningFinished, fmt.Errorf("folder path contains invalid references: %s", folderName)
		}
	}

	path := filepath.Join(basePath, folderName)
	if nodeName == "" {
		logrus.Infof("Creating volume %v at %v", name, path)
	} else {
		logrus.Infof("Creating volume %v at %v:%v", name, nodeName, path)
	}

	storage := pvc.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	provisionCmd := make([]string, 0, 2)
	if p.config.SetupCommand == "" {
		provisionCmd = append(provisionCmd, "/bin/sh", "/script/setup")
	} else {
		provisionCmd = append(provisionCmd, p.config.SetupCommand)
	}
	if err := p.createHelperPod(ActionTypeCreate, provisionCmd, volumeOptions{
		Name:        name,
		Path:        path,
		Mode:        *pvc.Spec.VolumeMode,
		SizeInBytes: storage.Value(),
		Node:        nodeName,
	}, c); err != nil {
		return nil, pvController.ProvisioningFinished, err
	}

	fs := v1.PersistentVolumeFilesystem

	var pvs v1.PersistentVolumeSource
	var volumeType string
	if dVal, ok := opts.StorageClass.GetAnnotations()["defaultVolumeType"]; ok {
		volumeType = dVal
	} else {
		volumeType = defaultVolumeType
	}
	if val, ok := opts.PVC.GetAnnotations()["volumeType"]; ok {
		volumeType = val
	}
	pvs, err = createPersistentVolumeSource(volumeType, path)
	if err != nil {
		return nil, pvController.ProvisioningFinished, err
	}

	var nodeAffinity *v1.VolumeNodeAffinity
	if sharedFS {
		// If the same filesystem is mounted across all nodes, we don't need
		// affinity, as path is accessible from any node
		nodeAffinity = nil
	} else {
		valueNode, ok := node.GetLabels()[KeyNode]
		if !ok {
			valueNode = nodeName
		}
		nodeAffinity = &v1.VolumeNodeAffinity{
			Required: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      KeyNode,
								Operator: v1.NodeSelectorOpIn,
								Values: []string{
									valueNode,
								},
							},
						},
					},
				},
			},
		}
	}
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Annotations: map[string]string{nodeNameAnnotationKey: nodeName, provisionerNameAnnotationKey: p.name},
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: *opts.StorageClass.ReclaimPolicy,
			AccessModes:                   pvc.Spec.AccessModes,
			VolumeMode:                    &fs,
			Capacity: v1.ResourceList{
				v1.ResourceStorage: pvc.Spec.Resources.Requests[v1.ResourceStorage],
			},
			PersistentVolumeSource: pvs,
			NodeAffinity:           nodeAffinity,
		},
	}, pvController.ProvisioningFinished, nil
}

func (p *LocalPathProvisioner) Delete(_ context.Context, pv *v1.PersistentVolume) (err error) {
	cfg, err := p.pickConfig(pv.Spec.StorageClassName)
	if err != nil {
		return err
	}
	return p.deleteFor(pv, cfg)
}

func (p *LocalPathProvisioner) deleteFor(pv *v1.PersistentVolume, c *StorageClassConfig) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to delete volume %v", pv.Name)
	}()

	path, node, err := p.getPathAndNodeForPV(pv, c)
	if err != nil {
		return err
	}
	if pv.Spec.PersistentVolumeReclaimPolicy != v1.PersistentVolumeReclaimRetain {
		if node == "" {
			logrus.Infof("Deleting volume %v at %v", pv.Name, path)
		} else {
			// Check if node still exists
			if _, err := p.kubeClient.CoreV1().Nodes().Get(context.TODO(), node, metav1.GetOptions{}); err != nil && k8serror.IsNotFound(err) {
				logrus.Infof("Node %v does not exist, skipping cleanup of volume %v", node, pv.Name)
				return nil
			}
			logrus.Infof("Deleting volume %v at %v:%v", pv.Name, node, path)
		}
		storage := pv.Spec.Capacity[v1.ResourceName(v1.ResourceStorage)]
		cleanupCmd := make([]string, 0, 2)
		if p.config.TeardownCommand == "" {
			cleanupCmd = append(cleanupCmd, "/bin/sh", "/script/teardown")
		} else {
			cleanupCmd = append(cleanupCmd, p.config.TeardownCommand)
		}
		if err := p.createHelperPod(ActionTypeDelete, cleanupCmd, volumeOptions{
			Name:        pv.Name,
			Path:        path,
			Mode:        *pv.Spec.VolumeMode,
			SizeInBytes: storage.Value(),
			Node:        node,
		}, c); err != nil {
			logrus.Infof("clean up volume %v failed: %v", pv.Name, err)
			return err
		}
		return nil
	}
	logrus.Infof("Retained volume %v", pv.Name)
	return nil
}

func createPersistentVolumeSource(volumeType string, path string) (pvs v1.PersistentVolumeSource, err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to create persistent volume source")
	}()

	switch strings.ToLower(volumeType) {
	case "local":
		pvs = v1.PersistentVolumeSource{
			Local: &v1.LocalVolumeSource{
				Path: path,
			},
		}
	case "hostpath":
		hostPathType := v1.HostPathDirectoryOrCreate
		pvs = v1.PersistentVolumeSource{
			HostPath: &v1.HostPathVolumeSource{
				Path: path,
				Type: &hostPathType,
			},
		}
	default:
		return pvs, fmt.Errorf("\"%s\" is not a recognised volume type", volumeType)
	}

	return pvs, nil
}
