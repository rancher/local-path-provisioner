//go:build quota_ci_e2e

// Package test provides CI-runnable e2e tests for XFS project quota enforcement
// using Kind clusters with loopback XFS filesystems.
//
// Prerequisites (handled by the GitHub Actions workflow):
//   - A loopback XFS image mounted at /tmp/xfs-mount with pquota
//   - Empty files at /tmp/xfs-mount-projects and /tmp/xfs-mount-projid
//   - Docker images built: local-path-provisioner:dev, local-path-quota-helper:dev
//   - Kind installed
//
// Run:
//
//	PROVISIONER_IMAGE=local-path-provisioner:dev \
//	QUOTA_HELPER_IMAGE=local-path-quota-helper:dev \
//	go test -tags quota_ci_e2e -v -timeout 20m ./test/
package test

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/kelseyhightower/envconfig"
)

// ===================== TestMain (one-time setup / teardown) =====================

func TestMain(m *testing.M) {
	if err := envconfig.Process("", &qCfg); err != nil {
		log.Fatalf("missing required env vars (PROVISIONER_IMAGE, QUOTA_HELPER_IMAGE): %v", err)
	}
	log.Printf("Config: provisioner=%s quotaHelper=%s",
		qCfg.ProvisionerImage, qCfg.QuotaHelperImage)

	if err := setupQuotaCIInfra(); err != nil {
		teardownQuotaCIInfra()
		log.Fatalf("setup failed: %v", err)
	}

	code := m.Run()

	teardownQuotaCIInfra()
	os.Exit(code)
}

func setupQuotaCIInfra() error {
	// 1. Clean up any leftover Kind cluster (idempotent)
	log.Println("Cleaning up any previous Kind cluster...")
	exec.Command("kind", "delete", "cluster").CombinedOutput() //nolint:errcheck,gosec

	// 2. Create Kind cluster with XFS mounts
	kindConfig, err := filepath.Abs(testdataFile("kind-cluster-quota.yaml"))
	if err != nil {
		return fmt.Errorf("kind config path: %w", err)
	}
	log.Println("Creating Kind cluster with XFS mounts...")
	rawOut, err := exec.Command("kind", "create", "cluster",
		"--config="+kindConfig, "--wait=120s").CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create Kind cluster: %s", rawOut)
	}
	log.Println("Kind cluster created")

	// 3. Load images into Kind
	for _, img := range []string{qCfg.ProvisionerImage, qCfg.QuotaHelperImage} {
		log.Printf("Loading image into Kind: %s", img)
		rawOut, err = exec.Command("kind", "load", "docker-image", img).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to load image %s: %s", img, rawOut)
		}
	}
	log.Println("Images loaded into Kind")

	// 4. Deploy provisioner via quota-ci-base kustomize overlay
	quotaCIBaseDir, err := filepath.Abs(testdataFile("quota-ci-base"))
	if err != nil {
		return fmt.Errorf("quota-ci-base path: %w", err)
	}
	out, err := runBashCmd("kustomize build | "+kubectlBin()+" apply -f -", quotaCIBaseDir)
	if err != nil {
		return fmt.Errorf("failed to deploy provisioner: %s", out)
	}

	// 5. Wait for provisioner to be available
	out, err = kubectlRun("wait", "deployment/local-path-provisioner",
		"-n", quotaProvNS, "--for=condition=Available", "--timeout=120s")
	if err != nil {
		return fmt.Errorf("provisioner not available: %s", out)
	}
	log.Println("Provisioner deployed and available")
	return nil
}

func teardownQuotaCIInfra() {
	// 1. Tear down provisioner
	quotaCIBaseDir, _ := filepath.Abs(testdataFile("quota-ci-base"))
	runBashCmd("kustomize build | "+kubectlBin()+" delete --timeout=60s --ignore-not-found -f -", quotaCIBaseDir) //nolint:errcheck

	// 2. Delete Kind cluster
	out, err := exec.Command("kind", "delete", "cluster").CombinedOutput()
	if err != nil {
		log.Printf("Warning: failed to delete Kind cluster: %s", out)
	}
	log.Println("TeardownQuotaCIInfra complete")
}
