/*
Copyright 2019 The Kubernetes Authors.

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
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	csilib "github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/accessmodes"
	"github.com/kubernetes-csi/csi-lib-utils/connection"
	"github.com/kubernetes-csi/external-resizer/pkg/csi"
	"github.com/kubernetes-csi/external-resizer/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	csitrans "k8s.io/csi-translation-lib"
	"k8s.io/klog/v2"
)

var (
	controllerServiceNotSupportErr = errors.New("CSI driver does not support controller service")
	resizeNotSupportErr            = errors.New("CSI driver neither supports controller resize nor node resize")
)

func NewResizerFromClient(
	csiClient csi.Client,
	timeout time.Duration,
	k8sClient kubernetes.Interface,
	driverName string) (Resizer, error) {

	supportControllerService, err := supportsPluginControllerService(csiClient, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to check if plugin supports controller service: %v", err)
	}

	if !supportControllerService {
		return nil, controllerServiceNotSupportErr
	}

	supportControllerResize, err := supportsControllerResize(csiClient, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to check if plugin supports controller resize: %v", err)
	}

	if !supportControllerResize {
		supportsNodeResize, err := supportsNodeResize(csiClient, timeout)
		if err != nil {
			return nil, fmt.Errorf("failed to check if plugin supports node resize: %v", err)
		}
		if supportsNodeResize {
			klog.InfoS("The CSI driver supports node resize only, using trivial resizer to handle resize requests")
			return newTrivialResizer(driverName), nil
		}
		return nil, resizeNotSupportErr
	}

	_, err = supportsControllerSingleNodeMultiWriter(csiClient, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to check if plugin supports the SINGLE_NODE_MULTI_WRITER capability: %v", err)
	}

	return &csiResizer{
		name:    driverName,
		client:  csiClient,
		timeout: timeout,

		k8sClient: k8sClient,
	}, nil
}

type csiResizer struct {
	name    string
	client  csi.Client
	timeout time.Duration

	k8sClient kubernetes.Interface
}

func (r *csiResizer) Name() string {
	return r.name
}

// CanSupport returns whether the PV is supported by resizer
// Resizer will resize the volume if it is CSI volume or is migration enabled in-tree volume
func (r *csiResizer) CanSupport(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool {
	resizerName := pvc.Annotations[util.VolumeResizerKey]
	// resizerName will be CSI driver name when CSI migration is enabled
	// otherwise, it will be in-tree plugin name
	// r.name is the CSI driver name, return true only when they match
	// and the CSI driver is migrated
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
		klog.V(4).InfoS("Skip resize PV", "PV", klog.KObj(pv), "resizer", source.Driver)
		return false
	}
	return true
}

func (r *csiResizer) DriverSupportsControlPlaneExpansion() bool {
	return true
}

// Resize resizes the persistence volume given request size
// It supports both CSI volume and migrated in-tree volume
func (r *csiResizer) Resize(pv *v1.PersistentVolume, requestSize resource.Quantity) (resource.Quantity, bool, error) {
	oldSize := pv.Spec.Capacity[v1.ResourceStorage]

	var volumeID string
	var source *v1.CSIPersistentVolumeSource
	var pvSpec v1.PersistentVolumeSpec
	var migrated bool
	if pv.Spec.CSI != nil {
		// handle CSI volume
		source = pv.Spec.CSI
		volumeID = source.VolumeHandle
		pvSpec = pv.Spec
	} else {
		translator := csitrans.New()
		if translator.IsMigratedCSIDriverByName(r.name) {
			// handle migrated in-tree volume
			csiPV, err := translator.TranslateInTreePVToCSI(pv)
			if err != nil {
				return oldSize, false, fmt.Errorf("failed to translate persistent volume: %v", err)
			}
			migrated = true
			source = csiPV.Spec.CSI
			pvSpec = csiPV.Spec
			volumeID = source.VolumeHandle
		} else {
			// non-migrated in-tree volume
			return oldSize, false, fmt.Errorf("volume %v is not migrated to CSI", pv.Name)
		}
	}

	if len(volumeID) == 0 {
		return oldSize, false, errors.New("empty volume handle")
	}

	var secrets map[string]string
	secreRef := source.ControllerExpandSecretRef
	if secreRef != nil {
		var err error
		secrets, err = getCredentials(r.k8sClient, secreRef)
		if err != nil {
			return oldSize, false, err
		}
	}

	capability, err := r.getVolumeCapabilities(pvSpec)
	if err != nil {
		return oldSize, false, fmt.Errorf("failed to get capabilities of volume %s with %v", pv.Name, err)
	}

	ctx, cancel := timeoutCtx(r.timeout)
	resizeCtx := context.WithValue(ctx, connection.AdditionalInfoKey, connection.AdditionalInfo{Migrated: strconv.FormatBool(migrated)})

	defer cancel()
	newSizeBytes, nodeResizeRequired, err := r.client.Expand(resizeCtx, volumeID, requestSize.Value(), secrets, capability)
	if err != nil {
		return oldSize, nodeResizeRequired, err
	}

	return *resource.NewQuantity(newSizeBytes, resource.BinarySI), nodeResizeRequired, err
}

func (r *csiResizer) getVolumeCapabilities(pvSpec v1.PersistentVolumeSpec) (*csilib.VolumeCapability, error) {
	supported, err := supportsControllerSingleNodeMultiWriter(r.client, r.timeout)
	if err != nil {
		return nil, err
	}
	return GetVolumeCapabilities(pvSpec, supported)
}

// GetVolumeCapabilities returns a VolumeCapability from the PV spec. Which access mode will be set depends if the driver supports the
// SINGLE_NODE_MULTI_WRITER capability.
func GetVolumeCapabilities(pvSpec v1.PersistentVolumeSpec, singleNodeMultiWriterCapable bool) (*csilib.VolumeCapability, error) {
	if pvSpec.CSI == nil {
		return nil, errors.New("CSI volume source was nil")
	}

	var cap *csilib.VolumeCapability
	if pvSpec.VolumeMode != nil && *pvSpec.VolumeMode == v1.PersistentVolumeBlock {
		cap = &csilib.VolumeCapability{
			AccessType: &csilib.VolumeCapability_Block{
				Block: &csilib.VolumeCapability_BlockVolume{},
			},
			AccessMode: &csilib.VolumeCapability_AccessMode{},
		}

	} else {
		fsType := pvSpec.CSI.FSType

		cap = &csilib.VolumeCapability{
			AccessType: &csilib.VolumeCapability_Mount{
				Mount: &csilib.VolumeCapability_MountVolume{
					FsType:     fsType,
					MountFlags: pvSpec.MountOptions,
				},
			},
			AccessMode: &csilib.VolumeCapability_AccessMode{},
		}
	}

	am, err := accessmodes.ToCSIAccessMode(pvSpec.AccessModes, singleNodeMultiWriterCapable)
	if err != nil {
		return nil, err
	}

	cap.AccessMode.Mode = am
	return cap, nil
}

func supportsPluginControllerService(client csi.Client, timeout time.Duration) (bool, error) {
	ctx, cancel := timeoutCtx(timeout)
	defer cancel()
	return client.SupportsPluginControllerService(ctx)
}

func supportsControllerResize(client csi.Client, timeout time.Duration) (bool, error) {
	ctx, cancel := timeoutCtx(timeout)
	defer cancel()
	return client.SupportsControllerResize(ctx)
}

func supportsNodeResize(client csi.Client, timeout time.Duration) (bool, error) {
	ctx, cancel := timeoutCtx(timeout)
	defer cancel()
	return client.SupportsNodeResize(ctx)
}

func supportsControllerSingleNodeMultiWriter(client csi.Client, timeout time.Duration) (bool, error) {
	ctx, cancel := timeoutCtx(timeout)
	defer cancel()
	return client.SupportsControllerSingleNodeMultiWriter(ctx)
}

func timeoutCtx(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func getCredentials(k8sClient kubernetes.Interface, ref *v1.SecretReference) (map[string]string, error) {
	if ref == nil {
		return nil, nil
	}

	secret, err := k8sClient.CoreV1().Secrets(ref.Namespace).Get(context.TODO(), ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting secret %s in namespace %s: %v", ref.Name, ref.Namespace, err)
	}

	credentials := map[string]string{}
	for key, value := range secret.Data {
		credentials[key] = string(value)
	}
	return credentials, nil
}

func uniqueAccessModes(pvSpec v1.PersistentVolumeSpec) map[v1.PersistentVolumeAccessMode]bool {
	m := map[v1.PersistentVolumeAccessMode]bool{}
	for _, mode := range pvSpec.AccessModes {
		m[mode] = true
	}
	return m
}
