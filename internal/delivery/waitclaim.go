package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// WaitTTL matches ClaimTTL: a poll refreshing every second has ten seconds of
// slack before a missed refresh reads as gone.
const WaitTTL = 10 * time.Second

// waitKeyPrefix is the kv prefix of wait registrations: one row per waited-on binding.
const waitKeyPrefix = "wait/"

// waitKey is the row a binding's registration lives under, keyed by binding --
// a pending payload belongs to one binding's log, so another binding's wait
// cannot collect it.
func waitKey(name string) string { return waitKeyPrefix + name }

// WaitClaim is one `relevo wait` process's hold on one binding.
type WaitClaim struct {
	// Name is the binding this process polls; required.
	Name string `json:"name"`
	// PID is required; > 0.
	PID int `json:"pid"`
	// StartedAt is required.
	StartedAt time.Time `json:"started_at"`
	// SeenAt is refreshed each poll; required.
	SeenAt time.Time `json:"seen_at"`
}

// ErrEmptyWaitName reports a registration naming no binding.
var ErrEmptyWaitName = errors.New("wait registration needs a binding name")

// WaitClaimStore is what the daemon and `relevo wait` share; nil means "no
// registrations exist".
type WaitClaimStore interface {
	// Live returns the registration for name if it is live; a stale row is
	// removed and (nil, nil) returned, as is a missing one.
	Live(name string, now time.Time) (*WaitClaim, error)
	// Write persists c, replacing whatever was registered for c.Name.
	Write(c WaitClaim, now time.Time) error
	// Remove deletes the registration for name if its PID equals pid.
	Remove(name string, pid int) error
}

// WaitBulk is the optional whole-namespace read: one pass over the wait rows
// instead of one read per name.
type WaitBulk interface {
	// LiveAll returns every live registration keyed by name, removing stale rows
	// exactly as Live does.
	LiveAll(now time.Time) (map[string]*WaitClaim, error)
}

// KVWaitClaims is the WaitClaimStore over the store root's kv rows.
type KVWaitClaims struct {
	// KV is the kv handle the registrations live in.
	KV db.DBTxKV

	// Alive reports whether pid names a live process. Nil uses the claim default.
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

func (w *KVWaitClaims) Live(name string, now time.Time) (*WaitClaim, error) {
	if name == "" {
		return nil, ErrEmptyWaitName
	}
	return w.liveFrom(w.KV, name, now)
}

// liveFrom is Live's body over one kv handle.
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

	// Stale reads clean up after themselves; a failed cleanup keeps the
	// caller's answer, "not live".
	_ = kv.KVDelete(waitKey(name))
	return nil, nil
}

// LiveAll implements WaitBulk, removing stale rows exactly as Live does.
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

// Write implements WaitClaimStore. Two waits may watch one binding, so this is
// a plain replace: newest wins, and the loser simply is not the status row's.
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

// Remove implements WaitClaimStore; a foreign pid's row is left for its holder.
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
		// Unparseable is not provably ours, so leave it.
		return nil
	}
	if existing.PID != pid {
		return nil
	}
	return w.KV.KVDelete(waitKey(name))
}
