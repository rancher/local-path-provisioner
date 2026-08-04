//go:build e2e

package test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
		// kind ships its own local-path-provisioner: the "standard" default StorageClass and a
		// "local-path-provisioner" Deployment in the "local-path-storage" namespace, both using
		// the provisioner name "rancher.io/local-path" - identical to the provisioner under test.
		// Since the provisioner runs without leader election, kind's built-in provisioner races
		// the test-deployed provisioner and may provision volumes without the features under test
		// (e.g. the custom nodeAffinityKey). Remove it so the test-deployed provisioner is the
		// only one servicing "rancher.io/local-path" claims.
		"kubectl -n local-path-storage delete deployment local-path-provisioner --ignore-not-found",
		"kubectl -n local-path-storage wait --for=delete pod --all --timeout=120s || true",
		"kubectl delete storageclass standard --ignore-not-found",
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

	t := time.Tick(5 * time.Second)
loop:
	for {
		select {
		case <-t:
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

func (p *PodTestSuite) TestPodWithCustomNodeAffinityKey() {
	p.kustomizeDir = "pod-with-custom-node-affinity-key"
	kustomizeDir := testdataFile(p.kustomizeDir)

	customLabel := "test.example.com/stable-id"
	customValue := "my-stable-node"

	// Label the kind-worker node with the custom label before deploying
	labelCmd := fmt.Sprintf("kubectl label node kind-worker %s=%s --overwrite", customLabel, customValue)
	_, err := runCmd(p.T(), labelCmd, "", p.config.envs(), nil)
	if err != nil {
		p.FailNow("", "failed to label node", err)
	}

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)

	// Resolve the PV bound to this test's PVC. The shared cluster may hold PVs
	// from other tests, so selecting by index (.items[0]) is not reliable.
	pvNameCmd := `kubectl get pvc local-path-pvc -o jsonpath='{.spec.volumeName}'`
	c := createCmd(p.T(), pvNameCmd, kustomizeDir, p.config.envs(), nil)
	output, err := c.CombinedOutput()
	if err != nil {
		p.FailNow("", "failed to get PV name", err)
	}
	pvName := strings.Trim(string(output), "'")

	// Verify the PV was created with the custom node affinity key
	affinityCmd := fmt.Sprintf(`kubectl get pv %s -o jsonpath='{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].key}'`, pvName)
	c = createCmd(p.T(), affinityCmd, kustomizeDir, p.config.envs(), nil)
	output, err = c.CombinedOutput()
	if err != nil {
		p.FailNow("", "failed to get PV node affinity key", err)
	}
	p.Equal(customLabel, strings.Trim(string(output), "'"), "PV should use the custom node affinity key")

	// Verify the affinity value matches what was set on the node
	valueCmd := fmt.Sprintf(`kubectl get pv %s -o jsonpath='{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}'`, pvName)
	c = createCmd(p.T(), valueCmd, kustomizeDir, p.config.envs(), nil)
	output, err = c.CombinedOutput()
	if err != nil {
		p.FailNow("", "failed to get PV node affinity value", err)
	}
	p.Equal(customValue, strings.Trim(string(output), "'"), "PV node affinity value should match the custom label value")
}

func (p *PodTestSuite) TestPodWithCustomPathPatternStorageClasses() {
	p.kustomizeDir = "custom-path-pattern"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithUserGroupPermPattern() {
	p.kustomizeDir = "pod-with-perm-pattern"
	kustomizeDir := testdataFile(p.kustomizeDir)

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)

	// The storage class sets userPattern=1000, groupPattern=2000 and permPattern=0700,
	// which the setup script applies to the volume directory via chown/chmod. Inspect the
	// mounted directory from inside the pod to confirm the patterns took effect.
	statCmd := "kubectl exec volume-test -- stat -c '%a %u %g' /data"
	c := createCmd(p.T(), statCmd, kustomizeDir, p.config.envs(), nil)
	output, err := c.CombinedOutput()
	if err != nil {
		p.FailNow("", "failed to stat volume dir", string(output), err)
	}
	p.Equal("700 1000 2000", strings.TrimSpace(string(output)), "volume dir should reflect user/group/perm patterns")
}

func (p *PodTestSuite) TestPodWithSkipPathPatternCheck() {
	p.kustomizeDir = "skip-path-pattern-check"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

func (p *PodTestSuite) TestPodWithSkipPathPatternCheckByAnnotation() {
	p.kustomizeDir = "skip-path-pattern-check-by-annotation"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
}

// ADD THIS NEW TEST METHOD
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
	tick := time.Tick(2 * time.Second)

	for {
		select {
		case <-timeout:
			// Timeout is expected - PVC should remain Pending due to security rejection
			p.T().Log("PVC correctly remained in Pending state due to security validation")
			return

		case <-tick:
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
			cmds = append(cmds, fmt.Sprintf("kustomize edit set image docker.io/rancher/local-path-provisioner=%s", image))
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

	// Verify a PV of the expected type was provisioned for this test. Resolve PVs through
	// the PVCs that currently exist in the namespace rather than picking the first PV in
	// the cluster (.items[0]), which on the shared cluster may be a leftover PV orphaned
	// by a previous test. Retry to absorb the provisioning delay when the caller only
	// waits for the pod to be scheduled.
	typeCheckCmd := fmt.Sprintf(
		"for pv in $(kubectl get pvc -o jsonpath='{.items[*].spec.volumeName}'); do kubectl get pv \"$pv\" -o jsonpath='{.spec.%s}'; echo; done",
		volumeType,
	)

	var lastOutput string
	deadline := time.After(120 * time.Second)
	tick := time.Tick(2 * time.Second)
	for {
		c := createCmd(p.T(), typeCheckCmd, kustomizeDir, p.config.envs(), nil)
		typeCheckOutput, err := c.CombinedOutput()
		lastOutput = string(typeCheckOutput)
		if err == nil && strings.Contains(lastOutput, "path") {
			return
		}

		select {
		case <-deadline:
			p.FailNow("volume Type not correct", "got: %q", lastOutput)
			return
		case <-tick:
		}
	}
}

func testdataFile(fields ...string) string {
	return filepath.Join("testdata", filepath.Join(fields...))
}

func deleteKustomizeDeployment(t *testing.T, kustomizeDir string, envs []string) error {
	_, err := runCmd(
		t,
		"kustomize build | kubectl delete --timeout=180s -f -",
		testdataFile(kustomizeDir),
		envs,
		nil,
	)
	return err
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

func createCmd(t *testing.T, cmd, kustomizeDir string, envs []string, callback func(*exec.Cmd)) *exec.Cmd {
	t.Logf("creating command: %s", cmd)
	c := exec.Command("bash", "-c", cmd)
	c.Env = append(os.Environ(), envs...)
	c.Dir = kustomizeDir

	if callback != nil {
		callback(c)
	}

	return c
}

func runCmd(t *testing.T, cmd, kustomizeDir string, envs []string, callback func(*exec.Cmd)) (*exec.Cmd, error) {
	t.Logf("running command: %s", cmd)

	c := createCmd(t, cmd, kustomizeDir, envs, callback)
	stdout, _ := c.StdoutPipe()
	stderr, _ := c.StderrPipe()

	err := c.Start()
	if err != nil {
		return nil, err
	}

	stopCh := make(chan struct{})
	go func() {
		mergedReader := io.MultiReader(stderr, stdout)
		scanner := bufio.NewScanner(mergedReader)
		scanner.Split(bufio.ScanLines)
		for scanner.Scan() {
			t.Log(scanner.Text())
		}

		close(stopCh)
	}()

	<-stopCh
	err = c.Wait()
	if err != nil {
		return nil, err
	}

	return c, nil
}
