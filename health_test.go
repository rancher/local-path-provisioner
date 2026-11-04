package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestHealthHandler(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		method string
		body   string
		code   int
	}{
		"GET /health":     {method: http.MethodGet, body: "OK", code: http.StatusOK},
		"HEAD /health":    {method: http.MethodHead, body: "", code: http.StatusOK},
		"OPTIONS /health": {method: http.MethodOptions, body: "", code: http.StatusOK},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/health", nil)
			w := httptest.NewRecorder()

			healthHandler(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, tt.code, resp.StatusCode)

			if tt.body != "" {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tt.body, string(body))
			}
		})
	}
}

func TestHealthHandler_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	methods := []string{http.MethodPost, http.MethodPut, http.MethodDelete}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/health", nil)
			w := httptest.NewRecorder()

			healthHandler(w, req)

			resp := w.Result()
			defer func() { _ = resp.Body.Close() }()

			require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		})
	}
}

// newFakeKubeClient returns a clientset backed by a test HTTP server that
// answers every request with an empty list. hold, if set, runs before the
// answer; call is the 1-based number of the request.
func newFakeKubeClient(t *testing.T, hold func(call int32, r *http.Request)) (clientset.Interface, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if hold != nil {
			hold(call, r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"metadata":{},"items":[]}`))
	}))
	t.Cleanup(srv.Close)

	kubeClient, err := clientset.NewForConfig(&rest.Config{Host: srv.URL})
	require.NoError(t, err)
	return kubeClient, &calls
}

func TestReadyHandler(t *testing.T) {
	t.Parallel()

	kubeClient, calls := newFakeKubeClient(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	w := httptest.NewRecorder()

	readyHandler(kubeClient, "default", w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "Ready", w.Body.String())
	require.EqualValues(t, 5, calls.Load())
}

func TestReadyHandler_StopsWhenRequestIsCancelled(t *testing.T) {
	t.Parallel()

	firstList := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }

	// The first LIST hangs until its client goes away or the test releases it.
	kubeClient, calls := newFakeKubeClient(t, func(call int32, r *http.Request) {
		if call != 1 {
			return
		}
		close(firstList)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	t.Cleanup(releaseAll) // runs before the server is closed

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		readyHandler(kubeClient, "default", w, req)
	}()

	select {
	case <-firstList:
	case <-time.After(5 * time.Second):
		t.Fatal("readyHandler made no API request")
	}

	// The kubelet closes the connection when a probe times out, which
	// cancels the request context.
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("readyHandler kept running after the request was cancelled")
		releaseAll()
		<-done
	}

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.EqualValues(t, 1, calls.Load(), "API requests made")
}
