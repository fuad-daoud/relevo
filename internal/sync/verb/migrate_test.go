package verb

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The migrate tests drive the verb over the same split-pair fixture as the
// other verbs, with a staged migration worker in place of the log transport: a
// migration never moves rows through the steady transport, so the fixture's
// record stays the steady worker the verb must release.

// fakeMigrator is the migration worker a test hands a runner: it answers with
// the result the test staged and records that it ran and was released.
type fakeMigrator struct {
	result   synclog.MigrateResult
	err      error
	closeErr error
	migrates int
	closes   int
}

func (f *fakeMigrator) Migrate() (synclog.MigrateResult, error) {
	f.migrates++
	return f.result, f.err
}

func (f *fakeMigrator) Close() error {
	f.closes++
	return f.closeErr
}

var _ synclog.Migrator = (*fakeMigrator)(nil)

// cancelCountingTransport is the steady worker a migrate releases: it records
// being cancelled, so a test can say the runner really let go of the replica
// file before the migration opened.
type cancelCountingTransport struct {
	*recordTransport
	cancels int
}

func (c *cancelCountingTransport) Cancel() { c.cancels++ }

// migrateFixture is a verb fixture with the steady worker wrapped in a cancel
// counter and the migrate opener installed.
type migrateFixture struct {
	base     *verbFixture
	steady   *cancelCountingTransport
	migrator *fakeMigrator
	opens    int
}

func newMigrateFixture(t *testing.T) *migrateFixture {
	t.Helper()
	f := newVerbFixture(t)
	steady := &cancelCountingTransport{recordTransport: f.client}
	f.runner.Runner.Client = steady
	fx := &migrateFixture{base: f, steady: steady, migrator: &fakeMigrator{}}
	f.runner.OpenMigrate = func(context.Context) (synclog.Migrator, error) {
		fx.opens++
		return fx.migrator, nil
	}
	return fx
}

// stagedMigrateResult is a migration answer with every total set, across two
// origins, so a conversion that drops or shuffles a field shows up.
func stagedMigrateResult() synclog.MigrateResult {
	return synclog.MigrateResult{
		AlreadyMigrated: true,
		Rows:            7, ObjectsCreated: 3, ObjectsVerified: 1,
		RowsSwept: 2, SharedRows: 5,
		R2Put: 11, R2Get: 13, TursoSent: 17, TursoReceived: 19,
		Origins: []synclog.MigrateEntry{
			{Origin: "origin-a", MigrateOrigin: synclog.MigrateOrigin{
				Rows: 4, Objects: 2, Seq: 9, Floor: 8, LegacyUpto: 7, Label: "shop",
			}},
			{Origin: "origin-b", MigrateOrigin: synclog.MigrateOrigin{
				Rows: 3, Objects: 1, Seq: 6, Floor: 5, LegacyUpto: 4, Label: "web",
			}},
		},
	}
}

// setMigrateWait parks the breaker in the migration wait, which is the state a
// migrate run by hand must clear.
func setMigrateWait(t *testing.T, f *verbFixture) {
	t.Helper()
	wait := fmt.Errorf("sync: pull: %w", relevosync.ErrMigrateWaiting)
	if err := relevosync.NewBreaker(f.local).Refused(wait); err != nil {
		t.Fatalf("set the migrate wait: %v", err)
	}
}

// migrateWaitSet reports whether the breaker is still waiting on a migration.
func migrateWaitSet(t *testing.T, f *verbFixture) bool {
	t.Helper()
	st, err := relevosync.NewBreaker(f.local).State()
	if err != nil {
		t.Fatalf("breaker state: %v", err)
	}
	return !st.WaitUntil.IsZero()
}

// TestSyncMigrateRefusesWithoutConfirm pins the confirm rule: a call without
// the flag is refused before anything opens, so the steady worker is never
// released and a waiting breaker stays waiting.
//
// The mutation is the gate skipped: the verb would open the migrator and
// answer OK on an unconfirmed call.
func TestSyncMigrateRefusesWithoutConfirm(t *testing.T) {
	fx := newMigrateFixture(t)
	setMigrateWait(t, fx.base)

	res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbMigrate}, nil)
	if res.OK {
		t.Fatal("an unconfirmed migrate reported OK")
	}
	if res.Code != wire.SyncCodeInvalid {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
	}
	if fx.opens != 0 {
		t.Errorf("an unconfirmed migrate opened the migrator %d times, want none", fx.opens)
	}
	if fx.steady.cancels != 0 {
		t.Errorf("an unconfirmed migrate cancelled the steady worker %d times, want none", fx.steady.cancels)
	}
	if !migrateWaitSet(t, fx.base) {
		t.Error("an unconfirmed migrate cleared the wait it must not touch")
	}
}

// TestSyncMigrateRunsAgainstTheReleasedReplica pins the confirmed run: the
// opener runs once, the steady worker is released on both sides of the call,
// the wait is cleared, a trigger is queued for the fresh worker, and the
// answer carries the migration's own counts.
//
// The mutations are the first Retry skipped (the steady worker's cancel count
// stays put), the close skipped, and the guard bypassed.
func TestSyncMigrateRunsAgainstTheReleasedReplica(t *testing.T) {
	fx := newMigrateFixture(t)
	fx.migrator.result = stagedMigrateResult()
	setMigrateWait(t, fx.base)
	triggered := false
	fx.base.runner.Trigger = func() { triggered = true }
	serialized := false
	fx.base.runner.Serialize = func(fn func()) { serialized = true; fn() }

	res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
		Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
	}, nil)
	if !res.OK {
		t.Fatalf("migrate refused: %s", res.Message)
	}
	if fx.opens != 1 {
		t.Errorf("the migrate opened the migrator %d times, want once", fx.opens)
	}
	if fx.migrator.migrates != 1 {
		t.Errorf("the migrate ran %d times, want once", fx.migrator.migrates)
	}
	if fx.migrator.closes != 1 {
		t.Errorf("the migrate closed the migrator %d times, want once", fx.migrator.closes)
	}
	if fx.steady.cancels != 2 {
		t.Errorf("the steady worker was cancelled %d times, want twice: before the open and after the run", fx.steady.cancels)
	}
	if migrateWaitSet(t, fx.base) {
		t.Error("a finished migrate left the breaker waiting")
	}
	if !triggered {
		t.Error("a finished migrate queued no attempt for the fresh worker")
	}
	if !serialized {
		t.Error("a migrate ran outside the daemon's guard")
	}
	want := &wire.SyncMigrate{
		AlreadyMigrated: true,
		Rows:            7, ObjectsCreated: 3, ObjectsVerified: 1,
		RowsSwept: 2, SharedRows: 5,
		R2Put: 11, R2Get: 13, TursoSent: 17, TursoReceived: 19,
		Origins: []wire.SyncMigrateOrigin{
			{Origin: "origin-a", Rows: 4, Objects: 2, Seq: 9, Floor: 8, LegacyUpto: 7, Label: "shop"},
			{Origin: "origin-b", Rows: 3, Objects: 1, Seq: 6, Floor: 5, LegacyUpto: 4, Label: "web"},
		},
	}
	if !reflect.DeepEqual(res.Migrate, want) {
		t.Errorf("migrate answer = %+v, want %+v", res.Migrate, want)
	}
}

// TestSyncMigrateSucceedsWithoutTriggerWhenCloseFails pins the nil-trigger
// success path and the ignored close error: a runner with no trigger bound
// still reports the migration's own counts, and a close that fails does not
// shadow them.
//
// The mutations are an unconditional trigger call (nil dereference on a
// triggerless runner) and a close error replacing the OK answer with a
// refusal.
func TestSyncMigrateSucceedsWithoutTriggerWhenCloseFails(t *testing.T) {
	fx := newMigrateFixture(t)
	fx.migrator.result = stagedMigrateResult()
	fx.migrator.closeErr = fmt.Errorf("sync: migrate: close the released replica: %w", relevosync.ErrBackingOff)

	res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
		Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
	}, nil)
	if !res.OK {
		t.Fatalf("migrate with no trigger and a failing close refused: %s", res.Message)
	}
	want := &wire.SyncMigrate{
		AlreadyMigrated: true,
		Rows:            7, ObjectsCreated: 3, ObjectsVerified: 1,
		RowsSwept: 2, SharedRows: 5,
		R2Put: 11, R2Get: 13, TursoSent: 17, TursoReceived: 19,
		Origins: []wire.SyncMigrateOrigin{
			{Origin: "origin-a", Rows: 4, Objects: 2, Seq: 9, Floor: 8, LegacyUpto: 7, Label: "shop"},
			{Origin: "origin-b", Rows: 3, Objects: 1, Seq: 6, Floor: 5, LegacyUpto: 4, Label: "web"},
		},
	}
	if !reflect.DeepEqual(res.Migrate, want) {
		t.Errorf("migrate answer = %+v, want %+v", res.Migrate, want)
	}
	if fx.migrator.migrates != 1 {
		t.Errorf("the migrate ran %d times, want once", fx.migrator.migrates)
	}
	if fx.migrator.closes != 1 {
		t.Errorf("the migrate closed the migrator %d times, want once", fx.migrator.closes)
	}
}

// TestSyncMigrateIncompleteRefusesWithClose pins the late-rows refusal: rows an
// old binary wrote after the read refuse with their own class, and the worker
// is still released.
func TestSyncMigrateIncompleteRefusesWithClose(t *testing.T) {
	fx := newMigrateFixture(t)
	fx.migrator.err = fmt.Errorf("sync: migrate: %w", relevosync.ErrMigrateIncomplete)

	res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
		Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
	}, nil)
	if res.OK {
		t.Fatal("a migrate met with late rows reported OK")
	}
	if res.Code != wire.SyncCodeMigrateIncomplete {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeMigrateIncomplete)
	}
	if fx.migrator.closes != 1 {
		t.Errorf("a refused migrate closed the migrator %d times, want once", fx.migrator.closes)
	}
}

// TestSyncMigrateOpenerErrorMapsThroughClassify pins that a worker that never
// opened classifies like any other open failure, through the same table.
func TestSyncMigrateOpenerErrorMapsThroughClassify(t *testing.T) {
	fx := newMigrateFixture(t)
	openErr := fmt.Errorf("sync: migrate: open: %w", relevosync.ErrBackingOff)
	fx.base.runner.OpenMigrate = func(context.Context) (synclog.Migrator, error) {
		fx.opens++
		return nil, openErr
	}

	res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
		Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
	}, nil)
	if res.OK {
		t.Fatal("a migrate whose worker never opened reported OK")
	}
	if res.Code != Classify(openErr) {
		t.Errorf("code = %q, want the Classify answer %q", res.Code, Classify(openErr))
	}
	if res.Code != wire.SyncCodeBackingOff {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeBackingOff)
	}
	if fx.migrator.migrates != 0 {
		t.Error("a migrate that never opened still ran")
	}
}

// TestSyncMigrateRefusesWithoutWiring pins the two halves of the unwired
// daemon: no opener, or no runner to release, is an invalid refusal rather
// than a call that opens something by hand.
func TestSyncMigrateRefusesWithoutWiring(t *testing.T) {
	t.Run("no opener", func(t *testing.T) {
		f := newVerbFixture(t)
		res := f.runner.Run(context.Background(), &wire.SyncVerb{
			Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
		}, nil)
		if res.OK {
			t.Fatal("a migrate with no opener reported OK")
		}
		if res.Code != wire.SyncCodeInvalid {
			t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
		}
	})
	t.Run("no runner", func(t *testing.T) {
		fx := newMigrateFixture(t)
		opens := fx.opens
		fx.base.runner.Runner = nil
		res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
			Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
		}, nil)
		if res.OK {
			t.Fatal("a migrate with no runner reported OK")
		}
		if res.Code != wire.SyncCodeInvalid {
			t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
		}
		if fx.opens != opens {
			t.Error("a migrate with no runner to release still opened the migrator")
		}
	})
}

// TestSyncMigrateRetryFailureRefuses pins both retries' errors: a latch that
// never cleared, before or after the run, is a refusal rather than a
// migration reported OK.
func TestSyncMigrateRetryFailureRefuses(t *testing.T) {
	t.Run("first retry", func(t *testing.T) {
		fx := newMigrateFixture(t)
		fx.base.runner.Runner.Local = nil
		res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
			Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
		}, nil)
		if res.OK {
			t.Fatal("a migrate whose first retry failed reported OK")
		}
		if fx.opens != 0 {
			t.Error("a migrate whose first retry failed still opened the migrator")
		}
	})
	t.Run("second retry", func(t *testing.T) {
		fx := newMigrateFixture(t)
		fx.migrator.result = stagedMigrateResult()
		fx.base.runner.OpenMigrate = func(context.Context) (synclog.Migrator, error) {
			fx.opens++
			// The rows moved, then the latch could not clear: the run must
			// refuse rather than report counts that never took effect.
			fx.base.runner.Runner.Local = nil
			return fx.migrator, nil
		}
		res := fx.base.runner.Run(context.Background(), &wire.SyncVerb{
			Verb: wire.SyncVerbMigrate, ConfirmAllUpgraded: true,
		}, nil)
		if res.OK {
			t.Fatal("a migrate whose second retry failed reported OK")
		}
		if fx.migrator.migrates != 1 {
			t.Error("a migrate refused after the run never ran")
		}
		if fx.migrator.closes != 1 {
			t.Errorf("a migrate refused after the run closed %d times, want once", fx.migrator.closes)
		}
	})
}
