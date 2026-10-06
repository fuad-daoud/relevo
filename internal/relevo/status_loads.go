package relevo

import (
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// rowLoads is one report's bulk read of the facts statusRow would otherwise
// fetch once per row: the mastermind records, the live channel claims and the
// wait registrations. A nil *rowLoads is the single-row path, which reads each
// fact directly.
type rowLoads struct {
	// masterminds is always non-nil on the report path: an absent id reads as
	// no name.
	masterminds map[string]mastermind.Record
	// claims and waits are non-nil only when the store offered a bulk surface,
	// and are authoritative when they are: an absent key is not live, never
	// "unknown".
	claims map[string]*delivery.Claim
	waits  map[string]*delivery.WaitClaim
}

func (l *rowLoads) claimMap() map[string]*delivery.Claim {
	if l == nil {
		return nil
	}
	return l.claims
}

func (l *rowLoads) waitMap() map[string]*delivery.WaitClaim {
	if l == nil {
		return nil
	}
	return l.waits
}

// masterMindName is the record's name for b's mastermind: the report's bulk map
// when there is one, a single Get on the single-row path. A nil map, an absent
// id and a failed Get all leave the name empty, the direction the caller's
// comment has always required.
func (l *rowLoads) masterMindName(rt Runtime, b store.Binding) string {
	if b.MasterMindID == "" {
		return ""
	}
	if l != nil {
		if rec, ok := l.masterminds[b.MasterMindID]; ok {
			return rec.Name
		}
		return ""
	}
	if rt.MasterMinds == nil {
		return ""
	}
	rec, err := rt.MasterMinds.Get(b.MasterMindID)
	if err != nil {
		return ""
	}
	return rec.Name
}

// loadRows reads the report's shared facts once. The mastermind records are one
// Get per distinct id; the claims and the waits come from one bulk read each
// when the store offers one, and stay nil when it does not, so the row builder
// reads those directly exactly as before.
func loadRows(rt Runtime, bindings []store.Binding) *rowLoads {
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	loads := &rowLoads{masterminds: loadMasterMinds(rt, bindings)}
	if claims, ok := loadClaimMap(rt, now); ok {
		loads.claims = claims
	}
	if waits, ok := loadWaitMap(rt, now); ok {
		loads.waits = waits
	}
	return loads
}

// loadMasterMinds resolves every distinct mastermind id the report names, once
// each, into a map. A Get error leaves the id absent, so only that binding's
// name goes empty, exactly as the per-row lookup behaved.
func loadMasterMinds(rt Runtime, bindings []store.Binding) map[string]mastermind.Record {
	out := make(map[string]mastermind.Record)
	if rt.MasterMinds == nil {
		return out
	}
	for _, b := range bindings {
		id := b.MasterMindID
		if id == "" {
			continue
		}
		if _, ok := out[id]; ok {
			continue
		}
		if rec, err := rt.MasterMinds.Get(id); err == nil {
			out[id] = rec
		}
	}
	return out
}

// loadClaimMap is the report's one channel-claim read. ok is false when the
// store has no bulk surface, and each row then reads its claim directly. A
// read error reads as no live claim at all -- the empty map is authoritative --
// because a claims read must never fail the report, and every failure has
// always read as not live.
func loadClaimMap(rt Runtime, now time.Time) (map[string]*delivery.Claim, bool) {
	if rt.Channels == nil {
		return nil, false
	}
	bulk, ok := rt.Channels.(delivery.ClaimBulk)
	if !ok {
		return nil, false
	}
	claims, err := bulk.LiveAll(now)
	if err != nil || claims == nil {
		return map[string]*delivery.Claim{}, true
	}
	return claims, true
}

// loadWaitMap is loadClaimMap's wait-side twin, keyed by binding name.
func loadWaitMap(rt Runtime, now time.Time) (map[string]*delivery.WaitClaim, bool) {
	if rt.Waits == nil {
		return nil, false
	}
	bulk, ok := rt.Waits.(delivery.WaitBulk)
	if !ok {
		return nil, false
	}
	waits, err := bulk.LiveAll(now)
	if err != nil || waits == nil {
		return map[string]*delivery.WaitClaim{}, true
	}
	return waits, true
}

// loadLedgerOnce reads and decodes the availability ledger once for a report.
// A load failure is reported on stderr exactly once and read as an empty
// ledger: Gates has always reported it that way, and a bookkeeping file must
// not take status down.
func loadLedgerOnce(rt Runtime) availability.Ledger {
	if rt.Gates == nil {
		return availability.Ledger{}
	}
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read ledger: %v\n", err)
		return availability.Ledger{}
	}
	return l
}
