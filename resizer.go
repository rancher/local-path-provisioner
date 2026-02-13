package main

import (
	"context"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	ActionTypeResize = "resize"
)

type LocalPathResizer struct {
	name            string
	provisionerName string
	LocalPathCsiController
}

func (r *LocalPathResizer) Name() string {
	return r.name
}

func (r *LocalPathResizer) DriverSupportsControlPlaneExpansion() bool {
	return false
}

func (r *LocalPathResizer) CanSupport(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool {
	annotaions := pv.Annotations
	for key, value := range annotaions {
		if key == provisionerNameAnnotationKey && value == r.provisionerName {
			return true
		}

		// fallback
		if key == defaultProvisionerNameAnnotationKey && value == r.provisionerName {
			return true
		}
	}
	logrus.Debugf("Resize not supported: %v", pv)
	return false
}

func (r *LocalPathResizer) Resize(pv *v1.PersistentVolume, requestSize resource.Quantity) (newSize resource.Quantity, fsResizeRequired bool, err error) {

	logrus.Debugf("Resize reconciler for pv %q with request size %q", pv.Name, requestSize.String())
	path, node, err := r.getPathAndNodeForPV(pv, nil)
	if err != nil {
		logrus.Errorf("failed to get path and node: %v", err)
		return requestSize, false, err
	}

	if node == "" {
		logrus.Infof("Resizing volume %v at %v", pv.Name, path)
	} else {
		logrus.Infof("Resizing volume %v at %v:%v", pv.Name, node, path)
	}

	resizeCmd := make([]string, 0, 2)
	if r.config.ResizeCommand == "" {
		resizeCmd = append(resizeCmd, "/bin/sh", "/script/resize")
	} else {
		resizeCmd = append(resizeCmd, r.config.ResizeCommand)
	}

	if err := r.createHelperPod(ActionTypeResize, resizeCmd, volumeOptions{
		Name:        pv.Name,
		Path:        path,
		Mode:        *pv.Spec.VolumeMode,
		SizeInBytes: requestSize.Value(),
		Node:        node,
	}, nil); err != nil {
		return requestSize, false, err
	}

	return requestSize, false, nil
}

func NewResizer(ctx context.Context, kubeClient *clientset.Clientset,
	configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml string, name string, provisionerName string) (*LocalPathResizer, error) {
	var err error
	csiController, err := NewCsiController(ctx, kubeClient, configFile, namespace, helperImage, configMapName, serviceAccountName, helperPodYaml)
	if err != nil {
		return nil, err
	}
	r := &LocalPathResizer{
		name:                   name,
		provisionerName:        provisionerName,
		LocalPathCsiController: *csiController,
	}
	return r, nil
}
