package relevo

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNoCandidates reports that no candidates are configured.
var ErrNoCandidates = errors.New("no candidates configured")

// ErrRoleNotServed reports that no configured candidate serves the requested role.
var ErrRoleNotServed = errors.New("no candidate serves actor")

// ErrAmbiguousCandidate reports that multiple candidates serve the requested role and none was specified.
var ErrAmbiguousCandidate = errors.New("more than one candidate serves actor")

// ErrUnknownRole reports a role that is not in the known role table.
var ErrUnknownRole = errors.New("unknown actor")

// ErrAllGated reports that every candidate serving the requested role is
// currently gated by the ledger, and no token was named to bypass the check.
var ErrAllGated = errors.New("every candidate serving the actor is gated")

// How names the rule that picked a Resolution's candidate.
type How string

const (
	// HowExplicit means the token was named by the mastermind (or inherited by
	// fork); gates were not consulted for the decision.
	HowExplicit How = "explicit"
	// HowSole means the candidate was the only one serving the role, and it
	// was ungated.
	HowSole How = "sole"
	// HowOrder means the candidate was taken from order[role]; Position is
	// its 1-based index in that list.
	HowOrder How = "order"
	// HowUnlisted means the candidate serves the role but is not in
	// order[role], and was reached after every listed candidate was gated.
	HowUnlisted How = "unlisted"
)

// Skip is one reason a resolution passed over a candidate: a gate, or an off
// entry the pick must not take (A2 §4.3).
type Skip struct {
	Token string
	Kind  availability.Kind
	Until time.Time // zero = until cleared
	// Off marks an entry skipped because it is off, not because a gate holds
	// it. It renders as "<name> (off)".
	Off bool
}

// Resolution is what resolveCandidate picked and why. T3 records it in the
// binding's log; T4 renders it in `relevo config`.
type Resolution struct {
	Candidate candidate.Candidate
	How       How
	// Position is the candidate's 1-based index in order[role] when
	// How == HowOrder, or 0 otherwise.
	Position int
	// Skipped is every gated candidate passed over, in the order they were
	// passed; nil for HowExplicit.
	Skipped []Skip
	// Gates is, for HowExplicit only, the live gates on the named candidate,
	// so a bypass is recorded even though it did not affect the decision.
	Gates []Skip
	// OffNote is, for HowExplicit only, the advisory line for a candidate
	// that is off for the role and was still named: it was run anyway.
	// "" otherwise.
	OffNote string
	// InheritedFrom is, for a fork that inherited its source's
	// BuilderCandidate, the source binding's name; "" otherwise.
	InheritedFrom string

	// RestoredWorktree is the path re-added on this resume; "" when nothing was restored.
	RestoredWorktree string
	// RestoredBranch is the branch it was checked out from; set with RestoredWorktree.
	RestoredBranch string
	// WasPaused is true when this resume took a PAUSED binding back to
	// ACTIVE (#137): the worktree was released by `relevo pause`, so the
	// restore path ran and --rebind is implied.
	WasPaused bool
}

// Token is the canonical ref of the resolved candidate, or "" when there
// was no resolution (an adopted pane has no candidate).
func (r Resolution) Token() string {
	if r.How == "" {
		return ""
	}
	return r.Candidate.Ref().String()
}

// rankedEntry is one row of the walk; T4's FormatPolicy renders the same rows.
type rankedEntry struct {
	Candidate candidate.Candidate
	How       How // HowOrder or HowUnlisted
	Position  int // 1-based index in order[role] for HowOrder; 0 for HowUnlisted
	Off       bool
}

// rankedRole is one role's candidates in the registry's ranked order, as the
// walk wants them: the candidate behind each token is looked up in set, and an
// entry whose token does not parse or is not configured is skipped here. An
// unknown role has no ranked list, so the result is nil (#374 §4.3).
func rankedRole(reg *roles.Registry, set *candidate.Set, role string) []rankedEntry {
	info, ok := reg.Role(role)
	if !ok {
		return nil
	}
	var out []rankedEntry
	for _, r := range info.Ranked {
		ref, err := candidate.ParseRef(r.Token)
		if err != nil {
			continue
		}
		c, err := set.Lookup(ref)
		if err != nil {
			continue
		}
		if r.Position > 0 {
			out = append(out, rankedEntry{Candidate: c, How: HowOrder, Position: r.Position, Off: r.Off})
			continue
		}
		out = append(out, rankedEntry{Candidate: c, How: HowUnlisted, Off: r.Off})
	}
	return out
}

// rankedList is the order's configured, serving entries first, then every
// other serving candidate in ref order; an order entry relevo cannot use is
// skipped here and reported by PolicyWarnings (T4), never an error, because
// removing a candidate must not break every subcommand (spec §3.1).
//
// It is the legacy registry's ranked list: rankedRole over the derivation of
// set and pol, which is what every pre-roles.json caller meant. It stays for
// policy_view.go (#374 §4.3).
func rankedList(set *candidate.Set, pol policy.Policy, role string) []rankedEntry {
	return rankedRole(legacyRegistry(set, pol), set, role)
}

// skipsFor is one Skip per gate whose Token == token, in gates order.
func skipsFor(gates []availability.Gate, token string) []Skip {
	var out []Skip
	for _, g := range gates {
		if g.Token != token {
			continue
		}
		out = append(out, Skip{Token: g.Token, Kind: g.Kind, Until: g.Until})
	}
	return out
}

// gatesForRole keeps every gate that applies to role: an unscoped gate
// (Role == "") and the ones scoped to role itself. A roles-missing gate for
// another role must neither refuse nor skip this role's picks (#374 §5). It is
// pure and returns a new slice.
func gatesForRole(gates []availability.Gate, role string) []availability.Gate {
	out := make([]availability.Gate, 0, len(gates))
	for _, g := range gates {
		if g.Role == "" || g.Role == role {
			out = append(out, g)
		}
	}
	return out
}

// skipText renders one Skip as "<token> (<kind> <until>)". An off skip renders
// as "<token> (off)". name maps every token before it is printed: identity for
// the stored pick note, Set.NameOf for the line a human reads (A1 §4.4).
func skipText(s Skip, name func(string) string) string {
	if s.Off {
		return name(s.Token) + " (off)"
	}
	return fmt.Sprintf("%s (%s %s)", name(s.Token), availability.GateKindText(s.Kind), availability.GateUntilText(s.Until))
}

// uniqStrings drops later duplicates, keeping first occurrences in order.
// Gate texts are de-duplicated at render time only (#93): the ledger keeps
// every `relevo gate <token>` entry and resolveCandidate yields one Skip per
// gate, but a row or a pick line says each distinct text once.
func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]bool, len(in))
	out := in[:0:0]
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// resolveCandidate is the one rule for an omitted candidate token, shared by
// bind, add, fork and ask: a named token is looked up and must serve the
// role, and gates never refuse it -- they are only recorded. With no token,
// it is the first ungated candidate in order[role], then the unlisted ones;
// it refuses only when everything serving the role is gated, or when
// nothing is ordered and several serve (the seam #61 step 3 fills). It is
// pure and deterministic: it takes gates as a value and never opens the
// ledger itself.
//
// It is the legacy registry's resolver: resolveRole over the derivation of
// set and pol. It stays for tests and policy_view.go (#374 §4.3).
func resolveCandidate(set *candidate.Set, pol policy.Policy, gates []availability.Gate, token, role string) (Resolution, error) {
	return resolveRole(legacyRegistry(set, pol), set, gates, token, role)
}

// resolveRole is resolveCandidate's rule with the role's ranked candidates,
// order and tier read from the registry -- roles.json when one is loaded, the
// legacy derivation otherwise (#374 §4.3). Every error text is the legacy one
// except where a candidate is refused for not being in the file's list, which
// the registry can only know in file mode.
func resolveRole(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, token, role string) (Resolution, error) {
	gates = gatesForRole(gates, role)
	if token != "" {
		// The argument may be a candidate name or a canonical token (A1
		// §4.2): Resolve accepts both. From here on tok is the canonical
		// token, and every refusal names the candidate by its short name.
		c, err := set.Resolve(token)
		if err != nil {
			return Resolution{}, err
		}
		tok := c.Ref().String()
		if !reg.Serves(role, c.Ref()) {
			if reg.FileMode() {
				return Resolution{}, fmt.Errorf("candidate %q does not serve actor %q (not in %s, or no definition for %s): %w", c.Name, role, reg.ListText(role), c.Harness, ErrRoleNotServed)
			}
			return Resolution{}, fmt.Errorf("candidate %q does not serve actor %q (its roles: %v): %w", c.Name, role, c.Roles, ErrRoleNotServed)
		}
		// Unlike every other gate, roles_missing is refused even for an
		// explicit pick: a candidate whose harness role files are not
		// installed cannot succeed, so recording-and-proceeding (what
		// every other explicit-pick gate does) would only spawn it to die
		// within seconds (#238).
		for _, g := range gates {
			if g.Token == tok && g.Kind == availability.RolesMissing {
				return Resolution{}, fmt.Errorf("%s: %s", c.Name, g.Note)
			}
		}
		// An off entry is served when named explicitly (A2 §4.3): the pick
		// proceeds, and the resolution records the advisory note.
		res := Resolution{Candidate: c, How: HowExplicit, Gates: skipsFor(gates, tok)}
		if roleOff(reg, role, tok) {
			res.OffNote = fmt.Sprintf("note: %s is off for %s; running it because you named it", c.Name, role)
		}
		return res, nil
	}

	if set.Len() == 0 {
		return Resolution{}, fmt.Errorf("%w; set one with relevo config set candidates (see README \"Candidates\")", ErrNoCandidates)
	}

	info, ok := reg.Role(role)
	if !ok {
		if reg.FileMode() {
			return Resolution{}, fmt.Errorf("no candidate in %s %s.candidates can run it (configured candidates: %v): %w", roleSectionText(reg), role, set.Refs(), ErrRoleNotServed)
		}
		return Resolution{}, fmt.Errorf("no configured candidate serves actor %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}

	ranked := rankedRole(reg, set, role)
	serving := make([]candidate.Candidate, 0, len(ranked))
	for _, r := range ranked {
		serving = append(serving, r.Candidate)
	}
	if len(serving) == 0 {
		if reg.FileMode() {
			return Resolution{}, fmt.Errorf("no candidate in %s %s.candidates can run it (configured candidates: %v): %w", roleSectionText(reg), role, set.Refs(), ErrRoleNotServed)
		}
		return Resolution{}, fmt.Errorf("no configured candidate serves actor %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}
	if len(serving) == 1 {
		var skipped []Skip
		if ranked[0].Off {
			skipped = append(skipped, offSkip(serving[0].Ref().String()))
		}
		skipped = append(skipped, skipsFor(gates, serving[0].Ref().String())...)
		if len(skipped) > 0 {
			return allGated(role, skipped)
		}
		return Resolution{Candidate: serving[0], How: HowSole}, nil
	}

	if !info.Ordered {
		refs := make([]string, 0, len(serving))
		for _, c := range serving {
			refs = append(refs, c.Ref().String())
		}
		return Resolution{}, fmt.Errorf("%d candidates serve %q: %v; name one with --candidate, or set order.%s in config policy: %w", len(serving), role, refs, role, ErrAmbiguousCandidate)
	}

	var skipped []Skip
	for _, r := range ranked {
		if r.Off {
			// An off entry is skipped exactly as a gated one is, but with
			// the reason "off" (A2 §4.3).
			skipped = append(skipped, offSkip(r.Candidate.Ref().String()))
			continue
		}
		s := skipsFor(gates, r.Candidate.Ref().String())
		if len(s) == 0 {
			return Resolution{Candidate: r.Candidate, How: r.How, Position: r.Position, Skipped: skipped}, nil
		}
		skipped = append(skipped, s...)
	}
	return allGated(role, skipped)
}

// offSkip is the skip recorded for an off entry: it renders as "<token> (off)".
func offSkip(token string) Skip { return Skip{Token: token, Off: true} }

// roleSectionText names the config section a file-mode role's candidates live
// in, for error and view texts. A4-1a renamed the section to "config actors",
// so both sources print that: a `roles` file is only ever the input of the A2
// migration now. Legacy mode never reaches it; its texts name the order
// directly.
func roleSectionText(reg *roles.Registry) string {
	return "config actors"
}

// roleOff reports whether token is an off entry of role. It is false for a
// role with no ranked entry for token, and in legacy mode, whose Ranked
// entries are never off.
func roleOff(reg *roles.Registry, role, token string) bool {
	info, ok := reg.Role(role)
	if !ok {
		return false
	}
	for _, r := range info.Ranked {
		if r.Token == token {
			return r.Off
		}
	}
	return false
}

// allGated builds the ErrAllGated resolution: every candidate serving role
// was gated, in skipped.
func allGated(role string, skipped []Skip) (Resolution, error) {
	texts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		texts = append(texts, skipText(s, identityName))
	}
	return Resolution{}, fmt.Errorf("every candidate serving %q is gated: %s; name one with --candidate to bypass, or clear a gate with relevo gate --clear <provider>: %w", role, strings.Join(uniqStrings(texts), ", "), ErrAllGated)
}

// identityName leaves every token as it is. It is what ExplainResolution
// passes, so the stored KindPick note is byte-identical to its pre-A1 text
// even though the rendering now takes a name mapping (A1 §4.4).
func identityName(token string) string {
	return token
}

// explainResolution is the one line that says what was picked and why, with
// every candidate token passed through name before it is printed.
func explainResolution(role string, res Resolution, name func(string) string) string {
	head := "picked " + name(res.Token()) + " for " + role + ": "

	var body string
	switch res.How {
	case HowSole:
		body = "sole candidate"
	case HowOrder:
		body = fmt.Sprintf("order #%d", res.Position)
	case HowUnlisted:
		body = "unlisted, after order"
	case HowExplicit:
		if res.InheritedFrom != "" {
			body = "explicit, inherited from " + res.InheritedFrom + ", policy bypassed"
		} else {
			body = "explicit, policy bypassed"
		}
	}

	out := head + body

	if len(res.Skipped) > 0 {
		texts := make([]string, 0, len(res.Skipped))
		for _, s := range res.Skipped {
			texts = append(texts, skipText(s, name))
		}
		out += "; skipped " + strings.Join(uniqStrings(texts), ", ")
	}

	if res.How == HowExplicit && len(res.Gates) > 0 {
		texts := make([]string, 0, len(res.Gates))
		for _, g := range res.Gates {
			texts = append(texts, availability.GateKindText(g.Kind)+" "+availability.GateUntilText(g.Until))
		}
		out += "; gated: " + strings.Join(uniqStrings(texts), ", ")
	}

	// An explicitly named candidate that is off runs anyway; the note says so
	// (A2 §4.3), printed with the gated candidate's own note.
	if res.OffNote != "" {
		out += "; " + res.OffNote
	}

	return out
}

// ExplainResolution is the one line that says what was picked and why.
// The pick log entry, the stderr line after a spawn, and `relevo config`
// all render from it, so a pick the mastermind reads in `relevo log` is
// word-for-word what bind printed (spec §1 principle 1). It names every
// candidate by its token: stored notes and their parsers are unchanged by A1.
func ExplainResolution(role string, res Resolution) string {
	return explainResolution(role, res, identityName)
}

// PickText is ExplainResolution for a line a human reads: every candidate is
// named by its short name, or by its token when the set no longer holds it
// (A1 §4.4). The stored KindPick note keeps ExplainResolution, so
// `relevo log` and the outcome and stats parsers read today's words.
func PickText(role string, res Resolution, set *candidate.Set) string {
	return explainResolution(role, res, set.NameOf)
}

// pickEntry is the log record of one resolution. Confirmed and bound for
// the mastermind so it is never mistaken for an undelivered payload; the
// note is ExplainResolution, so `relevo log` reads exactly what bind
// printed (spec §3.2, §4.4).
func pickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry {
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToMasterMind,
		Kind: store.KindPick, Confirmed: true, Note: ExplainResolution(role, res),
	}
}

// CandidateKind is CandidateKindFor with the built-in builder's role, so its
// existing callers and tests stay unchanged.
func CandidateKind(rt Runtime, token string) string {
	return CandidateKindFor(rt, token, "builder")
}

// CandidateKindFor returns the harness kind a bind with this token would start
// for a binding whose writer role is role, for advisory preflight only; every
// error is reported as "" because the real resolution happens inside Bind and
// says why.
func CandidateKindFor(rt Runtime, token, role string) string {
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), token, role)
	if err != nil {
		return ""
	}
	return res.Candidate.Harness
}
