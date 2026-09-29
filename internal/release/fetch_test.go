package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHTTPFetcherParsesTag uses httptest only: no test here may reach the network.
func TestHTTPFetcherParsesTag(t *testing.T) {
	var gotPath, gotAuth, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.7.0","name":"relevo v0.7.0","draft":false}`))
	}))
	defer srv.Close()

	tag, err := NewHTTPFetcher(srv.URL+"/repos/fuad-daoud/relevo/releases/latest", 5*time.Second).
		Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if tag != "v0.7.0" {
		t.Errorf("Latest = %q, want %q", tag, "v0.7.0")
	}
	if gotPath != "/repos/fuad-daoud/relevo/releases/latest" {
		t.Errorf("path = %q, want /repos/fuad-daoud/relevo/releases/latest", gotPath)
	}
	// Unauthenticated: a rate-limited machine degrades to "not checked", not an error.
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
	if gotAccept == "" {
		t.Error("Accept header is empty; the GitHub API is read as JSON")
	}
}

// TestHTTPFetcherEndpointFromEnv pins the RELEVO_RELEASE_API override.
func TestHTTPFetcherEndpointFromEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	}))
	defer srv.Close()
	t.Setenv("RELEVO_RELEASE_API", srv.URL)

	tag, err := NewHTTPFetcher("", 5*time.Second).Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if tag != "v9.9.9" {
		t.Errorf("Latest = %q, want v9.9.9 from RELEVO_RELEASE_API", tag)
	}
}

// TestHTTPFetcherFailures pins every failure mode as an error, never a panic or a hang.
func TestHTTPFetcherFailures(t *testing.T) {
	t.Run("prefix tags are kept verbatim", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":"v0.7.0-rc1"}`))
		}))
		defer srv.Close()

		tag, err := NewHTTPFetcher(srv.URL, time.Second).Latest(context.Background())
		if err != nil || tag != "v0.7.0-rc1" {
			t.Errorf("Latest = (%q, %v), want (v0.7.0-rc1, nil)", tag, err)
		}
	})

	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "nope", http.StatusForbidden)
		}))
		defer srv.Close()

		if _, err := NewHTTPFetcher(srv.URL, time.Second).Latest(context.Background()); err == nil {
			t.Error("Latest on a 403 = nil error, want an error")
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"tag_name":`))
		}))
		defer srv.Close()

		if _, err := NewHTTPFetcher(srv.URL, time.Second).Latest(context.Background()); err == nil {
			t.Error("Latest on a truncated body = nil error, want an error")
		}
	})

	t.Run("no tag_name", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"name":"relevo"}`))
		}))
		defer srv.Close()

		if _, err := NewHTTPFetcher(srv.URL, time.Second).Latest(context.Background()); err == nil {
			t.Error("Latest with no tag_name = nil error, want an error")
		}
	})

	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		url := srv.URL
		srv.Close()

		_, err := NewHTTPFetcher(url, time.Second).Latest(context.Background())
		if !errors.Is(err, ErrOffline) {
			t.Errorf("Latest on a closed server = %v, want ErrOffline", err)
		}
	})
}
