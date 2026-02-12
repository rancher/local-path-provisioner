//go:build quota_e2e

// Package test provides e2e tests for XFS project quota enforcement.
//
// Prerequisites:
//   - A k3s cluster reachable via kubectl (or KUBECTL env var)
//   - A data disk mounted at /mnt/data as XFS with prjquota mount option
//   - /etc/projects and /etc/projid existing on the host
//   - Docker available locally (for pushing images to the in-cluster registry)
//
// Run:
//
//	PROVISIONER_IMAGE=local-path-provisioner:dev \
//	QUOTA_HELPER_IMAGE=local-path-quota-helper:dev \
//	go test -tags quota_e2e -v -timeout 15m ./test/
//
//	Or with kubectl override:
//	KUBECTL="kubectl --kubeconfig=/path/to/kubeconfig" \
//	PROVISIONER_IMAGE=... QUOTA_HELPER_IMAGE=... \
//	go test -tags quota_e2e -v -timeout 15m ./test/
package test

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kelseyhightower/envconfig"
)

const (
	registryNodePort  = "30500"
	registryLocalPort = "5000"
)

// Package-level state for the in-cluster registry port-forward.
var portForwardCmd *exec.Cmd

// ===================== TestMain (one-time setup / teardown) =====================

func TestMain(m *testing.M) {
	if err := envconfig.Process("", &qCfg); err != nil {
		log.Fatalf("missing required env vars (PROVISIONER_IMAGE, QUOTA_HELPER_IMAGE): %v", err)
	}
	log.Printf("Config: provisioner=%s quotaHelper=%s kubectl=%s",
		qCfg.ProvisionerImage, qCfg.QuotaHelperImage, kubectlBin())

	if err := setupQuotaInfra(); err != nil {
		log.Fatalf("setup failed: %v", err)
	}

	code := m.Run()

	teardownQuotaInfra()
	os.Exit(code)
}

func setupQuotaInfra() error {
	// 1. Verify cluster connectivity
	out, err := kubectlRun("cluster-info")
	if err != nil {
		return fmt.Errorf("cannot reach cluster: %s", out)
	}

	// 2. Clean up any previous deployment (idempotent)
	cleanupPreviousDeployment()

	// 3. Deploy in-cluster registry
	registryYAML, err := filepath.Abs(testdataFile("quota-registry", "registry-deployment.yaml"))
	if err != nil {
		return fmt.Errorf("registry YAML path: %w", err)
	}
	out, err = kubectlRun("apply", "-f", registryYAML)
	if err != nil {
		return fmt.Errorf("failed to deploy registry: %s", out)
	}
	out, err = kubectlRun("wait", "deployment/registry", "-n", "default",
		"--for=condition=Available", "--timeout=60s")
	if err != nil {
		return fmt.Errorf("registry not ready: %s", out)
	}
	log.Println("In-cluster registry is running")

	// 4. Port-forward to the registry
	if err := startPortForward(); err != nil {
		return err
	}

	// 5. Tag and push images
	pushImage(qCfg.ProvisionerImage, "localhost:"+registryLocalPort+"/local-path-provisioner:test")
	pushImage(qCfg.QuotaHelperImage, "localhost:"+registryLocalPort+"/local-path-quota-helper:test")

	// 6. Deploy the provisioner via quota-base kustomize overlay
	quotaBaseDir, err := filepath.Abs(testdataFile("quota-base"))
	if err != nil {
		return fmt.Errorf("quota-base path: %w", err)
	}
	out, err = runBashCmd("kustomize build | "+kubectlBin()+" apply -f -", quotaBaseDir)
	if err != nil {
		return fmt.Errorf("failed to deploy provisioner: %s", out)
	}

	// 7. Wait for provisioner to be available
	out, err = kubectlRun("wait", "deployment/local-path-provisioner",
		"-n", quotaProvNS, "--for=condition=Available", "--timeout=120s")
	if err != nil {
		return fmt.Errorf("provisioner not available: %s", out)
	}
	log.Println("Provisioner deployed and available")
	return nil
}

func teardownQuotaInfra() {
	// 1. Tear down provisioner
	quotaBaseDir, _ := filepath.Abs(testdataFile("quota-base"))
	runBashCmd("kustomize build | "+kubectlBin()+" delete --timeout=180s --ignore-not-found -f -", quotaBaseDir) //nolint:errcheck

	// 2. Kill port-forward
	if portForwardCmd != nil && portForwardCmd.Process != nil {
		portForwardCmd.Process.Kill() //nolint:errcheck
		portForwardCmd.Wait()         //nolint:errcheck
		log.Println("Port-forward stopped")
	}

	// 3. Delete registry
	registryYAML, _ := filepath.Abs(testdataFile("quota-registry", "registry-deployment.yaml"))
	kubectlRun("delete", "-f", registryYAML, "--ignore-not-found") //nolint:errcheck
	log.Println("TeardownQuotaInfra complete")
}

// ===================== k3s-specific Setup Helpers =====================

func cleanupPreviousDeployment() {
	// Best-effort cleanup of leftover resources from a previous run
	quotaBaseDir, _ := filepath.Abs(testdataFile("quota-base"))
	runBashCmd("kustomize build | "+kubectlBin()+" delete --timeout=30s --ignore-not-found -f -", quotaBaseDir) //nolint:errcheck

	registryYAML, _ := filepath.Abs(testdataFile("quota-registry", "registry-deployment.yaml"))
	kubectlRun("delete", "-f", registryYAML, "--ignore-not-found") //nolint:errcheck

	// Delete any leftover quota SCs
	for _, sc := range []string{
		"quota-enforce-sc", "quota-teardown-sc", "quota-multi-sc",
		"quota-recovery-sc", "quota-resize-sc", "quota-shrink-ok-sc", "quota-shrink-sc",
	} {
		kubectlRun("delete", "sc", sc, "--ignore-not-found") //nolint:errcheck
	}
}

func startPortForward() error {
	parts := strings.Fields(kubectlBin())
	args := append(parts[1:], "port-forward", "svc/registry", "5000:5000", "-n", "default")
	cmd := exec.Command(parts[0], args...) //nolint:gosec
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start port-forward: %w", err)
	}
	portForwardCmd = cmd
	log.Println("Port-forward started (localhost:5000 -> registry:5000)")

	// Wait for port-forward to be ready
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("bash", "-c", "curl -sf http://localhost:5000/v2/").CombinedOutput() //nolint:gosec
		_ = out
		if err == nil {
			log.Println("Port-forward is ready")
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("port-forward to registry not ready within 30s")
}

func pushImage(localImage, targetImage string) {
	log.Printf("Pushing %s -> %s", localImage, targetImage)
	out, err := exec.Command("docker", "tag", localImage, targetImage).CombinedOutput()
	if err != nil {
		log.Fatalf("docker tag failed: %s", out)
	}
	out, err = exec.Command("docker", "push", targetImage).CombinedOutput()
	if err != nil {
		log.Fatalf("docker push failed: %s", out)
	}
	log.Printf("Pushed %s", targetImage)
}
