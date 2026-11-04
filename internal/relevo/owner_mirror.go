package relevo

import (
	"context"
	"log/slog"
	"time"
)

// OwnerMirror keeps one server owner's bindings mirrored into the database. A
// server's reconcile Daemons live for one tick, so they hold none of the memos
// that keep the mirror cheap: run there, every tick would ingest every live
// binding and list every archived record. An OwnerMirror lives as long as the
// server process, so an unchanged binding costs one revision read a pass and
// the archive one listing of what is new.
type OwnerMirror struct {
	d *Daemon
}

// NewOwnerMirror returns a mirror over rt, whose DB must be set. A record
// archived more than archiveLookback before since stays out: importing all of
// a server's past on its first start would write every old log and report for
// rows nothing asks of an already finished binding.
func NewOwnerMirror(rt Runtime, since time.Time) *OwnerMirror {
	d := NewDaemon(rt, 0)
	d.archived.primed = true
	d.archived.highWater = since
	return &OwnerMirror{d: d}
}

// Pass mirrors what changed since the last one. A record the owner archived is
// read first, so a binding that left the live list between passes still lands.
func (m *OwnerMirror) Pass(ctx context.Context) {
	m.d.safely("mirror", func() {
		m.d.mirrorArchived(ctx)
		bindings, err := m.d.rt.Store.List()
		if err != nil {
			slog.Warn("mirror: list bindings", "err", err)
			return
		}
		m.d.ingestLiveBindings(ctx, bindings)
	})
}
