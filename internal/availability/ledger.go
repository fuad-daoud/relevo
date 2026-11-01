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

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
)

// Kind classifies why a candidate could not be used.
// Each kind fixes what its Subject means.
type Kind string

const (
	// SpawnFailed gates a candidate that failed to start or died during
	// initialization. Subject is a candidate token.
	SpawnFailed Kind = "spawn_failed"

	// RateLimited records that a provider is currently rejecting requests
	// due to quota or concurrency limits. Subject is a provider name.
	RateLimited Kind = "rate_limited"

	// ExitedNoReport is never written to the ledger file: it is synthesised in
	// memory, per round, by switchBuilder from Binding.RoundExcluded, so Load
	// never needs to validate it.
	ExitedNoReport Kind = "exited_no_report"

	// RolesMissing gates a candidate whose harness kind lacks role definitions.
	// Like ExitedNoReport, it is never written to the ledger file: it is
	// synthesised in memory by relevo.Gates from an injectable
	// Runtime.Roles checker, so Load never needs to validate it.
	RolesMissing Kind = "roles_missing"

	// SkillMissing gates a candidate whose harness cannot see a skill its actor
	// needs. Synthesised in memory by relevo, never written to the
	// ledger file; its Note is the whole reason.
	SkillMissing Kind = "skill_missing"

	// Scheduled gates a candidate its configured window excludes at the moment
	// of the read (or, with Demote, moves last). It is never written to the
	// ledger file: windowGates synthesises it from config and the clock, so
	// Load never needs to validate it. Subject is a candidate token.
	Scheduled Kind = "scheduled"

	// ConcurrencyLimit gates a candidate whose pool is at its configured
	// max_concurrency. Like Scheduled it is never written to the ledger file:
	// concurrencyGates synthesises it from the live round count. Subject is a
	// candidate token, and the provider is not gated.
	ConcurrencyLimit Kind = "concurrency_limit"

	// AllowanceExhausted gates a muse candidate whose provider account is at or
	// above the allowance pick threshold. Like ConcurrencyLimit it is never
	// written to the ledger file: allowanceGates synthesises it from the
	// machine-local allowance document. Subject is a candidate token.
	AllowanceExhausted Kind = "allowance_exhausted"
)

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

// Expired never expires on a zero Until.
func (e Entry) Expired(now time.Time) bool {
	return !e.Until.IsZero() && !now.Before(e.Until)
}

// Entries are the ones this binary understands. Other holds entries with an
// unknown kind or source, preserved verbatim so an older relevo never erases a
// newer one's records: they are invisible to every reader, never
// pruned or cleared, and written back byte-for-byte.
type Ledger struct {
	Entries []Entry           `json:"-"`
	Other   []json.RawMessage `json:"-"`
}

// knownKind reports whether k is a kind this binary reads from the ledger file.
// ExitedNoReport, RolesMissing and SkillMissing are synthesised in memory,
// never written, so they are not known here either.
func knownKind(k Kind) bool {
	return k == SpawnFailed || k == RateLimited
}

// knownSource reports whether s is a source this binary reads from the ledger
// file.
func knownSource(s string) bool {
	// why: "planner" is ClearedByMasterMind's value, state already written.
	return s == "relevo" || s == "planner"
}

const ledgerKey = "ledger"

// LoadLedger reads the ledger from kv row "ledger". An absent row returns an
// empty Ledger without error. Entry schema is validated but expired entries
// are not pruned; callers prune against their own notion of time.
//
// An entry whose kind or source is unknown to this binary is preserved raw in
// Other rather than rejected, so a ledger written by a newer relevo survives a
// rollback. Malformed JSON is still an error.
func LoadLedger(kv db.KV) (Ledger, error) {
	data, ok, err := kv.KVGet(ledgerKey)
	if err != nil {
		return Ledger{}, err
	}
	if !ok {
		return Ledger{}, nil
	}
	return decode(data)
}

// decode parses and validates one ledger document from the kv row.
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

// SaveLedger writes the ledger to kv row "ledger". Other's raw bytes follow
// verbatim.
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

// Prune keeps every non-expired entry, in order. It does not mutate the
// receiver's slice. Other is carried through untouched: it is never pruned.
func (l Ledger) Prune(now time.Time) Ledger {
	var kept []Entry
	for _, e := range l.Entries {
		if !e.Expired(now) {
			kept = append(kept, e)
		}
	}
	return Ledger{Entries: append([]Entry(nil), kept...), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Append adds e to the end. It performs no deduplication: two spawn failures
// are two events. It does not mutate the receiver's slice. Other is carried
// through untouched.
func (l Ledger) Append(e Entry) Ledger {
	cp := append([]Entry(nil), l.Entries...)
	return Ledger{Entries: append(cp, e), Other: append([]json.RawMessage(nil), l.Other...)}
}

// Clear drops every entry with Kind == kind and Subject == subject. It does
// not mutate the receiver's slice. Other is never cleared.
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
	// Demote marks a Scheduled gate from a demote window: the candidate is
	// moved to the end of the order, not skipped, so a reader that treats a
	// gate as "unusable" must ignore it.
	Demote bool `json:"Demote,omitempty"`
}

// Gated projects each live entry onto configured candidates. A spawn failure
// gates its own token; a rate limit gates every candidate of its provider,
// because the quota is the provider's, not the model's.
//
// With accounts, a group@account entry gates its group only once every account
// in the group's pool is gated: until then the pick has somewhere to go, so a
// partially gated pool is not a gated token.
func Gated(l Ledger, refs []string, providerOf func(string) string, now time.Time, accounts ...account.Set) []Gate {
	var set account.Set
	if len(accounts) > 0 {
		set = accounts[0]
	}
	live := l.Prune(now).Entries

	var gates []Gate

	for _, e := range live {
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
				if !rateLimitedGates(e.Subject, ref, providerOf, set, live) {
					continue
				}
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

	sort.Slice(gates, func(i, j int) bool {
		if gates[i].Token != gates[j].Token {
			return gates[i].Token < gates[j].Token
		}
		return gates[i].Since.Before(gates[j].Since)
	})

	return gates
}

// rateLimitedGates reports whether a live rate-limit entry with this subject
// gates ref. A bare group entry gates every candidate of the group; a
// group@account entry gates them only when every account in the group's pool is
// gated, so a partially gated pool leaves the token ungated.
func rateLimitedGates(subject, ref string, providerOf func(string) string, set account.Set, live []Entry) bool {
	group, accountName, ok := account.ParseGateKey(subject)
	if !ok || group != providerOf(ref) {
		return false
	}
	if accountName == "" {
		return true
	}
	pool := poolFor(set, ref, group)
	if len(pool) == 0 {
		return false
	}
	for _, a := range pool {
		if !accountGated(live, group, a.Name) {
			return false
		}
	}
	return true
}

// poolFor returns the accounts a candidate token draws from: the pool of the
// token's harness and group. Nil when the token does not parse or no account
// serves it -- every host with no accounts configured.
func poolFor(set account.Set, ref, group string) []account.Account {
	r, err := candidate.ParseRef(ref)
	if err != nil {
		return nil
	}
	return set.Pool(account.Kind(r.Harness), group)
}

// accountGated reports whether a live rate-limit entry gates one account of
// group: a bare group entry covers every account, and group@name covers name.
func accountGated(live []Entry, group, name string) bool {
	for _, e := range live {
		if e.Kind != RateLimited {
			continue
		}
		g, a, ok := account.ParseGateKey(e.Subject)
		if ok && g == group && (a == "" || a == name) {
			return true
		}
	}
	return false
}
