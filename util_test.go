package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadHelperPodFile(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		helperPodYAML string
		allowUnsafe   bool
		wantErr       string
	}{
		"default template is accepted": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  priorityClassName: system-node-critical
  tolerations:
  - key: node.kubernetes.io/disk-pressure
    operator: Exists
    effect: NoSchedule
  containers:
  - name: helper-pod
    image: busybox
    imagePullPolicy: IfNotPresent
`,
		},
		"privileged helper template is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    securityContext:
      privileged: true
`,
			wantErr: "must not set container securityContext",
		},
		"custom volumes are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
  volumes:
  - name: host-root
    hostPath:
      path: /
`,
			wantErr: "must not define custom volumes",
		},
		"init containers are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  initContainers:
  - name: init
    image: busybox
    command: ["sh", "-c", "true"]
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not define initContainers",
		},
		"hostNetwork is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  hostNetwork: true
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not enable host namespaces",
		},
		"hostPID is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  hostPID: true
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not enable host namespaces",
		},
		"hostIPC is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  hostIPC: true
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not enable host namespaces",
		},
		"spec.nodeName is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  nodeName: target-node
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not set spec.nodeName",
		},
		"spec.serviceAccountName is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  serviceAccountName: custom-sa
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not set spec.serviceAccountName",
		},
		"pod securityContext is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  securityContext:
    runAsUser: 0
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not set pod securityContext",
		},
		"multiple containers are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
  - name: sidecar
    image: busybox
`,
			wantErr: "must specify exactly one container",
		},
		"ephemeral containers are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  ephemeralContainers:
  - name: debug
    image: busybox
  containers:
  - name: helper-pod
    image: busybox
`,
			wantErr: "must not define ephemeralContainers",
		},
		"volumeMounts are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    volumeMounts:
    - name: host-root
      mountPath: /host
`,
			wantErr: "must not define custom volumeMounts",
		},
		"envFrom is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    envFrom:
    - secretRef:
        name: stolen-secret
`,
			wantErr: "must not define envFrom",
		},
		"env.valueFrom is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    env:
    - name: STOLEN
      valueFrom:
        secretKeyRef:
          name: my-secret
          key: password
`,
			wantErr: `must not define env.valueFrom (env "STOLEN")`,
		},
		"lifecycle hooks are rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    lifecycle:
      postStart:
        exec:
          command: ["/bin/sh", "-c", "malicious-command"]
`,
			wantErr: "must not define container lifecycle hooks",
		},
		"livenessProbe is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    livenessProbe:
      exec:
        command: ["/bin/sh", "-c", "malicious-command"]
`,
			wantErr: "must not define container livenessProbe",
		},
		"readinessProbe is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    readinessProbe:
      exec:
        command: ["/bin/sh", "-c", "malicious-command"]
`,
			wantErr: "must not define container readinessProbe",
		},
		"startupProbe is rejected by default": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    startupProbe:
      exec:
        command: ["/bin/sh", "-c", "malicious-command"]
`,
			wantErr: "must not define container startupProbe",
		},
		"unsafe mode accepts privileged template": {
			helperPodYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
    securityContext:
      privileged: true
    volumeMounts:
    - name: host-root
      mountPath: /host
  volumes:
  - name: host-root
    hostPath:
      path: /
`,
			allowUnsafe: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pod, err := loadHelperPodFile(tt.helperPodYAML, tt.allowUnsafe)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NotNil(t, pod)
				return
			}

			require.Error(t, err)
			require.Nil(t, pod)
			require.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
