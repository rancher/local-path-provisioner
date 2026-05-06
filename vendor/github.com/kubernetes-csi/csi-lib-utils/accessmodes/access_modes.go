/*
Copyright 2021 The Kubernetes Authors.

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

package accessmodes

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	v1 "k8s.io/api/core/v1"
)

// ToCSIAccessMode maps PersistentVolume access modes in Kubernetes to CSI
// access modes. Which mapping is used depends if the driver supports the
// SINGLE_NODE_MULTI_WRITER capability.
func ToCSIAccessMode(pvAccessModes []v1.PersistentVolumeAccessMode, supportsSingleNodeMultiWriter bool) (csi.VolumeCapability_AccessMode_Mode, error) {
	if supportsSingleNodeMultiWriter {
		return toSingleNodeMultiWriterCapableCSIAccessMode(pvAccessModes)
	}
	return toCSIAccessMode(pvAccessModes)
}

// toCSIAccessMode maps PersistentVolume access modes in Kubernetes to CSI
// access modes.
//
// +------------------+-------------------------+----------------------------------------+
// |  K8s AccessMode  |     CSI AccessMode      |           Additional Details           |
// +------------------+-------------------------+----------------------------------------+
// | ReadWriteMany    | MULTI_NODE_MULTI_WRITER |                                        |
// | ReadOnlyMany     | MULTI_NODE_READER_ONLY  | Cannot be combined with ReadWriteOnce  |
// | ReadWriteOnce    | SINGLE_NODE_WRITER      | Cannot be combined with ReadOnlyMany   |
// | ReadWriteOncePod | SINGLE_NODE_WRITER      | Cannot be combined with any AccessMode |
// +------------------+-------------------------+----------------------------------------+
func toCSIAccessMode(pvAccessModes []v1.PersistentVolumeAccessMode) (csi.VolumeCapability_AccessMode_Mode, error) {
	m := uniqueAccessModes(pvAccessModes)

	switch {
	// This mapping exists to enable CSI drivers that lack the
	// SINGLE_NODE_MULTI_WRITER capability to work with the
	// ReadWriteOncePod access mode.
	case m[v1.ReadWriteOncePod]:
		if len(m) > 1 {
			return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("Kubernetes does not support use of ReadWriteOncePod with other access modes on the same PersistentVolume")
		}
		return csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, nil

	case m[v1.ReadWriteMany]:
		// ReadWriteMany takes precedence, regardless of what other
		// modes are set.
		return csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, nil

	case m[v1.ReadOnlyMany] && m[v1.ReadWriteOnce]:
		// This is not possible in the CSI spec.
		return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("CSI does not support ReadOnlyMany and ReadWriteOnce on the same PersistentVolume")

	case m[v1.ReadOnlyMany]:
		return csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY, nil

	case m[v1.ReadWriteOnce]:
		return csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER, nil

	default:
		return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("unsupported AccessMode combination: %+v", pvAccessModes)
	}
}

// toSingleNodeMultiWriterCapableCSIAccessMode maps PersistentVolume access
// modes in Kubernetes to CSI access modes for drivers that support the
// SINGLE_NODE_MULTI_WRITER capability.
//
// +------------------+---------------------------+----------------------------------------+
// |  K8s AccessMode  |      CSI AccessMode       |           Additional Details           |
// +------------------+---------------------------+----------------------------------------+
// | ReadWriteMany    | MULTI_NODE_MULTI_WRITER   |                                        |
// | ReadOnlyMany     | MULTI_NODE_READER_ONLY    | Cannot be combined with ReadWriteOnce  |
// | ReadWriteOnce    | SINGLE_NODE_MULTI_WRITER  | Cannot be combined with ReadOnlyMany   |
// | ReadWriteOncePod | SINGLE_NODE_SINGLE_WRITER | Cannot be combined with any AccessMode |
// +------------------+---------------------------+----------------------------------------+
func toSingleNodeMultiWriterCapableCSIAccessMode(pvAccessModes []v1.PersistentVolumeAccessMode) (csi.VolumeCapability_AccessMode_Mode, error) {
	m := uniqueAccessModes(pvAccessModes)

	switch {
	case m[v1.ReadWriteOncePod]:
		if len(m) > 1 {
			return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("Kubernetes does not support use of ReadWriteOncePod with other access modes on the same PersistentVolume")
		}
		return csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER, nil

	case m[v1.ReadWriteMany]:
		// ReadWriteMany trumps everything, regardless of what other
		// modes are set.
		return csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, nil

	case m[v1.ReadOnlyMany] && m[v1.ReadWriteOnce]:
		// This is not possible in the CSI spec.
		return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("CSI does not support ReadOnlyMany and ReadWriteOnce on the same PersistentVolume")

	case m[v1.ReadOnlyMany]:
		return csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY, nil

	case m[v1.ReadWriteOnce]:
		return csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER, nil

	default:
		return csi.VolumeCapability_AccessMode_UNKNOWN, fmt.Errorf("unsupported AccessMode combination: %+v", pvAccessModes)
	}
}

func uniqueAccessModes(pvAccessModes []v1.PersistentVolumeAccessMode) map[v1.PersistentVolumeAccessMode]bool {
	m := map[v1.PersistentVolumeAccessMode]bool{}
	for _, mode := range pvAccessModes {
		m[mode] = true
	}
	return m
}
