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

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	klog "k8s.io/klog/v2"
)

// VolumeStore is an interface that's used to save PersistentVolumes to API server.
// Implementation of the interface add custom error recovery policy.
// A volume is added via StoreVolume(). It's enough to store the volume only once.
// It is not possible to remove a volume, even when corresponding PVC is deleted
// and PV is not necessary any longer. PV will be always created.
// If corresponding PVC is deleted, the PV will be deleted by Kubernetes using
// standard deletion procedure. It saves us some code here.
type VolumeStore interface {
	// StoreVolume makes sure a volume is saved to Kubernetes API server.
	// If no error is returned, caller can assume that PV was saved or
	// is being saved in background.
	// In error is returned, no PV was saved and corresponding PVC needs
	// to be re-queued (so whole provisioning needs to be done again).
	StoreVolume(logger klog.Logger, claim *v1.PersistentVolumeClaim, volume *v1.PersistentVolume) error

	// Runs any background goroutines for implementation of the interface.
	Run(ctx context.Context, threadiness int)
}

// queueStore is implementation of VolumeStore that re-tries saving
// PVs to API server using a workqueue running in its own goroutine(s).
// After failed save, volume is re-qeueued with exponential backoff.
type queueStore struct {
	client        kubernetes.Interface
	queue         workqueue.RateLimitingInterface
	eventRecorder record.EventRecorder
	claimsIndexer cache.Indexer

	volumes sync.Map
}

var _ VolumeStore = &queueStore{}

// NewVolumeStoreQueue returns VolumeStore that uses asynchronous workqueue to save PVs.
func NewVolumeStoreQueue(
	client kubernetes.Interface,
	limiter workqueue.RateLimiter,
	claimsIndexer cache.Indexer,
	eventRecorder record.EventRecorder,
) VolumeStore {

	return &queueStore{
		client:        client,
		queue:         workqueue.NewNamedRateLimitingQueue(limiter, "unsavedpvs"),
		claimsIndexer: claimsIndexer,
		eventRecorder: eventRecorder,
	}
}

func (q *queueStore) StoreVolume(logger klog.Logger, _ *v1.PersistentVolumeClaim, volume *v1.PersistentVolume) error {
	if err := q.doSaveVolume(logger, volume); err != nil {
		q.volumes.Store(volume.Name, volume)
		q.queue.Add(volume.Name)
		logger.Error(err, "Failed to save volume", "volume", volume.Name)
	}
	// Consume any error, this Store will retry in background.
	return nil
}

func (q *queueStore) Run(ctx context.Context, threadiness int) {
	logger := klog.FromContext(ctx)
	logger.Info("Starting save volume queue")
	defer q.queue.ShutDown()

	for i := 0; i < threadiness; i++ {
		go wait.UntilWithContext(ctx, q.saveVolumeWorker, time.Second)
	}
	<-ctx.Done()
	logger.Info("Stopped save volume queue")
}

func (q *queueStore) saveVolumeWorker(ctx context.Context) {
	for q.processNextWorkItem(ctx) {
	}
}

func (q *queueStore) processNextWorkItem(ctx context.Context) bool {
	obj, shutdown := q.queue.Get()
	defer q.queue.Done(obj)

	if shutdown {
		return false
	}

	var volumeName string
	var ok bool
	if volumeName, ok = obj.(string); !ok {
		q.queue.Forget(obj)
		utilruntime.HandleError(fmt.Errorf("expected string in save workqueue but got %#v", obj))
		return true
	}

	volumeObj, found := q.volumes.Load(volumeName)
	if !found {
		q.queue.Forget(volumeName)
		utilruntime.HandleError(fmt.Errorf("did not find saved volume %s", volumeName))
		return true
	}

	volume, ok := volumeObj.(*v1.PersistentVolume)
	if !ok {
		q.queue.Forget(volumeName)
		utilruntime.HandleError(fmt.Errorf("saved object is not volume: %+v", volumeObj))
		return true
	}

	logger := klog.FromContext(ctx)
	if err := q.doSaveVolume(logger, volume); err != nil {
		q.queue.AddRateLimited(volumeName)
		utilruntime.HandleError(err)
		logger.V(5).Info("Volume enqueued", "volume", volume.Name)
		return true
	}
	q.volumes.Delete(volumeName)
	q.queue.Forget(volumeName)
	return true
}

func (q *queueStore) doSaveVolume(logger klog.Logger, volume *v1.PersistentVolume) error {
	logger.V(5).Info("Saving volume", "volume", volume.Name)
	_, err := q.client.CoreV1().PersistentVolumes().Create(context.Background(), volume, metav1.CreateOptions{})
	if err == nil || apierrs.IsAlreadyExists(err) {
		logger.V(5).Info("Volume saved", "volume", volume.Name)
		q.sendSuccessEvent(logger, volume)
		return nil
	}
	return fmt.Errorf("error saving volume %s: %s", volume.Name, err)
}

func (q *queueStore) sendSuccessEvent(logger klog.Logger, volume *v1.PersistentVolume) {
	claimObjs, err := q.claimsIndexer.ByIndex(uidIndex, string(volume.Spec.ClaimRef.UID))
	if err != nil {
		logger.V(2).Info("Error sending event to claim", "claimUID", volume.Spec.ClaimRef.UID, "err", err)
		return
	}
	if len(claimObjs) != 1 {
		return
	}
	claim, ok := claimObjs[0].(*v1.PersistentVolumeClaim)
	if !ok {
		return
	}
	msg := fmt.Sprintf("Successfully provisioned volume %s", volume.Name)
	q.eventRecorder.Event(claim, v1.EventTypeNormal, "ProvisioningSucceeded", msg)
}

// backoffStore is implementation of VolumeStore that blocks and tries to save
// a volume to API server with configurable backoff. If saving fails,
// StoreVolume() deletes the storage asset in the end and returns appropriate
// error code.
type backoffStore struct {
	client        kubernetes.Interface
	eventRecorder record.EventRecorder
	backoff       *wait.Backoff
	ctrl          *ProvisionController
}

var _ VolumeStore = &backoffStore{}

// NewBackoffStore returns VolumeStore that uses blocking exponential backoff to save PVs.
func NewBackoffStore(client kubernetes.Interface,
	eventRecorder record.EventRecorder,
	backoff *wait.Backoff,
	ctrl *ProvisionController,
) VolumeStore {
	return &backoffStore{
		client:        client,
		eventRecorder: eventRecorder,
		backoff:       backoff,
		ctrl:          ctrl,
	}
}

func (b *backoffStore) StoreVolume(logger klog.Logger, claim *v1.PersistentVolumeClaim, volume *v1.PersistentVolume) error {
	// Try to create the PV object several times
	var lastSaveError error
	err := wait.ExponentialBackoff(*b.backoff, func() (bool, error) {
		logger.V(4).Info("Trying to save persistentvolume", "persistentvolume", volume.Name)
		var err error
		if _, err = b.client.CoreV1().PersistentVolumes().Create(context.Background(), volume, metav1.CreateOptions{}); err == nil || apierrs.IsAlreadyExists(err) {
			// Save succeeded.
			if err != nil {
				logger.V(2).Info("Persistentvolume already exists, reusing", "persistentvolume", volume.Name)
			} else {
				logger.V(4).Info("Persistentvolume saved", "persistentvolume", volume.Name)
			}
			return true, nil
		}
		// Save failed, try again after a while.
		logger.Info("Failed to save persistentvolume", "persistentvolume", volume.Name, "err", err)
		lastSaveError = err
		return false, nil
	})

	if err == nil {
		// Save succeeded
		msg := fmt.Sprintf("Successfully provisioned volume %s", volume.Name)
		b.eventRecorder.Event(claim, v1.EventTypeNormal, "ProvisioningSucceeded", msg)
		return nil
	}

	// Save failed. Now we have a storage asset outside of Kubernetes,
	// but we don't have appropriate PV object for it.
	// Emit some event here and try to delete the storage asset several
	// times.
	logger.Error(lastSaveError, "Error creating provisioned PV object for claim. Deleting the volume.", "claim", klog.KObj(claim))
	strerr := fmt.Sprintf("Error creating provisioned PV object for claim %s: %v. Deleting the volume.", klog.KObj(claim), lastSaveError)
	b.eventRecorder.Event(claim, v1.EventTypeWarning, "ProvisioningFailed", strerr)

	var lastDeleteError error
	err = wait.ExponentialBackoff(*b.backoff, func() (bool, error) {
		if err = b.ctrl.provisioner.Delete(context.Background(), volume); err == nil {
			// Delete succeeded
			logger.V(4).Info("Cleaning volume succeeded", "volume", volume.Name)
			return true, nil
		}
		// Delete failed, try again after a while.
		logger.Info("Failed to clean volume", "volume", volume.Name, "err", err)
		lastDeleteError = err
		return false, nil
	})
	if err != nil {
		// Delete failed several times. There is an orphaned volume and there
		// is nothing we can do about it.
		logger.Error(lastSaveError, "Error cleaning provisioned volume for claim. Please delete manually.", "claim", klog.KObj(claim))
		strerr := fmt.Sprintf("Error cleaning provisioned volume for claim %s: %v. Please delete manually.", klog.KObj(claim), lastDeleteError)
		b.eventRecorder.Event(claim, v1.EventTypeWarning, "ProvisioningCleanupFailed", strerr)
	}

	return lastSaveError
}

func (b *backoffStore) Run(ctx context.Context, threadiness int) {
	// There is not background processing
}
