package test

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func createCmd(t *testing.T, cmd, kustomizeDir string, envs []string, callback func(*exec.Cmd)) *exec.Cmd {
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

func testEnvs(config interface{}) []string {
	var envs []string
	value := reflect.ValueOf(config).Elem()

	for i := 0; i < value.NumField(); i++ {
		fieldName := value.Type().Field(i).Name
		fieldValue := value.Field(i).Interface()

		envs = append(envs, fmt.Sprintf("%s=%v", fieldName, fieldValue))
	}

	return envs
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
