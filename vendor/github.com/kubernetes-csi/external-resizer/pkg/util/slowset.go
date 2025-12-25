/*
Copyright 2024 The Kubernetes Authors.

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
	"sync"
	"time"
)

// SlowSet is a set of API objects that should be synced at slower rate. Key is typically the object
// namespace + name and value is timestamp when the object was added to the set.
type SlowSet struct {
	sync.RWMutex
	// retentionTime is the time after which an item will be removed from the set
	// this indicates, how long before an operation on pvc can be retried.
	retentionTime time.Duration

	resyncPeriod time.Duration
	workSet      map[string]time.Time
}

func NewSlowSet(retTime time.Duration) *SlowSet {
	return &SlowSet{
		retentionTime: retTime,
		resyncPeriod:  100 * time.Millisecond,
		workSet:       make(map[string]time.Time),
	}
}

func (s *SlowSet) Add(key string) bool {
	s.Lock()
	defer s.Unlock()

	if _, ok := s.workSet[key]; ok {
		return false
	}

	s.workSet[key] = time.Now()
	return true
}

func (s *SlowSet) Contains(key string) bool {
	s.RLock()
	defer s.RUnlock()

	startTime, ok := s.workSet[key]
	if ok && time.Since(startTime) < s.retentionTime {
		return true
	}
	return false
}

func (s *SlowSet) Remove(key string) {
	s.Lock()
	defer s.Unlock()

	delete(s.workSet, key)
}

func (s *SlowSet) TimeRemaining(key string) time.Duration {
	s.RLock()
	defer s.RUnlock()

	if startTime, ok := s.workSet[key]; ok {
		return s.retentionTime - time.Since(startTime)
	}
	return 0
}

func (s *SlowSet) removeAllExpired() {
	s.Lock()
	defer s.Unlock()
	for key, t := range s.workSet {
		if time.Since(t) > s.retentionTime {
			delete(s.workSet, key)
		}
	}
}

func (s *SlowSet) Run(stopCh <-chan struct{}) {
	ticker := time.NewTicker(s.resyncPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			s.removeAllExpired()
		}
	}
}
