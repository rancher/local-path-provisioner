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

package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/kubernetes-csi/external-resizer/pkg/features"

	"github.com/kubernetes-csi/external-resizer/pkg/resizer"
	"github.com/kubernetes-csi/external-resizer/pkg/util"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// ResizeController watches PVCs and checks if they are requesting an resizing operation.
// If requested, it will resize according PVs and update PVCs' status to reflect the new size.
type ResizeController interface {
	// Run starts the controller.
	Run(workers int, ctx context.Context)
}

type resizeController struct {
	name          string
	resizer       resizer.Resizer
	kubeClient    kubernetes.Interface
	claimQueue    workqueue.RateLimitingInterface
	eventRecorder record.EventRecorder
	pvSynced      cache.InformerSynced
	pvcSynced     cache.InformerSynced

	usedPVCs *inUsePVCStore

	podLister       corelisters.PodLister
	podListerSynced cache.InformerSynced

	// a cache to store PersistentVolume objects
	volumes cache.Store
	// a cache to store PersistentVolumeClaim objects
	claims                 cache.Store
	handleVolumeInUseError bool
}

// NewResizeController returns a ResizeController.
func NewResizeController(
	name string,
	resizer resizer.Resizer,
	kubeClient kubernetes.Interface,
	resyncPeriod time.Duration,
	informerFactory informers.SharedInformerFactory,
	pvcRateLimiter workqueue.RateLimiter,
	handleVolumeInUseError bool) ResizeController {
	pvInformer := informerFactory.Core().V1().PersistentVolumes()
	pvcInformer := informerFactory.Core().V1().PersistentVolumeClaims()
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartStructuredLogging(0)
	eventBroadcaster.StartRecordingToSink(&corev1.EventSinkImpl{Interface: kubeClient.CoreV1().Events(v1.NamespaceAll)})
	eventRecorder := eventBroadcaster.NewRecorder(scheme.Scheme,
		v1.EventSource{Component: fmt.Sprintf("external-resizer %s", name)})

	claimQueue := workqueue.NewNamedRateLimitingQueue(
		pvcRateLimiter, fmt.Sprintf("%s-pvc", name))

	ctrl := &resizeController{
		name:                   name,
		resizer:                resizer,
		kubeClient:             kubeClient,
		pvSynced:               pvInformer.Informer().HasSynced,
		pvcSynced:              pvcInformer.Informer().HasSynced,
		claimQueue:             claimQueue,
		volumes:                pvInformer.Informer().GetStore(),
		claims:                 pvcInformer.Informer().GetStore(),
		eventRecorder:          eventRecorder,
		usedPVCs:               newUsedPVCStore(),
		handleVolumeInUseError: handleVolumeInUseError,
	}

	// Add a resync period as the PVC's request size can be resized again when we handling
	// a previous resizing request of the same PVC.
	pvcInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
		AddFunc:    ctrl.addPVC,
		UpdateFunc: ctrl.updatePVC,
		DeleteFunc: ctrl.deletePVC,
	}, resyncPeriod)

	if handleVolumeInUseError {
		// list pods so as we can identify PVC that are in-use
		klog.InfoS("Register Pod informer for resizer", "controller", ctrl.name)
		podInformer := informerFactory.Core().V1().Pods()
		ctrl.podLister = podInformer.Lister()
		ctrl.podListerSynced = podInformer.Informer().HasSynced
		podInformer.Informer().AddEventHandlerWithResyncPeriod(cache.ResourceEventHandlerFuncs{
			AddFunc:    ctrl.addPod,
			DeleteFunc: ctrl.deletePod,
			UpdateFunc: ctrl.updatePod,
		}, resyncPeriod)
	}

	return ctrl
}

func (ctrl *resizeController) addPVC(obj interface{}) {
	objKey, err := util.GetObjectKey(obj)
	if err != nil {
		return
	}
	ctrl.claimQueue.Add(objKey)
}

func (ctrl *resizeController) addPod(obj interface{}) {
	pod := parsePod(obj)
	if pod == nil {
		return
	}
	ctrl.usedPVCs.addPod(pod)
}

func (ctrl *resizeController) deletePod(obj interface{}) {
	pod := parsePod(obj)
	if pod == nil {
		return
	}
	ctrl.usedPVCs.removePod(pod)
}

func (ctrl *resizeController) updatePod(oldObj, newObj interface{}) {
	pod := parsePod(newObj)
	if pod == nil {
		return
	}

	if isPodTerminated(pod) {
		ctrl.usedPVCs.removePod(pod)
	} else {
		ctrl.usedPVCs.addPod(pod)
	}
}

func (ctrl *resizeController) updatePVC(oldObj, newObj interface{}) {
	oldPVC, ok := oldObj.(*v1.PersistentVolumeClaim)
	if !ok || oldPVC == nil {
		return
	}

	newPVC, ok := newObj.(*v1.PersistentVolumeClaim)
	if !ok || newPVC == nil {
		return
	}

	newReq := newPVC.Spec.Resources.Requests[v1.ResourceStorage]
	oldReq := oldPVC.Spec.Resources.Requests[v1.ResourceStorage]

	newCap := newPVC.Status.Capacity[v1.ResourceStorage]
	oldCap := oldPVC.Status.Capacity[v1.ResourceStorage]

	newResizerName := newPVC.Annotations[util.VolumeResizerKey]
	oldResizerName := oldPVC.Annotations[util.VolumeResizerKey]

	// We perform additional checks to avoid double processing of PVCs, as we will also receive Update event when:
	// 1. Administrator or users may introduce other changes(such as add labels, modify annotations, etc.)
	//    unrelated to volume resize.
	// 2. Informer will resync and send Update event periodically without any changes.
	//
	// We add the PVC into work queue when the new size is larger then the old size
	// or when the resizer name changes. This is needed for CSI migration for the follow two cases:
	//
	// 1. First time a migrated PVC is expanded:
	// It does not yet have the annotation because annotation is only added by in-tree resizer when it receives a volume
	// expansion request. So first update event that will be received by external-resizer will be ignored because it won't
	// know how to support resizing of a "un-annotated" in-tree PVC. When in-tree resizer does add the annotation, a second
	// update even will be received and we add the pvc to workqueue. If annotation matches the registered driver name in
	// csi_resizer object, we proceeds with expansion internally or we discard the PVC.
	// 3. An already expanded in-tree PVC:
	// An in-tree PVC is resized with in-tree resizer. And later, CSI migration is turned on and resizer name is updated from
	// in-tree resizer name to CSI driver name.
	if newReq.Cmp(oldReq) > 0 || newResizerName != oldResizerName {
		ctrl.addPVC(newObj)
	} else {
		// PVC's size not changed, so this Update event maybe caused by:
		//
		// 1. Administrators or users introduce other changes(such as add labels, modify annotations, etc.)
		//    unrelated to volume resize.
		// 2. Informer resynced the PVC and send this Update event without any changes.
		// 3. PV's filesystem has recently been resized and requires removal of its resize annotation
		//
		// If it is case 1, we can just discard this event. If case 2 or 3, we need to put it into the queue to
		// perform a resync operation.
		if newPVC.ResourceVersion == oldPVC.ResourceVersion {
			// This is case 2.
			ctrl.addPVC(newObj)
		} else if newCap.Cmp(oldCap) > 0 {
			// This is case 3
			ctrl.addPVC(newObj)
		}
	}
}

func (ctrl *resizeController) deletePVC(obj interface{}) {
	objKey, err := util.GetObjectKey(obj)
	if err != nil {
		return
	}
	ctrl.claimQueue.Forget(objKey)
}

// Run starts the controller.
func (ctrl *resizeController) Run(
	workers int, ctx context.Context) {
	defer ctrl.claimQueue.ShutDown()

	klog.InfoS("Starting external resizer", "controller", ctrl.name)
	defer klog.InfoS("Shutting down external resizer", "controller", ctrl.name)

	stopCh := ctx.Done()
	informersSyncd := []cache.InformerSynced{ctrl.pvSynced, ctrl.pvcSynced}
	if ctrl.handleVolumeInUseError {
		informersSyncd = append(informersSyncd, ctrl.podListerSynced)
	}

	if !cache.WaitForCacheSync(stopCh, informersSyncd...) {
		klog.ErrorS(nil, "Cannot sync pod, pv or pvc caches")
		return
	}

	for i := 0; i < workers; i++ {
		go wait.Until(ctrl.syncPVCs, 0, stopCh)
	}

	<-stopCh
}

// syncPVCs is the main worker.
func (ctrl *resizeController) syncPVCs() {
	key, quit := ctrl.claimQueue.Get()
	if quit {
		return
	}
	defer ctrl.claimQueue.Done(key)

	if err := ctrl.syncPVC(key.(string)); err != nil {
		// Put PVC back to the queue so that we can retry later.
		klog.ErrorS(err, "Error syncing PVC")
		ctrl.claimQueue.AddRateLimited(key)
	} else {
		ctrl.claimQueue.Forget(key)
	}
}

// syncPVC checks if a pvc requests resizing, and execute the resize operation if requested.
func (ctrl *resizeController) syncPVC(key string) error {
	klog.V(4).InfoS("Started PVC processing for resize controller", "key", key)

	namespace, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		return fmt.Errorf("getting namespace and name from key %s failed: %v", key, err)
	}

	pvcObject, exists, err := ctrl.claims.GetByKey(key)
	if err != nil {
		return fmt.Errorf("getting PVC %s/%s failed: %v", namespace, name, err)
	}

	if !exists {
		klog.V(3).InfoS("PVC is deleted or does not exist", "PVC", klog.KRef(namespace, name))
		return nil
	}

	pvc, ok := pvcObject.(*v1.PersistentVolumeClaim)
	if !ok {
		return fmt.Errorf("expected PVC got: %v", pvcObject)
	}

	if pvc.Spec.VolumeName == "" {
		klog.V(4).InfoS("PV bound to PVC is not created yet", "PVC", klog.KObj(pvc))
		return nil
	}

	volumeObj, exists, err := ctrl.volumes.GetByKey(pvc.Spec.VolumeName)
	if err != nil {
		return fmt.Errorf("Get PV %q of pvc %q failed: %v", pvc.Spec.VolumeName, klog.KObj(pvc), err)
	}
	if !exists {
		klog.InfoS("PV bound to PVC not found", "PV", pvc.Spec.VolumeName, "PVC", klog.KObj(pvc))
		return nil
	}

	pv, ok := volumeObj.(*v1.PersistentVolume)
	if !ok {
		return fmt.Errorf("expected volume but got %+v", volumeObj)
	}

	if utilfeature.DefaultFeatureGate.Enabled(features.AnnotateFsResize) && ctrl.isNodeExpandComplete(pvc, pv) && metav1.HasAnnotation(pv.ObjectMeta, util.AnnPreResizeCapacity) {
		if err := ctrl.deletePreResizeCapAnnotation(pv); err != nil {
			return fmt.Errorf("failed removing annotation %s from pv %q: %v", util.AnnPreResizeCapacity, pv.Name, err)
		}
	}

	if !ctrl.pvcNeedResize(pvc) {
		klog.V(4).InfoS("No need to resize PVC", "PVC", klog.KObj(pvc))
		return nil
	}

	if utilfeature.DefaultFeatureGate.Enabled(features.RecoverVolumeExpansionFailure) {
		_, _, err, _ := ctrl.expandAndRecover(pvc, pv)
		return err
	} else {
		if !ctrl.pvNeedResize(pvc, pv) {
			klog.V(4).InfoS("No need to resize PV", "PV", klog.KObj(pv))
			return nil
		}

		return ctrl.resizePVC(pvc, pv)
	}
}

// pvcNeedResize returns true is a pvc requests a resize operation.
func (ctrl *resizeController) pvcNeedResize(pvc *v1.PersistentVolumeClaim) bool {
	// Only Bound pvc can be expanded.
	if pvc.Status.Phase != v1.ClaimBound {
		return false
	}
	if pvc.Spec.VolumeName == "" {
		return false
	}
	actualSize := pvc.Status.Capacity[v1.ResourceStorage]
	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	return requestSize.Cmp(actualSize) > 0
}

// pvNeedResize returns true if a pv supports and also requests resize.
func (ctrl *resizeController) pvNeedResize(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) bool {
	if !ctrl.resizer.CanSupport(pv, pvc) {
		klog.V(4).InfoS("Resizer doesn't support PV", "controller", ctrl.name, "PV", klog.KObj(pv))
		return false
	}

	if (pv.Spec.ClaimRef == nil) || (pvc.Namespace != pv.Spec.ClaimRef.Namespace) || (pvc.UID != pv.Spec.ClaimRef.UID) {
		klog.V(4).InfoS("Persistent volume is not bound to PVC being updated", "PVC", klog.KObj(pvc))
		return false
	}

	pvSize := pv.Spec.Capacity[v1.ResourceStorage]
	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	if pvSize.Cmp(requestSize) >= 0 {
		// If PV size is equal or bigger than request size, that means we have already resized PV.
		// In this case we need to check PVC's condition.
		// 1. If PVC in PersistentVolumeClaimResizing condition, we should continue to perform the
		//    resizing operation as we need to know if file system resize if required. (What's more,
		//    we hope the driver can find that the actual size already matched the request size and do nothing).
		// 2. If PVC in PersistentVolumeClaimFileSystemResizePending condition, we need to
		//    do nothing as kubelet will finish file system resizing and mark resize as finished.
		if util.HasFileSystemResizePendingCondition(pvc) {
			// This is case 2.
			return false
		}
		// This is case 1.
		return true
	}

	// PV size is smaller than request size, we need to resize the volume.
	return true
}

// isNodeExpandComplete returns true if  pvc.Status.Capacity >= pv.Spec.Capacity
func (ctrl *resizeController) isNodeExpandComplete(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) bool {
	klog.V(4).InfoS("Capacity of pv and pvc", "PV", klog.KObj(pv), "pvCapacity", pv.Spec.Capacity[v1.ResourceStorage], "PVC", klog.KObj(pvc), "pvcCapacity", pvc.Status.Capacity[v1.ResourceStorage])
	pvcCap, pvCap := pvc.Status.Capacity[v1.ResourceStorage], pv.Spec.Capacity[v1.ResourceStorage]
	return pvcCap.Cmp(pvCap) >= 0
}

// resizePVC will:
// 1. Mark pvc as resizing.
// 2. Resize the volume and the pv object.
// 3. Mark pvc as resizing finished(no error, no need to resize fs), need resizing fs or resize failed.
func (ctrl *resizeController) resizePVC(pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume) error {
	if updatedPVC, err := ctrl.markPVCResizeInProgress(pvc); err != nil {
		return fmt.Errorf("marking pvc %q as resizing failed: %v", klog.KObj(pvc), err)
	} else if updatedPVC != nil {
		pvc = updatedPVC
	}

	// if pvc previously failed to expand because it can't be expanded when in-use
	// we must not try expansion here
	if ctrl.usedPVCs.hasInUseErrors(pvc) && ctrl.usedPVCs.checkForUse(pvc) {
		// Record an event to indicate that resizer is not expanding the pvc
		msg := fmt.Sprintf("Unable to expand %s because CSI driver %s only supports offline expansion and volume is currently in-use", klog.KObj(pvc), ctrl.resizer.Name())
		ctrl.eventRecorder.Event(pvc, v1.EventTypeWarning, util.VolumeResizeFailed, msg)
		return fmt.Errorf(msg)
	}

	// Record an event to indicate that external resizer is resizing this volume.
	ctrl.eventRecorder.Event(pvc, v1.EventTypeNormal, util.VolumeResizing,
		fmt.Sprintf("External resizer is resizing volume %s", pv.Name))

	err := func() error {
		newSize, fsResizeRequired, err := ctrl.resizeVolume(pvc, pv)
		if err != nil {
			return err
		}

		if fsResizeRequired {
			// Resize volume succeeded and need to resize file system by kubelet, mark it as file system resizing required.
			return ctrl.markPVCAsFSResizeRequired(pvc)
		}
		// Resize volume succeeded and no need to resize file system by kubelet, mark it as resizing finished.
		return ctrl.markPVCResizeFinished(pvc, newSize)
	}()

	if err != nil {
		// Record an event to indicate that resize operation is failed.
		ctrl.eventRecorder.Eventf(pvc, v1.EventTypeWarning, util.VolumeResizeFailed, err.Error())
	}
	return err
}

// resizeVolume resize the volume to request size, and update PV's capacity if succeeded.
func (ctrl *resizeController) resizeVolume(
	pvc *v1.PersistentVolumeClaim,
	pv *v1.PersistentVolume) (resource.Quantity, bool, error) {

	// before trying expansion we will remove the PVC from map
	// that tracks PVCs which can't be expanded when in-use. If
	// pvc indeed can not be expanded when in-use then it will be added
	// back when expansion fails with in-use error.
	ctrl.usedPVCs.removePVCWithInUseError(pvc)

	requestSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]

	newSize, fsResizeRequired, err := ctrl.resizer.Resize(pv, requestSize)

	if err != nil {
		// if this error was a in-use error then it must be tracked so as we don't retry without
		// first verifying if volume is in-use
		if inUseError(err) {
			ctrl.usedPVCs.addPVCWithInUseError(pvc)
		}
		return newSize, fsResizeRequired, fmt.Errorf("resize volume %q by resizer %q failed: %v", pv.Name, ctrl.name, err)
	}
	klog.V(4).InfoS("Resize volume succeeded start to update PV's capacity", "PV", klog.KObj(pv))

	_, err = ctrl.updatePVCapacity(pv, pvc.Status.Capacity[v1.ResourceStorage], newSize, fsResizeRequired)
	if err != nil {
		return newSize, fsResizeRequired, err
	}
	klog.V(4).InfoS("Update capacity succeeded", "PV", klog.KObj(pv), "capacity", newSize.String())

	return newSize, fsResizeRequired, nil
}

func (ctrl *resizeController) markPVCAsFSResizeRequired(pvc *v1.PersistentVolumeClaim) error {
	pvcCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimFileSystemResizePending,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
		Message:            "Waiting for user to (re-)start a pod to finish file system resize of volume on node.",
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions,
		[]v1.PersistentVolumeClaimCondition{pvcCondition})

	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)
	if err != nil {
		return fmt.Errorf("Mark PVC %q as file system resize required failed: %v", klog.KObj(pvc), err)
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}

	klog.V(4).InfoS("Mark PVC as file system resize required", "PVC", klog.KObj(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal,
		util.FileSystemResizeRequired, "Require file system resize of volume on node")

	return nil
}

// legacy markPVCResizeInProgress function, should be removed once RecoverFromVolumeExpansionFailure feature goes GA.
func (ctrl *resizeController) markPVCResizeInProgress(pvc *v1.PersistentVolumeClaim) (*v1.PersistentVolumeClaim, error) {
	// Mark PVC as Resize Started
	progressCondition := v1.PersistentVolumeClaimCondition{
		Type:               v1.PersistentVolumeClaimResizing,
		Status:             v1.ConditionTrue,
		LastTransitionTime: metav1.Now(),
	}
	newPVC := pvc.DeepCopy()
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(newPVC.Status.Conditions,
		[]v1.PersistentVolumeClaimCondition{progressCondition})

	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)
	if err != nil {
		return updatedPVC, fmt.Errorf("Mark PVC %q as resize as in progress failed: %v", klog.KObj(pvc), err)
	}
	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return updatedPVC, fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}
	return updatedPVC, err
}

func (ctrl *resizeController) markPVCResizeFinished(
	pvc *v1.PersistentVolumeClaim,
	newSize resource.Quantity) error {
	newPVC := pvc.DeepCopy()
	newPVC.Status.Capacity[v1.ResourceStorage] = newSize
	newPVC.Status.Conditions = util.MergeResizeConditionsOfPVC(pvc.Status.Conditions, []v1.PersistentVolumeClaimCondition{})

	updatedPVC, err := util.PatchClaim(ctrl.kubeClient, pvc, newPVC, true /* addResourceVersionCheck */)
	if err != nil {
		return fmt.Errorf("Mark PVC %q as resize finished failed: %v", klog.KObj(pvc), err)
	}

	err = ctrl.claims.Update(updatedPVC)
	if err != nil {
		return fmt.Errorf("error updating PVC %s in local cache: %v", klog.KObj(newPVC), err)
	}

	klog.V(4).InfoS("Resize PVC finished", "PVC", klog.KObj(pvc))
	ctrl.eventRecorder.Eventf(pvc, v1.EventTypeNormal, util.VolumeResizeSuccess, "Resize volume succeeded")

	return nil
}

func (ctrl *resizeController) deletePreResizeCapAnnotation(pv *v1.PersistentVolume) error {
	// if the pv does not have a resize annotation skip the entire process
	if !metav1.HasAnnotation(pv.ObjectMeta, util.AnnPreResizeCapacity) {
		return nil
	}
	pvClone := pv.DeepCopy()
	delete(pvClone.ObjectMeta.Annotations, util.AnnPreResizeCapacity)

	_, err := ctrl.patchPersistentVolume(pv, pvClone)
	return err
}

func (ctrl *resizeController) updatePVCapacity(
	pv *v1.PersistentVolume,
	oldCapacity, newCapacity resource.Quantity,
	fsResizeRequired bool) (*v1.PersistentVolume, error) {

	klog.V(4).InfoS("Resize volume succeeded, start to update PV's capacity", "PV", klog.KObj(pv))
	newPV := pv.DeepCopy()
	newPV.Spec.Capacity[v1.ResourceStorage] = newCapacity

	if utilfeature.DefaultFeatureGate.Enabled(features.AnnotateFsResize) && fsResizeRequired {
		// only update annotation if there already isn't one
		if !metav1.HasAnnotation(pv.ObjectMeta, util.AnnPreResizeCapacity) {
			if newPV.ObjectMeta.Annotations == nil {
				newPV.ObjectMeta.Annotations = make(map[string]string)
			}
			newPV.ObjectMeta.Annotations[util.AnnPreResizeCapacity] = oldCapacity.String()
		}
	}

	updatedPV, err := ctrl.patchPersistentVolume(pv, newPV)
	if err != nil {
		return pv, fmt.Errorf("updating capacity of PV %q to %s failed: %v", pv.Name, newCapacity.String(), err)
	}
	return updatedPV, nil
}

func parsePod(obj interface{}) *v1.Pod {
	if obj == nil {
		return nil
	}
	pod, ok := obj.(*v1.Pod)
	if !ok {
		staleObj, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("couldn't get stale object %#v", obj))
			return nil
		}
		pod, ok = staleObj.Obj.(*v1.Pod)
		if !ok {
			utilruntime.HandleError(fmt.Errorf("stale object is not a Pod %#v", obj))
			return nil
		}
	}
	return pod
}

func (ctrl *resizeController) patchPersistentVolume(oldPV, newPV *v1.PersistentVolume) (*v1.PersistentVolume, error) {
	patchBytes, err := util.GetPatchData(oldPV, newPV)
	if err != nil {
		return nil, fmt.Errorf("can't update capacity of PV %s as generate path data failed: %v", newPV.Name, err)
	}
	updatedPV, updateErr := ctrl.kubeClient.CoreV1().PersistentVolumes().Patch(context.TODO(), newPV.Name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if updateErr != nil {
		return nil, fmt.Errorf("update capacity of PV %s failed: %v", newPV.Name, updateErr)
	}
	err = ctrl.volumes.Update(updatedPV)
	if err != nil {
		return nil, fmt.Errorf("error updating PV %s in local cache: %v", newPV.Name, err)
	}
	return updatedPV, nil
}

func inUseError(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		// not a grpc error
		return false
	}
	// if this is a failed precondition error then that means driver does not support expansion
	// of in-use volumes
	// More info - https://github.com/container-storage-interface/spec/blob/master/spec.md#controllerexpandvolume-errors
	if st.Code() == codes.FailedPrecondition {
		return true
	}
	return false
}
