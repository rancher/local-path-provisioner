//go:build quota_e2e || quota_ci_e2e

// Package test provides shared test functions and helpers for XFS project quota e2e tests.
// These are used by both quota_e2e (real k3s cluster) and quota_ci_e2e (Kind + loopback XFS).
package test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const quotaProvNS = "local-path-storage"

// Package-level state initialised once by TestMain (defined per build tag).
var qCfg quotaConfig

// ===================== Tests =====================

// TestQuotaEnforcement verifies that writes within the PVC quota succeed
// and writes exceeding it fail with ENOSPC.
func TestQuotaEnforcement(t *testing.T) {
	t.Parallel()
	dir := "quota-xfs-enforce"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-enforce-writer", 120*time.Second)

	phase := getPVCPhase(t, "quota-enforce-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 50M — should succeed (within 100Mi quota)
	out, err := kubectlExecPod(t, "quota-enforce-writer", "dd", "if=/dev/zero", "of=/data/file1", "bs=1M", "count=50")
	require.NoError(t, err, "writing 50M should succeed: %s", out)
	t.Logf("50M write output: %s", out)

	// Write 60M more — should fail with ENOSPC (50+60 > 100Mi)
	out, err = kubectlExecPod(t, "quota-enforce-writer", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=60")
	require.Error(t, err, "writing 60M should fail when 50M already written (100Mi limit)")
	assert.Contains(t, out, "No space left on device", "expected ENOSPC error")
	t.Logf("Expected failure output: %s", out)
}

// TestBackwardCompatNoQuota verifies that the default StorageClass
// (without quotaEnforcement) allows unlimited writes.
func TestBackwardCompatNoQuota(t *testing.T) {
	t.Parallel()
	dir := "quota-backward-compat"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "no-quota-compat-writer", 120*time.Second)

	phase := getPVCPhase(t, "no-quota-compat-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 200M — should succeed even though PVC requests only 100Mi
	out, err := kubectlExecPod(t, "no-quota-compat-writer", "dd", "if=/dev/zero", "of=/data/bigfile", "bs=1M", "count=200")
	require.NoError(t, err, "writing 200M should succeed with no quota: %s", out)
	t.Logf("200M write (no quota) output: %s", out)

	// Verify provisioner log says no quota (filter by PV name for parallel safety)
	pvName := getPVNameFromPVC(t, "no-quota-compat-pvc")
	logOut := getProvisionerLogs(t, 500)
	filtered := extractHelperLogs(logOut, pvName)
	assert.Contains(t, filtered, "No quota enforcement requested", "provisioner should log no quota message")
}

// TestTeardownCleanup verifies that deleting a quota-enforced PVC properly
// removes the data directory, the XFS quota, and /etc/projects + /etc/projid entries.
func TestTeardownCleanup(t *testing.T) {
	t.Parallel()
	dir := "quota-teardown-cleanup"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-teardown-writer", 120*time.Second)

	// Write some data
	_, err := kubectlExecPod(t, "quota-teardown-writer", "dd", "if=/dev/zero", "of=/data/testfile", "bs=1M", "count=10")
	require.NoError(t, err, "writing 10M should succeed within 50Mi quota")

	// Get the PV name for later verification
	pvName, err := kubectlRun("get", "pvc", "quota-teardown-pvc",
		"-o", "jsonpath={.spec.volumeName}")
	require.NoError(t, err, "failed to get PV name")
	pvName = strings.TrimSpace(pvName)
	t.Logf("PV name: %s", pvName)

	// Get PV annotation to verify quota type was stored
	quotaAnnotation, _ := kubectlRun("get", "pv", pvName,
		"-o", "jsonpath={.metadata.annotations.local\\.path\\.provisioner/quota-type}")
	assert.Equal(t, "xfs", strings.TrimSpace(quotaAnnotation), "PV should have quota-type annotation")

	// Delete pod + PVC (manual cleanup — t.Cleanup will handle SC)
	kubectlRun("delete", "pod", "quota-teardown-writer", "--grace-period=0", "--force") //nolint:errcheck
	kubectlRun("delete", "pvc", "quota-teardown-pvc")                                  //nolint:errcheck

	// Wait for teardown helper pod to complete
	t.Log("Waiting for teardown helper pod to complete...")
	waitForPVDeletion(t, pvName, 60*time.Second)

	// Verify provisioner logs show proper teardown (filter by PV name for parallel safety)
	logOut := getProvisionerLogs(t, 500)
	filtered := extractHelperLogs(logOut, pvName)
	assert.Contains(t, filtered, "Teardown complete", "provisioner should log teardown completion")
	assert.Contains(t, filtered, "Removing XFS project quota", "provisioner should log quota removal")
}

// TestMultiplePVCsIndependentQuotas verifies that two quota-enforced PVCs
// have independent limits.
func TestMultiplePVCsIndependentQuotas(t *testing.T) {
	t.Parallel()
	dir := "quota-multiple-pvcs"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-multi-writer-1", 120*time.Second)
	waitPodReady(t, "quota-multi-writer-2", 120*time.Second)

	// PVC-1: 100Mi limit — write 80M (should succeed)
	out, err := kubectlExecPod(t, "quota-multi-writer-1", "dd", "if=/dev/zero", "of=/data/file1", "bs=1M", "count=80")
	require.NoError(t, err, "PVC-1: writing 80M within 100Mi should succeed: %s", out)

	// PVC-2: 200Mi limit — write 150M (should succeed)
	out, err = kubectlExecPod(t, "quota-multi-writer-2", "dd", "if=/dev/zero", "of=/data/file1", "bs=1M", "count=150")
	require.NoError(t, err, "PVC-2: writing 150M within 200Mi should succeed: %s", out)

	// PVC-1: write 30M more — should fail (80+30 > 100Mi)
	out, err = kubectlExecPod(t, "quota-multi-writer-1", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=30")
	require.Error(t, err, "PVC-1: writing 30M more should fail (80+30 > 100Mi)")
	assert.Contains(t, out, "No space left on device")

	// PVC-2: write 40M more — should succeed (150+40 < 200Mi)
	out, err = kubectlExecPod(t, "quota-multi-writer-2", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=40")
	require.NoError(t, err, "PVC-2: writing 40M more within 200Mi should succeed: %s", out)

	// PVC-2: write 20M more — should fail (150+40+20 > 200Mi)
	out, err = kubectlExecPod(t, "quota-multi-writer-2", "dd", "if=/dev/zero", "of=/data/file3", "bs=1M", "count=20")
	require.Error(t, err, "PVC-2: writing 20M more should fail (190+20 > 200Mi)")
	assert.Contains(t, out, "No space left on device")
}

// TestQuotaSpaceRecovery verifies that XFS quota properly accounts for
// freed space after file deletion.
func TestQuotaSpaceRecovery(t *testing.T) {
	t.Parallel()
	dir := "quota-space-recovery"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-recovery-writer", 120*time.Second)

	phase := getPVCPhase(t, "quota-recovery-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 90M — should succeed (within 100Mi quota)
	out, err := kubectlExecPod(t, "quota-recovery-writer", "dd", "if=/dev/zero", "of=/data/bigfile", "bs=1M", "count=90")
	require.NoError(t, err, "writing 90M should succeed: %s", out)
	t.Logf("90M write output: %s", out)

	// Delete the file to free space
	out, err = kubectlExecPod(t, "quota-recovery-writer", "rm", "/data/bigfile")
	require.NoError(t, err, "deleting file should succeed: %s", out)

	// Sync to ensure filesystem accounts for freed space
	kubectlExecPod(t, "quota-recovery-writer", "sync") //nolint:errcheck

	// Write 90M again — should succeed because space was freed
	out, err = kubectlExecPod(t, "quota-recovery-writer", "dd", "if=/dev/zero", "of=/data/bigfile2", "bs=1M", "count=90")
	require.NoError(t, err, "writing 90M after deletion should succeed (quota reuse): %s", out)
	t.Logf("90M write after deletion output: %s", out)
}

// TestQuotaResizeExpand verifies that expanding a PVC updates the XFS quota limit.
func TestQuotaResizeExpand(t *testing.T) {
	t.Parallel()
	dir := "quota-resize-expand"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-resize-writer", 120*time.Second)

	phase := getPVCPhase(t, "quota-resize-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 90M — should succeed (within 100Mi quota)
	out, err := kubectlExecPod(t, "quota-resize-writer", "dd", "if=/dev/zero", "of=/data/file1", "bs=1M", "count=90")
	require.NoError(t, err, "writing 90M should succeed: %s", out)

	// Write 20M more — should fail (90+20 > 100Mi)
	out, err = kubectlExecPod(t, "quota-resize-writer", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=20")
	require.Error(t, err, "writing 20M more should fail (90+20 > 100Mi)")
	assert.Contains(t, out, "No space left on device")

	// Remove failed partial file
	kubectlExecPod(t, "quota-resize-writer", "rm", "-f", "/data/file2") //nolint:errcheck

	// Expand PVC from 100Mi to 200Mi
	out, err = kubectlRun("patch", "pvc", "quota-resize-pvc", "--type=merge",
		"-p", `{"spec":{"resources":{"requests":{"storage":"200Mi"}}}}`)
	require.NoError(t, err, "patching PVC should succeed: %s", out)
	t.Log("Patched PVC to 200Mi")

	// Wait for PV capacity to update
	waitForPVCapacity(t, "quota-resize-pvc", "200Mi", 120*time.Second)

	// Write 20M more — should now succeed (90+20 < 200Mi)
	out, err = kubectlExecPod(t, "quota-resize-writer", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=20")
	require.NoError(t, err, "writing 20M after expand should succeed: %s", out)

	// Write 100M more — should fail (90+20+100 > 200Mi)
	out, err = kubectlExecPod(t, "quota-resize-writer", "dd", "if=/dev/zero", "of=/data/file3", "bs=1M", "count=100")
	require.Error(t, err, "writing 100M more should fail (210 > 200Mi)")
	assert.Contains(t, out, "No space left on device")
}

// TestQuotaShrinkSuccess verifies that shrinking a PVC via annotation updates
// the XFS quota limit when used space is less than the new target size.
func TestQuotaShrinkSuccess(t *testing.T) {
	t.Parallel()
	dir := "quota-shrink-success"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-shrink-ok-writer", 120*time.Second)

	phase := getPVCPhase(t, "quota-shrink-ok-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 50M — should succeed (within 200Mi quota)
	out, err := kubectlExecPod(t, "quota-shrink-ok-writer", "dd", "if=/dev/zero", "of=/data/file1", "bs=1M", "count=50")
	require.NoError(t, err, "writing 50M should succeed: %s", out)
	t.Logf("50M write output: %s", out)

	// Request shrink to 100Mi via annotation (50M used < 100Mi target = OK)
	annotatePVC(t, "quota-shrink-ok-pvc", "local.path.provisioner/requested-size", "100Mi")
	t.Log("Annotated PVC with requested-size=100Mi")

	// Wait for resize status to become completed
	waitForResizeStatus(t, "quota-shrink-ok-pvc", "completed", 120*time.Second)

	// Verify PV capacity was updated to 100Mi
	waitForPVCapacity(t, "quota-shrink-ok-pvc", "100Mi", 30*time.Second)

	// Write 40M more — should succeed (50+40 < 100Mi)
	out, err = kubectlExecPod(t, "quota-shrink-ok-writer", "dd", "if=/dev/zero", "of=/data/file2", "bs=1M", "count=40")
	require.NoError(t, err, "writing 40M after shrink should succeed: %s", out)

	// Write 20M more — should fail (50+40+20 > 100Mi, proves quota was reduced)
	out, err = kubectlExecPod(t, "quota-shrink-ok-writer", "dd", "if=/dev/zero", "of=/data/file3", "bs=1M", "count=20")
	require.Error(t, err, "writing 20M more should fail (50+40+20 > 100Mi)")
	assert.Contains(t, out, "No space left on device", "expected ENOSPC proving quota was reduced")
}

// TestQuotaShrinkRejectedUsageExceedsTarget verifies that shrinking is rejected
// when current disk usage exceeds the target size.
func TestQuotaShrinkRejectedUsageExceedsTarget(t *testing.T) {
	t.Parallel()
	dir := "quota-shrink"
	applyKustomize(t, dir)
	t.Cleanup(func() { cleanupKustomize(t, dir) })

	waitPodReady(t, "quota-shrink-writer", 120*time.Second)

	phase := getPVCPhase(t, "quota-shrink-pvc")
	assert.Equal(t, "Bound", phase, "PVC should be Bound")

	// Write 150M — should succeed (within 200Mi quota)
	out, err := kubectlExecPod(t, "quota-shrink-writer", "dd", "if=/dev/zero", "of=/data/bigfile", "bs=1M", "count=150")
	require.NoError(t, err, "writing 150M should succeed: %s", out)
	t.Logf("150M write output: %s", out)

	// Request shrink to 100Mi via annotation (150M used > 100Mi target = REJECTED)
	annotatePVC(t, "quota-shrink-pvc", "local.path.provisioner/requested-size", "100Mi")
	t.Log("Annotated PVC with requested-size=100Mi (should be rejected)")

	// Wait for resize status to become failed
	waitForResizeStatus(t, "quota-shrink-pvc", "failed", 120*time.Second)

	// Verify the failure message mentions usage exceeding target
	msg := getPVCAnnotation("quota-shrink-pvc", "local.path.provisioner/resize-message")
	t.Logf("Resize message: %s", msg)
	assert.Contains(t, msg, "current disk usage", "failure message should mention current disk usage")
	assert.Contains(t, msg, "exceeds requested size", "failure message should mention exceeds requested size")

	// Verify PV capacity remains at 200Mi (shrink was rejected)
	pvCapacity := getPVCapacityViaPVC(t, "quota-shrink-pvc")
	t.Logf("PV capacity after rejected shrink: %s", pvCapacity)
	assert.Equal(t, "200Mi", pvCapacity, "PV capacity should remain at 200Mi")
}

// ===================== kubectl Helpers =====================

// kubectlBin returns the kubectl command to use.
func kubectlBin() string {
	if k := os.Getenv("KUBECTL"); k != "" {
		return k
	}
	return "kubectl"
}

func kubectlRun(args ...string) (string, error) {
	parts := strings.Fields(kubectlBin())
	cmd := exec.Command(parts[0], append(parts[1:], args...)...) //nolint:gosec
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func kubectlExecPod(t *testing.T, pod string, command ...string) (string, error) {
	t.Helper()
	args := append([]string{"exec", pod, "--"}, command...)
	return kubectlRun(args...)
}

func runBashCmd(cmd, workDir string) (string, error) {
	c := exec.Command("bash", "-c", cmd) //nolint:gosec
	c.Dir = workDir
	out, err := c.CombinedOutput()
	return string(out), err
}

// ===================== Kustomize Helpers =====================

func applyKustomize(t *testing.T, dir string) {
	t.Helper()
	absDir, err := filepath.Abs(testdataFile(dir))
	require.NoError(t, err)
	out, err := runBashCmd("kustomize build | "+kubectlBin()+" apply -f -", absDir)
	require.NoError(t, err, "failed to apply kustomize for %s: %s", dir, out)
	t.Logf("Applied kustomize: %s", dir)
}

func cleanupKustomize(t *testing.T, dir string) {
	t.Helper()
	absDir, err := filepath.Abs(testdataFile(dir))
	if err != nil {
		t.Logf("cleanup warning: %v", err)
		return
	}
	out, err := runBashCmd(
		"kustomize build | "+kubectlBin()+" delete --timeout=60s --ignore-not-found -f -",
		absDir,
	)
	if err != nil {
		t.Logf("cleanup warning for %s: %v\nOutput: %s", dir, err, out)
	}
}

// ===================== Wait / Poll Helpers =====================

func waitPodReady(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	out, err := kubectlRun("wait", "pod/"+name,
		"--for=condition=Ready",
		fmt.Sprintf("--timeout=%ds", int(timeout.Seconds())))
	require.NoError(t, err, "pod %s did not become Ready: %s", name, out)
	t.Logf("Pod %s is Ready", name)
}

func getPVCPhase(t *testing.T, name string) string {
	t.Helper()
	out, err := kubectlRun("get", "pvc", name, "-o", "jsonpath={.status.phase}")
	require.NoError(t, err, "failed to get PVC phase: %s", out)
	return strings.TrimSpace(out)
}

func waitForPVDeletion(t *testing.T, pvName string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := kubectlRun("get", "pv", pvName, "--ignore-not-found")
		if err != nil || strings.TrimSpace(out) == "" {
			t.Logf("PV %s deleted", pvName)
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Logf("Warning: PV %s not yet deleted after %v (may still be cleaning up)", pvName, timeout)
}

func waitForPVCapacity(t *testing.T, pvcName, expectedCapacity string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pvName, err := kubectlRun("get", "pvc", pvcName, "-o", "jsonpath={.spec.volumeName}")
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		pvName = strings.TrimSpace(pvName)
		if pvName == "" {
			time.Sleep(3 * time.Second)
			continue
		}

		capacity, err := kubectlRun("get", "pv", pvName, "-o", "jsonpath={.spec.capacity.storage}")
		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}
		capacity = strings.TrimSpace(capacity)
		t.Logf("PV %s capacity: %s (waiting for %s)", pvName, capacity, expectedCapacity)
		if capacity == expectedCapacity {
			return
		}
		time.Sleep(3 * time.Second)
	}
	require.Fail(t, fmt.Sprintf("PV capacity did not reach %s within %v", expectedCapacity, timeout))
}

func waitForResizeStatus(t *testing.T, pvcName, expectedStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status := getPVCAnnotation(pvcName, "local.path.provisioner/resize-status")
		t.Logf("PVC %s resize-status: %s (waiting for %s)", pvcName, status, expectedStatus)
		if status == expectedStatus {
			return
		}
		// If we're waiting for "completed" but got "failed", fail fast
		if expectedStatus == "completed" && status == "failed" {
			msg := getPVCAnnotation(pvcName, "local.path.provisioner/resize-message")
			require.Fail(t, fmt.Sprintf("resize failed instead of completing: %s", msg))
		}
		time.Sleep(3 * time.Second)
	}
	require.Fail(t, fmt.Sprintf("PVC %s resize-status did not reach %s within %v", pvcName, expectedStatus, timeout))
}

// ===================== Annotation Helpers =====================

func annotatePVC(t *testing.T, pvcName, key, value string) {
	t.Helper()
	out, err := kubectlRun("annotate", "pvc", pvcName, fmt.Sprintf("%s=%s", key, value), "--overwrite")
	require.NoError(t, err, "failed to annotate PVC %s: %s", pvcName, out)
}

func getPVCAnnotation(pvcName, annotation string) string {
	escapedKey := strings.ReplaceAll(annotation, ".", "\\.")
	jsonpath := fmt.Sprintf("{.metadata.annotations.%s}", escapedKey)
	out, err := kubectlRun("get", "pvc", pvcName, "-o", fmt.Sprintf("jsonpath=%s", jsonpath))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func getPVCapacityViaPVC(t *testing.T, pvcName string) string {
	t.Helper()
	pvName, err := kubectlRun("get", "pvc", pvcName, "-o", "jsonpath={.spec.volumeName}")
	require.NoError(t, err, "failed to get PV name from PVC")
	pvName = strings.TrimSpace(pvName)

	capacity, err := kubectlRun("get", "pv", pvName, "-o", "jsonpath={.spec.capacity.storage}")
	require.NoError(t, err, "failed to get PV capacity")
	return strings.TrimSpace(capacity)
}

// ===================== Log Helpers =====================

// getPVNameFromPVC retrieves the PV name bound to a PVC.
func getPVNameFromPVC(t *testing.T, pvcName string) string {
	t.Helper()
	out, err := kubectlRun("get", "pvc", pvcName, "-o", "jsonpath={.spec.volumeName}")
	require.NoError(t, err, "failed to get PV name for PVC %s: %s", pvcName, out)
	return strings.TrimSpace(out)
}

// getProvisionerLogs fetches the last N lines from the provisioner pod.
func getProvisionerLogs(t *testing.T, tail int) string {
	t.Helper()
	out, _ := kubectlRun("logs", "-l", "app=local-path-provisioner",
		"-n", quotaProvNS, fmt.Sprintf("--tail=%d", tail))
	return out
}

// extractHelperLogs extracts log lines emitted by helper pods for a specific PV.
// The provisioner wraps helper pod output between marker lines:
//
//	"Start of <helperPodName> logs"
//	... helper script output ...
//	"End of <helperPodName> logs"
//
// The helper pod name contains the PV name (e.g. "helper-pod-create-pvc-abc123"),
// so we match blocks whose Start/End markers contain the pvName, plus any top-level
// log lines that directly reference the PV name.
func extractHelperLogs(allLogs, pvName string) string {
	var result []string
	inBlock := false
	for _, line := range strings.Split(allLogs, "\n") {
		if strings.Contains(line, pvName) && strings.Contains(line, "Start of") {
			inBlock = true
			result = append(result, line)
			continue
		}
		if strings.Contains(line, pvName) && strings.Contains(line, "End of") {
			inBlock = false
			result = append(result, line)
			continue
		}
		if inBlock {
			// Lines inside a helper pod log block for this PV
			result = append(result, line)
			continue
		}
		// Also capture top-level provisioner lines that reference this PV
		if strings.Contains(line, pvName) {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
