package test

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testdataFile builds the path to a testdata file or directory.
func testdataFile(fields ...string) string {
	return filepath.Join("testdata", filepath.Join(fields...))
}

// createCmd creates a bash command with the given environment and working directory.
func createCmd(t *testing.T, cmd, workDir string, envs []string, callback func(*exec.Cmd)) *exec.Cmd {
	t.Logf("creating command: %s", cmd)
	c := exec.Command("bash", "-c", cmd)
	c.Env = append(os.Environ(), envs...)
	c.Dir = workDir

	if callback != nil {
		callback(c)
	}

	return c
}

// runCmd runs a bash command, streaming output to the test log.
func runCmd(t *testing.T, cmd, workDir string, envs []string, callback func(*exec.Cmd)) (*exec.Cmd, error) {
	t.Logf("running command: %s", cmd)

	c := createCmd(t, cmd, workDir, envs, callback)
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

// deleteKustomizeDeployment tears down all resources built by kustomize in the given directory.
func deleteKustomizeDeployment(t *testing.T, kustomizeDir string, envs []string) error {
	_, err := runCmd(
		t,
		"kustomize build | kubectl delete --timeout=180s --ignore-not-found -f -",
		testdataFile(kustomizeDir),
		envs,
		nil,
	)
	return err
}
