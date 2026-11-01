package resolve

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

var ErrNoCandidates = errors.New("no candidates configured")

var ErrRoleNotServed = errors.New("no candidate serves actor")

var ErrAmbiguousCandidate = errors.New("more than one candidate serves actor")

var ErrUnknownRole = errors.New("unknown actor")

// ErrCandidateCannotRun reports an explicitly named candidate refused because
// it is missing something the actor needs to start (role files or a skill).
var ErrCandidateCannotRun = errors.New("the named candidate cannot run this actor")

// ErrAllGated is the omitted-token refusal when every serving candidate is gated.
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
	// HowRotation means a limit gated the builder's account and the pick
	// stayed on the same candidate with the next ungated login of its pool.
	HowRotation How = "rotation"
)

// Skip is one reason a resolution passed over a candidate: a gate, or an off
// entry the pick must not take.
type Skip struct {
	Token string
	Kind  availability.Kind
	Until time.Time // zero = until cleared
	Note  string
	// Off marks an entry skipped because it is off, not because a gate holds
	// it. It renders as "<name> (off)".
	Off bool
}

// Resolution is what Resolve picked and why, recorded in the
// binding's log and rendered in `relevo config`.
type Resolution struct {
	Candidate candidate.Candidate
	How       How
	// Account is the login of the candidate's pool the round draws from,
	// or "" on a host with no accounts.
	Account string
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
	// ACTIVE: the worktree was released by `relevo pause`, so the
	// restore path ran and --rebind is implied.
	WasPaused bool

	// Placement is where the create landed, and every placement it passed
	// over on the way. The zero value means the actor named no preference:
	// nothing was probed, and the PlacementClause renders nothing.
	Placement PlacementResolution

	// Gate is the acceptance check the binding resolved and the tier that
	// supplied it. The zero value means no gate: the printed line reads
	// "gate: none".
	Gate GateResolution
}

// Token is the canonical ref of the resolved candidate, or "" when there
// was no resolution (an adopted pane has no candidate).
func (r Resolution) Token() string {
	if r.How == "" {
		return ""
	}
	return r.Candidate.Ref().String()
}

// SkipsFor is one Skip per gate whose Token == token, in gates order.
func SkipsFor(gates []availability.Gate, token string) []Skip {
	var out []Skip
	for _, g := range gates {
		if g.Token != token {
			continue
		}
		out = append(out, Skip{Token: g.Token, Kind: g.Kind, Until: g.Until, Note: g.Note})
	}
	return out
}

// GatesForRole keeps every gate that applies to role, so a roles-missing
// gate for another role neither refuses nor skips this role's picks.
func GatesForRole(gates []availability.Gate, role string) []availability.Gate {
	out := make([]availability.Gate, 0, len(gates))
	for _, g := range gates {
		if g.Role == "" || g.Role == role {
			out = append(out, g)
		}
	}
	return out
}

// skipText renders one Skip, naming every token through name: identity for
// the stored pick note, Set.NameOf for the line a human reads.
func skipText(s Skip, name func(string) string) string {
	if s.Off {
		return name(s.Token) + " (off)"
	}
	return fmt.Sprintf("%s (%s)", name(s.Token), availability.GateText(s.Kind, s.Until, s.Note))
}

// UniqStrings drops later duplicates, keeping first occurrences in order.
// Gate texts de-duplicate at render time only: the ledger keeps every
// entry, but a row or pick line says each distinct text once.
func UniqStrings(in []string) []string {
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

// Resolve is the one rule for an omitted candidate token, shared by
// bind, add, fork and ask: a named token is looked up and must serve the
// role, and gates never refuse it -- they are only recorded. With no token,
// it is the first ungated candidate in order[role], then the unlisted ones;
// it refuses only when everything serving the role is gated, or when
// nothing is ordered and several serve. It is pure and deterministic: it
// takes gates as a value and never opens the ledger itself.
func Resolve(set *candidate.Set, pol policy.Policy, gates []availability.Gate, token, role string, picks ...AccountPick) (Resolution, error) {
	return ResolveRole(LegacyRegistry(set, pol), set, gates, token, role, picks...)
}

// LegacyRegistry is the registry derived from candidates.json and
// policy.json: roles.Build with no file, which never errors in legacy mode,
// so the error is discarded.
func LegacyRegistry(set *candidate.Set, pol policy.Policy) *roles.Registry {
	reg, _ := roles.Build(nil, set, pol)
	return reg
}

// AccountPick is the login pool a pick draws from: the configured set, the
// live gate keys and the configured rotation mode. The zero value is a host
// with no accounts, where a pick records no account and every note is
// byte-identical to its pre-account text.
type AccountPick struct {
	Set   account.Set
	Gates []string
	Mode  account.Mode
}

// PickFor is the AccountPick a pick at d draws from. It reads the live gate
// keys, so a pool that still holds a free login is not skipped; a ledger this
// cannot read reads as no gates, the same rule Gates follows.
func PickFor(d Deps) AccountPick {
	if len(d.Accounts) == 0 {
		return AccountPick{}
	}
	var keys []string
	if d.Gates != nil {
		if l, err := availability.LoadLedger(d.Gates); err == nil {
			now := d.Now
			if now == nil {
				now = time.Now
			}
			keys = availability.LiveGateKeys(l, now())
		}
	}
	return AccountPick{Set: d.Accounts, Gates: keys, Mode: account.Mode(d.Policy.AccountsRotation())}
}

// withAccount fills the resolution's account: the first login of the chosen
// candidate's harness and provider that no live gate covers. A host with no
// accounts, or a decision that chose no candidate, is returned unchanged.
func withAccount(res Resolution, picks ...AccountPick) Resolution {
	if len(picks) == 0 || len(picks[0].Set) == 0 || res.How == "" {
		return res
	}
	p := picks[0]
	ref, err := candidate.ParseRef(res.Token())
	if err != nil {
		return res
	}
	pool := p.Set.Pool(account.Kind(ref.Harness), ref.Provider)
	if len(pool) == 0 {
		return res
	}
	a, ok, err := account.Select(pool, p.Gates, p.Mode)
	if err != nil || !ok {
		return res
	}
	res.Account = a.Name
	return res
}

// ResolveRole is Resolve's rule with the role's ranked candidates, order and
// tier read from the registry, and then the account of the chosen
// candidate's pool filled in. Every error text is the legacy one except
// where a candidate is refused for not being in the file's list, which the
// registry can only know in file mode.
func ResolveRole(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, token, role string, picks ...AccountPick) (Resolution, error) {
	res, err := resolveRoleBase(reg, set, gates, token, role)
	if err != nil {
		return res, err
	}
	return withAccount(res, picks...), nil
}

// resolveRoleBase is ResolveRole without the account dimension.
func resolveRoleBase(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, token, role string) (Resolution, error) {
	gates = GatesForRole(gates, role)
	if token != "" {
		return explicitRole(reg, set, gates, token, role)
	}
	return orderedRole(reg, set, gates, role)
}

// explicitRole resolves a named candidate: the token is looked up and must
// serve the role, and gates never refuse it -- they are only recorded. A
// candidate whose harness role files are missing is the one exception: it
// cannot succeed, so it is refused instead of spawned to die within seconds.
func explicitRole(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, token, role string) (Resolution, error) {
	// The argument may be a candidate name or a canonical token: Resolve
	// accepts both. From here on tok is the canonical token, and every
	// refusal names the candidate by its short name.
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
	for _, g := range gates {
		if g.Token == tok && (g.Kind == availability.RolesMissing || g.Kind == availability.SkillMissing) {
			return Resolution{}, fmt.Errorf("%s: %s: %w", c.Name, g.Note, ErrCandidateCannotRun)
		}
	}
	// An off entry is served when named explicitly: the pick proceeds, and
	// the resolution records the advisory note.
	res := Resolution{Candidate: c, How: HowExplicit, Gates: SkipsFor(gates, tok)}
	if roleOff(reg, role, tok) {
		res.OffNote = fmt.Sprintf("note: %s is off for %s; running it because you named it", c.Name, role)
	}
	return res, nil
}

// orderedRole resolves an omitted token: the first ungated candidate in
// order[role], then the unlisted ones. It refuses only when everything
// serving the role is gated, or when nothing is ordered and several serve.
func orderedRole(reg *roles.Registry, set *candidate.Set, gates []availability.Gate, role string) (Resolution, error) {
	if set.Len() == 0 {
		return Resolution{}, fmt.Errorf("%w; set one with relevo config set candidates (see README \"Candidates\")", ErrNoCandidates)
	}

	info, ok := reg.Role(role)
	if !ok {
		if reg.FileMode() {
			return Resolution{}, fmt.Errorf("no candidate in %s %s.candidates can run it (configured candidates: %v): %w", RoleSectionText(reg), role, set.Refs(), ErrRoleNotServed)
		}
		return Resolution{}, fmt.Errorf("no configured candidate serves actor %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}

	ranked := RankedRoleFor(reg, set, role, gates)
	gates = SkipGates(gates)
	serving := make([]candidate.Candidate, 0, len(ranked))
	for _, r := range ranked {
		serving = append(serving, r.Candidate)
	}
	if len(serving) == 0 {
		if reg.FileMode() {
			return Resolution{}, fmt.Errorf("no candidate in %s %s.candidates can run it (configured candidates: %v): %w", RoleSectionText(reg), role, set.Refs(), ErrRoleNotServed)
		}
		return Resolution{}, fmt.Errorf("no configured candidate serves actor %q (configured: %v): %w", role, set.Refs(), ErrRoleNotServed)
	}
	if len(serving) == 1 {
		var skipped []Skip
		if ranked[0].Off {
			skipped = append(skipped, offSkip(serving[0].Ref().String()))
		}
		skipped = append(skipped, SkipsFor(gates, serving[0].Ref().String())...)
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
			// the reason "off".
			skipped = append(skipped, offSkip(r.Candidate.Ref().String()))
			continue
		}
		s := SkipsFor(gates, r.Candidate.Ref().String())
		if len(s) == 0 {
			return Resolution{Candidate: r.Candidate, How: r.How, Position: r.Position, Skipped: skipped}, nil
		}
		skipped = append(skipped, s...)
	}
	return allGated(role, skipped)
}

func offSkip(token string) Skip { return Skip{Token: token, Off: true} }

// RoleSectionText names the config section a file-mode role's candidates live
// in, for error and view texts: "config actors". Legacy mode never reaches
// it; its texts name the order directly.
func RoleSectionText(reg *roles.Registry) string {
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
	var waits, real, missing bool
	for _, s := range skipped {
		texts = append(texts, skipText(s, identityName))
		switch {
		case s.Off:
		case s.Kind == availability.Scheduled || s.Kind == availability.ConcurrencyLimit || s.Kind == availability.AllowanceExhausted:
			waits = true
		case s.Kind == availability.SkillMissing:
			missing = true
		default:
			real = true
		}
	}
	return Resolution{}, fmt.Errorf("every candidate serving %q is gated: %s; %s: %w", role, strings.Join(UniqStrings(texts), ", "), allGatedNext(waits, real, missing), ErrAllGated)
}

// allGatedNext names the way out of an all-gated pick: a missing skill is
// neither bypassed by naming the candidate nor cleared by a gate.
func allGatedNext(waits, real, missing bool) string {
	var parts []string
	if waits || real || !missing {
		parts = append(parts, "name one with --candidate to bypass")
	}
	if waits {
		parts = append(parts, "wait for a window to open or a running round to finish")
	}
	if real || (!waits && !missing) {
		parts = append(parts, "clear a gate with relevo gate --clear <provider>")
	}
	if missing {
		parts = append(parts, "install the missing skill where relevo doctor says")
	}
	return strings.Join(parts, ", or ")
}

// identityName leaves every token as it is, so the stored KindPick note
// keeps tokens while rendering takes a name mapping.
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
	case HowRotation:
		body = "same candidate, next account"
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
		out += "; skipped " + strings.Join(UniqStrings(texts), ", ")
	}

	if res.How == HowExplicit && len(res.Gates) > 0 {
		texts := make([]string, 0, len(res.Gates))
		for _, g := range res.Gates {
			texts = append(texts, availability.GateText(g.Kind, g.Until, g.Note))
		}
		out += "; gated: " + strings.Join(UniqStrings(texts), ", ")
	}

	// An explicitly named candidate that is off runs anyway; the note says so.
	if res.OffNote != "" {
		out += "; " + res.OffNote
	}

	// The account is part of the record: a limit is recorded per login, so a
	// note that named only the candidate would hide which login the round
	// actually drew from. Empty on a host with no accounts, which keeps the
	// note byte-identical to its pre-account text.
	if res.Account != "" {
		out += "; account " + res.Account
	}

	return out + PlacementClause(res.Placement)
}

// PlacementClause renders the placement and every skip as the clauses the pick
// note and the bind line append: one "; placement <name> (<how>)" and one
// "; skipped <name> (<reason>)" per entry passed over. A zero Placement renders
// nothing, so a note written for an actor that names no placement is
// byte-identical to its pre-placement text.
func PlacementClause(p PlacementResolution) string {
	if p.How == "" {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "; placement %s (%s)", p.Name, p.How)
	for _, s := range p.Skipped {
		fmt.Fprintf(&sb, "; skipped %s (%s)", s.Name, s.Reason)
	}
	return sb.String()
}

// ExplainResolution is the one line that says what was picked and why. The
// pick log entry, the stderr line after a spawn, and `relevo config` all
// render from it, so a pick the mastermind reads in `relevo log` is
// word-for-word what bind printed. It names every candidate by its token:
// stored notes keep tokens because the parsers read tokens.
func ExplainResolution(role string, res Resolution) string {
	return explainResolution(role, res, identityName)
}

// PickText is ExplainResolution for a line a human reads: every candidate is
// named by its short name, or by its token when the set does not hold it.
// The stored KindPick note keeps ExplainResolution, so
// `relevo log` and the outcome and stats parsers read the same words.
func PickText(role string, res Resolution, set *candidate.Set) string {
	return explainResolution(role, res, set.NameOf)
}

// PickEntry is the log record of one resolution. Confirmed and bound for
// the mastermind so it is never mistaken for an undelivered payload; the
// note is ExplainResolution, so `relevo log` reads exactly what bind
// printed.
func PickEntry(now time.Time, round int, role string, res Resolution) store.LogEntry {
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToMasterMind,
		Kind: store.KindPick, Confirmed: true, Note: ExplainResolution(role, res),
	}
}

// CandidateKind is CandidateKindFor with the built-in builder's role, so its
// existing callers and tests stay unchanged.
func CandidateKind(d Deps, token string) string {
	return CandidateKindFor(d, token, "builder")
}

// CandidateKindFor returns the harness kind a bind with this token would start
// for a binding whose writer role is role, for advisory preflight only; every
// error is reported as "" because the real resolution happens inside Bind and
// says why.
func CandidateKindFor(d Deps, token, role string) string {
	res, err := ResolveRole(d.RoleRegistry(), d.Candidates, availability.Gates(d.Deps), token, role)
	if err != nil {
		return ""
	}
	return res.Candidate.Harness
}
