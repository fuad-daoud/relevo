package relevo

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// An idle remote binding must not queue a synced write per tick: the daemon
// polls every binding each tick, and each save is an outbox row every other
// machine pulls. The first sightings may record the server's status word, so
// the test warms up before it counts.
func TestIdleRemoteBindingsQueueNoSyncedWritePerTick(t *testing.T) {
	t.Parallel()

	w := newIdleWorld(t)
	seedOutboxWrite(t, w.mdb, "idle-control")
	control := outboxCount(t, w.raw)
	w.tick(t)
	w.tick(t)
	if got := outboxCount(t, w.raw); got < control {
		t.Fatalf("the control write did not survive two ticks with sync on: %d rows before, %d after",
			control, got)
	}
	if err := w.mdb.TruncateOutbox(); err != nil {
		t.Fatalf("truncate outbox: %v", err)
	}
	for i := 0; i < 20; i++ {
		w.tick(t)
	}
	if rows := idleOutboxRows(t, w.raw); len(rows) != 0 {
		t.Errorf("20 idle ticks queued %d outbox rows, want 0: %v", len(rows), rows)
	}

	t.Run("volatile clocks", func(t *testing.T) {
		w := newIdleWorld(t)
		var polls int
		w.fr.beforeCall = func(string) {
			polls++
			at := baseTime.Add(time.Duration(polls) * time.Second)
			w.fr.getBindingResp.RoundStartedAt = at
			w.fr.getBindingResp.StalledSince = at
		}
		w.tick(t)
		w.tick(t)
		if err := w.mdb.TruncateOutbox(); err != nil {
			t.Fatalf("truncate outbox: %v", err)
		}
		for i := 0; i < 20; i++ {
			w.tick(t)
		}
		if rows := idleOutboxRows(t, w.raw); len(rows) != 0 {
			t.Errorf("20 idle ticks against moving clocks queued %d outbox rows, want 0: %v",
				len(rows), rows)
		}
	})
}

// idleWorld is two remote bindings whose server has no round running, on a
// machine whose sync is on so the tick's truncate stays off it and every row
// the polls queue is still there to count.
type idleWorld struct {
	mdb *db.DB
	raw *sql.DB
	fr  *fakeRemote
	now time.Time
	d   *Daemon
}

func newIdleWorld(t *testing.T) *idleWorld {
	t.Helper()
	st := store.New(t.TempDir())
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, baseTime); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	first := remoteBinding("zen")
	second := remoteBinding("zen")
	second.Name = "web"
	for _, b := range []store.Binding{first, second} {
		if err := st.Save(b); err != nil {
			t.Fatalf("save binding: %v", err)
		}
	}
	w := &idleWorld{now: baseTime}
	w.fr = &fakeRemote{getBindingResp: remote.BindingView{
		State: string(store.StateActive), Round: 1, RoundState: remote.RoundIdle,
	}}
	w.d = NewDaemon(Runtime{Store: st, Remote: w.fr, Now: func() time.Time { return w.now }}, time.Second)
	raw, err := db.OpenRaw(st.DBPath())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	w.mdb, w.raw = mdb, raw
	return w
}

func (w *idleWorld) tick(t *testing.T) {
	t.Helper()
	if err := w.d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	w.now = w.now.Add(time.Second)
}

// idleOutboxRows lists every outbox entry's table and pk, for a failure
// message that names what an idle tick rewrote.
func idleOutboxRows(t *testing.T, raw *sql.DB) []string {
	t.Helper()
	rows, err := raw.Query(`SELECT tbl, pk FROM sync_outbox ORDER BY seq`)
	if err != nil {
		t.Fatalf("read sync_outbox: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var tbl, pk string
		if err := rows.Scan(&tbl, &pk); err != nil {
			t.Fatalf("scan sync_outbox: %v", err)
		}
		out = append(out, tbl+":"+pk)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("walk sync_outbox: %v", err)
	}
	return out
}
