package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// newClientlessVerbFixture is newVerbFixture with no cached client: the shape
// production constructs, where the first attempt builds through Ensure.
func newClientlessVerbFixture(t *testing.T) *verbFixture {
	t.Helper()
	f := newVerbFixture(t)
	f.runner.Runner = &relevosync.Runner{Local: f.local}
	f.client.Calls = nil
	return f
}

// TestSyncVerbLifecycleNeedsNoRestart pins the regression behind this change:
// enable, push, pull and disable all run on a runner that starts clientless,
// with no restart and no prebuilt handle anywhere in between.
func TestSyncVerbLifecycleNeedsNoRestart(t *testing.T) {
	f := newClientlessVerbFixture(t)
	ctx := context.Background()

	putVerbMarker(t, f.shared.LocalOrSelf(), "zstd-compress.v1", `{"done_at":"1970-01-01T00:00:00Z"}`)
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbEnable}, []byte(verbFixtureToken)); !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	// In production the enable's own open joins the live file to the remote;
	// the fake opens nothing, so the test marks the join the way the driver
	// would have. Without it the push below refuses at the membership check,
	// which is the turn-off test's case, not this one's.
	joinFixtureFile(t, f.shared.Path())
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil); !res.OK {
		t.Fatalf("push after enable refused: %s", res.Message)
	}
	if f.runner.Runner.Client == nil {
		t.Fatal("push succeeded but cached no client for the next attempt")
	}
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil); !res.OK {
		t.Fatalf("pull refused: %s", res.Message)
	}
	if res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbDisable}, nil); !res.OK {
		t.Fatalf("disable refused: %s", res.Message)
	}
	if f.runner.Runner.Client != nil {
		t.Fatal("disable left the old client cached past the mark going off")
	}
	res := f.runner.Run(ctx, &wire.SyncVerb{Verb: wire.SyncVerbPush}, nil)
	if res.OK {
		t.Fatal("push after disable succeeded; a disabled machine must refuse")
	}
}

// joinFixtureFile marks a fixture's shared file as joined the way a real
// enable's open would: the driver creates its marker tables on first open.
// Fakes open nothing, so without this every member open refuses and every
// test below would pin the turn-off's case instead of its own.
func joinFixtureFile(t *testing.T, path string) {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	for _, table := range []string{"turso_cdc", "turso_sync_last_change_id", "turso_named_syncs"} {
		if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS ` + table + ` (client_id TEXT PRIMARY KEY)`); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
	}
}

// TestSyncTickSharesTheVerbRunner pins that the background tick drives the
// runner the verbs share: one client built lazily, whichever trigger asked
// first, with the fake recording the order.
func TestSyncTickSharesTheVerbRunner(t *testing.T) {
	f := newClientlessVerbFixture(t)
	if err := relevosync.MarkEnabled(f.local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	joinFixtureFile(t, f.shared.Path())
	d := NewDaemon(Runtime{}, time.Second)
	d.SetSyncVerbs(f.runner)
	d.queueSync(context.Background())
	waitSyncIdle(t, d)
	if got := strings.Join(f.client.Calls, ","); got != "push,pull,stats" {
		t.Fatalf("tick drove %v, want push,pull,stats through the shared runner", f.client.Calls)
	}
}

// TestSyncTickSkipsWhenTheMarkIsOff pins that a shared runner with the mark
// off opens nothing: the tick stays quiet without dialing.
func TestSyncTickSkipsWhenTheMarkIsOff(t *testing.T) {
	f := newClientlessVerbFixture(t)
	d := NewDaemon(Runtime{}, time.Second)
	d.SetSyncVerbs(f.runner)
	d.queueSync(context.Background())
	waitSyncIdle(t, d)
	if len(f.client.Calls) != 0 {
		t.Fatalf("tick with the mark off drove %v, want nothing", f.client.Calls)
	}
}

// TestSyncTickWithoutVerbsKeepsTodaysPath pins that a daemon whose owner
// serves no verbs drives rt.Sync exactly as before: preset client runs,
// nothing preset skips. This is the compatibility half of the tickRunner
// contract.
func TestSyncTickWithoutVerbsKeepsTodaysPath(t *testing.T) {
	f := newClientlessVerbFixture(t)
	d := NewDaemon(Runtime{Sync: &relevosync.Runner{Client: f.client, Local: f.local}}, time.Second)
	d.queueSync(context.Background())
	waitSyncIdle(t, d)
	if got := strings.Join(f.client.Calls, ","); got != "push,pull,stats" {
		t.Fatalf("tick without verbs drove %v, want push,pull,stats", f.client.Calls)
	}

	d2 := NewDaemon(Runtime{}, time.Second)
	d2.queueSync(context.Background())
	waitSyncIdle(t, d2)
}
