package main

import (
	"strings"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHelperPodFinished(t *testing.T) {
	failed := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "helper-pod-delete-pvc-1"},
		Status: v1.PodStatus{
			Phase: v1.PodFailed,
			ContainerStatuses: []v1.ContainerStatus{{
				Name:  "helper-pod",
				State: v1.ContainerState{Terminated: &v1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}},
			}},
		},
	}

	tests := []struct {
		name     string
		pod      *v1.Pod
		finished bool
		errPart  string
	}{
		{"pending", &v1.Pod{Status: v1.PodStatus{Phase: v1.PodPending}}, false, ""},
		{"running", &v1.Pod{Status: v1.PodStatus{Phase: v1.PodRunning}}, false, ""},
		{"succeeded", &v1.Pod{Status: v1.PodStatus{Phase: v1.PodSucceeded}}, true, ""},
		// The helper pod never restarts, so Failed is final and must not be
		// polled until cmdTimeoutSeconds expires.
		{"failed", failed, false, "helper pod helper-pod-delete-pvc-1 failed: container helper-pod exited with code 1 (Error)"},
		{"failed without container status", &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "helper-pod-create-pvc-2"},
			Status:     v1.PodStatus{Phase: v1.PodFailed, Reason: "DeadlineExceeded"},
		}, false, "helper pod helper-pod-create-pvc-2 failed: DeadlineExceeded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			finished, err := helperPodFinished(tc.pod)
			if finished != tc.finished {
				t.Errorf("finished = %v, want %v", finished, tc.finished)
			}
			if tc.errPart == "" && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tc.errPart != "" && (err == nil || !strings.Contains(err.Error(), tc.errPart)) {
				t.Errorf("error = %v, want it to contain %q", err, tc.errPart)
			}
		})
	}
}
