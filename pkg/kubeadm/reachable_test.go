package kubeadm

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReachableProbesVersionOnceAMinute(t *testing.T) {
	var hits atomic.Int32
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/version" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sa" {
			t.Errorf("bearer token missing: %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"kind":"Status","message":"scripted"}`))
	}))
	t.Cleanup(srv.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	now := time.Now()
	c := NewClient(KubeConfig{APIServer: srv.URL, Token: "sa", CAData: ca})
	c.probeNow = func() time.Time { return now }
	ctx := context.Background()
	for range 3 {
		if ok, err := c.Reachable(ctx); !ok || err != nil {
			t.Fatalf("reachable: %v %v", ok, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("three calls within ProbeTTL must cost one request, got %d", hits.Load())
	}

	status.Store(http.StatusUnauthorized)
	now = now.Add(ProbeTTL)
	ok, err := c.Reachable(ctx)
	var se *StatusError
	if !ok || !errors.As(err, &se) || se.Code != http.StatusUnauthorized || !errors.Is(err, ErrForbidden) {
		t.Fatalf("a 401 is reachable with the status as the error: %v %v", ok, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("after ProbeTTL the server is asked again, got %d hits", hits.Load())
	}
	if ok, err := c.Reachable(ctx); !ok || err == nil || hits.Load() != 2 {
		t.Fatalf("a failed answer is cached too: %v %v %d", ok, err, hits.Load())
	}

	dead := NewClient(KubeConfig{APIServer: "https://127.0.0.1:1", Token: "sa"})
	start := time.Now()
	if ok, err := dead.Reachable(ctx); ok || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("dead server: %v %v", ok, err)
	}
	if elapsed := time.Since(start); elapsed > probeTimeout {
		t.Fatalf("a refused connection must fail fast, took %s", elapsed)
	}

	none := NewClient(KubeConfig{})
	if ok, err := none.Reachable(ctx); ok || !errors.Is(err, ErrNotInCluster) {
		t.Fatalf("no API server: %v %v", ok, err)
	}
}
