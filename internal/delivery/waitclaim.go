package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// WaitTTL is how stale a wait registration's SeenAt may be before it is
// treated as dead. It matches ClaimTTL: both answer "is the other process
// still polling?", and a poll that refreshes every second has ten seconds of
// slack before a single missed refresh reads as gone.
const WaitTTL = 10 * time.Second

// waitKeyPrefix is the kv prefix every wait registration's row shares: one
// `wait/<binding-name>` row per binding somebody is waiting on.
const waitKeyPrefix = "wait/"

// waitKey is the row a binding's registration lives under. It is keyed by
// binding name rather than by mastermind id because the question it answers is
// per binding: a pending payload belongs to one binding's log, so a wait on a
// different binding of the same mastermind cannot collect it.
func waitKey(name string) string { return waitKeyPrefix + name }

// WaitClaim is one `relevo wait` process's hold on one binding, keyed by
// binding name.
type WaitClaim struct {
	// Name is required: the binding this process is polling.
	Name string `json:"name"`
	// PID is required; > 0.
	PID int `json:"pid"`
	// StartedAt is required.
	StartedAt time.Time `json:"started_at"`
	// SeenAt is required; refreshed each poll.
	SeenAt time.Time `json:"seen_at"`
}

// ErrEmptyWaitName reports a registration that names no binding.
var ErrEmptyWaitName = errors.New("wait registration needs a binding name")

// WaitClaimStore is what the daemon and `relevo wait` share, so a status read
// can tell a pull payload nobody is collecting from one a live wait is about to
// collect. A nil WaitClaimStore on a Runtime means "no registrations exist"; the
// status path then reads every wait as not live.
type WaitClaimStore interface {
	// Live returns the registration for name if it is live: parses, pid alive,
	// now - SeenAt <= WaitTTL. A row that exists but is not live is removed
	// and (nil, nil) returned. Missing row -> (nil, nil).
	Live(name string, now time.Time) (*WaitClaim, error)
	// Write persists c, replacing whatever was registered for c.Name.
	Write(c WaitClaim, now time.Time) error
	// Remove deletes the registration for name if its PID equals pid. No error
	// when absent, or when a different pid holds it.
	Remove(name string, pid int) error
}

// WaitBulk is the optional whole-namespace read a WaitClaimStore may also
// implement: one pass over the wait rows instead of one read per binding name.
// A status report resolves every row's wait liveness from the returned map; a
// store that does not implement it is read one name at a time.
type WaitBulk interface {
	// LiveAll returns every live registration keyed by binding name. A stale
	// or unparseable row is removed, exactly as Live removes it, and is absent
	// from the map.
	LiveAll(now time.Time) (map[string]*WaitClaim, error)
}

// KVWaitClaims is the WaitClaimStore over the store root database's kv rows:
// one `wait/<binding-name>` row per binding somebody is waiting on.
type KVWaitClaims struct {
	// KV is the kv handle the registrations live in: the store root's database.
	KV db.DBTxKV

	// Alive reports whether pid names a live process. Nil uses the same
	// default as the claim store's.
	Alive func(pid int) bool
}

var _ WaitClaimStore = (*KVWaitClaims)(nil)

var _ WaitBulk = (*KVWaitClaims)(nil)

func (w *KVWaitClaims) alive(pid int) bool {
	if w.Alive != nil {
		return w.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

// Live implements WaitClaimStore.
func (w *KVWaitClaims) Live(name string, now time.Time) (*WaitClaim, error) {
	if name == "" {
		return nil, ErrEmptyWaitName
	}
	return w.liveFrom(w.KV, name, now)
}

// liveFrom is Live's body over one kv handle: the *DB for a read, or the
// transaction a write-side caller runs in.
func (w *KVWaitClaims) liveFrom(kv db.KVTx, name string, now time.Time) (*WaitClaim, error) {
	raw, ok, err := kv.KVGet(waitKey(name))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	var existing WaitClaim
	live := true
	if err := json.Unmarshal(raw, &existing); err != nil {
		live = false
	} else if existing.PID <= 0 || !w.alive(existing.PID) || now.Sub(existing.SeenAt) > WaitTTL {
		live = false
	}
	if live {
		return &existing, nil
	}

	// Stale: whoever reads it cleans it up, exactly as a claim's stale row is.
	// Removal failure is not worth failing the read over -- the caller already
	// has its answer, "not live".
	_ = kv.KVDelete(waitKey(name))
	return nil, nil
}

// LiveAll implements WaitBulk: every live registration keyed by binding name,
// with each stale row removed exactly as Live removes it.
func (w *KVWaitClaims) LiveAll(now time.Time) (map[string]*WaitClaim, error) {
	keys, err := w.KV.KVKeys(waitKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*WaitClaim, len(keys))
	for _, key := range keys {
		name := strings.TrimPrefix(key, waitKeyPrefix)
		if name == "" {
			continue
		}
		claim, err := w.liveFrom(w.KV, name, now)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			out[name] = claim
		}
	}
	return out, nil
}

// Write implements WaitClaimStore.
//
// Two waits may legitimately watch the same binding -- one from the
// mastermind's own poll loop, one from a human at the terminal -- so this is a
// plain replace with no ErrClaimHeld equivalent: the newest registration wins,
// and the loser keeps polling and simply is not the one the status row names.
func (w *KVWaitClaims) Write(c WaitClaim, now time.Time) error {
	if c.Name == "" {
		return ErrEmptyWaitName
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return w.KV.KVPut(waitKey(c.Name), raw)
}

// Remove implements WaitClaimStore. A row held by a different pid is left for
// its holder, or for a reader's Live to clean up.
func (w *KVWaitClaims) Remove(name string, pid int) error {
	if name == "" {
		return ErrEmptyWaitName
	}
	raw, ok, err := w.KV.KVGet(waitKey(name))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	var existing WaitClaim
	if err := json.Unmarshal(raw, &existing); err != nil {
		// Unparseable: not provably "present with pid == pid", so leave it.
		return nil
	}
	if existing.PID != pid {
		return nil
	}
	return w.KV.KVDelete(waitKey(name))
}
