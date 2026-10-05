package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// ClaimTTL is how stale a claim's SeenAt may be before it is treated as dead.
const ClaimTTL = 10 * time.Second

// claimKeyPrefix is the kv prefix every claim's row shares: one claim is one
// `claim/<mastermind-id>` row holding exactly the JSON the file held.
const claimKeyPrefix = "claim/"

// claimKey is the row a claim's JSON lives under.
func claimKey(mastermindID string) string { return claimKeyPrefix + mastermindID }

// Claim is one relevo mcp process's hold on a mastermind's mailbox, keyed by
// mastermind id.
type Claim struct {
	MasterMind    string    `json:"planner"`         // required; the relevo mastermind id, e.g. "pl_abc…"; json key is state already written
	PID           int       `json:"pid"`             // required; > 0
	HostPID       int       `json:"host_pid"`        // the harness process `relevo mcp` runs under; 0 when unknown
	HostStartedAt int64     `json:"host_started_at"` // start time of HostPID in Unix seconds; 0 when unknown
	StartedAt     time.Time `json:"started_at"`      // required
	SeenAt        time.Time `json:"seen_at"`         // required; refreshed each poll
	CWD           string    `json:"cwd"`             // informational
	Version       string    `json:"version"`         // relevo version that wrote it; informational
}

// ErrClaimHeld reports that a different live claim already exists for a
// mastermind.
var ErrClaimHeld = errors.New("mastermind already has a live channel")

// ClaimStore is what the daemon and relevo mcp share. A nil ClaimStore on a
// Runtime means "no claims exist"; DeliverPending's guard treats it that way.
type ClaimStore interface {
	// Live returns the claim for mastermind if it is live: parses, pid alive,
	// now - SeenAt <= ClaimTTL. A row that exists but is not live is
	// removed and (nil, nil) returned. Missing row -> (nil, nil). A name
	// that is not a mastermind id is not a claim this version made: it is
	// ignored (nil, nil) and never rewritten.
	Live(mastermind string, now time.Time) (*Claim, error)
	// Write persists c. Returns ErrClaimHeld when a different live claim
	// (other PID) exists for c.MasterMind.
	Write(c Claim, now time.Time) error
	// Remove deletes the claim for mastermind if its PID equals pid. No error
	// when absent.
	Remove(mastermind string, pid int) error
}

// ClaimBulk is the optional whole-namespace read a ClaimStore may also
// implement: one pass over the claim rows instead of one read per mastermind.
// A status report resolves every row's channel route from the returned map; a
// store that does not implement it is read one mastermind at a time.
type ClaimBulk interface {
	// LiveAll returns every live claim keyed by mastermind id. A row that is
	// stale or unparseable is removed, exactly as Live removes it, and is
	// absent from the map.
	LiveAll(now time.Time) (map[string]*Claim, error)
}

// KVClaims is the ClaimStore over the store root database's kv rows: one
// `claim/<mastermind-id>` row per mastermind.
type KVClaims struct {
	// KV is the kv handle the claims live in: the store root's database.
	KV db.DBTxKV

	// Alive reports whether pid names a live process. Nil uses the
	// default: syscall.Kill(pid, 0) == nil || the error is EPERM (a
	// process we cannot signal is still alive).
	Alive func(pid int) bool
}

var _ ClaimStore = (*KVClaims)(nil)

var _ ClaimBulk = (*KVClaims)(nil)

func (c *KVClaims) alive(pid int) bool {
	if c.Alive != nil {
		return c.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

// Live implements ClaimStore.
func (c *KVClaims) Live(mastermindID string, now time.Time) (*Claim, error) {
	if mastermindID == "" {
		return nil, errors.New("empty mastermind")
	}
	if mastermind.ValidID(mastermindID) != nil {
		// Not a mastermind id: a pane-keyed claim from before this version. It did
		// not write it and must not rewrite it; the old row is ignored and
		// left alone.
		return nil, nil
	}
	return c.liveFrom(c.KV, mastermindID, now)
}

// liveFrom is Live's body over one kv handle: the *DB for a read, or the
// transaction Write's check-then-write runs in.
func (c *KVClaims) liveFrom(kv db.KVTx, mastermindID string, now time.Time) (*Claim, error) {
	raw, ok, err := kv.KVGet(claimKey(mastermindID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	var existing Claim
	live := true
	if err := json.Unmarshal(raw, &existing); err != nil {
		live = false
	} else if existing.PID <= 0 || !c.alive(existing.PID) || now.Sub(existing.SeenAt) > ClaimTTL {
		live = false
	}
	if live {
		return &existing, nil
	}

	// Stale: whoever reads it cleans it up. Removal failures are not worth
	// failing the read over -- the caller already has its answer, "not live".
	_ = kv.KVDelete(claimKey(mastermindID))
	return nil, nil
}

// LiveAll implements ClaimBulk: every live claim keyed by mastermind id, with
// each stale row removed exactly as Live removes it. A pane-keyed row from an
// older version is skipped, never rewritten, exactly as Live skips it.
func (c *KVClaims) LiveAll(now time.Time) (map[string]*Claim, error) {
	keys, err := c.KV.KVKeys(claimKeyPrefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*Claim, len(keys))
	for _, key := range keys {
		id := strings.TrimPrefix(key, claimKeyPrefix)
		if mastermind.ValidID(id) != nil {
			continue
		}
		claim, err := c.liveFrom(c.KV, id, now)
		if err != nil {
			return nil, err
		}
		if claim != nil {
			out[id] = claim
		}
	}
	return out, nil
}

// Write implements ClaimStore.
//
// Its check-then-write runs inside one DBTxKV.Tx, so the check and the write
// are atomic: two relevo mcp processes starting together cannot both read "no
// live claim" and both write, which a read followed by a separate write could.
// The second writer sees the first's row and gets
// ErrClaimHeld.
func (c *KVClaims) Write(claim Claim, now time.Time) error {
	if claim.MasterMind == "" {
		return errors.New("empty mastermind")
	}
	if mastermind.ValidID(claim.MasterMind) != nil {
		// A claim is keyed by a mastermind id; refusing anything else keeps a
		// new pane-keyed row from ever being written again.
		return errors.New("claim mastermind must be a mastermind id")
	}

	return c.KV.Tx(func(tx db.KVTx) error {
		existing, err := c.liveFrom(tx, claim.MasterMind, now)
		if err != nil {
			return err
		}
		if existing != nil && existing.PID != claim.PID {
			return ErrClaimHeld
		}

		raw, err := json.Marshal(claim)
		if err != nil {
			return err
		}
		return tx.KVPut(claimKey(claim.MasterMind), raw)
	})
}

// Remove implements ClaimStore. A name that is not a mastermind id names no
// claim this version wrote, so it is absent, not an error.
func (c *KVClaims) Remove(mastermindID string, pid int) error {
	if mastermindID == "" {
		return errors.New("empty mastermind")
	}
	if mastermind.ValidID(mastermindID) != nil {
		return nil
	}

	raw, ok, err := c.KV.KVGet(claimKey(mastermindID))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}

	var existing Claim
	if err := json.Unmarshal(raw, &existing); err != nil {
		// Unparseable: not provably "present with pid == pid", so leave it
		// for a reader's Live to clean up.
		return nil
	}
	if existing.PID != pid {
		return nil
	}

	return c.KV.KVDelete(claimKey(mastermindID))
}

// paneKeyedClaimDead is the pane-keyed claim rule, pure so it is
// tested directly: a claim document is removed only when it parses and carries
// a pid that is not alive. Anything else -- unparseable bytes, no pid, a live
// pid -- is left.
func paneKeyedClaimDead(raw []byte, alive func(pid int) bool) bool {
	var c struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return false
	}
	if c.PID <= 0 {
		return false
	}
	return !alive(c.PID)
}
