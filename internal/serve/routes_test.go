package serve

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// serveSigned routes one signed GET through srv and returns the response code
// with everything the server logged while serving it. The log capture starts
// after the server exists so setup noise never lands in the buffer.
func serveSigned(t *testing.T, srv *Server, kp remote.Keypair, target string) (int, string) {
	t.Helper()
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := signedRequest(t, kp, "GET", target, nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, logged.String()
}

// requestLines returns the logged access lines: a request always logs exactly
// one, whatever else the handlers emit around it.
func requestLines(logged string) []string {
	var lines []string
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, "http request") {
			lines = append(lines, line)
		}
	}
	return lines
}

func enrolledServer(t *testing.T) (*Server, remote.Keypair) {
	t.Helper()
	srv, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
		t.Fatalf("clients.Add: %v", err)
	}
	trackOwnerTmp(t, srv, remote.IDOf(kp.Public))
	return srv, kp
}

// TestSlowRequestLogsOneWarnLineWithDuration pins the slow side: with the
// threshold at zero every request is slow, so the single access line goes out
// at WARN carrying the request's path and duration, and neither the query
// string nor the signature it was signed with.
func TestSlowRequestLogsOneWarnLineWithDuration(t *testing.T) {
	prevThreshold := slowRequestThreshold
	slowRequestThreshold = 0
	t.Cleanup(func() { slowRequestThreshold = prevThreshold })

	srv, kp := enrolledServer(t)
	code, logged := serveSigned(t, srv, kp, "/v1/whoami?debug-leak=query-9z&token=tok-leak-9z")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; logs:\n%s", code, logged)
	}

	lines := requestLines(logged)
	if len(lines) != 1 {
		t.Fatalf("logged %d http request lines, want 1:\n%s", len(lines), logged)
	}
	for _, want := range []string{`level=WARN`, `msg="http request"`, `path=/v1/whoami`, `duration=`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("slow line lacks %q:\n%s", want, lines[0])
		}
	}
	for _, leak := range []string{"debug-leak", "query-9z", "tok-leak-9z"} {
		if strings.Contains(logged, leak) {
			t.Errorf("logs contain query material %q:\n%s", leak, logged)
		}
	}
}

// TestSlowRequestHidesSignature pins that the slow line carries no token
// material: the signature header the request was signed with appears nowhere
// in what the server logged.
func TestSlowRequestHidesSignature(t *testing.T) {
	prevThreshold := slowRequestThreshold
	slowRequestThreshold = 0
	t.Cleanup(func() { slowRequestThreshold = prevThreshold })

	srv, kp := enrolledServer(t)

	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	req := signedRequest(t, kp, "GET", "/v1/whoami", nil)
	sig := req.Header.Get(remote.HeaderSignature)
	if sig == "" {
		t.Fatal("signed request carries no signature")
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; logs:\n%s", rec.Code, logged.String())
	}
	if strings.Contains(logged.String(), sig) {
		t.Errorf("logs contain the request signature:\n%s", logged.String())
	}
}

// TestFastRequestKeepsSingleInfoLine pins the fast side: with the threshold
// at an hour no request is slow, so the access line stays exactly today's
// INFO line with no duration attached.
func TestFastRequestKeepsSingleInfoLine(t *testing.T) {
	prevThreshold := slowRequestThreshold
	slowRequestThreshold = time.Hour
	t.Cleanup(func() { slowRequestThreshold = prevThreshold })

	srv, kp := enrolledServer(t)
	code, logged := serveSigned(t, srv, kp, "/v1/whoami")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200; logs:\n%s", code, logged)
	}

	lines := requestLines(logged)
	if len(lines) != 1 {
		t.Fatalf("logged %d http request lines, want 1:\n%s", len(lines), logged)
	}
	for _, want := range []string{`level=INFO`, `msg="http request"`, `path=/v1/whoami`} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("fast line lacks %q:\n%s", want, lines[0])
		}
	}
	if strings.Contains(lines[0], "duration=") {
		t.Errorf("fast line carries a duration:\n%s", lines[0])
	}
	if strings.Contains(logged, "level=WARN") {
		t.Errorf("fast request logged at WARN:\n%s", logged)
	}
}
