package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func loadFile(filepath string) (string, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
	}()
	helperPodYaml, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(helperPodYaml), nil
}

func loadHelperPodFile(helperPodYaml string, allowUnsafe bool) (*v1.Pod, error) {
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
	if err := validateHelperPodTemplate(&p, allowUnsafe); err != nil {
		return nil, err
	}
	return &p, nil
}

func validateHelperPodTemplate(p *v1.Pod, allowUnsafe bool) error {
	if allowUnsafe {
		return nil
	}

	forbid := func(cond bool, field string) error {
		if !cond {
			return nil
		}
		return fmt.Errorf("helper pod template must not %s unless %s is enabled", field, FlagAllowUnsafeHelperPodTemplate)
	}

	if len(p.Spec.Containers) != 1 {
		return fmt.Errorf("helper pod template must specify exactly one container")
	}

	container := p.Spec.Containers[0]
	checks := []struct {
		invalid bool
		field   string
	}{
		{len(p.Spec.InitContainers) > 0, "define initContainers"},
		{len(p.Spec.EphemeralContainers) > 0, "define ephemeralContainers"},
		{len(p.Spec.Volumes) > 0, "define custom volumes"},
		{p.Spec.HostNetwork || p.Spec.HostPID || p.Spec.HostIPC, "enable host namespaces"},
		{p.Spec.NodeName != "", "set spec.nodeName"},
		{p.Spec.ServiceAccountName != "", "set spec.serviceAccountName"},
		{len(container.VolumeMounts) > 0, "define custom volumeMounts"},
		{len(container.EnvFrom) > 0, "define envFrom"},
		{container.Lifecycle != nil, "define container lifecycle hooks"},
		{container.LivenessProbe != nil, "define container livenessProbe"},
		{container.ReadinessProbe != nil, "define container readinessProbe"},
		{container.StartupProbe != nil, "define container startupProbe"},
	}
	for _, check := range checks {
		if err := forbid(check.invalid, check.field); err != nil {
			return err
		}
	}
	for _, env := range container.Env {
		if err := forbid(env.ValueFrom != nil, fmt.Sprintf("define env.valueFrom (env %q)", env.Name)); err != nil {
			return err
		}
	}

	// Validate pod securityContext: block dangerous fields,
	// but allow hardening fields (runAsNonRoot, fsGroup, seccompProfile, etc.)
	if psc := p.Spec.SecurityContext; psc != nil {
		if err := forbid(psc.RunAsUser != nil && *psc.RunAsUser == 0, "set pod runAsUser to 0 (root)"); err != nil {
			return err
		}
		if err := forbid(psc.RunAsGroup != nil && *psc.RunAsGroup == 0, "set pod runAsGroup to 0 (root)"); err != nil {
			return err
		}
		if err := forbid(len(psc.Sysctls) > 0, "define sysctls"); err != nil {
			return err
		}
	}

	// Validate container securityContext: block privilege escalation fields,
	// but allow hardening fields (runAsNonRoot, readOnlyRootFilesystem, etc.)
	if sc := container.SecurityContext; sc != nil {
		if err := forbid(sc.Privileged != nil && *sc.Privileged, "set privileged: true"); err != nil {
			return err
		}
		if err := forbid(sc.Capabilities != nil && len(sc.Capabilities.Add) > 0, "add capabilities"); err != nil {
			return err
		}
		if err := forbid(sc.AllowPrivilegeEscalation != nil && *sc.AllowPrivilegeEscalation, "set allowPrivilegeEscalation: true"); err != nil {
			return err
		}
	}

	return nil
}
