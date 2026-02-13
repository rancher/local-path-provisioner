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
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// Resizer is responsible for handling pvc resize requests.
type Resizer interface {
	// Name returns the resizer's name.
	Name() string
	// DriverSupportsControlPlaneExpansion returns true if driver really supports control-plane expansion
	DriverSupportsControlPlaneExpansion() bool
	// CanSupport returns true if resizer supports resize operation of this PV
	// with its corresponding PVC.
	CanSupport(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool
	// Resize executes the resize operation of this PV.
	Resize(pv *v1.PersistentVolume, requestSize resource.Quantity) (newSize resource.Quantity, fsResizeRequired bool, err error)
}
