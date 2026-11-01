package availability

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
)

// AllowanceThreshold is the used share at which a muse provider account's
// stored allowance gates its candidates from the pre-round pick, for the
// window and the weekly dimensions alike.
const AllowanceThreshold = 85

// AllowanceEntry is one provider account's last-seen muse allowance: the used
// share and the reset bounding it, per dimension. A zero reset bounds nothing:
// the dimension reads as stale and never gates.
type AllowanceEntry struct {
	WindowUsed       float64 `json:"window_used"`
	WindowResetsAtMS int64   `json:"window_resets_at_ms"`
	WeeklyUsed       float64 `json:"weekly_used"`
	WeeklyResetsAtMS int64   `json:"weekly_resets_at_ms"`
}

// Allowance is the machine-local muse allowance document: the last
// observation per provider account, keyed by group@account or the bare group
// when the round drew no login. It is a sibling of the ledger document, not a
// ledger entry: a sub-threshold observation must never look like a gate, and
// ledger clears must never erase observations.
type Allowance struct {
	Accounts map[string]AllowanceEntry `json:"accounts"`
}

// AllowanceObservation is one round-close allowance sighting. HasWindow and
// HasWeekly say the stream carried both numbers of that dimension; a
// dimension without them leaves the stored one untouched.
type AllowanceObservation struct {
	WindowUsed       float64
	WindowResetsAtMS int64
	WeeklyUsed       float64
	WeeklyResetsAtMS int64
	HasWindow        bool
	HasWeekly        bool
}

const allowanceKey = "muse_allowance"

// LoadAllowance reads the allowance document from the store database's kv row.
// An absent row returns an empty Allowance without error, as a machine that
// never closed a muse round observed nothing yet.
func LoadAllowance(kv db.KV) (Allowance, error) {
	a := Allowance{Accounts: map[string]AllowanceEntry{}}
	data, ok, err := kv.KVGet(allowanceKey)
	if err != nil {
		return Allowance{}, err
	}
	if !ok {
		return a, nil
	}
	if err := json.Unmarshal(data, &a); err != nil {
		return Allowance{}, fmt.Errorf("decode allowance: %w", err)
	}
	if a.Accounts == nil {
		a.Accounts = map[string]AllowanceEntry{}
	}
	return a, nil
}

func SaveAllowance(kv db.KV, a Allowance) error {
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal allowance: %w", err)
	}
	return kv.KVPut(allowanceKey, data)
}

// Observe folds one round-close sighting into the document and returns the
// result; it does not mutate the receiver. Each dimension merges on its own
// reset generation: an equal resets_at_ms keeps the maximum used share, so
// concurrent rounds closing out of order never regress it, while a new
// resets_at_ms starts a new generation and replaces the old record. A
// dimension without a usable reset leaves the stored one untouched.
func (a Allowance) Observe(key string, o AllowanceObservation) Allowance {
	if _, ok := a.Accounts[key]; !ok && !o.usable() {
		return a
	}
	out := Allowance{Accounts: make(map[string]AllowanceEntry, len(a.Accounts)+1)}
	for k, e := range a.Accounts {
		out.Accounts[k] = e
	}
	e := out.Accounts[key]
	if o.HasWindow && o.WindowResetsAtMS > 0 {
		if o.WindowResetsAtMS != e.WindowResetsAtMS {
			e.WindowUsed, e.WindowResetsAtMS = o.WindowUsed, o.WindowResetsAtMS
		} else if o.WindowUsed > e.WindowUsed {
			e.WindowUsed = o.WindowUsed
		}
	}
	if o.HasWeekly && o.WeeklyResetsAtMS > 0 {
		if o.WeeklyResetsAtMS != e.WeeklyResetsAtMS {
			e.WeeklyUsed, e.WeeklyResetsAtMS = o.WeeklyUsed, o.WeeklyResetsAtMS
		} else if o.WeeklyUsed > e.WeeklyUsed {
			e.WeeklyUsed = o.WeeklyUsed
		}
	}
	out.Accounts[key] = e
	return out
}

func (o AllowanceObservation) usable() bool {
	return o.HasWindow && o.WindowResetsAtMS > 0 || o.HasWeekly && o.WeeklyResetsAtMS > 0
}

// allowanceDimGates reports whether one dimension's stored values gate a pick
// now: the used share reached the threshold while its reset is still in the
// future. Anything else -- below threshold, reset past, or no usable reset --
// reads as ungated.
func allowanceDimGates(used float64, resetsAtMS, nowMS int64) bool {
	return resetsAtMS > 0 && nowMS < resetsAtMS && used >= AllowanceThreshold
}

// allowanceEntryGates reports whether either dimension of one stored record
// gates a pick now.
func allowanceEntryGates(e AllowanceEntry, nowMS int64) bool {
	return allowanceDimGates(e.WindowUsed, e.WindowResetsAtMS, nowMS) ||
		allowanceDimGates(e.WeeklyUsed, e.WeeklyResetsAtMS, nowMS)
}

// allowanceLoginGates reports whether one login's stored allowance gates it:
// its own group@account record, or the bare group record, which covers every
// login of the group.
func allowanceLoginGates(doc Allowance, provider, name string, nowMS int64) bool {
	if e, ok := doc.Accounts[account.GateKey(provider, name)]; ok && allowanceEntryGates(e, nowMS) {
		return true
	}
	if e, ok := doc.Accounts[provider]; ok && allowanceEntryGates(e, nowMS) {
		return true
	}
	return false
}

// allowanceNote names the exhausted dimensions both a reader and the pick
// refusal show: each triggering dimension's highest used share and its reset.
func allowanceNote(entries []AllowanceEntry, nowMS int64) string {
	var windowUsed, weeklyUsed float64
	var windowReset, weeklyReset int64
	for _, e := range entries {
		if allowanceDimGates(e.WindowUsed, e.WindowResetsAtMS, nowMS) && e.WindowUsed > windowUsed {
			windowUsed, windowReset = e.WindowUsed, e.WindowResetsAtMS
		}
		if allowanceDimGates(e.WeeklyUsed, e.WeeklyResetsAtMS, nowMS) && e.WeeklyUsed > weeklyUsed {
			weeklyUsed, weeklyReset = e.WeeklyUsed, e.WeeklyResetsAtMS
		}
	}
	var parts []string
	if windowReset > 0 {
		parts = append(parts, "window "+allowancePct(windowUsed)+"% used, resets "+GateTimeText(time.UnixMilli(windowReset)))
	}
	if weeklyReset > 0 {
		parts = append(parts, "weekly "+allowancePct(weeklyUsed)+"% used, resets "+GateTimeText(time.UnixMilli(weeklyReset)))
	}
	return "muse allowance exhausted: " + strings.Join(parts, "; ")
}

func allowancePct(used float64) string {
	return strconv.FormatFloat(used, 'f', -1, 64)
}

// allowanceGates gates a muse candidate only on exhausted allowance. A
// candidate with no pool reads its bare group record; one with a pool is gated
// only once every login is, so a gate on one login leaves the candidate
// pickable on the other. Nothing is recorded, and a non-muse candidate never
// gates.
func allowanceGates(d Deps) []Gate {
	if d.Candidates == nil || d.Gates == nil {
		return nil
	}
	doc, err := LoadAllowance(d.Gates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read allowance: %v\n", err)
		return nil
	}
	if len(doc.Accounts) == 0 {
		return nil
	}
	var now time.Time
	if d.Now != nil {
		now = d.Now()
	}
	nowMS := now.UnixMilli()
	var out []Gate
	for _, ref := range d.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil || r.Harness != "muse" {
			continue
		}
		entries, gated := allowanceRefGates(doc, d.Accounts, r, nowMS)
		if !gated {
			continue
		}
		out = append(out, Gate{
			Token:  ref,
			Kind:   AllowanceExhausted,
			Since:  now,
			Note:   allowanceNote(entries, nowMS),
			Source: "relevo",
		})
	}
	return out
}

func allowanceRefGates(doc Allowance, accounts account.Set, r candidate.Ref, nowMS int64) ([]AllowanceEntry, bool) {
	pool := accounts.Pool(account.Kind(r.Harness), r.Provider)
	if len(pool) == 0 {
		e, ok := doc.Accounts[r.Provider]
		if !ok || !allowanceEntryGates(e, nowMS) {
			return nil, false
		}
		return []AllowanceEntry{e}, true
	}
	for _, a := range pool {
		if !allowanceLoginGates(doc, r.Provider, a.Name, nowMS) {
			return nil, false
		}
	}
	var entries []AllowanceEntry
	for _, a := range pool {
		if e, ok := doc.Accounts[account.GateKey(r.Provider, a.Name)]; ok {
			entries = append(entries, e)
		}
	}
	if e, ok := doc.Accounts[r.Provider]; ok {
		entries = append(entries, e)
	}
	return entries, true
}
