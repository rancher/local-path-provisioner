package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func loadFile(filepath string) (string, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	helperPodYaml, err := ioutil.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(helperPodYaml), nil
}

func loadHelperPodFile(helperPodYaml string) (*v1.Pod, error) {
	helperPodJSON, err := yaml.YAMLToJSON([]byte(helperPodYaml))
	if err != nil {
		return nil, fmt.Errorf("invalid YAMLToJSON the helper pod with helperPodYaml: %v", helperPodYaml)
	}
	p := v1.Pod{}
	err = json.Unmarshal(helperPodJSON, &p)
	if err != nil {
		return nil, fmt.Errorf("invalid unmarshal the helper pod with helperPodJson: %v", string(helperPodJSON))
	}
	if len(p.Spec.Containers) == 0 {
		return nil, fmt.Errorf("helper pod template does not specify any container")
	}
	return &p, nil
}
