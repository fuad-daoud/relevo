package relevo

import (
	"context"
	"sync"
	"testing"
	"time"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// unreadyTransport is the placeholder a daemon starts with before anything has
// opened the remote: it names no remote, and every call that reaches it is
// recorded, because a call here is a worker started with no origin, URL or
// token.
type unreadyTransport struct {
	mu    sync.Mutex
	calls int
}

func (u *unreadyTransport) Ready() bool { return false }

func (u *unreadyTransport) hit() {
	u.mu.Lock()
	u.calls++
	u.mu.Unlock()
}

func (u *unreadyTransport) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *unreadyTransport) Append([]synclog.Entry) ([]synclog.Entry, error) { u.hit(); return nil, nil }
func (u *unreadyTransport) Pull(map[string]int) ([]synclog.Entry, error)    { u.hit(); return nil, nil }
func (u *unreadyTransport) Head(string) ([]synclog.HeadRow, error)          { u.hit(); return nil, nil }
func (u *unreadyTransport) Stats() (synclog.Stats, error)                   { u.hit(); return synclog.Stats{}, nil }

var _ synclog.LogTransport = (*unreadyTransport)(nil)

// TestTickOpensTheStoredRemoteAfterARestart pins the daemon that starts on a
// machine already marked on -- a reboot, an upgrade, a re-exec: its runner holds
// the placeholder, and the first tick must open the remote the stored rows name
// and sync through it, never drive the placeholder.
func TestTickOpensTheStoredRemoteAfterARestart(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, _ := bindReader(t, repo)
	mdb, err := rt.Store.DB()
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

	log := synclog.NewMemTransport("m2")
	_, known := mdb.SchemaVersions()
	seedFar(t, log, "m2", "far-1", known)
	opened := &tickTransport{LogTransport: log.OnLog(mdb.Origin())}

	placeholder := &unreadyTransport{}
	opens := 0
	verbs := &VerbRunner{
		Shared: mdb,
		Local:  local,
		Runner: &relevosync.Runner{Client: placeholder, Local: local},
		Open: func(context.Context) (synclog.LogTransport, error) {
			opens++
			return opened, nil
		},
	}

	d := NewDaemon(rt, time.Second)
	d.SetSyncVerbs(verbs)
	if err := d.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	waitSyncIdle(t, d)

	if got := placeholder.count(); got != 0 {
		t.Errorf("the tick drove the placeholder %d times, want none", got)
	}
	if opens != 1 {
		t.Errorf("the tick opened the stored remote %d times, want once", opens)
	}
	if !opened.called("pull") {
		t.Error("the tick never imported through the remote it opened")
	}
	if got, ok, err := mdb.ImportMark("m2"); err != nil || !ok || got == 0 {
		t.Errorf("import mark for m2 = (%d, %v, %v), want the far entry applied", got, ok, err)
	}
}
