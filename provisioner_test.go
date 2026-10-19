package main

import (
	"sync"
	"testing"
)

// An empty string in a "paths" list must be rejected with an error, not
// crash the provisioner.
func TestCanonicalizeConfigEmptyPathDoesNotPanic(t *testing.T) {
	data, err := unmarshalFromString(`{
		"nodePathMap": [],
		"storageClassConfigs": {
			"node02-zfs-retain": {
				"nodePathMap": [
					{"node": "DEFAULT_PATH_FOR_NON_LISTED_NODES", "paths": []},
					{"node": "kube-node-02", "paths": [""]}
				]
			}
		}
	}`)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var cfgErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("canonicalizeConfig panicked on empty path: %v", r)
			}
		}()
		_, cfgErr = canonicalizeConfig(data)
	}()
	if cfgErr == nil {
		t.Fatalf("expected an error for an empty path, got nil")
	}
	t.Logf("got expected error: %v", cfgErr)
}

// A null entry in nodePathMap must be rejected with an error, not crash.
func TestCanonicalizeConfigNullNodeEntryDoesNotPanic(t *testing.T) {
	data, err := unmarshalFromString(`{"nodePathMap": [null]}`)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var cfgErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("canonicalizeConfig panicked on null nodePathMap entry: %v", r)
			}
		}()
		_, cfgErr = canonicalizeConfig(data)
	}()
	if cfgErr == nil {
		t.Fatalf("expected an error for a null nodePathMap entry, got nil")
	}
	t.Logf("got expected error: %v", cfgErr)
}

// README "Reloading": "If the reload fails, the provisioner will log the error
// and continue using the last valid configuration". A reload that introduces
// an empty path instead panics inside the config-watcher goroutine, which
// takes down the whole provisioner (and it then crash-loops on startup).
func TestRefreshConfigKeepsLastValidConfigOnEmptyPath(t *testing.T) {
	p := &LocalPathProvisioner{
		configFile:  `{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":["/opt/local-path-provisioner"]}]}`,
		configMutex: &sync.RWMutex{},
	}
	if err := p.refreshConfig(); err != nil {
		t.Fatalf("initial refreshConfig: %v", err)
	}
	good := p.config

	p.configFile = `{"nodePathMap":[{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":["/opt/local-path-provisioner", ""]}]}`
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("config reload panicked (would crash the provisioner): %v", r)
			}
		}()
		err = p.refreshConfig()
	}()
	if err == nil {
		t.Fatalf("expected reload error")
	}
	if p.config != good {
		t.Fatalf("last valid config was not kept")
	}
}
