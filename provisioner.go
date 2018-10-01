package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/pkg/errors"

	pvController "github.com/kubernetes-incubator/external-storage/lib/controller"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	KeyNode = "kubernetes.io/hostname"
)

var (
	DefaultPath   = "/opt"
	CleanupCounts = 120
)

type LocalPathProvisioner struct {
	kubeClient *clientset.Clientset
}

func NewProvisioner(kubeClient *clientset.Clientset) *LocalPathProvisioner {
	return &LocalPathProvisioner{
		kubeClient: kubeClient,
	}
}

func (p *LocalPathProvisioner) Provision(opts pvController.VolumeOptions) (*v1.PersistentVolume, error) {
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
	name := opts.PVName
	path := filepath.Join(DefaultPath, name)

	logrus.Infof("Created volume %v", name)
	fs := v1.PersistentVolumeFilesystem
	hostPathType := v1.HostPathDirectoryOrCreate

	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: opts.PersistentVolumeReclaimPolicy,
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
		if err := p.cleanupVolume(pv.Name, path, node); err != nil {
			logrus.Infof("clean up volume %v failed: %v", pv.Name, err)
			return err
		}
		return nil
	}
	logrus.Infof("retained volume %v", pv.Name)
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

func (p *LocalPathProvisioner) cleanupVolume(name, path, node string) (err error) {
	defer func() {
		err = errors.Wrapf(err, "failed to cleanup volume %v", name)
	}()
	if name == "" || path == "" || node == "" {
		return fmt.Errorf("invalid empty name or path or node")
	}
	//TODO match up with node prefixes, make sure it's one of them (and not `/`)
	if filepath.Clean(path) == "/" {
		return fmt.Errorf("not sure why you want to DESTROY the root directory")
	}
	hostPathType := v1.HostPathDirectoryOrCreate
	cleanupPod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cleanup-" + name,
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:    "local-path-cleanup",
					Image:   "busybox",
					Command: []string{"rm"},
					Args:    []string{"-rf", "/data-to-cleanup/*"},
					VolumeMounts: []v1.VolumeMount{
						{
							Name:      "data-to-cleanup",
							ReadOnly:  false,
							MountPath: "/data-to-cleanup/",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				{
					Name: "data-to-cleanup",
					VolumeSource: v1.VolumeSource{
						HostPath: &v1.HostPathVolumeSource{
							Path: path,
							Type: &hostPathType,
						},
					},
				},
			},
		},
	}

	namespace := "default"
	pod, err := p.kubeClient.CoreV1().Pods(namespace).Create(cleanupPod)
	if err != nil {
		return err
	}

	defer func() {
		e := p.kubeClient.CoreV1().Pods(namespace).Delete(pod.Name, &metav1.DeleteOptions{})
		if e != nil {
			logrus.Errorf("unable to delete the cleanup pod: %v", e)
		}
	}()

	completed := false
	for i := 0; i < CleanupCounts; i++ {
		if pod, err := p.kubeClient.CoreV1().Pods(namespace).Get(pod.Name, metav1.GetOptions{}); err != nil {
			return err
		} else if pod.Status.Phase == v1.PodSucceeded {
			completed = true
			break
		}
		time.Sleep(1 * time.Second)
	}
	if !completed {
		return fmt.Errorf("cleanup process timeout after %v seconds", CleanupCounts)
	}

	logrus.Infof("Volume %v has been cleaned up", name)
	return nil
}
