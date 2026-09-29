package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// deleteTimeout bounds one harness session delete. The delete is best-effort
// bookkeeping beside a stopped round, so it must not hold a tick open.
const deleteTimeout = 30 * time.Second

// reapMaxAttempts is how many failed deletes an entry survives before relevo
// gives up and warns for a human: a persistent failure (a missing binary, a
// locked database) must not retry forever.
const reapMaxAttempts = 5

// SessionDeleter deletes one abandoned harness session. A nil return means the
// session no longer exists, whether this call deleted it or it was already
// gone.
type SessionDeleter interface {
	DeleteSession(ctx context.Context, s store.AbandonedSession) error
}

// sessionReaper is the real SessionDeleter: it runs the harness's own delete
// command through an Exec.
type sessionReaper struct {
	exec usage.Exec
}

// NewSessionReaper returns the SessionDeleter that deletes sessions through x.
func NewSessionReaper(x usage.Exec) SessionDeleter {
	return sessionReaper{exec: x}
}

func (r sessionReaper) DeleteSession(ctx context.Context, s store.AbandonedSession) error {
	h, ok := harness.Lookup(s.Kind)
	if !ok {
		return fmt.Errorf("delete session %q: unknown harness kind %q", s.ID, s.Kind)
	}
	argv, err := h.DeleteSession(s.ID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()
	if _, err := r.exec.Run(ctx, h.Binary, argv...); err != nil {
		// A session that is already gone is the postcondition, not a failure:
		// it keeps the delete idempotent across attempts.
		if strings.Contains(err.Error(), "Session not found") {
			return nil
		}
		return err
	}
	return nil
}

// abandonSessionID records one harness session as abandoned on b. It is pure:
// the caller saves. An id is recorded only when it is non-empty, its kind
// names a harness relevo can delete sessions for, and the pair is not already
// present.
func abandonSessionID(b store.Binding, kind, id string) store.Binding {
	if id == "" {
		return b
	}
	h, ok := harness.Lookup(kind)
	if !ok {
		return b
	}
	if _, err := h.DeleteSession(id); err != nil {
		return b
	}
	for _, s := range b.AbandonedSessions {
		if s.Kind == kind && s.ID == id {
			return b
		}
	}
	// Copy rather than append in place: b's slice may be shared with another
	// binding value, and the function's purity is part of its contract.
	next := make([]store.AbandonedSession, len(b.AbandonedSessions), len(b.AbandonedSessions)+1)
	copy(next, b.AbandonedSessions)
	b.AbandonedSessions = append(next, store.AbandonedSession{Kind: kind, ID: id})
	return b
}

// abandonSession records b's current builder session as abandoned and clears
// the id. Clearing is deliberate: the stopped round's report entry then names
// no session instead of one that is about to be deleted. A non-headless
// builder is left alone -- only a headless round has a harness session.
func abandonSession(b store.Binding) store.Binding {
	if !b.Builder.Headless() {
		return b
	}
	b = abandonSessionID(b, b.Builder.Kind, b.Builder.StreamSessionID)
	b.Builder.StreamSessionID = ""
	return b
}

// reapable reports whether b's abandoned sessions may be deleted yet. A live
// process that has not announced its session blocks: a --fork resume reads the
// original session when it starts, so deleting that session first would break
// the fork.
func reapable(b store.Binding) bool {
	if b.Builder.PID != 0 && b.Builder.StreamSessionID == "" {
		return false
	}
	return len(b.AbandonedSessions) > 0
}

// sessionKey identifies an abandoned session by its harness and id. Attempts
// is bookkeeping, not identity.
func sessionKey(s store.AbandonedSession) string { return s.Kind + "\x00" + s.ID }

// reapAbandoned deletes every abandoned session on one binding, outside the
// state lock, then updates the record under the lock. It returns nothing: a
// delete is recoverable and must never fail a tick, a stop, a done or an
// unbind, so failures are logged and counted instead.
//
// Accepted window: if the harness's background service resumes a session
// before relevo deletes it -- a reboot where that service starts first -- the
// resumed session runs until the delete, and its in-flight tool call finishes.
func reapAbandoned(ctx context.Context, rt Runtime, name string) {
	if rt.SessionReaper == nil {
		return
	}

	// The unlocked load decides the worklist; the lock below is taken only to
	// write the result, never around a delete.
	b, err := rt.Store.Load(name)
	if err != nil || !reapable(b) {
		return
	}

	attempted := make(map[string]bool, len(b.AbandonedSessions))
	deleted := make(map[string]bool, len(b.AbandonedSessions))
	for _, s := range b.AbandonedSessions {
		attempted[sessionKey(s)] = true
		if err := rt.SessionReaper.DeleteSession(ctx, s); err != nil {
			slog.Warn("abandoned harness session not deleted", "binding", name, "kind", s.Kind, "id", s.ID, "err", err)
			continue
		}
		slog.Info("abandoned harness session deleted", "binding", name, "kind", s.Kind, "id", s.ID)
		deleted[sessionKey(s)] = true
	}

	err = rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(name)
		if err != nil {
			return err
		}
		kept := make([]store.AbandonedSession, 0, len(cur.AbandonedSessions))
		changed := false
		for _, s := range cur.AbandonedSessions {
			key := sessionKey(s)
			switch {
			case deleted[key]:
				changed = true
			case attempted[key]:
				// The entry survived this attempt: count it, and drop it at
				// the limit rather than retrying a broken delete forever.
				s.Attempts++
				changed = true
				if s.Attempts >= reapMaxAttempts {
					slog.Warn("giving up on an abandoned harness session; delete it by hand",
						"binding", name, "kind", s.Kind, "id", s.ID, "attempts", s.Attempts)
					continue
				}
				kept = append(kept, s)
			default:
				// Added between the unlocked load and the lock: leave it for
				// the next reap.
				kept = append(kept, s)
			}
		}
		if !changed {
			return nil
		}
		cur.AbandonedSessions = kept
		return tx.Save(cur)
	})
	if err != nil {
		slog.Warn("updating abandoned sessions failed", "binding", name, "err", err)
	}
}

// reapAll reaps every binding that has an abandoned session.
func reapAll(ctx context.Context, rt Runtime, bindings []store.Binding) {
	for _, b := range bindings {
		if len(b.AbandonedSessions) > 0 {
			reapAbandoned(ctx, rt, b.Name)
		}
	}
}
