package main

import (
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const LocalPathResizerName = "LocalPathResizer"

type LocalPathResizer struct {
	name string
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
	return requestSize, false, nil
}

func NewResizer() *LocalPathResizer {
	r := &LocalPathResizer{
		name: LocalPathResizerName,
	}
	return r
}
