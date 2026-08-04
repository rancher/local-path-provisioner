/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resizer

import (
	"github.com/kubernetes-csi/external-resizer/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	csitrans "k8s.io/csi-translation-lib"
	"k8s.io/klog/v2"
)

// newTrivialResizer returns a trivial resizer which will mark all pvs' resize process as finished.
func newTrivialResizer(name string) Resizer {
	return &trivialResizer{name: name}
}

type trivialResizer struct {
	name string
}

func (r *trivialResizer) Name() string {
	return r.name
}

func (r *trivialResizer) CanSupport(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool {
	resizerName := pvc.Annotations[util.VolumeResizerKey]
	translator := csitrans.New()
	if translator.IsMigratedCSIDriverByName(r.name) && resizerName == r.name {
		return true
	}

	source := pv.Spec.CSI
	if source == nil {
		klog.V(4).InfoS("PV is not a CSI volume, skip it", "PV", klog.KObj(pv))
		return false
	}
	if source.Driver != r.name {
		klog.V(4).InfoS("Skip resize", "PV", klog.KObj(pv), "resizer", source.Driver)
		return false
	}
	return true
}

func (r *trivialResizer) DriverSupportsControlPlaneExpansion() bool {
	return false
}

func (r *trivialResizer) Resize(pv *v1.PersistentVolume, requestSize resource.Quantity) (newSize resource.Quantity, fsResizeRequired bool, err error) {
	return requestSize, true, nil
}
