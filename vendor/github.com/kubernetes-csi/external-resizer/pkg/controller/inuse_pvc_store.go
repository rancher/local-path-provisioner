/*
Copyright 2020 The Kubernetes Authors.

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
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// UniquePodName defines the type to key pods off of
type UniquePodName types.UID

// UniquePVCName defines the type to key pvc off
type UniquePVCName types.UID

type inUsePVCStore struct {
	// map of pvc unique name and number of pods using it.
	inUsePVC map[string]map[UniquePodName]UniquePodName
	// map of PVC unique name and whether in-use error has been triggered for it.
	inUseErrors map[UniquePVCName]bool
	sync.RWMutex
}

func newUsedPVCStore() *inUsePVCStore {
	return &inUsePVCStore{
		inUseErrors: map[UniquePVCName]bool{},
		inUsePVC:    map[string]map[UniquePodName]UniquePodName{},
	}
}

func (store *inUsePVCStore) addPod(pod *v1.Pod) {
	store.Lock()
	defer store.Unlock()

	if isPodTerminated(pod) {
		return
	}

	// add all PVCs that the pod uses
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvcNameUniqueName := pod.Namespace + "/" + volume.PersistentVolumeClaim.ClaimName

			podsUsingPVC, ok := store.inUsePVC[pvcNameUniqueName]
			if !ok {
				podsUsingPVC = map[UniquePodName]UniquePodName{}
			}
			podUID := UniquePodName(pod.UID)
			podsUsingPVC[podUID] = podUID
			store.inUsePVC[pvcNameUniqueName] = podsUsingPVC
		}
	}
}

func (store *inUsePVCStore) removePod(pod *v1.Pod) {
	store.Lock()
	defer store.Unlock()

	podUID := UniquePodName(pod.UID)

	// remove all PVCs that this pod was using
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			pvcNameUniqueName := pod.Namespace + "/" + volume.PersistentVolumeClaim.ClaimName

			podsUsingPVC, ok := store.inUsePVC[pvcNameUniqueName]
			if !ok {
				continue
			}
			delete(podsUsingPVC, podUID)
			if len(podsUsingPVC) == 0 {
				delete(store.inUsePVC, pvcNameUniqueName)
			} else {
				store.inUsePVC[pvcNameUniqueName] = podsUsingPVC
			}
		}
	}
}

func (store *inUsePVCStore) checkForUse(pvc *v1.PersistentVolumeClaim) bool {
	store.RLock()
	defer store.RUnlock()

	pvcNameUniqueName := pvc.Namespace + "/" + pvc.Name
	if _, ok := store.inUsePVC[pvcNameUniqueName]; ok {
		return true
	}
	return false
}

func (store *inUsePVCStore) hasInUseErrors(pvc *v1.PersistentVolumeClaim) bool {
	store.RLock()
	defer store.RUnlock()

	hasErrors, ok := store.inUseErrors[UniquePVCName(pvc.UID)]
	if ok {
		return hasErrors
	}
	return false
}

func (store *inUsePVCStore) addPVCWithInUseError(pvc *v1.PersistentVolumeClaim) {
	store.Lock()
	defer store.Unlock()
	pvcUID := UniquePVCName(pvc.UID)
	store.inUseErrors[pvcUID] = true
}

func (store *inUsePVCStore) removePVCWithInUseError(pvc *v1.PersistentVolumeClaim) {
	store.Lock()
	defer store.Unlock()

	delete(store.inUseErrors, UniquePVCName(pvc.UID))
}

// isPodTerminated checks if pod is terminated
func isPodTerminated(pod *v1.Pod) bool {
	podStatus := pod.Status
	return podStatus.Phase == v1.PodFailed || podStatus.Phase == v1.PodSucceeded
}
