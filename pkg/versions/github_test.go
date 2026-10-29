package versions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGithubGetETag(t *testing.T) {
	var full, notModified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	body, cached, err := githubGetConditional(ctx, srv.URL+"/x", 1<<10)
	if err != nil || cached || string(body) != `{"ok":true}` {
		t.Fatalf("first = %q cached=%v err=%v", body, cached, err)
	}
	body, cached, err = githubGetConditional(ctx, srv.URL+"/x", 1<<10)
	if err != nil || !cached || string(body) != `{"ok":true}` {
		t.Fatalf("second = %q cached=%v err=%v", body, cached, err)
	}
	if full.Load() != 1 || notModified.Load() != 1 {
		t.Errorf("full=%d 304=%d", full.Load(), notModified.Load())
	}
}

func TestGithubGetUnsolicited304IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(srv.Close)
	if _, err := githubGet(context.Background(), srv.URL+"/y", 1<<10); err == nil {
		t.Error("304 without a cached body accepted")
	}
}
