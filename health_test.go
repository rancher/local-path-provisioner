package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStartHealthServer(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		port           int
		waitForReady   time.Duration
		requestTimeout time.Duration
		wantErr        string
	}{
		"starts on default port 8080": {
			port:           DefaultHealthPort,
			waitForReady:   100 * time.Millisecond,
			requestTimeout: 5 * time.Second,
		},
		"starts on custom port": {
			port:           9090,
			waitForReady:   100 * time.Millisecond,
			requestTimeout: 5 * time.Second,
		},
		"starts on port 8081": {
			port:           8081,
			waitForReady:   100 * time.Millisecond,
			requestTimeout: 5 * time.Second,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, cancelFn := context.WithCancel(context.TODO())
			defer cancelFn()

			var wg sync.WaitGroup
			wg.Add(1)

			go func() {
				defer wg.Done()
				startHealthServer(ctx, tt.port)
			}()

			time.Sleep(tt.waitForReady)

			address := fmt.Sprintf("localhost:%d", tt.port)
			conn, err := net.DialTimeout("tcp", address, tt.requestTimeout)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.NotNil(t, conn)
				conn.Close()
			} else {
				require.Error(t, err)
				require.Nil(t, conn)
			}

			cancelFn()
			wg.Wait()
		})
	}
}

func TestHealthEndpoint(t *testing.T) {
	t.Parallel()

	ctx, cancelFn := context.WithCancel(context.TODO())
	defer cancelFn()

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		startHealthServer(ctx, 8082)
	}()

	time.Sleep(100 * time.Millisecond)

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get("http://localhost:8082/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)

	bodyBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "OK", string(bodyBytes))
}

func TestHealthEndpointMultipleRequests(t *testing.T) {
	t.Parallel()

	ctx, cancelFn := context.WithCancel(context.TODO())
	defer cancelFn()

	var wg sync.WaitGroup
	wg.Add(1)

	go func() {
		defer wg.Done()
		startHealthServer(ctx, 8083)
	}()

	time.Sleep(100 * time.Millisecond)

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	numRequests := 10
	var mu sync.Mutex
	successCount := 0

	var requestWg sync.WaitGroup
	for i := 0; i < numRequests; i++ {
		requestWg.Add(1)
		go func() {
			defer requestWg.Done()
			resp, err := client.Get("http://localhost:8083/health")
			if err == nil && resp.StatusCode == http.StatusOK {
				mu.Lock()
				successCount++
				mu.Unlock()
				resp.Body.Close()
			}
		}()
	}

	requestWg.Wait()
	require.Equal(t, numRequests, successCount)
}
