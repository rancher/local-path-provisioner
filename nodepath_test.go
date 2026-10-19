package main

import (
	"sync"
	"testing"
)

// The StorageClass nodePath parameter must match a configured path even
// when it is spelled differently (trailing or doubled slashes): config
// paths are normalized when the config is loaded, the parameter was not.
func TestGetPathOnNodeCleansNodePathParameter(t *testing.T) {
	p := &LocalPathProvisioner{
		configFile: `{"nodePathMap":[],"storageClassConfigs":{"local-path":{"nodePathMap":[
			{"node":"DEFAULT_PATH_FOR_NON_LISTED_NODES","paths":["/opt/local-path-provisioner/"]}]}}}`,
		configMutex: &sync.RWMutex{},
	}
	if err := p.refreshConfig(); err != nil {
		t.Fatalf("refreshConfig: %v", err)
	}
	cfg, err := p.pickConfig("local-path")
	if err != nil {
		t.Fatalf("pickConfig: %v", err)
	}

	for _, requested := range []string{
		"/opt/local-path-provisioner",
		"/opt/local-path-provisioner/",
		"/opt//local-path-provisioner",
	} {
		got, err := p.getPathOnNode("kube-node-01", requested, cfg)
		if err != nil {
			t.Errorf("nodePath %q: %v", requested, err)
			continue
		}
		if got != "/opt/local-path-provisioner" {
			t.Errorf("nodePath %q resolved to %q, want /opt/local-path-provisioner", requested, got)
		}
	}

	if _, err := p.getPathOnNode("kube-node-01", "/srv/other", cfg); err == nil {
		t.Errorf("expected an error for a path that is not in the config")
	}
}
