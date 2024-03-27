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

package util

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

var (
	knownResizeConditions = map[v1.PersistentVolumeClaimConditionType]bool{
		v1.PersistentVolumeClaimResizing:                true,
		v1.PersistentVolumeClaimFileSystemResizePending: true,
	}

	knownModifyVolumeConditions = map[v1.PersistentVolumeClaimConditionType]bool{
		v1.PersistentVolumeClaimVolumeModifyingVolume:   true,
		v1.PersistentVolumeClaimVolumeModifyVolumeError: true,
	}

	// AnnPreResizeCapacity annotation is added to a PV when expanding volume.
	// Its value is status capacity of the PVC prior to the volume expansion
	// Its value will be set by the external-resizer when it deems that filesystem resize is required after resizing volume.
	// Its value will be used by pv_controller to determine pvc's status capacity when binding pvc and pv.
	AnnPreResizeCapacity = "volume.alpha.kubernetes.io/pre-resize-capacity"
)

// MergeResizeConditionsOfPVC updates pvc with requested resize conditions
// leaving other conditions untouched.
func MergeResizeConditionsOfPVC(oldConditions, newConditions []v1.PersistentVolumeClaimCondition) []v1.PersistentVolumeClaimCondition {
	newConditionSet := make(map[v1.PersistentVolumeClaimConditionType]v1.PersistentVolumeClaimCondition, len(newConditions))
	for _, condition := range newConditions {
		newConditionSet[condition.Type] = condition
	}

	var resultConditions []v1.PersistentVolumeClaimCondition
	for _, condition := range oldConditions {
		// If Condition is of not resize type, we keep it.
		if _, ok := knownResizeConditions[condition.Type]; !ok {
			newConditions = append(newConditions, condition)
			continue
		}
		if newCondition, ok := newConditionSet[condition.Type]; ok {
			// Use the new condition to replace old condition with same type.
			resultConditions = append(resultConditions, newCondition)
			delete(newConditionSet, condition.Type)
		}

		// Drop old conditions whose type not exist in new conditions.
	}

	// Append remains resize conditions.
	for _, condition := range newConditionSet {
		resultConditions = append(resultConditions, condition)
	}

	return resultConditions
}

// MergeModifyVolumeConditionsOfPVC updates pvc with requested modify volume conditions
// leaving other conditions untouched.
func MergeModifyVolumeConditionsOfPVC(oldConditions, newConditions []v1.PersistentVolumeClaimCondition) []v1.PersistentVolumeClaimCondition {
	newConditionSet := make(map[v1.PersistentVolumeClaimConditionType]v1.PersistentVolumeClaimCondition, len(newConditions))
	for _, condition := range newConditions {
		newConditionSet[condition.Type] = condition
	}

	resultConditions := []v1.PersistentVolumeClaimCondition{}
	for _, condition := range oldConditions {
		// If Condition is not modifyVolume type, we keep it.
		if _, ok := knownModifyVolumeConditions[condition.Type]; !ok {
			resultConditions = append(resultConditions, condition)
			continue
		}
		if newCondition, ok := newConditionSet[condition.Type]; ok {
			// Use the new condition to replace old condition with same type.
			resultConditions = append(resultConditions, newCondition)
			delete(newConditionSet, condition.Type)
		}
		// Drop other modify volume conditions that are not in the newConditionSet
	}

	// Append remains modify volume conditions.
	for _, condition := range newConditionSet {
		resultConditions = append(resultConditions, condition)
	}

	return resultConditions
}

func GetPVCPatchData(oldPVC, newPVC *v1.PersistentVolumeClaim, addResourceVersionCheck bool) ([]byte, error) {
	patchBytes, err := GetPatchData(oldPVC, newPVC)
	if err != nil {
		return patchBytes, err
	}

	if addResourceVersionCheck {
		patchBytes, err = addResourceVersion(patchBytes, oldPVC.ResourceVersion)
		if err != nil {
			return nil, fmt.Errorf("apply ResourceVersion to patch data failed: %v", err)
		}
	}
	return patchBytes, nil
}

func addResourceVersion(patchBytes []byte, resourceVersion string) ([]byte, error) {
	var patchMap map[string]interface{}
	err := json.Unmarshal(patchBytes, &patchMap)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling patch with %v", err)
	}
	u := unstructured.Unstructured{Object: patchMap}
	a, err := meta.Accessor(&u)
	if err != nil {
		return nil, fmt.Errorf("error creating accessor with  %v", err)
	}
	a.SetResourceVersion(resourceVersion)
	versionBytes, err := json.Marshal(patchMap)
	if err != nil {
		return nil, fmt.Errorf("error marshalling json patch with %v", err)
	}
	return versionBytes, nil
}

func GetPatchData(oldObj, newObj interface{}) ([]byte, error) {
	oldData, err := json.Marshal(oldObj)
	if err != nil {
		return nil, fmt.Errorf("marshal old object failed: %v", err)
	}
	newData, err := json.Marshal(newObj)
	if err != nil {
		return nil, fmt.Errorf("marshal new object failed: %v", err)
	}
	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, oldObj)
	if err != nil {
		return nil, fmt.Errorf("CreateTwoWayMergePatch failed: %v", err)
	}
	return patchBytes, nil
}

// HasFileSystemResizePendingCondition returns true if a pvc has a FileSystemResizePending condition.
// This means the controller side resize operation is finished, and kubelet side operation is in progress.
func HasFileSystemResizePendingCondition(pvc *v1.PersistentVolumeClaim) bool {
	for _, condition := range pvc.Status.Conditions {
		if condition.Type == v1.PersistentVolumeClaimFileSystemResizePending && condition.Status == v1.ConditionTrue {
			return true
		}
	}
	return false
}

// SanitizeName changes any name to a sanitized name which can be accepted by kubernetes.
func SanitizeName(name string) string {
	re := regexp.MustCompile("[^a-zA-Z0-9-]")
	name = re.ReplaceAllString(name, "-")
	if name[len(name)-1] == '-' {
		// name must not end with '-'
		name = name + "X"
	}
	return name
}

func GetObjectKey(obj interface{}) (string, error) {
	if unknown, ok := obj.(cache.DeletedFinalStateUnknown); ok && unknown.Obj != nil {
		obj = unknown.Obj
	}
	objKey, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		klog.ErrorS(err, "Failed to get key from object")
		return "", err
	}
	return objKey, nil
}

// Patches a given PVC with changes from newPVC. If addResourceVersionCheck is true
// then a version check is added to the patch to ensure that we are not patching
// old(and possibly outdated) PVC objects.
func PatchClaim(kubeClient kubernetes.Interface, oldPVC, newPVC *v1.PersistentVolumeClaim, addResourceVersionCheck bool) (*v1.PersistentVolumeClaim, error) {
	patchBytes, err := GetPVCPatchData(oldPVC, newPVC, addResourceVersionCheck)
	if err != nil {
		return oldPVC, fmt.Errorf("can't patch status of PVC %s as generate path data failed: %v", klog.KObj(oldPVC), err)
	}
	updatedClaim, updateErr := kubeClient.CoreV1().PersistentVolumeClaims(oldPVC.Namespace).
		Patch(context.TODO(), oldPVC.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if updateErr != nil {
		return oldPVC, fmt.Errorf("can't patch status of  PVC %s with %v", klog.KObj(oldPVC), updateErr)
	}

	return updatedClaim, nil
}

func PatchPersistentVolume(kubeClient kubernetes.Interface, oldPV, newPV *v1.PersistentVolume) (*v1.PersistentVolume, error) {
	patchBytes, err := GetPatchData(oldPV, newPV)
	if err != nil {
		return nil, fmt.Errorf("can't update capacity of PV %s as generate path data failed: %v", newPV.Name, err)
	}
	updatedPV, updateErr := kubeClient.CoreV1().PersistentVolumes().Patch(context.TODO(), newPV.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if updateErr != nil {
		return nil, fmt.Errorf("update capacity of PV %s failed: %v", newPV.Name, updateErr)
	}
	return updatedPV, nil
}

func IsFinalError(err error) bool {
	// Sources:
	// https://github.com/grpc/grpc/blob/master/doc/statuscodes.md
	// https://github.com/container-storage-interface/spec/blob/master/spec.md
	st, ok := status.FromError(err)
	if !ok {
		// This is not gRPC error. The operation must have failed before gRPC
		// method was called, otherwise we would get gRPC error.
		// We don't know if any previous volume operation is in progress, be on the safe side.
		return false
	}
	switch st.Code() {
	case codes.Canceled, // gRPC: Client Application cancelled the request
		codes.DeadlineExceeded,  // gRPC: Timeout
		codes.Unavailable,       // gRPC: Server shutting down, TCP connection broken - previous volume operation may be still in progress.
		codes.ResourceExhausted, // gRPC: Server temporarily out of resources - previous volume operation may be still in progress.
		codes.Aborted:           // CSI: Operation pending for volume
		return false
	}
	// All other errors mean that operation either did not
	// even start or failed. It is for sure not in progress.
	return true
}
