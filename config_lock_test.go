package main

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const lockTestConfig = `{
	"nodePathMap": [],
	"storageClassConfigs": {
		"local-path": {
			"nodePathMap": [
				{"node": "DEFAULT_PATH_FOR_NON_LISTED_NODES", "paths": ["/opt/local-path-provisioner"]}
			]
		}
	}
}`

// getPathOnNode used to take configMutex.RLock and then call
// p.isSharedFilesystem, which RLocks again. sync.RWMutex forbids recursive
// read locking: if refreshConfig (which takes configMutex.Lock every
// ConfigFileCheckInterval) queued between the two calls, every later
// provision and delete blocked forever.
func TestGetPathOnNodeDoesNotDeadlockWithConfigRefresh(t *testing.T) {
	p := &LocalPathProvisioner{
		configFile:  lockTestConfig,
		configMutex: &sync.RWMutex{},
	}
	if err := p.refreshConfig(); err != nil {
		t.Fatalf("refreshConfig: %v", err)
	}
	cfg, err := p.pickConfig("local-path")
	if err != nil {
		t.Fatalf("pickConfig: %v", err)
	}

	stop := make(chan struct{})
	defer close(stop)

	// Simulates the 30s config watcher (compressed).
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				_ = p.refreshConfig()
			}
		}
	}()

	// Simulates provisioner worker threads.
	const workers = 4
	const iterations = 20000
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				if _, err := p.getPathOnNode("kube-node-01", "", cfg); err != nil {
					t.Errorf("getPathOnNode: %v", err)
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		for _, g := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(g, "sync.(*RWMutex)") {
				t.Logf("blocked goroutine:\n%s", g)
			}
		}
		t.Fatalf("getPathOnNode deadlocked against refreshConfig (recursive RLock on configMutex)")
	}
}

// pickConfig reads p.config, which refreshConfig replaces under
// configMutex.Lock. Run with -race.
func TestPickConfigConcurrentWithConfigReloadIsRaceFree(t *testing.T) {
	p := &LocalPathProvisioner{
		configFile:  lockTestConfig,
		configMutex: &sync.RWMutex{},
	}
	if err := p.refreshConfig(); err != nil {
		t.Fatalf("refreshConfig: %v", err)
	}
	other := strings.Replace(lockTestConfig, "/opt/local-path-provisioner", "/data/local-path-provisioner", 1)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			p.configMutex.Lock()
			if i%2 == 0 {
				p.configFile = other
			} else {
				p.configFile = lockTestConfig
			}
			p.configMutex.Unlock()
			_ = p.refreshConfig()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_, _ = p.pickConfig("local-path")
		}
	}()
	wg.Wait()
}
