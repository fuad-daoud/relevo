package relevo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TakeoverFacts is what the takeover rules read.
type TakeoverFacts struct {
	Name        string
	This        string
	Force       bool
	SyncEnabled bool
	Locked      bool
	// RootFound is true when this machine holds the binding's root row.
	RootFound bool
	// Writer is the installation the root row names as its writer.
	Writer string
	// Mark is how far this machine imported Writer's log.
	Mark int
}

// TakeoverDecision is the old writer and the fence a takeover applies.
type TakeoverDecision struct {
	OldWriter string
	Upto      int
	// Warning is printed before the takeover runs; empty when the taker has
	// imported everything it can.
	Warning string
}

// TakeoverPlan decides a takeover. The fence is the taker's own import mark of
// the old writer: every entry above it stops being valid for every importer.
func TakeoverPlan(f TakeoverFacts) (TakeoverDecision, error) {
	switch {
	case !f.SyncEnabled:
		return TakeoverDecision{}, refuse("sync is off on this machine: relevo db sync enable first; a takeover travels through the sync log")
	case f.Locked:
		return TakeoverDecision{}, refuse("sync is locked on this machine: relevo db sync unlock first; a takeover is signed")
	case !f.Force:
		return TakeoverDecision{}, refuse("relevo takeover fences the old writer's later changes to %q: pass --force once you are sure it will not write again", f.Name)
	case !f.RootFound:
		return TakeoverDecision{}, refuse("this machine holds no record of %q: run relevo db sync pull, then take it over", f.Name)
	case f.Writer == "":
		return TakeoverDecision{}, refuse("binding %q has no writer to take it from", f.Name)
	case f.Writer == f.This:
		return TakeoverDecision{}, refuse("this machine already writes %q", f.Name)
	}
	d := TakeoverDecision{OldWriter: f.Writer, Upto: f.Mark}
	d.Warning = fmt.Sprintf("entries of %s after %d for %q will be fenced; anything it wrote that this machine has not imported is refused everywhere", f.Writer, f.Mark, f.Name)
	return d, nil
}

// TakeoverOptions names the binding. Drain imports the old writer's log as far
// as it goes before the fence is read; nil skips it.
type TakeoverOptions struct {
	Name  string
	Force bool
	Drain func(context.Context) error
}

// Takeover makes this machine the writer of a binding another machine writes.
func Takeover(ctx context.Context, rt Runtime, opts TakeoverOptions) (TakeoverDecision, error) {
	d, err := rt.Store.DB()
	if err != nil {
		return TakeoverDecision{}, err
	}
	local, err := relevosync.LocalHandle(d)
	if err != nil {
		return TakeoverDecision{}, err
	}
	settings, err := relevosync.ReadSettings(local)
	if err != nil {
		return TakeoverDecision{}, err
	}
	if settings.Enabled && opts.Drain != nil {
		if err := opts.Drain(ctx); err != nil {
			return TakeoverDecision{}, err
		}
	}
	owner := rt.Store.Owner()
	roots, _, writer, found, err := d.ForeignRoots(owner, opts.Name)
	if err != nil {
		return TakeoverDecision{}, err
	}
	marks, err := d.ImportMarks()
	if err != nil {
		return TakeoverDecision{}, err
	}
	key, _ := relevosync.KeyState(local)
	dec, err := TakeoverPlan(TakeoverFacts{
		Name: opts.Name, This: d.Origin(), Force: opts.Force,
		SyncEnabled: settings.Enabled, Locked: key == "locked" || key == "stale",
		RootFound: found, Writer: writer, Mark: marks[writer],
	})
	if err != nil {
		return TakeoverDecision{}, err
	}
	err = d.Tx(func(t *db.Tx) error { return applyTakeover(t, roots, dec) })
	if err != nil {
		return TakeoverDecision{}, err
	}
	if rt.SyncNow != nil {
		rt.SyncNow()
	}
	return dec, nil
}

// applyTakeover flips the roots, raises the old writer's term, points the
// taker's entries at the fence and queues the control entry, in one transaction.
func applyTakeover(t *db.Tx, roots db.Roots, dec TakeoverDecision) error {
	if err := t.TakeOver(roots, dec.OldWriter, dec.Upto); err != nil {
		return err
	}
	if dec.Upto > 0 {
		for _, id := range []string{roots.RecordID, roots.BindingID} {
			if id == "" {
				continue
			}
			if err := t.SetWriterAfter(id, dec.OldWriter, dec.Upto); err != nil {
				return err
			}
		}
	}
	body, err := json.Marshal(synclog.Takeover{
		RecordID: roots.RecordID, BindingID: roots.BindingID,
		OldWriter: dec.OldWriter, Upto: dec.Upto, Taker: t.Origin(),
	})
	if err != nil {
		return err
	}
	return t.QueueCtl(db.CtlItem{
		Table: synclog.CtlTakeover, PK: synclog.TakeoverPK(roots.RecordID, dec.OldWriter),
		Body: body, AfterOrigin: dec.OldWriter, AfterSeq: dec.Upto,
	})
}

// TakeoverText renders a takeover for the taker.
func TakeoverText(name string, dec TakeoverDecision) string {
	return fmt.Sprintf("took over %s from %s: its entries after %d are fenced\nrun relevo db sync to publish it; the binding resumes with relevo bind --resume --name %s\n",
		name, dec.OldWriter, dec.Upto, name)
}

