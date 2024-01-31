package main

import (
	"github.com/Sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	LocalPathResizerName = "LocalPathResizer"
	ActionTypeResize     = "resize"
)

type LocalPathResizer struct {
	name        string
	provisioner *LocalPathProvisioner
}

func (r *LocalPathResizer) Name() string {
	return r.name
}

func (r *LocalPathResizer) DriverSupportsControlPlaneExpansion() bool {
	return false
}

func (r *LocalPathResizer) CanSupport(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool {
	return true
}

func (r *LocalPathResizer) Resize(pv *v1.PersistentVolume, requestSize resource.Quantity) (newSize resource.Quantity, fsResizeRequired bool, err error) {

	logrus.Debugf("Resize reconciler for pv %q with request size %q", pv.Name, requestSize.String())
	path, node, err := r.provisioner.getPathAndNodeForPV(pv)
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
	if r.provisioner.config.ResizeCommand == "" {
		resizeCmd = append(resizeCmd, "/bin/sh", "/script/resize")
	} else {
		resizeCmd = append(resizeCmd, r.provisioner.config.ResizeCommand)
	}

	if err := r.provisioner.createHelperPod(ActionTypeResize, resizeCmd, volumeOptions{
		Name:        pv.Name,
		Path:        path,
		Mode:        *pv.Spec.VolumeMode,
		SizeInBytes: requestSize.Value(),
		Node:        node,
	}); err != nil {
		return requestSize, false, err
	}

	return requestSize, false, nil
}

func NewResizer(p *LocalPathProvisioner) *LocalPathResizer {
	r := &LocalPathResizer{
		name:        LocalPathResizerName,
		provisioner: p,
	}
	return r
}
