package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForDaemonStartRetriesUntilReady(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			t.Errorf("unexpected readiness path: %s", r.URL.Path)
		}
		if requests.Add(1) < 4 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"starting"}`)
			return
		}
		fmt.Fprint(w, `{"status":"running"}`)
	}))
	defer server.Close()
	client := &daemonClient{http: &http.Client{Transport: testDaemonTransport{server: server.URL}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForDaemonStart(ctx, client); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 4 {
		t.Fatalf("status requests = %d, want 4", requests.Load())
	}
}

func TestWaitForDaemonStartCancelsInFlightRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &daemonClient{http: &http.Client{Transport: testDaemonTransport{server: server.URL}}}
	if err := waitForDaemonStart(ctx, client); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want cancellation", err)
	}
}

func TestWaitForDaemonStartHonorsExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	// No HTTP client is needed: an expired deadline must prevent the request.
	if err := waitForDaemonStart(ctx, &daemonClient{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v, want deadline exceeded", err)
	}
}

type testDaemonTransport struct {
	server string
}

func (transport testDaemonTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Scheme = "http"
	request.URL.Host = transport.server[len("http://"):]
	return http.DefaultTransport.RoundTrip(request)
}
