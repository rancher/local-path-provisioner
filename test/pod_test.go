//go:build e2e
// +build e2e

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
	hostPathVolumeType        = "hostPath"
	localVolumeType           = "local"
	hostPathDirectoryOrCreate = "DirectoryOrCreate"
	hostPathDirectory         = "Directory"
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

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectoryOrCreate)
}

func (p *PodTestSuite) TestPodWithLocalVolume() {
	p.kustomizeDir = "pod-with-local-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType, "")
}

func (p *PodTestSuite) TestPodWithLocalVolumeDefault() {
	p.kustomizeDir = "pod-with-default-local-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType, "")
}

func (p *PodTestSuite) TestPodWithNodeAffinity() {
	p.kustomizeDir = "pod-with-node-affinity"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectoryOrCreate)
}

func (p *PodTestSuite) TestPodWithRWOPVolume() {
	p.kustomizeDir = "pod-with-rwop-volume"

	runTest(p, []string{p.config.IMAGE}, "ready", localVolumeType, hostPathDirectoryOrCreate)
}

func (p *PodTestSuite) TestPodWithSecurityContext() {
	p.kustomizeDir = "pod-with-security-context"
	kustomizeDir := testdataFile(p.kustomizeDir)

	runTest(p, []string{p.config.IMAGE}, "podscheduled", hostPathVolumeType, hostPathDirectoryOrCreate)

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

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectoryOrCreate)
}

func (p *PodTestSuite) xxTestPodWithMultipleStorageClasses() {
	p.kustomizeDir = "multiple-storage-classes"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectoryOrCreate)
}

func (p *PodTestSuite) TestPodWithHostPathType() {
	p.kustomizeDir = "pod-with-host-path-type"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectory)
}

func (p *PodTestSuite) TestPodWithDefaultHostPathType() {
	p.kustomizeDir = "pod-with-default-host-path-type"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectory)
}

func (p *PodTestSuite) TestPodWithCustomPathPatternStorageClasses() {
	p.kustomizeDir = "custom-path-pattern"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType, hostPathDirectoryOrCreate)
}

func runTest(p *PodTestSuite, images []string, waitCondition, volumeType string, hostPathType string) {
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
	if volumeType == hostPathVolumeType {
		hostPathTypeCheckCmd := fmt.Sprintf("kubectl get pv $(%s) -o jsonpath='{.spec.hostPath.type}'", "kubectl get pv -o jsonpath='{.items[0].metadata.name}'")
		c := createCmd(p.T(), hostPathTypeCheckCmd, kustomizeDir, p.config.envs(), nil)
		hostPathTypeCheckOutput, err := c.CombinedOutput()
		if err != nil {
			p.FailNow("", "failed to check type of host path volume: %v", err)
		}
		if string(hostPathTypeCheckOutput) != hostPathType {
			p.FailNow("type of host path volume not correct")
		}
	}
}
