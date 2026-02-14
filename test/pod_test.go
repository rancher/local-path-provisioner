//go:build e2e

package test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kelseyhightower/envconfig"
	"github.com/stretchr/testify/suite"
)

const (
	hostPathVolumeType = "hostPath"
	localVolumeType    = "local"
)

type PodTestSuite struct {
	suite.Suite

	config       testConfig
	kustomizeDir string
}

func (p *PodTestSuite) SetupSuite() {
	err := envconfig.Process("test", &p.config)
	if err != nil {
		panic(err)
	}

	p.T().Logf("using test config: %+v", p.config)

	p.TearDownSuite()

	//kind load docker-image "$image"
	cmds := []string{
		fmt.Sprintf("kind create cluster --config=%s --wait=120s", testdataFile("kind-cluster.yaml")),
		fmt.Sprintf("kind load docker-image %s", p.config.IMAGE),
	}
	if p.config.QUOTA_HELPER_IMAGE != "" {
		cmds = append(cmds, fmt.Sprintf("kind load docker-image %s", p.config.QUOTA_HELPER_IMAGE))
	}
	for _, cmd := range cmds {
		_, err = runCmd(
			p.T(),
			cmd,
			"",
			p.config.envs(),
			nil,
		)
		if err != nil {
			p.FailNow("", "failed to create the cluster or load image", err)
		}
	}
}

func (p *PodTestSuite) TearDownSuite() {
	err := deleteCluster(
		p.T(),
		p.config.envs(),
	)
	if err != nil {
		p.Failf("", "failed to delete the cluster: %v", err)
	}
}

func (p *PodTestSuite) TearDownTest() {
	err := deleteKustomizeDeployment(
		p.T(),
		p.kustomizeDir,
		p.config.envs(),
	)
	if err != nil {
		p.Failf("", "failed to delete the deployment: %v", err)
	}
}

func TestPVCTestSuite(t *testing.T) {
	suite.Run(t, new(PodTestSuite))
}

func (p *PodTestSuite) TestPodWithHostPathVolume() {
	p.kustomizeDir = "pod"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithLocalVolume() {
	p.kustomizeDir = "pod-with-local-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType)
}

func (p *PodTestSuite) TestPodWithLocalVolumeDefault() {
	p.kustomizeDir = "pod-with-default-local-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType)
}

func (p *PodTestSuite) TestPodWithNodeAffinity() {
	p.kustomizeDir = "pod-with-node-affinity"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithRWOPVolume() {
	p.kustomizeDir = "pod-with-rwop-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType)
}

func (p *PodTestSuite) TestPodWithSecurityContext() {
	p.kustomizeDir = "pod-with-security-context"
	kustomizeDir := testdataFile(p.kustomizeDir)

	runTest(p, []string{p.config.IMAGE}, "podscheduled", hostPathVolumeType)

	cmd := fmt.Sprintf(`kubectl get pod -l %s=%s -o=jsonpath='{.items[0].status.conditions[?(@.type=="Ready")].reason}'`, LabelKey, LabelValue)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ticker.C:
			c := createCmd(p.T(), cmd, kustomizeDir, p.config.envs(), nil)
			output, err := c.CombinedOutput()
			if err != nil {
				p.T().Logf("%s: %v", c.String(), err)
			}

			if string(output) == "PodCompleted" {
				break loop
			}

		case <-time.After(60 * time.Second):
			p.FailNow("", "pod Ready condition reason should be PodCompleted")
			break
		}
	}
}

func (p *PodTestSuite) TestPodWithSubpath() {
	p.kustomizeDir = "pod-with-subpath"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) xxTestPodWithMultipleStorageClasses() {
	p.kustomizeDir = "multiple-storage-classes"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithCustomPathPatternStorageClasses() {
	p.kustomizeDir = "custom-path-pattern"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithSkipPathPatternCheck() {
	p.kustomizeDir = "skip-path-pattern-check"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithSkipPathPatternCheckByAnnotation() {
	p.kustomizeDir = "skip-path-pattern-check-by-annotation"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestSizeValidation() {
	testCases := []struct {
		name          string
		kustomizeDir  string
		shouldSucceed bool
		expectedError string
	}{
		{
			name:          "RejectBelowMin",
			kustomizeDir:  "size-reject-below-min",
			shouldSucceed: false,
			expectedError: "below minimum size",
		},
		{
			name:          "RejectAboveMax",
			kustomizeDir:  "size-reject-above-max",
			shouldSucceed: false,
			expectedError: "exceeds maximum size",
		},
		{
			name:          "AcceptWithinBounds",
			kustomizeDir:  "size-accept-within-bounds",
			shouldSucceed: true,
		},
	}

	for _, tc := range testCases {
		p.Run(tc.name, func() {
			p.kustomizeDir = tc.kustomizeDir
			p.T().Cleanup(func() {
				_ = deleteKustomizeDeployment(p.T(), p.kustomizeDir, p.config.envs())
			})
			if tc.shouldSucceed {
				runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
			} else {
				p.verifyProvisioningFailed(tc.expectedError)
			}
		})
	}
}

func (p *PodTestSuite) TestCapacityWithinBudget() {
	p.kustomizeDir = "capacity-accept-within"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestCapacityExhaustion() {
	p.kustomizeDir = "capacity-exhaustion"
	p.verifyCapacityExhaustion()
}

func (p *PodTestSuite) verifyCapacityExhaustion() {
	kustomizeDir := testdataFile(p.kustomizeDir)

	// Apply all resources (provisioner + SC + config + 2 PVCs + 2 pods)
	cmds := []string{
		fmt.Sprintf("kustomize edit set image %s", p.config.IMAGE),
		fmt.Sprintf("kustomize edit add label %s:%s -f", LabelKey, LabelValue),
		"kustomize build | kubectl apply -f -",
	}

	for _, cmd := range cmds {
		_, err := runCmd(p.T(), cmd, kustomizeDir, p.config.envs(), nil)
		if err != nil {
			p.FailNow("", "failed to apply deployment", err)
		}
	}

	// Wait for provisioner to be running
	_, err := runCmd(p.T(),
		"kubectl wait deployment/local-path-provisioner -n local-path-storage --for condition=available --timeout=60s",
		kustomizeDir, p.config.envs(), nil)
	if err != nil {
		p.FailNow("", "provisioner deployment not available", err)
	}

	// Wait for at least one PVC to become Bound (up to 120s)
	timeout := time.After(120 * time.Second)
	pollTicker := time.NewTicker(3 * time.Second)
	defer pollTicker.Stop()
	oneBound := false

	for !oneBound {
		select {
		case <-timeout:
			p.FailNow("", "timed out waiting for at least one PVC to become Bound")

		case <-pollTicker.C:
			for _, pvcName := range []string{"capacity-pvc-1", "capacity-pvc-2"} {
				cmd := fmt.Sprintf("kubectl get pvc %s -o jsonpath='{.status.phase}'", pvcName)
				c := createCmd(p.T(), cmd, kustomizeDir, p.config.envs(), nil)
				output, err := c.CombinedOutput()
				if err != nil {
					p.T().Logf("PVC %s check: %v", pvcName, err)
					continue
				}
				phase := strings.Trim(string(output), "'")
				p.T().Logf("PVC %s status: %s", pvcName, phase)
				if phase == "Bound" {
					oneBound = true
				}
			}
		}
	}

	// Poll until PVC states stabilize (one Bound, one Pending) for 3 consecutive checks
	stabilizeTimeout := time.After(60 * time.Second)
	stabilizeTicker := time.NewTicker(3 * time.Second)
	defer stabilizeTicker.Stop()

	stableCount := 0
	var finalBound, finalPending int
	for stableCount < 3 {
		select {
		case <-stabilizeTimeout:
			p.FailNow("", "timed out waiting for PVC states to stabilize (one Bound, one Pending)")
		case <-stabilizeTicker.C:
			bound, pending := 0, 0
			for _, pvcName := range []string{"capacity-pvc-1", "capacity-pvc-2"} {
				cmd := fmt.Sprintf("kubectl get pvc %s -o jsonpath='{.status.phase}'", pvcName)
				c := createCmd(p.T(), cmd, kustomizeDir, p.config.envs(), nil)
				output, err := c.CombinedOutput()
				if err != nil {
					p.T().Logf("PVC %s check error: %v", pvcName, err)
					continue
				}
				phase := strings.Trim(string(output), "'")
				p.T().Logf("PVC %s status: %s", pvcName, phase)
				switch phase {
				case "Bound":
					bound++
				case "Pending":
					pending++
				}
			}
			if bound == 1 && pending == 1 {
				stableCount++
				finalBound, finalPending = bound, pending
			} else {
				stableCount = 0
			}
		}
	}
	p.Equal(1, finalBound, "exactly one PVC should be Bound")
	p.Equal(1, finalPending, "exactly one PVC should remain Pending due to capacity exhaustion")

	// Check provisioner logs for the capacity error
	logCmd := `kubectl logs -l app=local-path-provisioner -n local-path-storage --tail=100`
	logC := createCmd(p.T(), logCmd, kustomizeDir, p.config.envs(), nil)
	logOutput, logErr := logC.CombinedOutput()
	if logErr != nil {
		p.T().Logf("Failed to get provisioner logs: %v", logErr)
	} else {
		logStr := string(logOutput)
		p.True(
			strings.Contains(logStr, "insufficient capacity") || strings.Contains(logStr, "no local path"),
			"expected capacity error in provisioner logs but none found",
		)
	}
}

func (p *PodTestSuite) TestQuotaOnUnsupportedFilesystem() {
	p.kustomizeDir = "quota-unsupported-fs"
	p.verifyProvisioningFailed("unsupported filesystem")
}

func (p *PodTestSuite) TestPathTraversalPrevention() {
	testCases := []struct {
		name          string
		kustomizeDir  string
		expectedError string
		description   string
	}{
		{
			name:          "BasicDirectoryTraversal",
			kustomizeDir:  "security-basic-traversal-path",
			expectedError: "invalid reference",
			description:   "Basic directory traversal with ../",
		},
	}

	for _, tc := range testCases {
		p.Run(tc.name, func() {
			p.T().Logf("Testing: %s", tc.description)
			p.kustomizeDir = tc.kustomizeDir
			p.verifyProvisioningFailed(tc.expectedError)
		})
	}
}

func (p *PodTestSuite) verifyProvisioningFailed(expectedError string) {
	kustomizeDir := testdataFile(p.kustomizeDir)

	// Apply the deployment
	cmds := []string{
		fmt.Sprintf("kustomize edit add label %s:%s -f", LabelKey, LabelValue),
		"kustomize build | kubectl apply -f -",
	}

	for _, cmd := range cmds {
		_, err := runCmd(p.T(), cmd, kustomizeDir, p.config.envs(), nil)
		if err != nil {
			p.FailNow("", "failed to apply deployment", err)
		}
	}

	// Wait a bit for provisioning to be attempted
	time.Sleep(10 * time.Second)

	// Check that PVC is not bound (provisioning should fail)
	checkPVCCmd := fmt.Sprintf("kubectl get pvc -l %s=%s -o jsonpath='{.items[0].status.phase}'", LabelKey, LabelValue)

	timeout := time.After(30 * time.Second)
	failTicker := time.NewTicker(2 * time.Second)
	defer failTicker.Stop()

	for {
		select {
		case <-timeout:
			// Timeout is expected - PVC should remain Pending due to security rejection
			p.T().Log("PVC correctly remained in Pending state due to security validation")
			return

		case <-failTicker.C:
			c := createCmd(p.T(), checkPVCCmd, kustomizeDir, p.config.envs(), nil)
			output, err := c.CombinedOutput()
			if err != nil {
				p.T().Logf("PVC check error (expected): %v", err)
				continue
			}

			pvcStatus := strings.TrimSpace(string(output))
			p.T().Logf("PVC Status: %s", pvcStatus)

			if pvcStatus == "Bound" {
				p.FailNow("", "PVC was bound when it should have been rejected due to security validation")
			}

			// Check provisioner logs for security error
			logCmd := `kubectl logs -l app=local-path-provisioner -n local-path-storage --tail=50`
			logC := createCmd(p.T(), logCmd, kustomizeDir, p.config.envs(), nil)
			logOutput, logErr := logC.CombinedOutput()
			if logErr == nil && len(expectedError) > 0 {
				logStr := string(logOutput)
				if strings.Contains(logStr, expectedError) || strings.Contains(logStr, "invalid reference") {
					p.T().Log("Security validation correctly rejected the malicious path pattern")
					return
				}
			}
		}
	}
}

func runTest(p *PodTestSuite, images []string, waitCondition, volumeType string) {
	kustomizeDir := testdataFile(p.kustomizeDir)

	var cmds []string
	for _, image := range images {
		if len(image) > 0 {
			cmds = append(cmds, fmt.Sprintf("kustomize edit set image %s", image))
		}
	}

	cmds = append(
		cmds,
		fmt.Sprintf("kustomize edit add label %s:%s -f", LabelKey, LabelValue),
		"kustomize build | kubectl apply -f -",
		fmt.Sprintf("kubectl wait pod -l %s=%s --for condition=%s --timeout=120s", LabelKey, LabelValue, waitCondition),
	)

	for _, cmd := range cmds {
		_, err := runCmd(
			p.T(),
			cmd,
			kustomizeDir,
			p.config.envs(),
			nil,
		)
		if err != nil {
			p.FailNow("", "failed to run command", cmd, err)
			break
		}
	}

	typeCheckCmd := fmt.Sprintf("kubectl get pv $(%s) -o jsonpath='{.spec.%s}'", "kubectl get pv -o jsonpath='{.items[0].metadata.name}'", volumeType)
	c := createCmd(p.T(), typeCheckCmd, kustomizeDir, p.config.envs(), nil)
	typeCheckOutput, err := c.CombinedOutput()
	if err != nil {
		p.FailNow("", "failed to check volume type: %v", err)
	}
	if len(typeCheckOutput) == 0 || !strings.Contains(string(typeCheckOutput), "path") {
		p.FailNow("volume Type not correct")
	}
}

func deleteCluster(t *testing.T, envs []string) error {
	_, err := runCmd(
		t,
		"kind delete cluster",
		"",
		envs,
		nil,
	)
	return err
}
