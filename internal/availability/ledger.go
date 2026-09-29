// Package availability records why a candidate could not be used, the 30-day
// history and latency samples kept alongside that ledger, and the gate and
// window questions renderers ask. It records and answers; it never decides.
package availability

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// Kind classifies why a candidate could not be used.
// Each kind fixes what its Subject means.
type Kind string

const (
	// SpawnFailed records that relevo failed to start a candidate process or
	// that the process died during initialization. Subject is a candidate token.
	SpawnFailed Kind = "spawn_failed"

	// RateLimited records that a provider is currently rejecting requests
	// due to quota or concurrency limits. Subject is a provider name.
	RateLimited Kind = "rate_limited"

	// ExitedNoReport records that a headless builder exited without a
	// report during the current round. It is never written to the ledger
	// file: it is synthesised in memory, per round, by switchBuilder from
	// Binding.RoundExcluded, so Load never needs to validate it.
	ExitedNoReport Kind = "exited_no_report"

	// RolesMissing records that a candidate's harness kind is missing role
	// files (definitions) relevo needs to run it as a builder. Like
	// ExitedNoReport, it is never written to the ledger file: it is
	// synthesised in memory by relevo.Gates from an injectable
	// Runtime.Roles checker, so Load never needs to validate it.
	RolesMissing Kind = "roles_missing"
)

// ErrBadEntry reports a ledger entry that fails validation.
var ErrBadEntry = errors.New("bad ledger entry")

// Entry records a single availability event for a candidate or provider.
type Entry struct {
	Kind    Kind      `json:"kind"`
	Subject string    `json:"subject"`
	At      time.Time `json:"at"`
	Until   time.Time `json:"until,omitempty"`
	Note    string    `json:"note,omitempty"`
	Source  string    `json:"source"`
	Binding string    `json:"binding,omitempty"`
}

// Expired reports whether the entry has expired at the given time.
// An entry with a zero Until never expires automatically.
func (e Entry) Expired(now time.Time) bool {
	return !e.Until.IsZero() && !now.Before(e.Until)
}

// Ledger holds an ordered collection of availability entries.
//
// Entries are the ones this binary understands. Other holds entries with an
// unknown kind or source, preserved verbatim so an older relevo never erases a
// newer one's records: they are invisible to every reader, never
// pruned or cleared, and written back byte-for-byte.
type Ledger struct {
	Entries []Entry           `json:"-"`
	Other   []json.RawMessage `json:"-"`
}

// knownKind reports whether k is a kind this binary reads from the ledger
// file. ExitedNoReport and RolesMissing are synthesised in memory, never
// written, so they are not known here either.
func knownKind(k Kind) bool {
	return k == SpawnFailed || k == RateLimited
}

// knownSource reports whether s is a source this binary reads from the ledger
// file.
func knownSource(s string) bool {
	// why: "planner" is ClearedByMasterMind's value, state already written.
	return s == "relevo" || s == "planner"
}

// ledgerKey is the kv row the ledger document lives in.
const ledgerKey = "ledger"

// LoadLedger reads and validates the availability ledger from the store database's
// kv row "ledger", importing a present legacyPath file (ledger.json) on first
// read. An absent row with no file behind it returns an
// empty Ledger without error, as a fresh install records no events yet. LoadLedger
// validates entry schema but does not prune expired entries; callers prune
// against their own notion of time.
//
// An entry whose kind or source is unknown to this binary is preserved raw in
// Other rather than rejected, so a ledger written by a newer relevo survives a
// rollback. Malformed JSON is still an error.
func LoadLedger(kv db.KV, legacyPath string) (Ledger, error) {
	data, ok, err := db.KVImportFile(kv, ledgerKey, legacyPath)
	if err != nil {
		return Ledger{}, err
	}
	if !ok {
		return Ledger{}, nil
	}
	return decode(data)
}

// decode parses and validates one ledger document. It is today's file Load body
// factored out unchanged: the medium moved to the kv row, the document did not.
func decode(data []byte) (Ledger, error) {
	var doc struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return Ledger{}, fmt.Errorf("decode ledger: %w", err)
	}

	var l Ledger
	for i, raw := range doc.Entries {
		var e Entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return Ledger{}, fmt.Errorf("decode ledger: entry %d: %w", i, err)
		}

		if !knownKind(e.Kind) || !knownSource(e.Source) {
			l.Other = append(l.Other, raw)
			continue
		}

		var why string
		switch {
		case e.Subject == "":
			why = "subject is empty"
		case e.At.IsZero():
			why = "at is zero"
		case !e.Until.IsZero() && e.Until.Before(e.At):
			why = "until precedes at"
		}
		if why != "" {
			return Ledger{}, fmt.Errorf("ledger: entry %d: %s: %w", i, why, ErrBadEntry)
		}
		l.Entries = append(l.Entries, e)
	}

	return l, nil
}

// SaveLedger writes the whole ledger document to the kv row "ledger". The known
// entries are marshalled as before; Other's raw bytes follow verbatim
// The row holds the same JSON document the file did.
func SaveLedger(kv db.KV, l Ledger) error {
	var entries []json.RawMessage
	for _, e := range l.Entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal ledger entry: %w", err)
		}
		entries = append(entries, raw)
	}
	entries = append(entries, l.Other...)

	doc := struct {
		Entries []json.RawMessage `json:"entries"`
	}{Entries: entries}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ledger: %w", err)
	}

	return kv.KVPut(ledgerKey, data)
}

// Prune returns a new Ledger containing every non-expired entry, in order.
// It does not mutate the receiver's slice. Other is carried through untouched:
// it is never pruned.
func (l Ledger) Prune(now time.Time) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if !e.Expired(now) {
			kept = append(kept, e)
		}
	}
	return Ledger{Entries: append([]Entry(nil), kept...), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Append returns a new Ledger with e added to the end.
// It performs no deduplication: two spawn failures are two events; step 7 counts them.
// It does not mutate the receiver's slice. Other is carried through untouched.
func (l Ledger) Append(e Entry) Ledger {
	cp := append([]Entry(nil), l.Entries...)
	return Ledger{Entries: append(cp, e), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Clear returns a new Ledger without every entry whose Kind == kind and Subject == subject.
// It does not mutate the receiver's slice. Other is never cleared.
func (l Ledger) Clear(kind Kind, subject string) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if e.Kind == kind && e.Subject == subject {
			continue
		}
		kept = append(kept, e)
	}
	return Ledger{Entries: append([]Entry(nil), kept...), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Gate is one candidate's exposure to one live ledger entry: which token it
// gates, why, and since when. A candidate can carry several gates; the
// renderer shows them all.
type Gate struct {
	Token string // the gated candidate, canonical ref
	// Name is the gated candidate's short name, filled by
	// relevo.Gates for display only: every lookup and comparison stays on
	// Token. Empty when no set was available to resolve it through.
	Name    string `json:",omitempty"`
	Kind    Kind
	Since   time.Time // Entry.At
	Until   time.Time // zero = until cleared
	Note    string
	Source  string
	Binding string // Entry.Binding; "" for mastermind entries
	// Role scopes this gate to one role: non-empty means it applies
	// only to that role, "" means every role -- which covers every gate read
	// from the ledger file and every gate Gated produces. Only
	// rolesMissingGates sets it.
	Role string `json:"Role,omitempty"`
}

// Gated is the one view every renderer uses: for each live entry, which
// configured candidates it gates. A spawn failure gates its own token; a
// rate limit gates every candidate of its provider, because the quota is
// the provider's, not the model's.
func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time) []Gate {
	var gates []Gate

	for _, e := range l.Prune(now).Entries {
		switch e.Kind {
		case SpawnFailed:
			if slices.Contains(refs, e.Subject) {
				gates = append(gates, Gate{
					Token:   e.Subject,
					Kind:    e.Kind,
					Since:   e.At,
					Until:   e.Until,
					Note:    e.Note,
					Source:  e.Source,
					Binding: e.Binding,
				})
			}
		case RateLimited:
			for _, ref := range refs {
				if providerOf(ref) == e.Subject {
					gates = append(gates, Gate{
						Token:   ref,
						Kind:    e.Kind,
						Since:   e.At,
						Until:   e.Until,
						Note:    e.Note,
						Source:  e.Source,
						Binding: e.Binding,
					})
				}
			}
		}
	}

	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Token != gates[j].Token {
			return gates[i].Token < gates[j].Token
		}
		return gates[i].Since.Before(gates[j].Since)
	})

	return gates
}
