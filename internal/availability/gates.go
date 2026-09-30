package availability

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNoGates is what a gate write reports when the runtime carries no gates
// store: a nil Gates means gates read as empty and writes are dropped with
// this error.
var ErrNoGates = errors.New("no gates store configured")

// LoadHistory reads the availability history for display, pruning it to the
// retention window at d's clock. An unreadable record is an error the caller
// reports -- the same rule Gates applies to the ledger -- and a nil Gates reads
// as an empty history.
func LoadHistory(d Deps) (History, error) {
	if d.Gates == nil {
		return History{}, nil
	}
	h, err := loadHistory(d.Gates)
	if err != nil {
		return History{}, err
	}
	return h.Prune(d.Now()), nil
}

// ProviderOf resolves a candidate token to its provider, or "" if the token
// does not parse. It is the one place Gates and AppendEntryLocked's history
// mirror agree on what a token's provider is.
func ProviderOf(tok string) string {
	r, err := candidate.ParseRef(tok)
	if err != nil {
		return ""
	}
	return r.Provider
}

// SpawnFailedCooldown is how long a spawn failure gates its candidate. A
// constant, not config: a failed start is nearly always a binary mid-upgrade,
// and ten minutes outlasts that.
const SpawnFailedCooldown = 10 * time.Minute

// mutateLedgerLocked loads, prunes, applies fn and saves the ledger for a
// caller that already holds the state lock, so every ledger write serialises on
// the state lock the caller already holds. It does not take Store.WithLock
// itself: that lock is a plain mutex and is not reentrant, so a second Lock from
// the goroutine that already holds it blocks forever rather than erroring.
func mutateLedgerLocked(d Deps, fn func(Ledger) Ledger) error {
	if d.Gates == nil {
		return ErrNoGates
	}
	l, err := LoadLedger(d.Gates)
	if err != nil {
		return err
	}
	l = fn(l.Prune(d.Now()))
	return SaveLedger(d.Gates, l)
}

// AppendEntryLocked commits one observation: the ledger entry that gates, then
// its mirror in the history that remembers. The caller holds the store lock. A
// history failure is printed and dropped -- the ledger write is the one that
// matters, and it already happened.
func AppendEntryLocked(d Deps, e Entry) error {
	if err := mutateLedgerLocked(d, func(l Ledger) Ledger { return l.Append(e) }); err != nil {
		return err
	}
	h, err := loadHistory(d.Gates)
	if err == nil {
		err = SaveHistory(d.Gates, h.Prune(d.Now()).Append(FromEntry(e, ProviderOf)))
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", err)
	}
	return nil
}

// recordSpawnFailureWith notes that relevo failed to start token's process for
// binding, committing the record through AppendEntryLocked -- taking the state
// lock itself when !locked, or running directly when the caller already holds
// it. RecordSpawnFailure and RecordSpawnFailureLocked are this function with
// locked threaded through, so the two never drift on what a spawn failure looks
// like or on the never-returns-an-error rule.
//
// It never returns an error: a failed bookkeeping write must not mask the spawn
// error the caller is about to return, so a write failure is printed to stderr
// and dropped instead.
func recordSpawnFailureWith(d Deps, locked bool, token, binding string, cause error) {
	now := d.Now()
	entry := Entry{
		Kind:    SpawnFailed,
		Subject: token,
		At:      now,
		Until:   now.Add(SpawnFailedCooldown),
		Note:    cause.Error(),
		Source:  "relevo",
		Binding: binding,
	}
	commit := func() error { return AppendEntryLocked(d, entry) }

	var err error
	if locked {
		err = commit()
	} else {
		err = d.Store.WithLock(func(*store.Tx) error { return commit() })
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record spawn failure: %v\n", err)
	}
}

// RecordSpawnFailure notes that relevo failed to start token's process for
// binding. It takes the state lock itself; a caller that already holds it must
// use RecordSpawnFailureLocked instead, or this function deadlocks re-entering
// the lock.
func RecordSpawnFailure(d Deps, token, binding string, cause error) {
	recordSpawnFailureWith(d, false, token, binding, cause)
}

// RecordSpawnFailureLocked is RecordSpawnFailure for a caller that already
// holds the state lock: switchBuilder, reached through resolveBuilder's tx
// parameter when a switch's replacement spawn fails. Same record, same
// never-returns-an-error contract, just committed directly instead of re-taking
// Store.WithLock.
func RecordSpawnFailureLocked(d Deps, token, binding string, cause error) {
	recordSpawnFailureWith(d, true, token, binding, cause)
}

// Unavailable records that token's provider is rate-limited, so every candidate
// sharing that provider shows as gated -- a quota is enforced per subscription
// or key, not per model. token (a candidate name or a canonical token) must
// resolve to a configured candidate: a typo is refused rather than recorded.
func Unavailable(d Deps, token string, until time.Time, reason string) (provider string, err error) {
	c, err := d.Candidates.Resolve(token)
	if err != nil {
		return "", err
	}
	ref := c.Ref()

	now := d.Now()
	entry := Entry{
		Kind:    RateLimited,
		Subject: ref.Provider,
		At:      now,
		Until:   until,
		Note:    sanitize.Text(reason),
		Source:  "planner", // why: ClearedByMasterMind's value is state already written
	}
	if err := d.Store.WithLock(func(*store.Tx) error { return AppendEntryLocked(d, entry) }); err != nil {
		return "", err
	}

	return ref.Provider, nil
}

// Available clears every rate-limit gate on subject's provider. subject may be
// a candidate token or a bare provider name; ResolveClearSubject decides what it
// names and refuses one relevo knows nothing about. A clear that removed
// anything is recorded in the availability history as a Cleared event whose
// Source is source. Zero removed is not an error and records nothing -- the
// caller reports that nothing was gating.
//
// source must be ClearedByMasterMind or ClearedByServer; anything else is a
// programming error and returns an error before any write.
func Available(d Deps, subject, source string) (provider string, removed int, err error) {
	if source != ClearedByMasterMind && source != ClearedByServer {
		return "", 0, fmt.Errorf("available: unknown clear source %q", source)
	}
	if d.Gates == nil {
		return "", 0, ErrNoGates
	}

	var oldest time.Time

	err = d.Store.WithLock(func(*store.Tx) error {
		l, lerr := LoadLedger(d.Gates)
		if lerr != nil {
			return lerr
		}
		l = l.Prune(d.Now())

		provider, err = ResolveClearSubject(d.Candidates, l, subject)
		if err != nil {
			return err
		}

		for _, e := range l.Entries {
			if e.Kind != RateLimited || e.Subject != provider {
				continue
			}
			removed++
			if oldest.IsZero() || e.At.Before(oldest) {
				oldest = e.At
			}
		}

		if serr := SaveLedger(d.Gates, l.Clear(RateLimited, provider)); serr != nil {
			return serr
		}

		if removed > 0 {
			ev := Event{
				At:       d.Now(),
				Kind:     Cleared,
				Provider: provider,
				Source:   source,
				Note:     fmt.Sprintf("cleared %d entries", removed),
				Since:    oldest,
			}
			h, herr := loadHistory(d.Gates)
			if herr == nil {
				herr = SaveHistory(d.Gates, h.Prune(d.Now()).Append(ev))
			}
			if herr != nil {
				fmt.Fprintf(os.Stderr, "relevo: could not record history: %v\n", herr)
			}
		}

		return nil
	})
	if err != nil {
		return provider, 0, err
	}

	return provider, removed, nil
}

// LedgerGates projects the live ledger onto tokens, whether or not the
// configured set holds them: a rate limit gates every token of its provider, a
// spawn failure gates its own token.
func LedgerGates(d Deps, tokens []string) []Gate {
	if d.Gates == nil {
		return nil
	}

	l, err := LoadLedger(d.Gates)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read ledger: %v\n", err)
		return nil
	}

	return Gated(l, tokens, ProviderOf, d.Now())
}

// Gates is what every reader renders from: the live ledger projected onto the
// configured candidates. A load error is reported once on stderr and read as
// an empty ledger -- status, candidates and doctor must not go down over a
// bookkeeping file.
func Gates(d Deps) []Gate {
	if d.Candidates == nil {
		return nil
	}

	gates := LedgerGates(d, d.Candidates.Refs())
	gates = append(gates, rolesMissingGates(d)...)

	// Every gate carries the candidate's short name when the set holds its
	// token, so the gates block and `relevo serve gates` can print it. A token
	// no longer configured leaves Name empty and the renderers fall back to
	// printing the token. The token stays the gate's identity.
	for i := range gates {
		if name, ok := d.Candidates.NameFor(gates[i].Token); ok {
			gates[i].Name = name
		}
	}
	return gates
}

// rolesMissingGates synthesises an in-memory RolesMissing gate for every
// (candidate, role) pair whose role's resolved definitions are not all on disk,
// per d.Roles, the same way ExitedNoReport is synthesised from
// Binding.RoundExcluded rather than read from the ledger file. Each gate carries
// the role it belongs to, so resolveRole can ignore the gates of every other
// role. nil when d.Roles is nil: no checker configured (every test that does not
// set one, and every caller before cmd/relevo wires harness.OSRoleChecker()).
//
// When a candidate's provider is served by an account pool, the definitions are
// checked in each account's own home -- the builder reads them under that
// account, not the default home -- and a missing account gates as
// group@account, so one broken home gates only that account and the rest of the
// pool stays usable.
func rolesMissingGates(d Deps) []Gate {
	if d.Roles == nil || d.Candidates == nil {
		return nil
	}

	reg := d.RoleRegistry()

	// Missing is called once per distinct (kind, definition list), not once per
	// candidate or role: several candidates commonly share a kind, and a role's
	// definition list is usually the shipped one. The same cache keyed by home
	// serves the account path.
	cache := map[string][]string{}

	var out []Gate
	for _, ref := range d.Candidates.Refs() {
		r, err := candidate.ParseRef(ref)
		if err != nil {
			continue
		}
		pool := d.Accounts.Pool(account.Kind(r.Harness), r.Provider)
		for _, role := range reg.Names() {
			spec, ok := servedRoleSpec(reg, role, r)
			if !ok {
				continue
			}
			out = append(out, roleMissingGates(d, ref, r, role, spec, pool, cache)...)
		}
	}
	return out
}

// servedRoleSpec is role's resolved spec when the role serves r, and false when
// it does not or its spec cannot be built.
func servedRoleSpec(reg *roles.Registry, role string, r candidate.Ref) (harness.RoleSpec, bool) {
	if !reg.Serves(role, r) {
		return harness.RoleSpec{}, false
	}
	spec, err := reg.Spec(role, r.Harness)
	if err != nil {
		return harness.RoleSpec{}, false
	}
	return spec, true
}

// roleMissingGates is every RolesMissing gate for one role of one candidate:
// the default-home gate when no account serves the provider, else one
// group@account gate per account home that lacks the definitions.
func roleMissingGates(d Deps, ref string, r candidate.Ref, role string, spec harness.RoleSpec, pool []account.Account, cache map[string][]string) []Gate {
	defsKey := strings.Join(spec.Definitions, ",")
	if len(pool) == 0 {
		key := r.Harness + "\x00" + defsKey
		paths := cachedMissing(cache, key, func() []string {
			return d.Roles.Missing(r.Harness, spec.Definitions)
		})
		if len(paths) == 0 {
			return nil
		}
		return []Gate{{
			Token:  ref,
			Kind:   RolesMissing,
			Role:   role,
			Since:  d.Now(),
			Note:   rolesMissingNote(role, r.Harness, spec.Definitions, paths, ""),
			Source: "relevo",
		}}
	}

	var out []Gate
	for _, a := range pool {
		home, ok := harness.AccountHome(a)
		if !ok {
			continue
		}
		key := home + "\x00" + r.Harness + "\x00" + defsKey
		paths := cachedMissing(cache, key, func() []string {
			return missingInHome(d.Roles, home, r.Harness, spec.Definitions)
		})
		if len(paths) == 0 {
			continue
		}
		out = append(out, Gate{
			Token:  account.GateKey(r.Provider, a.Name),
			Kind:   RolesMissing,
			Role:   role,
			Since:  d.Now(),
			Note:   rolesMissingNote(role, r.Harness, spec.Definitions, paths, " in account "+a.Name+" ("+home+")"),
			Source: "relevo",
		})
	}
	return out
}

// cachedMissing is a missing-definitions lookup memoised under key, so one
// (kind or home, definition list) pair is checked once per Gates call.
func cachedMissing(cache map[string][]string, key string, load func() []string) []string {
	if paths, ok := cache[key]; ok {
		return paths
	}
	paths := load()
	cache[key] = paths
	return paths
}

// homeChecker is the account-aware half of harness.RoleChecker: the same
// missing-definitions check rooted at one per-process home. A checker that does
// not implement it (a test double, or a caller that predates accounts) falls
// back to the default-home check, so a gate still appears rather than silently
// vanishing.
type homeChecker interface {
	MissingIn(home, kind string, definitions []string) []string
}

// missingInHome asks c which definitions are missing from one account home;
// c's default check answers when it is not account-aware.
func missingInHome(c harness.RoleChecker, home, kind string, definitions []string) []string {
	if hc, ok := c.(homeChecker); ok {
		return hc.MissingIn(home, kind, definitions)
	}
	return c.Missing(kind, definitions)
}

// rolesMissingNote is one roles-missing gate's note: which of role's definitions
// are missing on kind, and how to fix each class of them. where names the
// account home when the check was per account, and is empty for the default
// home. A shipped path is installed by `relevo config agents`; a custom one may
// be rendered from a source agent by that same command, or be the user's own
// native definition, so its fix names both.
func rolesMissingNote(role, kind string, defs, paths []string, where string) string {
	var shipped, custom []string
	for _, path := range paths {
		if definitionIsShipped(kind, defs, path) {
			shipped = append(shipped, path)
			continue
		}
		custom = append(custom, path)
	}

	var fixes []string
	if len(shipped) > 0 {
		fixes = append(fixes, "run relevo config agents --kind "+kind)
	}
	if len(custom) > 0 {
		fixes = append(fixes, "run relevo config agents --kind "+kind+" for a custom agent relevo renders, or install "+strings.Join(custom, ", ")+" yourself")
	}
	return "agent definitions missing for " + role + where + ": " + strings.Join(paths, ", ") + "; " + strings.Join(fixes, "; ")
}

// definitionIsShipped reports whether path is one of defs' shipped paths for
// kind: it resolves every name through DefinitionPath and asks IsShipped about
// the one that lands on path.
func definitionIsShipped(kind string, defs []string, path string) bool {
	for _, name := range defs {
		p, ok := harness.DefinitionPath(kind, name)
		if !ok || p != path {
			continue
		}
		return harness.IsShipped(kind, name)
	}
	return false
}

// GateKindText is the human wording for a gate kind in status, candidates and
// doctor, so the three never drift: "spawn failed", "rate-limited".
func GateKindText(k Kind) string {
	switch k {
	case SpawnFailed:
		return "spawn failed"
	case RateLimited:
		return "rate-limited"
	case ExitedNoReport:
		return "exited without a report"
	case RolesMissing:
		return "agents missing"
	default:
		return string(k)
	}
}

// gateClock is the "now" GateTimeText compares a gate time against. Tests
// override it and restore it with t.Cleanup.
var gateClock = time.Now

// SetGateClock replaces the clock gate times are formatted against and returns
// a func that restores the previous one. For tests that render at a fixed time,
// in this package and others; production never calls it. Not safe to use from
// parallel tests.
func SetGateClock(now func() time.Time) (restore func()) {
	prev := gateClock
	gateClock = now
	return func() { gateClock = prev }
}

// GateTimeText renders a gate time in local time: the clock time alone when it
// falls on today's local date, the date as well otherwise, so a gate hours or
// days out never reads as later today.
func GateTimeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	lt, ln := t.Local(), gateClock().Local()
	if lt.Year() == ln.Year() && lt.Month() == ln.Month() && lt.Day() == ln.Day() {
		return lt.Format("15:04")
	}
	if lt.Year() == ln.Year() {
		return lt.Format("Jan 2 15:04")
	}
	return lt.Format("2006-01-02 15:04")
}

// GateUntilText renders Until as "until <gate time>" in local time, or
// "until cleared" for a zero Until.
func GateUntilText(until time.Time) string {
	if until.IsZero() {
		return "until cleared"
	}
	return "until " + GateTimeText(until)
}

// gatedNote is the one advisory line bind, add, fork and ask print after a
// successful spawn of a candidate the ledger says is gated. Advisory only: the
// agent is already running, and refusing is the mastermind's job.
func gatedNote(d Deps, token string) string {
	var parts []string
	for _, g := range append(LedgerGates(d, []string{token}), rolesMissingGates(d)...) {
		if g.Token != token {
			continue
		}
		part := fmt.Sprintf("%s since %s %s", GateKindText(g.Kind), GateTimeText(g.Since), GateUntilText(g.Until))
		if g.Note != "" {
			part += ": " + sanitize.Text(g.Note)
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return ""
	}

	// The note names the candidate by its short name; a token no longer
	// configured reads as itself (NameOf returns it unchanged).
	return fmt.Sprintf("note: %s is gated: %s; proceeding", d.Candidates.NameOf(token), strings.Join(parts, "; "))
}

// GatedNote is gatedNote exported for cmd/relevo, which prints it to stderr
// after bind, add, fork and ask spawn successfully.
func GatedNote(d Deps, token string) string {
	return gatedNote(d, token)
}

// BindingsOnProvider names the active bindings with an open round whose builder
// runs on provider, sorted: the ones the daemon will switch once that provider
// is gated. Pure, for cmdUnavailable's note.
func BindingsOnProvider(bindings []store.Binding, provider string) []string {
	var names []string
	for _, b := range bindings {
		if b.State != store.StateActive {
			continue
		}
		if b.RoundStartedAt.IsZero() {
			continue
		}
		if b.BuilderCandidate == "" {
			continue
		}
		ref, err := candidate.ParseRef(b.BuilderCandidate)
		if err != nil || ref.Provider != provider {
			continue
		}
		names = append(names, b.Name)
	}
	sort.Strings(names)
	return names
}
