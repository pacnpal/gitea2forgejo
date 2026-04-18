package selfupdate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchRelease_retriesOn5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if r.Header.Get("Cache-Control") != "no-cache" {
			t.Errorf("missing Cache-Control: no-cache (hit %d)", n)
		}
		if n == 1 {
			http.Error(w, "backend flaky", http.StatusBadGateway)
			return
		}
		fmt.Fprintln(w, `{"tag_name":"v1.2.3","html_url":"h","published_at":"p"}`)
	}))
	defer srv.Close()

	r, err := fetchRelease(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("expected 2 hits (one retry), got %d", got)
	}
	if r.TagName != "v1.2.3" {
		t.Errorf("tag = %q, want v1.2.3", r.TagName)
	}
}

func TestFetchRelease_no404Retry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := fetchRelease(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("404 must not retry; got %d hits", got)
	}
}

func TestFetchRelease_retriesExhausted(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := fetchRelease(ctx, srv.URL)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("expected exactly 2 attempts (one retry), got %d", got)
	}
}
