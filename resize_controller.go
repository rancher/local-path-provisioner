package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// ResizeController watches for PVC resize requests and handles them.
// It supports two resize directions:
//   - Expand: detected when pvc.spec.resources.requests.storage > pv.spec.capacity.storage
//   - Shrink: detected when annotation "local.path.provisioner/requested-size" is set on the PVC
type ResizeController struct {
	ctx             context.Context
	kubeClient      *clientset.Clientset
	provisioner     *LocalPathProvisioner
	provisionerName string
	queue           workqueue.TypedRateLimitingInterface[string]
}

// NewResizeController creates a new resize controller.
func NewResizeController(ctx context.Context, kubeClient *clientset.Clientset, provisioner *LocalPathProvisioner, provisionerName string) *ResizeController {
	return &ResizeController{
		ctx:             ctx,
		kubeClient:      kubeClient,
		provisioner:     provisioner,
		provisionerName: provisionerName,
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: "resize"},
		),
	}
}

// Run starts the resize controller with PVC informer and workers.
func (rc *ResizeController) Run(ctx context.Context) {
	defer rc.queue.ShutDown()

	factory := informers.NewSharedInformerFactory(rc.kubeClient, 30*time.Second)
	pvcInformer := factory.Core().V1().PersistentVolumeClaims().Informer()

	pvcInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: rc.handlePVCUpdate,
	})

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	logrus.Info("Resize controller started")

	// Run startup reconciliation
	rc.reconcileOnStartup()

	// Start worker
	go rc.runWorker(ctx)

	<-ctx.Done()
	logrus.Info("Resize controller stopped")
}

// reconcileOnStartup scans all bound PVCs and checks for pending resize operations.
// This handles the case where the provisioner crashed mid-resize.
func (rc *ResizeController) reconcileOnStartup() {
	pvcList, err := rc.kubeClient.CoreV1().PersistentVolumeClaims("").List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		logrus.Warnf("Resize controller: failed to list PVCs for reconciliation: %v", err)
		return
	}

	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if pvc.Status.Phase != v1.ClaimBound || pvc.Spec.VolumeName == "" {
			continue
		}

		pv, err := rc.kubeClient.CoreV1().PersistentVolumes().Get(context.TODO(), pvc.Spec.VolumeName, metav1.GetOptions{})
		if err != nil {
			continue
		}
		if !rc.isOurPV(pv) {
			continue
		}

		// Check for pending expand
		pvcSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
		pvCapacity := pv.Spec.Capacity[v1.ResourceStorage]
		if pvcSize.Cmp(pvCapacity) > 0 {
			key := pvc.Namespace + "/" + pvc.Name
			logrus.Infof("Resize controller: reconciling pending expand for %s (%v -> %v)", key, pvCapacity.String(), pvcSize.String())
			rc.queue.Add(key)
			continue
		}

		// Check for pending shrink annotation
		if _, ok := pvc.Annotations[annRequestedSize]; ok {
			key := pvc.Namespace + "/" + pvc.Name
			logrus.Infof("Resize controller: reconciling pending shrink for %s", key)
			rc.queue.Add(key)
		}
	}
}

func (rc *ResizeController) runWorker(ctx context.Context) {
	for rc.processNextItem(ctx) {
	}
}

func (rc *ResizeController) processNextItem(ctx context.Context) bool {
	key, shutdown := rc.queue.Get()
	if shutdown {
		return false
	}
	defer rc.queue.Done(key)

	err := rc.processResize(ctx, key)
	if err != nil {
		logrus.Errorf("Resize controller: error processing %s: %v", key, err)
		rc.queue.AddRateLimited(key)
		return true
	}

	rc.queue.Forget(key)
	return true
}

// handlePVCUpdate is the informer callback for PVC updates.
func (rc *ResizeController) handlePVCUpdate(oldObj, newObj interface{}) {
	pvc, ok := newObj.(*v1.PersistentVolumeClaim)
	if !ok {
		return
	}
	oldPvc, ok := oldObj.(*v1.PersistentVolumeClaim)
	if !ok {
		return
	}

	// Only process bound PVCs
	if pvc.Status.Phase != v1.ClaimBound || pvc.Spec.VolumeName == "" {
		return
	}

	key := pvc.Namespace + "/" + pvc.Name

	// Check for expand: spec size changed upward
	newSpecSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]
	oldSpecSize := oldPvc.Spec.Resources.Requests[v1.ResourceStorage]
	if newSpecSize.Cmp(oldSpecSize) > 0 {
		logrus.Infof("Resize controller: detected expand request for %s (%v -> %v)", key, oldSpecSize.String(), newSpecSize.String())
		rc.queue.Add(key)
		return
	}

	// Check for shrink: annotation changed
	newRequestedSize := pvc.Annotations[annRequestedSize]
	oldRequestedSize := oldPvc.Annotations[annRequestedSize]
	if newRequestedSize != "" && newRequestedSize != oldRequestedSize {
		logrus.Infof("Resize controller: detected shrink request for %s (requested-size=%s)", key, newRequestedSize)
		rc.queue.Add(key)
		return
	}
}

// processResize handles a resize request for a single PVC.
func (rc *ResizeController) processResize(ctx context.Context, key string) error {
	parts := strings.SplitN(key, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid key %q", key)
	}
	namespace, name := parts[0], parts[1]

	// Fresh read of PVC and PV
	pvc, err := rc.kubeClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PVC %s: %v", key, err)
	}
	if pvc.Status.Phase != v1.ClaimBound || pvc.Spec.VolumeName == "" {
		return nil // not bound, skip
	}

	pv, err := rc.kubeClient.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get PV %s: %v", pvc.Spec.VolumeName, err)
	}
	if !rc.isOurPV(pv) {
		return nil // not our PV
	}

	pvCapacity := pv.Spec.Capacity[v1.ResourceStorage]
	pvcSpecSize := pvc.Spec.Resources.Requests[v1.ResourceStorage]

	// Determine resize direction
	requestedSizeStr, hasShrinkAnnotation := pvc.Annotations[annRequestedSize]

	if hasShrinkAnnotation && requestedSizeStr != "" {
		// Shrink path
		requestedSize, err := resource.ParseQuantity(requestedSizeStr)
		if err != nil {
			return rc.setResizeStatus(pvc, "failed", fmt.Sprintf("invalid requested-size %q: %v", requestedSizeStr, err))
		}
		if requestedSize.Cmp(pvCapacity) == 0 {
			// PV already at requested size — previous shrink succeeded but cleanup failed.
			// Just clean up the annotation.
			logrus.Infof("Resize controller: PV %s already at requested size %v, cleaning up shrink annotation", pv.Name, requestedSize.String())
			if err := rc.updatePVCStatusCapacity(ctx, pvc, requestedSize); err != nil {
				logrus.Warnf("Resize controller: failed to update PVC status capacity: %v", err)
			}
			if err := rc.cleanShrinkAnnotation(ctx, pvc); err != nil {
				logrus.Warnf("Resize controller: failed to clean shrink annotation: %v", err)
			}
			rc.emitEvent(pvc, v1.EventTypeNormal, "ResizeSuccessful",
				fmt.Sprintf("Volume shrunk to %v (recovered from partial completion)", requestedSize.String()))
			return nil
		}
		if requestedSize.Cmp(pvCapacity) > 0 {
			// Not actually a shrink — requested size is > current capacity
			return rc.setResizeStatus(pvc, "failed", fmt.Sprintf("requested size %v is not smaller than current capacity %v", requestedSize.String(), pvCapacity.String()))
		}
		return rc.processShrink(ctx, pvc, pv, requestedSize)
	}

	if pvcSpecSize.Cmp(pvCapacity) > 0 {
		// Expand path
		return rc.processExpand(ctx, pvc, pv, pvcSpecSize)
	}

	return nil // nothing to do
}

// processExpand handles volume expansion.
func (rc *ResizeController) processExpand(ctx context.Context, pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume, newSize resource.Quantity) error {
	oldSize := pv.Spec.Capacity[v1.ResourceStorage]
	node := pv.Annotations[nodeNameAnnotationKey]
	volumePath := rc.extractPath(pv)
	basePath := filepath.Dir(volumePath)
	quotaType := pv.Annotations[quotaTypeAnnotationKey]
	storageClassName := pv.Spec.StorageClassName

	logrus.Infof("Resize controller: expanding %s/%s from %v to %v", pvc.Namespace, pvc.Name, oldSize.String(), newSize.String())

	// Get StorageClass config for validation
	sc, err := rc.kubeClient.StorageV1().StorageClasses().Get(ctx, storageClassName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get StorageClass %s: %v", storageClassName, err)
	}

	cfg, err := rc.provisioner.pickConfig(storageClassName)
	if err != nil {
		return fmt.Errorf("failed to get provisioner config for SC %s: %v", storageClassName, err)
	}

	// Validate maxSize
	maxSize := cfg.MaxSize
	if sc.Parameters != nil {
		if v, ok := sc.Parameters["maxSize"]; ok {
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return fmt.Errorf("invalid StorageClass parameter maxSize %q: %v", v, err)
			}
			maxSize = &q
		}
	}
	if maxSize != nil && newSize.Cmp(*maxSize) > 0 {
		logrus.Warnf("Resize controller: expand rejected for %s/%s — requested size %v exceeds maxSize %v", pvc.Namespace, pvc.Name, newSize.String(), maxSize.String())
		rc.emitEvent(pvc, v1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("Expand rejected: requested size %v exceeds StorageClass maxSize %v", newSize.String(), maxSize.String()))
		return nil // don't retry — user must fix the request
	}

	// Validate maxCapacity budget via atomic TryResize
	pathCfg := rc.getPathConfig(cfg, node, basePath)
	var maxCapacity *resource.Quantity
	if pathCfg != nil {
		maxCapacity = pathCfg.MaxCapacity
	}
	if !rc.provisioner.capacityTracker.TryResize(node, basePath, oldSize.Value(), newSize.Value(), maxCapacity) {
		logrus.Warnf("Resize controller: expand rejected for %s/%s — would exceed path capacity budget", pvc.Namespace, pvc.Name)
		rc.emitEvent(pvc, v1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("Expand rejected: expanding to %v would exceed path capacity budget on %s:%s", newSize.String(), node, basePath))
		return nil // don't retry
	}
	// TryResize already updated the tracker; rollback on failure
	rollbackTracker := true
	defer func() {
		if rollbackTracker {
			rc.provisioner.capacityTracker.TryResize(node, basePath, newSize.Value(), oldSize.Value(), nil)
		}
	}()

	// Run resize helper pod to update XFS quota (if enabled)
	if err := rc.runResizeHelperPod(pv, newSize.Value(), quotaType, cfg); err != nil {
		rc.emitEvent(pvc, v1.EventTypeWarning, "ResizeFailed",
			fmt.Sprintf("Failed to run resize helper pod: %v", err))
		return err // retry
	}

	// Update PV capacity
	pvCopy := pv.DeepCopy()
	pvCopy.Spec.Capacity[v1.ResourceStorage] = newSize
	if _, err := rc.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pvCopy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update PV %s capacity: %v", pv.Name, err)
	}

	// Update PVC status capacity
	if err := rc.updatePVCStatusCapacity(ctx, pvc, newSize); err != nil {
		return fmt.Errorf("failed to update PVC %s/%s status capacity: %v", pvc.Namespace, pvc.Name, err)
	}

	rollbackTracker = false
	logrus.Infof("Resize controller: successfully expanded %s/%s to %v", pvc.Namespace, pvc.Name, newSize.String())
	rc.emitEvent(pvc, v1.EventTypeNormal, "ResizeSuccessful",
		fmt.Sprintf("Volume expanded from %v to %v", oldSize.String(), newSize.String()))
	return nil
}

// processShrink handles volume shrinking via annotation.
func (rc *ResizeController) processShrink(ctx context.Context, pvc *v1.PersistentVolumeClaim, pv *v1.PersistentVolume, newSize resource.Quantity) error {
	oldSize := pv.Spec.Capacity[v1.ResourceStorage]
	node := pv.Annotations[nodeNameAnnotationKey]
	volumePath := rc.extractPath(pv)
	basePath := filepath.Dir(volumePath)
	quotaType := pv.Annotations[quotaTypeAnnotationKey]
	storageClassName := pv.Spec.StorageClassName

	logrus.Infof("Resize controller: shrinking %s/%s from %v to %v", pvc.Namespace, pvc.Name, oldSize.String(), newSize.String())

	// Get StorageClass
	sc, err := rc.kubeClient.StorageV1().StorageClasses().Get(ctx, storageClassName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get StorageClass %s: %v", storageClassName, err)
	}

	// Check allowVolumeShrink gate
	allowShrink := false
	if sc.Parameters != nil {
		if v, ok := sc.Parameters["allowVolumeShrink"]; ok {
			allowShrink = v == "true"
		}
	}
	if !allowShrink {
		msg := fmt.Sprintf("Shrink rejected: StorageClass %q does not allow volume shrinking (set parameter allowVolumeShrink: \"true\" to enable)", storageClassName)
		logrus.Warnf("Resize controller: %s", msg)
		return rc.setResizeStatus(pvc, "failed", msg)
	}

	cfg, err := rc.provisioner.pickConfig(storageClassName)
	if err != nil {
		return fmt.Errorf("failed to get provisioner config for SC %s: %v", storageClassName, err)
	}

	// Validate minSize
	minSize := cfg.MinSize
	if sc.Parameters != nil {
		if v, ok := sc.Parameters["minSize"]; ok {
			q, err := resource.ParseQuantity(v)
			if err != nil {
				return rc.setResizeStatus(pvc, "failed", fmt.Sprintf("invalid StorageClass parameter minSize %q: %v", v, err))
			}
			minSize = &q
		}
	}
	if minSize != nil && newSize.Cmp(*minSize) < 0 {
		msg := fmt.Sprintf("Shrink rejected: requested size %v is below StorageClass minSize %v", newSize.String(), minSize.String())
		logrus.Warnf("Resize controller: %s", msg)
		return rc.setResizeStatus(pvc, "failed", msg)
	}

	// Set in-progress status
	if err := rc.setResizeStatus(pvc, "in-progress", "Checking disk usage..."); err != nil {
		return err
	}

	// Check actual disk usage via helper pod
	usageBytes, err := rc.checkDiskUsage(pv, quotaType, cfg)
	if err != nil {
		return rc.setResizeStatus(pvc, "failed", fmt.Sprintf("Failed to check disk usage: %v", err))
	}
	if usageBytes > newSize.Value() {
		msg := fmt.Sprintf("Shrink rejected: current disk usage (%s) exceeds requested size %v",
			resource.NewQuantity(usageBytes, resource.BinarySI).String(), newSize.String())
		logrus.Warnf("Resize controller: %s for %s/%s", msg, pvc.Namespace, pvc.Name)
		return rc.setResizeStatus(pvc, "failed", msg)
	}

	// Run resize helper pod to update XFS quota
	if err := rc.runResizeHelperPod(pv, newSize.Value(), quotaType, cfg); err != nil {
		return rc.setResizeStatus(pvc, "failed", fmt.Sprintf("Failed to run resize helper pod: %v", err))
	}

	// Update capacity tracker
	pathCfg := rc.getPathConfig(cfg, node, basePath)
	var maxCapacity *resource.Quantity
	if pathCfg != nil {
		maxCapacity = pathCfg.MaxCapacity
	}
	rc.provisioner.capacityTracker.TryResize(node, basePath, oldSize.Value(), newSize.Value(), maxCapacity)

	// Update PV capacity
	pvCopy := pv.DeepCopy()
	pvCopy.Spec.Capacity[v1.ResourceStorage] = newSize
	if _, err := rc.kubeClient.CoreV1().PersistentVolumes().Update(ctx, pvCopy, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update PV %s capacity: %v", pv.Name, err)
	}

	// Update PVC status capacity
	if err := rc.updatePVCStatusCapacity(ctx, pvc, newSize); err != nil {
		return fmt.Errorf("failed to update PVC %s/%s status capacity: %v", pvc.Namespace, pvc.Name, err)
	}

	// Clean up shrink annotation and set completed status
	if err := rc.cleanShrinkAnnotation(ctx, pvc); err != nil {
		logrus.Warnf("Resize controller: failed to clean shrink annotation on %s/%s: %v", pvc.Namespace, pvc.Name, err)
	}

	logrus.Infof("Resize controller: successfully shrunk %s/%s to %v", pvc.Namespace, pvc.Name, newSize.String())
	rc.emitEvent(pvc, v1.EventTypeNormal, "ResizeSuccessful",
		fmt.Sprintf("Volume shrunk from %v to %v", oldSize.String(), newSize.String()))
	return nil
}

// runResizeHelperPod runs the resize helper pod to update XFS quota limits.
func (rc *ResizeController) runResizeHelperPod(pv *v1.PersistentVolume, newSizeBytes int64, quotaType string, cfg *StorageClassConfig) error {
	volumePath := rc.extractPath(pv)
	node := pv.Annotations[nodeNameAnnotationKey]

	resizeCmd := make([]string, 0, 2)
	if rc.provisioner.config.ResizeCommand == "" {
		resizeCmd = append(resizeCmd, "/bin/sh", "/script/resize")
	} else {
		resizeCmd = append(resizeCmd, rc.provisioner.config.ResizeCommand)
	}

	return rc.provisioner.createHelperPod(ActionTypeResize, resizeCmd, volumeOptions{
		Name:        pv.Name,
		Path:        volumePath,
		Mode:        v1.PersistentVolumeFilesystem,
		SizeInBytes: newSizeBytes,
		Node:        node,
		QuotaType:   quotaType,
	}, cfg)
}

// checkDiskUsage runs a helper pod in check-usage mode to determine actual disk usage.
// It uses createHelperPodWithOutput to capture the USAGE_BYTES=N output.
func (rc *ResizeController) checkDiskUsage(pv *v1.PersistentVolume, quotaType string, cfg *StorageClassConfig) (int64, error) {
	volumePath := rc.extractPath(pv)
	node := pv.Annotations[nodeNameAnnotationKey]

	checkCmd := []string{"/bin/sh", "/script/resize", "check-usage"}

	output, err := rc.provisioner.createHelperPodWithOutput("check-usage", checkCmd, volumeOptions{
		Name:        pv.Name + "-chk",
		Path:        volumePath,
		Mode:        v1.PersistentVolumeFilesystem,
		SizeInBytes: 0,
		Node:        node,
		QuotaType:   quotaType,
	}, cfg)
	if err != nil {
		return 0, fmt.Errorf("failed to run check-usage helper pod: %v", err)
	}

	usageBytes, err := parseUsageBytes(output)
	if err != nil {
		return 0, fmt.Errorf("failed to parse check-usage output: %v (output: %q)", err, output)
	}
	logrus.Infof("Resize controller: disk usage for %s is %d bytes", pv.Name, usageBytes)
	return usageBytes, nil
}

// parseUsageBytes parses USAGE_BYTES=N from helper pod output.
func parseUsageBytes(output string) (int64, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "USAGE_BYTES=") {
			val := strings.TrimPrefix(line, "USAGE_BYTES=")
			return strconv.ParseInt(val, 10, 64)
		}
	}
	return 0, fmt.Errorf("USAGE_BYTES not found in output")
}

// isOurPV checks whether a PV was provisioned by this provisioner.
func (rc *ResizeController) isOurPV(pv *v1.PersistentVolume) bool {
	if pv.Annotations == nil {
		return false
	}
	// Check for our node annotation (set by provisioner)
	if _, ok := pv.Annotations[nodeNameAnnotationKey]; !ok {
		return false
	}
	// Check provisioner annotation
	provisioner, ok := pv.Annotations["pv.kubernetes.io/provisioned-by"]
	if !ok {
		return false
	}
	return provisioner == rc.provisionerName
}

// extractPath gets the volume path from a PV.
func (rc *ResizeController) extractPath(pv *v1.PersistentVolume) string {
	if pv.Spec.PersistentVolumeSource.HostPath != nil {
		return pv.Spec.PersistentVolumeSource.HostPath.Path
	}
	if pv.Spec.PersistentVolumeSource.Local != nil {
		return pv.Spec.PersistentVolumeSource.Local.Path
	}
	return ""
}

// getPathConfig looks up the PathConfig for a given node and basePath.
func (rc *ResizeController) getPathConfig(cfg *StorageClassConfig, node, basePath string) *PathConfig {
	npMap := cfg.NodePathMap[node]
	if npMap == nil {
		npMap = cfg.NodePathMap[NodeDefaultNonListedNodes]
	}
	if npMap == nil {
		return nil
	}
	return npMap.Paths[basePath]
}

// updatePVCStatusCapacity updates the PVC status to reflect the new capacity.
func (rc *ResizeController) updatePVCStatusCapacity(ctx context.Context, pvc *v1.PersistentVolumeClaim, newSize resource.Quantity) error {
	pvcCopy := pvc.DeepCopy()
	if pvcCopy.Status.Capacity == nil {
		pvcCopy.Status.Capacity = v1.ResourceList{}
	}
	pvcCopy.Status.Capacity[v1.ResourceStorage] = newSize

	// Remove FileSystemResizePending condition if present (expand is complete)
	newConditions := make([]v1.PersistentVolumeClaimCondition, 0, len(pvcCopy.Status.Conditions))
	for _, cond := range pvcCopy.Status.Conditions {
		if cond.Type != v1.PersistentVolumeClaimFileSystemResizePending {
			newConditions = append(newConditions, cond)
		}
	}
	pvcCopy.Status.Conditions = newConditions

	_, err := rc.kubeClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).UpdateStatus(ctx, pvcCopy, metav1.UpdateOptions{})
	return err
}

// setResizeStatus updates the resize status annotations on the PVC.
// Returns nil if successful, or the first error encountered.
func (rc *ResizeController) setResizeStatus(pvc *v1.PersistentVolumeClaim, status, message string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s","%s":"%s"}}}`,
		annResizeStatus, status, annResizeMessage, escapeJSONString(message))
	_, err := rc.kubeClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Patch(
		context.TODO(), pvc.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		logrus.Errorf("Resize controller: failed to set resize status on %s/%s: %v", pvc.Namespace, pvc.Name, err)
	}
	return err
}

// cleanShrinkAnnotation removes the requested-size annotation and sets status to completed.
func (rc *ResizeController) cleanShrinkAnnotation(ctx context.Context, pvc *v1.PersistentVolumeClaim) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":null,"%s":"completed","%s":"Volume shrink completed successfully"}}}`,
		annRequestedSize, annResizeStatus, annResizeMessage)
	_, err := rc.kubeClient.CoreV1().PersistentVolumeClaims(pvc.Namespace).Patch(
		ctx, pvc.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	return err
}

// emitEvent creates a Kubernetes event on the PVC.
func (rc *ResizeController) emitEvent(pvc *v1.PersistentVolumeClaim, eventType, reason, message string) {
	event := &v1.Event{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: pvc.Name + "-",
			Namespace:    pvc.Namespace,
		},
		InvolvedObject: v1.ObjectReference{
			Kind:       "PersistentVolumeClaim",
			Namespace:  pvc.Namespace,
			Name:       pvc.Name,
			UID:        pvc.UID,
			APIVersion: "v1",
		},
		Reason:  reason,
		Message: message,
		Type:    eventType,
		Source: v1.EventSource{
			Component: "local-path-provisioner-resize",
		},
		FirstTimestamp: metav1.Now(),
		LastTimestamp:  metav1.Now(),
		Count:         1,
	}
	if _, err := rc.kubeClient.CoreV1().Events(pvc.Namespace).Create(context.TODO(), event, metav1.CreateOptions{}); err != nil {
		logrus.Warnf("Resize controller: failed to emit event for %s/%s: %v", pvc.Namespace, pvc.Name, err)
	}
}

// escapeJSONString escapes special characters in a string for JSON embedding.
func escapeJSONString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
