package delivery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// KVClaims is the ClaimStore over the store root database's kv rows: one
// `claim/<mastermind-id>` row per mastermind.
type KVClaims struct {
	// KV is the kv handle the claims live in: the store root's database.
	KV db.DBTxKV

	// Alive reports whether pid names a live process. Nil uses the
	// default: syscall.Kill(pid, 0) == nil || the error is EPERM (a
	// process we cannot signal is still alive).
	Alive func(pid int) bool

	// Root is the pre-database channels directory (store.ChannelsDir) the
	// import reads once per store. "" imports nothing.
	Root string

	importOnce sync.Once
	importErr  error
}

var _ ClaimStore = (*KVClaims)(nil)

func (c *KVClaims) alive(pid int) bool {
	if c.Alive != nil {
		return c.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

// ensureImported adopts the pre-database claim files once per store: every
// <Root>/*.json present is put to claim/<name> and removed, then the directory
// itself goes when it is left empty. It is a no-op with no Root.
func (c *KVClaims) ensureImported() error {
	if c.Root == "" {
		return nil
	}
	c.importOnce.Do(func() { c.importErr = c.importFiles() })
	return c.importErr
}

func (c *KVClaims) importFiles() error {
	entries, err := os.ReadDir(c.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, e := range entries {
		name := e.Name()
		// Only a *.json file was ever a claim. The base name is the key's
		// tail, pane-keyed names included -- those old rows are ignored and
		// never rewritten.
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if _, _, err := db.KVImportFile(c.KV, claimKey(id), filepath.Join(c.Root, name)); err != nil {
			return err
		}
	}

	_ = os.Remove(c.Root)
	return nil
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
	if err := c.ensureImported(); err != nil {
		return nil, err
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
	if err := c.ensureImported(); err != nil {
		return err
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
	if err := c.ensureImported(); err != nil {
		return err
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
