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

package features

import (
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
)

const (
	// owner: @sunpa93
	// alpha: v1.22
	AnnotateFsResize featuregate.Feature = "AnnotateFsResize"

	// owner: @gnufied
	// alpha: v1.23
	//
	// Allows users to recover from volume expansion failures
	RecoverVolumeExpansionFailure featuregate.Feature = "RecoverVolumeExpansionFailure"

	// owner: @sunnylovestiramisu
	// kep: https://kep.k8s.io/3751
	// alpha: v1.29
	//
	// Pass VolumeAttributesClass parameters to supporting CSI drivers during ModifyVolume
	VolumeAttributesClass featuregate.Feature = "VolumeAttributesClass"
)

func init() {
	utilfeature.DefaultMutableFeatureGate.Add(defaultResizerFeatureGates)
}

var defaultResizerFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	AnnotateFsResize:              {Default: false, PreRelease: featuregate.Alpha},
	RecoverVolumeExpansionFailure: {Default: false, PreRelease: featuregate.Alpha},
	VolumeAttributesClass:         {Default: false, PreRelease: featuregate.Alpha},
}
