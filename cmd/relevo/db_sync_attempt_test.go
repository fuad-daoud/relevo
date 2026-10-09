//go:build unix

package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// seedAttempt writes the daemon's last-attempt marker beside a tick marker
// that still says the last tick was fine, which is the machine the old status
// read as healthy.
func seedAttempt(t *testing.T, a relevosync.Attempt) {
	t.Helper()
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("encode the attempt: %v", err)
	}
	if err := dialledLocal(t).KVPut(relevosync.KeyLastAttempt, body); err != nil {
		t.Fatalf("write the attempt marker: %v", err)
	}
}

func staleHealthyMachine() dbSyncStatusCase {
	return dbSyncStatusCase{
		name:      "stale times",
		enabled:   true,
		tickOK:    true,
		remote:    syncStatusRemote,
		namespace: syncStatusNamespace,
		token:     syncStatusSecret,
	}
}

func TestStatusShowsAFailedAttemptOnBothShapes(t *testing.T) {
	serveSyncStatus(t, staleHealthyMachine())
	end := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	seedAttempt(t, relevosync.Attempt{
		Start: end.Add(-time.Second), End: end, Exported: 3, Applied: 1,
		Error: "remote down\x1b[31m", Attention: true,
	})

	text, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	for _, want := range []string{"last attempt failed at 2026-10-04T12:05:00Z", "remote down", "exported 3, applied 1", "needs attention"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("status line %q is missing %q", string(text), want)
		}
	}
	if strings.Contains(string(text), "\x1b") {
		t.Errorf("status line %q carries a control sequence", string(text))
	}

	jsonOut, _, err := captureOutput(t, func() error { return cmdDBSyncStatus([]string{"--json"}) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus --json: %v", err)
	}
	var doc dbSyncStatusDoc
	if err := json.Unmarshal(jsonOut, &doc); err != nil {
		t.Fatalf("decode the document: %v", err)
	}
	if doc.LastAttempt == nil || doc.LastAttempt.Error == "" || !doc.LastAttempt.Attention ||
		doc.LastAttempt.Exported != 3 || doc.LastAttempt.Applied != 1 {
		t.Errorf("document last_attempt = %+v, want the failed attempt", doc.LastAttempt)
	}

	state, err := relevosync.ReadState(dialledLocal(t))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got := relevosync.Token(state); got != relevosync.TokenBehind {
		t.Errorf("token for a machine whose last attempt failed = %q, want %q", got, relevosync.TokenBehind)
	}
}

func TestStatusShowsACleanAttemptAndOmitsNone(t *testing.T) {
	serveSyncStatus(t, staleHealthyMachine())

	text, _, err := captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	if strings.Contains(string(text), "last attempt") {
		t.Errorf("a machine with no recorded attempt rendered one: %q", string(text))
	}

	end := time.Date(2026, 10, 4, 12, 5, 0, 0, time.UTC)
	seedAttempt(t, relevosync.Attempt{Start: end.Add(-time.Second), End: end, Exported: 2})
	text, _, err = captureOutput(t, func() error { return cmdDBSyncStatus(nil) })
	if err != nil {
		t.Fatalf("cmdDBSyncStatus: %v", err)
	}
	if !strings.Contains(string(text), "last attempt ok at 2026-10-04T12:05:00Z (exported 2, applied 0)") {
		t.Errorf("status line %q does not show the clean attempt", string(text))
	}
}
