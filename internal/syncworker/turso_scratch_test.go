package syncworker

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestScratchRemoteExportPullRoundTrip is the one test that reaches a real
// remote. It is skipped unless both scratch variables are set, because CI has
// no network and no Turso, and it is the only place the real driver runs: the
// rest of the round serves the pipe with a local file and a driver double.
//
// It proves the shape the backend is for: one installation creates the log on
// an empty remote and pushes, a second joins with its own replica, pulls the
// first's entry, appends its own, and the first pulls that back.
func TestScratchRemoteExportPullRoundTrip(t *testing.T) {
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN to run against a scratch remote")
	}
	dir := t.TempDir()
	// A fresh origin per run keeps one run's entries out of the next one's
	// assertions; the log itself accumulates across runs, which is what a real
	// remote does.
	aOrigin := "scratch-a-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	bOrigin := "scratch-b-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	a := openScratch(t, filepath.Join(dir, "a.db"), aOrigin, url, token)
	written, err := a.Append([]Entry{
		upsertEntry(aOrigin, "board", `["scratch"]`, `{"title":"round trip"}`),
	})
	if err != nil {
		t.Fatalf("append to the scratch remote: %v", err)
	}
	if len(written) != 1 || written[0].Seq < 1 {
		t.Fatalf("append = %+v, want one numbered entry", written)
	}

	b := openScratch(t, filepath.Join(dir, "b.db"), bOrigin, url, token)
	pulled, err := b.Pull(nil)
	if err != nil {
		t.Fatalf("pull from the scratch remote: %v", err)
	}
	if !carries(pulled, aOrigin, written[0].Seq) {
		t.Errorf("the other installation pulled %d entries but not %s seq %d",
			len(pulled), aOrigin, written[0].Seq)
	}

	back, err := b.Append([]Entry{
		upsertEntry(bOrigin, "board", `["scratch-b"]`, `{"title":"answered"}`),
	})
	if err != nil {
		t.Fatalf("append back: %v", err)
	}
	pulled, err = a.Pull(nil)
	if err != nil {
		t.Fatalf("pull back: %v", err)
	}
	if !carries(pulled, bOrigin, back[0].Seq) {
		t.Errorf("the first installation pulled %d entries but not %s seq %d",
			len(pulled), bOrigin, back[0].Seq)
	}

	head, err := b.Head(bOrigin, "", 0)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if len(head) != 1 || head[0].Seq != back[0].Seq {
		t.Errorf("head = %+v, want the row the append wrote", head)
	}
	stats, err := b.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries < 2 || stats.Origins < 2 {
		t.Errorf("stats = %+v, want the log to hold both origins' entries", stats)
	}
}

// openScratch opens one installation's backend against the real remote through
// the production driver.
func openScratch(t *testing.T, replicaPath, origin, url, token string) *TursoBackend {
	t.Helper()
	b := NewTursoBackend(newTursoDriver(replicaPath))
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  origin,
		URL:     url,
		Token:   token,
	}); err != nil {
		t.Fatalf("open %s against the scratch remote: %v", origin, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// carries reports whether an origin's entry at seq came back from a pull.
func carries(entries []Entry, origin string, seq int) bool {
	for _, e := range entries {
		if e.Origin == origin && e.Seq == seq {
			return true
		}
	}
	return false
}
