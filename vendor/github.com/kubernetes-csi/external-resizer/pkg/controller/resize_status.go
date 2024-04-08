/*
Copyright 2022 The Kubernetes Authors.

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

package controller

import (
	"fmt"

	"github.com/kubernetes-csi/external-resizer/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

// markControllerResizeInProgress will mark PVC for controller resize, this function is newer version that uses
// resizeStatus and sets allocatedResources.
func (ctrl *resizeController) markControllerResizeInProgress(
	pvc *v1.PersistentVolumeClaim, newSize resource.Quantity) (*v1.PersistentVolumeClaim, error) {

	progressCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimResizing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}
	conditions := []v1.PersistentVolumeClaimCondition{progressCondition}

	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions, conditions)
	newPVC = mergeStorageResourceStatus(newPVC, v1.PersistentVolumeClaimControllerResizeInProgress)
	newPVC = mergeStorageAllocatedResources(newPVC, newSize)
	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)
	if err != nil {
		return pvc, err
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return updatedPVC, fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}

	return updatedPVC, nil
}

// markForPendingNodeExpansion is new set of functions designed around feature RecoverVolumeExpansionFailure
// which correctly sets pvc.Status.ResizeStatus
func (ctrl *resizeController) markForPendingNodeExpansion(pvc *v1.PersistentVolumeClaim) (*v1.PersistentVolumeClaim, error) {
	pvcCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimFileSystemResizePending,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Waiting for user to (re-)start a pod to finish file system resize of volume on node.",
	}

	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions,
		[]v1.PersistentVolumeClaimCondition{pvcCondition})
	newPVC = mergeStorageResourceStatus(newPVC, v1.PersistentVolumeClaimNodeResizePending)
	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)

	if err != nil {
		return updatedPVC, fmt.Errorf("mark PVC %q as node expansion required failed: %v", klog.KObj(pvc), err)
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return updatedPVC, fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}

	klog.V(4).InfoS("Mark PVC as file system resize required", "PVC", klog.KObj(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal,
		util.FileSystemResizeRequired, "Require file system resize of volume on node")

	return updatedPVC, nil
}

func (ctrl *resizeController) markControllerExpansionFailed(pvc *v1.PersistentVolumeClaim) (*v1.PersistentVolumeClaim, error) {
	newPVC := pvc.DeepCopy()
	newPVC = mergeStorageResourceStatus(newPVC, v1.PersistentVolumeClaimControllerResizeFailed)

	// We are setting addResourceVersionCheck as false as an optimization
	// because if expansion fails on controller and somehow we can't update PVC
	// because our version of object is slightly older then the entire resize
	// operation must be restarted before ResizeStatus can be set to Expansionfailedoncontroller.
	// Setting addResourceVersionCheck to `false` ensures that we set `ResizeStatus`
	// even if our version of PVC was slightly older.
	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, false /* addResourceVersionCheck */)
	if err != nil {
		return pvc, fmt.Errorf("mark PVC %q as controller expansion failed, errored with: %v", klog.KObj(pvc), err)
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return updatedPVC, fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}
	return updatedPVC, nil
}

func (ctrl *resizeController) markOverallExpansionAsFinished(
	pvc *v1.PersistentVolumeClaim,
	newSize resource.Quantity) (*v1.PersistentVolumeClaim, error) {

	newPVC := pvc.DeepCopy()
	newPVC.Status.Capacity[v1.ResourceStorage] = newSize
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(pvc.Status.Conditions, []v1.PersistentVolumeClaimCondition{})

	resourceStatusMap := newPVC.Status.AllocatedResourceStatuses
	delete(resourceStatusMap, v1.ResourceStorage)
	if len(resourceStatusMap) == 0 {
		newPVC.Status.AllocatedResourceStatuses = nil
	} else {
		newPVC.Status.AllocatedResourceStatuses = resourceStatusMap
	}

	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)
	if err != nil {
		return pvc, fmt.Errorf("mark PVC %q as resize finished failed: %v", klog.KObj(pvc), err)
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return updatedPVC, fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}

	klog.V(4).InfoS("Resize PVC finished", "PVC", klog.KObj(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal, util.VolumeResizeSuccess, "Resize volume succeeded")

	return updatedPVC, nil
}

func mergeStorageResourceStatus(pvc *v1.PersistentVolumeClaim, status v1.ClaimResourceStatus) *v1.PersistentVolumeClaim {
	allocatedResourceStatusMap := pvc.Status.AllocatedResourceStatuses
	if allocatedResourceStatusMap == nil {
		pvc.Status.AllocatedResourceStatuses = map[v1.ResourceName]v1.ClaimResourceStatus{
			v1.ResourceStorage: status,
		}
		return pvc
	}
	allocatedResourceStatusMap[v1.ResourceStorage] = status
	pvc.Status.AllocatedResourceStatuses = allocatedResourceStatusMap
	return pvc
}

func mergeStorageAllocatedResources(pvc *v1.PersistentVolumeClaim, size resource.Quantity) *v1.PersistentVolumeClaim {
	allocatedResourcesMap := pvc.Status.AllocatedResources
	if allocatedResourcesMap == nil {
		pvc.Status.AllocatedResources = map[v1.ResourceName]resource.Quantity{
			v1.ResourceStorage: size,
		}
		return pvc
	}
	allocatedResourcesMap[v1.ResourceStorage] = size
	pvc.Status.AllocatedResources = allocatedResourcesMap
	return pvc
}
