// +build e2e

package test

import (
	"fmt"
	"github.com/kelseyhightower/envconfig"
	"github.com/stretchr/testify/suite"
	"testing"
	"time"
	"strings"
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

func (p *PodTestSuite) TestPodWithNodeAffinity() {
	p.kustomizeDir = "pod-with-node-affinity"

	runTest(p, []string{p.config.IMAGE}, "ready", hostPathVolumeType)
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
