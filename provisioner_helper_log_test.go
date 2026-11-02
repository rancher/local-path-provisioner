package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	pvController "sigs.k8s.io/sig-storage-lib-external-provisioner/v11/controller"
)

const (
	testNamespace = "local-path-storage"
	testNode      = "worker"
	testBasePath  = "/opt/local-path-provisioner"
)

// logMode is how the fake API server answers a helper pod log request.
type logMode string

const (
	logsComplete     logMode = "complete"      // headers, a body, then EOF
	logsNoHeaders    logMode = "no-headers"    // never sends response headers
	logsNoEOF        logMode = "no-eof"        // sends headers and a line, never ends the body
	logsServerError  logMode = "server-error"  // answers 500
	logsOversizeBody logMode = "oversize-body" // sends more than HelperPodLogMaxBytes
)

const partialLogLine = "helper wrote this before the stream stalled"

// fakeAPIServer serves the few Kubernetes API calls a helper pod round trip makes.
type fakeAPIServer struct {
	t        *testing.T
	phase    v1.PodPhase
	logMode  logMode
	release  chan struct{}
	mu       sync.Mutex
	deleted  []string
	logLimit string
}

func (f *fakeAPIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	podsPrefix := "/api/v1/namespaces/" + testNamespace + "/pods"
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes/"+testNode:
		writeJSON(w, http.StatusOK, &v1.Node{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Node"},
			ObjectMeta: metav1.ObjectMeta{Name: testNode},
		})
	case r.Method == http.MethodPost && r.URL.Path == podsPrefix:
		pod := &v1.Pod{}
		if err := json.NewDecoder(r.Body).Decode(pod); err != nil {
			f.t.Errorf("decode created pod: %v", err)
		}
		pod.APIVersion, pod.Kind = "v1", "Pod"
		pod.Namespace = testNamespace
		writeJSON(w, http.StatusCreated, pod)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/log"):
		f.mu.Lock()
		f.logLimit = r.URL.Query().Get("limitBytes")
		f.mu.Unlock()
		f.serveLogs(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, podsPrefix+"/"):
		name := strings.TrimPrefix(r.URL.Path, podsPrefix+"/")
		writeJSON(w, http.StatusOK, &v1.Pod{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Status:     v1.PodStatus{Phase: f.phase},
		})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, podsPrefix+"/"):
		f.mu.Lock()
		f.deleted = append(f.deleted, strings.TrimPrefix(r.URL.Path, podsPrefix+"/"))
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, &metav1.Status{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
			Status:   metav1.StatusSuccess,
		})
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}
}

func (f *fakeAPIServer) serveLogs(w http.ResponseWriter, r *http.Request) {
	switch f.logMode {
	case logsComplete:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("mkdir done\nchmod done\n"))
	case logsNoHeaders:
		f.stall(r)
	case logsNoEOF:
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(partialLogLine + "\n"))
		w.(http.Flusher).Flush()
		f.stall(r)
	case logsServerError:
		http.Error(w, "kubelet unreachable", http.StatusInternalServerError)
	case logsOversizeBody:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(HelperPodLogMaxBytes)*4))
	default:
		f.t.Errorf("unknown log mode %q", f.logMode)
	}
}

// stall holds the request open until the client goes away or the test ends.
func (f *fakeAPIServer) stall(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-f.release:
	}
}

func (f *fakeAPIServer) deletedPods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func writeJSON(w http.ResponseWriter, status int, obj interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(obj)
}

// syncBuffer is a bytes.Buffer that is safe to use as the logrus output.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	out := &syncBuffer{}
	prev := logrus.StandardLogger().Out
	logrus.SetOutput(out)
	t.Cleanup(func() { logrus.SetOutput(prev) })
	return out
}

func newTestProvisioner(t *testing.T, phase v1.PodPhase, mode logMode) (*LocalPathProvisioner, *fakeAPIServer) {
	t.Helper()
	fake := &fakeAPIServer{t: t, phase: phase, logMode: mode, release: make(chan struct{})}
	srv := httptest.NewServer(fake)
	t.Cleanup(func() {
		close(fake.release)
		srv.Close()
	})

	kubeClient, err := clientset.NewForConfig(&rest.Config{
		Host:          srv.URL,
		QPS:           1000,
		Burst:         1000,
		ContentConfig: rest.ContentConfig{ContentType: "application/json"},
	})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	helperPod, err := loadHelperPodFile(`
apiVersion: v1
kind: Pod
metadata:
  name: helper-pod
spec:
  containers:
  - name: helper-pod
    image: busybox
`, false)
	if err != nil {
		t.Fatalf("load helper pod: %v", err)
	}
	cfg, err := canonicalizeConfig(&ConfigData{
		CmdTimeoutSeconds:          1,
		HelperPodLogTimeoutSeconds: 1,
		StorageClassConfigData: StorageClassConfigData{
			NodePathMap: []*NodePathMapData{{Node: NodeDefaultNonListedNodes, Paths: []string{testBasePath}}},
		},
	})
	if err != nil {
		t.Fatalf("canonicalize config: %v", err)
	}
	return &LocalPathProvisioner{
		ctx:           context.Background(),
		kubeClient:    kubeClient,
		namespace:     testNamespace,
		configMapName: "local-path-config",
		config:        cfg,
		configMutex:   &sync.RWMutex{},
		helperPod:     helperPod,
	}, fake
}

func provisionOptions() pvController.ProvisionOptions {
	mode := v1.PersistentVolumeFilesystem
	reclaim := v1.PersistentVolumeReclaimDelete
	return pvController.ProvisionOptions{
		PVName: "pvc-1234",
		PVC: &v1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default"},
			Spec: v1.PersistentVolumeClaimSpec{
				AccessModes: []v1.PersistentVolumeAccessMode{v1.ReadWriteOnce},
				VolumeMode:  &mode,
				Resources: v1.VolumeResourceRequirements{
					Requests: v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")},
				},
			},
		},
		SelectedNode: &v1.Node{ObjectMeta: metav1.ObjectMeta{Name: testNode}},
		StorageClass: &storagev1.StorageClass{
			ObjectMeta:    metav1.ObjectMeta{Name: "local-path"},
			ReclaimPolicy: &reclaim,
		},
	}
}

func persistentVolume() *v1.PersistentVolume {
	mode := v1.PersistentVolumeFilesystem
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-1234"},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeReclaimPolicy: v1.PersistentVolumeReclaimDelete,
			StorageClassName:              "local-path",
			VolumeMode:                    &mode,
			Capacity:                      v1.ResourceList{v1.ResourceStorage: resource.MustParse("1Gi")},
			PersistentVolumeSource: v1.PersistentVolumeSource{
				HostPath: &v1.HostPathVolumeSource{Path: testBasePath + "/pvc-1234_default_data"},
			},
			NodeAffinity: &v1.VolumeNodeAffinity{
				Required: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{{
						MatchExpressions: []v1.NodeSelectorRequirement{{
							Key: DefaultNodeAffinityKey, Operator: v1.NodeSelectorOpIn, Values: []string{testNode},
						}},
					}},
				},
			},
		},
	}
}

// runAction drives one helper pod round trip through the real provision or
// delete path and reports its error and how long it took.
func runAction(t *testing.T, p *LocalPathProvisioner, action ActionType) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	var err error
	switch action {
	case ActionTypeCreate:
		var pv *v1.PersistentVolume
		pv, _, err = p.provisionFor(provisionOptions(), &p.config.StorageClassConfig)
		if err == nil && pv == nil {
			t.Errorf("provisioning succeeded without returning a volume")
		}
	case ActionTypeDelete:
		err = p.deleteFor(persistentVolume(), &p.config.StorageClassConfig)
	default:
		t.Fatalf("unknown action %q", action)
	}
	return time.Since(start), err
}

// The deadline is one second in these tests; a stuck log read without it never returns.
const maxActionDuration = 10 * time.Second

func TestHelperPodLogsAreBestEffort(t *testing.T) {
	for _, action := range []ActionType{ActionTypeCreate, ActionTypeDelete} {
		for _, tc := range []struct {
			mode        logMode
			wantLogs    []string
			wantSaveErr bool
		}{
			{mode: logsComplete, wantLogs: []string{"mkdir done", "chmod done"}},
			{mode: logsNoHeaders, wantSaveErr: true},
			{mode: logsNoEOF, wantLogs: []string{partialLogLine}, wantSaveErr: true},
			{mode: logsServerError, wantSaveErr: true},
		} {
			t.Run(string(action)+"/"+string(tc.mode), func(t *testing.T) {
				logs := captureLogs(t)
				p, fake := newTestProvisioner(t, v1.PodSucceeded, tc.mode)

				took, err := runAction(t, p, action)

				if err != nil {
					t.Fatalf("a helper pod that succeeded must report success, got: %v", err)
				}
				if took > maxActionDuration {
					t.Fatalf("%s took %v, the log read was not bounded", action, took)
				}
				if got := fake.deletedPods(); len(got) != 1 {
					t.Errorf("want the helper pod deleted once, deleted %v", got)
				}
				out := logs.String()
				for _, want := range tc.wantLogs {
					if !strings.Contains(out, want) {
						t.Errorf("provisioner log is missing helper output %q:\n%s", want, out)
					}
				}
				if gotSaveErr := strings.Contains(out, "failed to save"); gotSaveErr != tc.wantSaveErr {
					t.Errorf("log-save error reported = %v, want %v:\n%s", gotSaveErr, tc.wantSaveErr, out)
				}
			})
		}
	}
}

func TestHelperPodFailureIsStillReported(t *testing.T) {
	for _, action := range []ActionType{ActionTypeCreate, ActionTypeDelete} {
		for _, mode := range []logMode{logsComplete, logsNoHeaders, logsNoEOF} {
			t.Run(string(action)+"/"+string(mode), func(t *testing.T) {
				captureLogs(t)
				p, fake := newTestProvisioner(t, v1.PodFailed, mode)

				took, err := runAction(t, p, action)

				if err == nil {
					t.Fatalf("a helper pod that failed must not report success")
				}
				if !strings.Contains(err.Error(), "timeout") {
					t.Errorf("want the helper failure reported as a timeout, got: %v", err)
				}
				if took > maxActionDuration {
					t.Fatalf("%s took %v, the log read was not bounded", action, took)
				}
				if got := fake.deletedPods(); len(got) != 1 {
					t.Errorf("want the helper pod deleted once, deleted %v", got)
				}
			})
		}
	}
}

func TestHelperPodLogsAreCapped(t *testing.T) {
	prev := HelperPodLogMaxBytes
	HelperPodLogMaxBytes = 64
	t.Cleanup(func() { HelperPodLogMaxBytes = prev })

	logs := captureLogs(t)
	p, fake := newTestProvisioner(t, v1.PodSucceeded, logsOversizeBody)

	if _, err := runAction(t, p, ActionTypeCreate); err != nil {
		t.Fatalf("provision: %v", err)
	}
	out := logs.String()
	if strings.Contains(out, strings.Repeat("x", int(HelperPodLogMaxBytes)+1)) {
		t.Errorf("more than %d bytes of helper output reached the log", HelperPodLogMaxBytes)
	}
	if !strings.Contains(out, strings.Repeat("x", int(HelperPodLogMaxBytes))) {
		t.Errorf("the first %d bytes of helper output are missing from the log", HelperPodLogMaxBytes)
	}
	if !strings.Contains(out, "truncated at "+strconv.FormatInt(HelperPodLogMaxBytes, 10)+" bytes") {
		t.Errorf("truncation is not reported:\n%s", out)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if want := strconv.FormatInt(HelperPodLogMaxBytes+1, 10); fake.logLimit != want {
		t.Errorf("log request limitBytes = %q, want %q", fake.logLimit, want)
	}
}

func TestHelperPodLogTimeoutConfig(t *testing.T) {
	for _, tc := range []struct {
		configured int
		want       int
	}{
		{configured: 0, want: defaultHelperPodLogTimeoutSeconds},
		{configured: -3, want: defaultHelperPodLogTimeoutSeconds},
		{configured: 30, want: 30},
	} {
		cfg, err := canonicalizeConfig(&ConfigData{
			HelperPodLogTimeoutSeconds: tc.configured,
			StorageClassConfigData: StorageClassConfigData{
				NodePathMap: []*NodePathMapData{{Node: NodeDefaultNonListedNodes, Paths: []string{testBasePath}}},
			},
		})
		if err != nil {
			t.Fatalf("canonicalize config: %v", err)
		}
		if cfg.HelperPodLogTimeoutSeconds != tc.want {
			t.Errorf("helperPodLogTimeoutSeconds %d -> %d, want %d", tc.configured, cfg.HelperPodLogTimeoutSeconds, tc.want)
		}
	}
}
