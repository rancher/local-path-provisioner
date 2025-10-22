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
	"errors"
	"fmt"

	"github.com/kubernetes-csi/external-resizer/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

// NOTE: Some of the if-else conditions in following code can be short-circuited, but it hasn't been done so
// to make it explicit, why we are doing what we are doing. This code is verbose on purpose, and we should
// keep it that way for easier readability.
func (ctrl *resizeController) expandAndRecover(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) (*v1.PersistentVolumeClaim, *v1.PersistentVolume, error, bool) {
	if !ctrl.pvCanBeExpanded(pv, pvc) {
		klog.V(4).InfoS("No need to resize", "PV", klog.KObj(pv))
		return pvc, pv, nil, false
	}
	// only used as a sentinel value when function returns without
	// actually performing expansion on the volume.
	resizeNotCalled := false

	// if we are here that already means pvc.Spec.Size > pvc.Status.Size
	pvcSpecSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	pvcStatusSize := pvc.Status.Capacity[v1.ResourceStorage]
	pvSize := pv.Spec.Capacity[v1.ResourceStorage]

	newSize := pvcSpecSize
	resizeStatus := pvc.Status.AllocatedResourceStatuses[v1.ResourceStorage]

	pvcKey, err := util.GetObjectKey(pvc)
	if err != nil {
		return pvc, pv, fmt.Errorf("error getting object key for pvc %s: %v", pvc.Name, err), resizeNotCalled
	}

	inSlowSet := ctrl.slowSet.Contains(pvcKey)

	var allocatedSize *resource.Quantity
	t, ok := pvc.Status.AllocatedResources[v1.ResourceStorage]
	if ok {
		allocatedSize = &t
	}

	updateStatus := true
	ctrl.markForSlowRetry(pvcKey, resizeStatus)

	if pvSize.Cmp(pvcSpecSize) < 0 {
		// PV is smaller than user requested size. In general some control-plane volume expansion
		// is necessary at this point but if we were expanding the PVC before we should let
		// previous operation finish before starting expansion to new user requested size.
		switch resizeStatus {
		case v1.PersistentVolumeClaimNodeResizeInfeasible:
			if allocatedSize != nil {
				newSize = *allocatedSize
			}
		case v1.PersistentVolumeClaimNodeResizePending,
			v1.PersistentVolumeClaimNodeResizeInProgress:
			if allocatedSize != nil {
				newSize = *allocatedSize
			}
			// If PV is already greater or equal to whatever newSize is, then we should
			// let previously issued node expansion finish before starting new one.
			// This case is essentially an optimization on previous case, generally it is safe
			// to let volume plugin receive the expansion call (expansion calls are idempotent)
			// but having this check here *avoids* unnecessary RPC calls to plugin.
			if pvSize.Cmp(newSize) >= 0 {
				return pvc, pv, nil, resizeNotCalled
			}
		case v1.PersistentVolumeClaimControllerResizeInProgress:
			// There could be two reasons why controller resize was in-progress:
			//	1. Either previous operation failed with uncertain error.
			//	2. Or Previous operation failed with final error, but we did not update API object.
			// For case#1 - Recovery from failure is not immediately possible, and we should let
			// previous operation succeed or fail.
			if ctrl.finalErrorPVCs.Has(pvcKey) {
				newSize = pvcSpecSize
			} else if allocatedSize != nil {
				newSize = *allocatedSize
			}
		default:
			newSize = pvcSpecSize
		}
	} else {
		// PV has already been expanded and hence we can be here for following reasons:
		//   1. If expansion is pending on the node and this was just a spurious update event
		//      we don't need to do anything and let kubelet handle it.
		//   2. It could be that - although we successfully expanded the volume, we failed to
		//      record our work in API objects, in which case - we should resume resizing operation
		//      and let API objects be updated.
		//   3. Controller successfully expanded the volume, but expansion is failing on the node
		//      and before kubelet can retry failed node expansion - controller must verify if it is
		//      safe to do so.
		//   4. While expansion was still pending on the node, user reduced the pvc size.
		switch resizeStatus {
		case v1.PersistentVolumeClaimNodeResizeInProgress,
			v1.PersistentVolumeClaimNodeResizePending:
			// we don't need to do any work. We could be here because of a spurious update event.
			// This is case #1
			return pvc, pv, nil, resizeNotCalled
		case v1.PersistentVolumeClaimNodeResizeInfeasible:
			// This is case#3, we need to reset the pvc status in such a way that kubelet can safely retry volume
			// expansion.
			if ctrl.resizer.DriverSupportsControlPlaneExpansion() && allocatedSize != nil {
				newSize = *allocatedSize
			} else {
				newSize = pvcSpecSize
			}
		case v1.PersistentVolumeClaimControllerResizeInProgress,
			v1.PersistentVolumeClaimControllerResizeInfeasible:
			// This is case#2 or it could also be case#4 when user manually shrunk the PVC
			// after expanding it.
			if allocatedSize != nil {
				newSize = *allocatedSize
			}
		default:
			// Usually it is impossible for ResizeStatus to be empty and allocatedSize to be not nil but somehow
			// if we do end up in this state, it is safest to resume expansion to last recorded size in
			// allocatedSize variable.
			// This can also happen if external-resizer is running with a version of api-server
			// which has volume recovery feature disabled.
			if resizeStatus == "" && allocatedSize != nil {
				newSize = *allocatedSize
			} else {
				klog.Warningf("external-resizer may be running with older version of kubernetes api-server, please upgrade to k8s version 1.32")
				// Usually resize operations are idempotent and it should be safe to reque resize here, but skipping expansion of already
				// expanded volumes saves some CPU cycles and helps in keeping e2es happy when running against older versions of kube-apiserver
				if util.HasFileSystemResizePendingCondition(pvc) {
					return pvc, pv, nil, false
				}
				newSize = pvcSpecSize
			}
		}
	}
	// If we are expanding volume to same size as before, then there is no point in changing
	// status fields again.
	if allocatedSize != nil && allocatedSize.Cmp(newSize) == 0 {
		if inSlowSet {
			msg := fmt.Sprintf("skipping volume expansion for pvc %s, because expansion previously failed with infeasible error", pvcKey)
			klog.V(4).Info(msg)
			delayRetryError := util.NewDelayRetryError(msg, ctrl.slowSet.TimeRemaining(pvcKey))
			return pvc, pv, delayRetryError, resizeNotCalled
		}
		klog.V(4).Infof("skipping updating status for pvc %s", pvcKey)
		updateStatus = false
	}

	pvc, err = ctrl.markControllerResizeInProgress(pvc, newSize, updateStatus)
	if err != nil {
		return pvc, pv, fmt.Errorf("marking pvc %q as resizing failed: %v", klog.KObj(pvc), err), resizeNotCalled
	}

	// if pvc previously failed to expand because it can't be expanded when in-use
	// we must not try expansion here
	if ctrl.usedPVCs.hasInUseErrors(pvc) && ctrl.usedPVCs.checkForUse(pvc) {
		// Record an event to indicate that resizer is not expanding the pvc
		msg := fmt.Sprintf("Unable to expand %s because CSI driver %s only supports offline expansion and volume is currently in-use", klog.KObj(pvc), ctrl.resizer.Name())
		ctrl.eventRecorder.Event(pvc, v1.EventTypeWarning, util.VolumeResizeFailed, msg)
		return pvc, pv, errors.New(msg), resizeNotCalled
	}

	// Record an event to indicate that external resizer is resizing this volume.
	ctrl.eventRecorder.Event(pvc, v1.EventTypeNormal, util.VolumeResizing,
		fmt.Sprintf("External resizer is resizing volume %s", pv.Name))

	// before trying expansion we will remove the PVC from map
	// that tracks PVCs which can't be expanded when in-use. If
	// pvc indeed can not be expanded when in-use then it will be added
	// back when expansion fails with in-use error.
	ctrl.usedPVCs.removePVCWithInUseError(pvc)
	pvc, pv, err = ctrl.callResizeOnPlugin(pvc, pv, newSize, pvcStatusSize)

	if err != nil {
		// Record an event to indicate that resize operation is failed.
		ctrl.eventRecorder.Eventf(pvc, v1.EventTypeWarning, util.VolumeResizeFailed, err.Error())
		return pvc, pv, err, true
	}

	klog.V(4).InfoS("Update capacity of PV succeeded", "PV", klog.KObj(pv), "capacity", newSize.String())
	return pvc, pv, nil, true
}

func (ctrl *resizeController) markForSlowRetry(pvcKey string, resizeStatus v1.ClaimResourceStatus) {
	if resizeStatus == v1.PersistentVolumeClaimControllerResizeInfeasible {
		ctrl.slowSet.Add(pvcKey)
	}
}

func (ctrl *resizeController) callResizeOnPlugin(
	pvc *v1.PersistentVolumeClaim,
	pv *v1.PersistentVolume,
	newSize, oldSize resource.Quantity) (*v1.PersistentVolumeClaim, *v1.PersistentVolume, error) {
	updatedSize, fsResizeRequired, err := ctrl.resizer.Resize(pv, newSize)

	pvcKey, objectKeyError := util.GetObjectKey(pvc)
	if objectKeyError != nil {
		return pvc, pv, fmt.Errorf("error getting object key for pvc %s: %v", pvc.Name, err)
	}

	if err != nil {
		// if this error was a in-use error then it must be tracked so as we don't retry without
		// first verifying if volume is in-use
		if inUseError(err) {
			ctrl.usedPVCs.addPVCWithInUseError(pvc)
		}
		if util.IsFinalError(err) {
			var markExpansionFailedError error
			ctrl.finalErrorPVCs.Insert(pvcKey)
			if util.IsInfeasibleError(err) {
				pvc, markExpansionFailedError = ctrl.markControllerExpansionInfeasible(pvc, err)
				if markExpansionFailedError != nil {
					return pvc, pv, fmt.Errorf("resizing failed in controller with %v but failed to update PVC %s with: %v", err, klog.KObj(pvc), markExpansionFailedError)
				}
			} else {
				// remvoe key from slowSet because controller expansion failed with non-infeasible error
				ctrl.slowSet.Remove(pvcKey)
				pvc, markExpansionFailedError = ctrl.markControllerExpansionFailedCondition(pvc, err)
				if markExpansionFailedError != nil {
					return pvc, pv, fmt.Errorf("resizing failed in controller with %v but failed to update PVC %s with: %v", err, klog.KObj(pvc), markExpansionFailedError)
				}
			}
		} else {
			// remove key from slowSet because resizer expansion failed with non-final error
			ctrl.slowSet.Remove(pvcKey)
			// remove key from finalErrorPVCs
			ctrl.finalErrorPVCs.Delete(pvcKey)
		}
		return pvc, pv, fmt.Errorf("resize volume %q by resizer %q failed: %v", pv.Name, ctrl.name, err)
	}

	ctrl.finalErrorPVCs.Delete(pvcKey)
	ctrl.slowSet.Remove(pvcKey)

	klog.V(4).InfoS("Resize volume succeeded, start to update PV's capacity", "PV", klog.KObj(pv))

	pv, err = ctrl.updatePVCapacity(pv, oldSize, updatedSize, fsResizeRequired)
	if err != nil {
		return pvc, pv, fmt.Errorf("error updating pv %q by resizer: %v", pv.Name, err)
	}

	if fsResizeRequired {
		pvc, err = ctrl.markForPendingNodeExpansion(pvc)
		return pvc, pv, err
	}
	pvc, err = ctrl.markOverallExpansionAsFinished(pvc, updatedSize)
	return pvc, pv, err
}

// checks if pv can be expanded
func (ctrl *resizeController) pvCanBeExpanded(pv *v1.PersistentVolume, pvc *v1.PersistentVolumeClaim) bool {
	if !ctrl.resizer.CanSupport(pv, pvc) {
		klog.V(4).InfoS("Resizer doesn't support PV", "controller", ctrl.name, "PV", klog.KObj(pv))
		return false
	}

	if (pv.Spec.ClaimRef == nil) || (pvc.Namespace != pv.Spec.ClaimRef.Namespace) || (pvc.UID != pv.Spec.ClaimRef.UID) {
		klog.V(4).InfoS("Persistent volume is not bound to PVC being updated", "PVC", klog.KObj(pvc))
		return false
	}
	return true
}
