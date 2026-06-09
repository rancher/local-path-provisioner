package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHealthHandler(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		method string
		body   string
		code   int
	}{
		"GET /health":   {method: http.MethodGet, body: "OK", code: http.StatusOK},
		"HEAD /health":  {method: http.MethodHead, body: "", code: http.StatusOK},
		"OPTIONS /health": {method: http.MethodOptions, body: "", code: http.StatusOK},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/health", nil)
			w := httptest.NewRecorder()

			healthHandler(w, req)

			resp := w.Result()
			defer resp.Body.Close()

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
			defer resp.Body.Close()

			require.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)
		})
	}
}
