package relevo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// pullLog installs an in-memory log as the fixture's transport, so a pull reads
// real entries instead of a recorded call.
func pullLog(t *testing.T, f *verbFixture) *synclog.MemTransport {
	t.Helper()
	log := synclog.NewMemTransport("m1")
	f.runner.Runner.Client = log
	return log
}

// seedPullEntry appends one binding entry written by origin at schema.
func seedPullEntry(t *testing.T, log *synclog.MemTransport, origin, id string, schema int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"id": id, "name": id, "cwd": "/far", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual",
	})
	if err != nil {
		t.Fatalf("encode the entry: %v", err)
	}
	entry, err := synclog.NewUpsert(origin, "binding", `["`+id+`"]`, schema, body, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatalf("build the entry: %v", err)
	}
	if _, err := log.OnLog(origin).Append([]synclog.Entry{entry}); err != nil {
		t.Fatalf("append the entry: %v", err)
	}
}

func pullTrouble(t *testing.T, f *verbFixture) relevosync.Trouble {
	t.Helper()
	state, err := relevosync.ReadState(f.local)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	return state.Trouble
}

func pullOnce(t *testing.T, f *verbFixture) *wire.SyncResult {
	t.Helper()
	res := f.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil)
	if !res.OK {
		t.Fatalf("pull refused: %s", res.Message)
	}
	return res
}

// TestPullReportsAndRecordsAHeldOrigin pins that a pull which holds an origin
// says so in its result and leaves the same report in the trouble marker, so a
// status read straight after needs no steady tick. The pull also applied the
// other origin, and both facts are reported.
func TestPullReportsAndRecordsAHeldOrigin(t *testing.T) {
	f := newVerbFixture(t)
	log := pullLog(t, f)
	_, known := f.shared.SchemaVersions()
	seedPullEntry(t, log, "m2", "b2", known)
	seedPullEntry(t, log, "m3", "b3", known+1)

	res := pullOnce(t, f)
	if !res.Applied {
		t.Error("pull applied nothing, want the readable origin applied")
	}
	if len(res.Holds) != 1 || !strings.Contains(res.Holds[0], "upgrade") {
		t.Fatalf("pull holds = %v, want the one held origin", res.Holds)
	}
	got := pullTrouble(t, f)
	if len(got.Held) != 1 || got.Held[0] != res.Holds[0] {
		t.Errorf("trouble marker held = %v, want the hold the pull printed %v", got.Held, res.Holds)
	}
}

// TestPullClearsAHeldMarkerOnceCleanAndKeepsDrops pins the write-empty rule: a
// clean pull replaces the per-run hold report with nothing, while dropped
// reports an earlier run left stay.
func TestPullClearsAHeldMarkerOnceCleanAndKeepsDrops(t *testing.T) {
	f := newVerbFixture(t)
	pullLog(t, f)
	relevosync.RecordTrouble(f.local, relevosync.Trouble{Held: []string{"m3 is held"}, Dropped: []string{"dropped m2"}})

	res := pullOnce(t, f)
	if len(res.Holds) != 0 {
		t.Errorf("clean pull holds = %v, want none", res.Holds)
	}
	got := pullTrouble(t, f)
	if len(got.Held) != 0 || len(got.Dropped) != 1 {
		t.Errorf("trouble marker = %+v, want the hold cleared and the drop kept", got)
	}
}

// TestPullReportsAGap pins that a log hole is reported by the pull as a hold.
func TestPullReportsAGap(t *testing.T) {
	trouble := relevosync.TroubleFrom(synclog.ImportResult{Gaps: []synclog.Gap{{Origin: "m2", Want: 3, Got: 5}}})
	holds := trouble.Holds()
	if len(holds) != 1 || !strings.Contains(holds[0], "m2 is held at 2") {
		t.Errorf("holds = %v, want the gap sentence", holds)
	}
}
