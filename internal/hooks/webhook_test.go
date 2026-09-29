package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
)

type recordedRequest struct {
	method      string
	contentType string
	body        []byte
}

type recordingServer struct {
	mu    sync.Mutex
	calls []recordedRequest
	ch    chan recordedRequest
	*httptest.Server
}

func newRecordingServer(status int, delay time.Duration) *recordingServer {
	rs := &recordingServer{ch: make(chan recordedRequest, 10)}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			time.Sleep(delay)
		}
		body, _ := io.ReadAll(r.Body)
		call := recordedRequest{method: r.Method, contentType: r.Header.Get("Content-Type"), body: body}
		rs.mu.Lock()
		rs.calls = append(rs.calls, call)
		rs.mu.Unlock()
		select {
		case rs.ch <- call:
		default:
		}
		w.WriteHeader(status)
	}))
	return rs
}

func (rs *recordingServer) waitCall(t *testing.T, timeout time.Duration) recordedRequest {
	t.Helper()
	select {
	case call := <-rs.ch:
		return call
	case <-time.After(timeout):
		t.Fatal("timed out waiting for webhook POST")
		return recordedRequest{}
	}
}

func (rs *recordingServer) assertNoCalls(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case call := <-rs.ch:
		t.Fatalf("unexpected POST: %+v", call)
	case <-time.After(wait):
	}
}

func (rs *recordingServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return len(rs.calls)
}

func TestWebhookPostsMatchingEventsOnly(t *testing.T) {
	srv := newRecordingServer(http.StatusOK, 0)
	defer srv.Close()

	sink := &WebhookSink{
		Hooks: []policy.Webhook{
			{URL: srv.URL, Events: []string{"state_changed:needs_you"}},
		},
	}

	sink.Dispatch(context.Background(), Event{
		Type: EventStateChanged, State: "needs_you", BindingID: "api-auth", Round: 4,
	})
	srv.waitCall(t, 2*time.Second)
	if got := srv.count(); got != 1 {
		t.Fatalf("calls after matching state_changed:needs_you = %d, want 1", got)
	}

	sink.Dispatch(context.Background(), Event{
		Type: EventStateChanged, State: "active", BindingID: "api-auth", Round: 4,
	})
	srv.assertNoCalls(t, 100*time.Millisecond)
	if got := srv.count(); got != 1 {
		t.Fatalf("calls after non-matching state_changed:active = %d, want 1", got)
	}

	sink.Dispatch(context.Background(), Event{
		Type: EventBuilderStalled, BindingID: "api-auth", Round: 4,
	})
	srv.assertNoCalls(t, 100*time.Millisecond)
	if got := srv.count(); got != 1 {
		t.Fatalf("calls after builder_stalled = %d, want 1", got)
	}
}

func TestWebhookFormats(t *testing.T) {
	ev := Event{
		Type:      EventStateChanged,
		BindingID: "api-auth",
		State:     "needs_you",
		OldState:  "active",
		Round:     4,
	}

	t.Run("json", func(t *testing.T) {
		srv := newRecordingServer(http.StatusOK, 0)
		defer srv.Close()

		sink := &WebhookSink{Hooks: []policy.Webhook{{URL: srv.URL, Format: "json"}}}
		sink.Dispatch(context.Background(), ev)
		call := srv.waitCall(t, 2*time.Second)

		var m map[string]any
		if err := json.Unmarshal(call.body, &m); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		wantKeys := []string{"type", "binding", "state", "old_state", "round", "timestamp", "text"}
		if len(m) != len(wantKeys) {
			t.Fatalf("json body has %d keys (%v), want %d (%v)", len(m), m, len(wantKeys), wantKeys)
		}
		for _, k := range wantKeys {
			if _, ok := m[k]; !ok {
				t.Errorf("json body missing key %q: %v", k, m)
			}
		}
		if m["text"] != "relevo: api-auth NEEDS YOU round 4 (was active)" {
			t.Errorf("text = %q", m["text"])
		}
	})

	t.Run("slack", func(t *testing.T) {
		srv := newRecordingServer(http.StatusOK, 0)
		defer srv.Close()

		sink := &WebhookSink{Hooks: []policy.Webhook{{URL: srv.URL, Format: "slack"}}}
		sink.Dispatch(context.Background(), ev)
		call := srv.waitCall(t, 2*time.Second)

		want := `{"text":"relevo: api-auth NEEDS YOU round 4 (was active)"}`
		if strings.TrimSpace(string(call.body)) != want {
			t.Errorf("slack body = %s, want %s", call.body, want)
		}
	})

	t.Run("discord", func(t *testing.T) {
		srv := newRecordingServer(http.StatusOK, 0)
		defer srv.Close()

		sink := &WebhookSink{Hooks: []policy.Webhook{{URL: srv.URL, Format: "discord"}}}
		sink.Dispatch(context.Background(), ev)
		call := srv.waitCall(t, 2*time.Second)

		want := `{"content":"relevo: api-auth NEEDS YOU round 4 (was active)"}`
		if strings.TrimSpace(string(call.body)) != want {
			t.Errorf("discord body = %s, want %s", call.body, want)
		}
	})
}

// syncBuffer is a mutex-guarded bytes.Buffer: Dispatch writes the failure log
// from a detached goroutine, so a bare bytes.Buffer would race under -race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func TestWebhookFailureIsLoggedNotRaised(t *testing.T) {
	srv := newRecordingServer(http.StatusInternalServerError, time.Second)
	defer srv.Close()

	logBuf := &syncBuffer{}
	sink := &WebhookSink{
		Hooks: []policy.Webhook{{URL: srv.URL}},
		Log:   logBuf,
	}

	start := time.Now()
	sink.Dispatch(context.Background(), Event{Type: EventBuilderStalled, BindingID: "api-auth", Round: 3})
	elapsed := time.Since(start)
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Dispatch took %v, want < 100ms (must not wait for the POST)", elapsed)
	}

	srv.waitCall(t, 2*time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for logBuf.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	logged := logBuf.String()
	host := strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://")
	if !strings.Contains(logged, host) {
		t.Errorf("log %q does not contain host %q", logged, host)
	}
	if !strings.Contains(logged, "500") {
		t.Errorf("log %q does not contain 500", logged)
	}
}

func TestMultiDispatcherFansOut(t *testing.T) {
	srvA := newRecordingServer(http.StatusOK, 0)
	defer srvA.Close()
	srvB := newRecordingServer(http.StatusOK, 0)
	defer srvB.Close()

	sinkA := &WebhookSink{Hooks: []policy.Webhook{{URL: srvA.URL}}}
	sinkB := &WebhookSink{Hooks: []policy.Webhook{{URL: srvB.URL}}}

	m := MultiDispatcher{sinkA, sinkB, nil}
	m.Dispatch(context.Background(), Event{Type: EventRoundStarted, BindingID: "api-auth", Round: 1})

	srvA.waitCall(t, 2*time.Second)
	srvB.waitCall(t, 2*time.Second)
}
