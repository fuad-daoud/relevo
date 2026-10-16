package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// noHarnessMessage is the refusal an empty probe earns -- the same text the
// nothing-on-PATH paths have always printed.
const noHarnessMessage = "no harness binaries on PATH (agy, claude, codex, opencode); install one first"

// chainOrder is the order the actors doc writes in: the two builders first,
// then the plan actors and the chain's reader actors.
var chainOrder = []string{"builder", "librarian", "planner", "reviewer", "security", "lite-planner"}

// picks are what Build writes, after validation: the ordered builder
// tokens, the one planner token, the tier, and each role's tokens, answered
// or derived.
type picks struct {
	builder  []string
	planner  string
	tier     string
	roles    Answers
	opencode []string // every picked opencode model, a candidate whether or not an actor names it
}

// Build assembles the starter configuration from a probe plus the answers
// the caller carries: candidates one per distinct model the picks name, a
// policy at the tier answered, and the builder and librarian writers plus
// the chain's planner, reviewer, security and lite-planner. It refuses
// before writing anything when no harness is on PATH, or none is logged in.
func Build(inv Inventory, answers Answers) (Files, error) {
	if len(inv.Harnesses) == 0 {
		return Files{}, errors.New(noHarnessMessage)
	}
	if !anyLoggedIn(inv) {
		return Files{}, &NotAvailableError{Message: notAvailableMessage(inv)}
	}
	p, err := requiredPicks(inv, answers)
	if err != nil {
		return Files{}, err
	}
	if p.roles, err = rolePicks(inv, p, answers); err != nil {
		return Files{}, err
	}
	p.opencode = opencodePicked(inv, answers)
	cands, err := sharedCandidates(allPicks(p))
	if err != nil {
		return Files{}, err
	}
	return filesWith(inv, p, cands)
}

// requiredPicks validates the builder tokens, the planner token and the tier
// against each one's choices, erroring on an answer outside them; a missing
// one is an error too, because a front end that asks everything hands all
// three over.
func requiredPicks(inv Inventory, answers Answers) (picks, error) {
	byID := questionSet(inv, answers)
	tokens, err := pickedList(byID["builder"], answers["builder"])
	if err != nil {
		return picks{}, err
	}
	planner, err := pickedOne(byID["planner"], answers["planner"])
	if err != nil {
		return picks{}, err
	}
	tier, err := pickedOne(byID["tier"], answers["tier"])
	if err != nil {
		return picks{}, err
	}
	return picks{builder: tokens, planner: planner, tier: tier}, nil
}

// questionSet maps the questions' ids to their shape with every choice
// value: the candidate, planner and tier questions always stand. The
// answers decide which opencode models those questions offer.
func questionSet(inv Inventory, answers Answers) map[string]Question {
	qs := Questions(inv, answers)
	byID := make(map[string]Question, len(qs))
	for _, q := range qs {
		byID[q.ID] = q
	}
	return byID
}

// pickedList validates one multi answer's tokens against a question's
// choices; no tokens means the answer itself is missing.
func pickedList(q Question, tokens []string) ([]string, error) {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		m, ok := matchChoice(q, t)
		if !ok {
			return nil, fmt.Errorf("invalid %s value %q; valid values: %s", q.ID, t, validValues(q))
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("missing %s answer", q.ID)
	}
	return out, nil
}

// pickedOne validates a singleton answer: exactly one token, one choice.
func pickedOne(q Question, tokens []string) (string, error) {
	if len(tokens) != 1 {
		return "", fmt.Errorf("missing %s answer", q.ID)
	}
	m, ok := matchChoice(q, tokens[0])
	if !ok {
		return "", fmt.Errorf("invalid %s value %q; valid values: %s", q.ID, tokens[0], validValues(q))
	}
	return m, nil
}

// cheapestPick is the one model the reviewer, security and lite-planner
// actors share: claude's sonnet alias first, then the codex model the offer
// list lists last, then agy's first flash model else its first. Empty when
// no rule can name one.
func cheapestPick(inv Inventory) string {
	models := loggedModels(inv)
	if len(models["claude"]) > 0 {
		return refFor("claude", "sonnet")
	}
	if len(models["codex"]) > 0 {
		return refFor("codex", models["codex"][len(models["codex"])-1])
	}
	if m, ok := agyFlashPick(models["agy"]); ok {
		return refFor("agy", m)
	}
	if len(models["opencode"]) > 0 {
		return refFor("opencode", models["opencode"][0])
	}
	return ""
}

// rolePicks is each role's tokens: its own answer, validated, where one was
// given, else what DerivedRoles makes of the builder and planner picks.
func rolePicks(inv Inventory, p picks, answers Answers) (Answers, error) {
	qs := Questions(inv, answers)
	out := DerivedRoles(qs, Answers{"builder": p.builder, "planner": {p.planner}})
	for _, q := range qs {
		if !IsRole(q.ID) || len(answers[q.ID]) == 0 {
			continue
		}
		var err error
		if q.Multi {
			out[q.ID], err = pickedList(q, answers[q.ID])
		} else {
			var one string
			one, err = pickedOne(q, answers[q.ID])
			out[q.ID] = []string{one}
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// allPicks joins the picks in candidate order: builder tokens first, then
// the planner's, then each role's. A token picked twice names one
// candidate, so repeats fall out in the dedup.
func allPicks(p picks) []string {
	tokens := append(append([]string{}, p.builder...), p.planner)
	for _, id := range roleIDs {
		tokens = append(tokens, p.roles[id]...)
	}
	for _, t := range p.opencode {
		if !slices.ContainsFunc(tokens, func(have string) bool { return have == t || strings.HasPrefix(have, t+"#") }) {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// opencodePicked are the opencode models the answers picked, else the
// default picks, as candidate tokens. allPicks drops one an actor already
// runs, at a variant or not.
func opencodePicked(inv Inventory, answers Answers) []string {
	var out []string
	for _, s := range withOpencodePicks(inv, answers).Harnesses {
		if s.Harness == "opencode" {
			for _, m := range s.Models {
				out = append(out, refFor("opencode", m))
			}
		}
	}
	return out
}

// sharedCandidates turns the picked tokens into the candidate list Build
// writes: one per distinct token, pick order. A `:effort` suffix after a
// token's model is that token's own model, never a separate entry.
func sharedCandidates(tokens []string) ([]candidate.Candidate, error) {
	cands := make([]candidate.Candidate, 0, len(tokens))
	seen := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		if token == "" || seen[token] {
			continue
		}
		r, err := candidate.ParseRef(token)
		if err != nil {
			return nil, err
		}
		seen[token] = true
		cands = append(cands, candidate.Candidate{Harness: r.Harness, Provider: r.Provider, Model: r.Model})
	}
	return cands, nil
}

// filesWith marshals the three sections the picks stand for: candidates, the
// policy's ceiling, and the actors, plus the harness kinds the agent install
// walks.
func filesWith(inv Inventory, p picks, cands []candidate.Candidate) (Files, error) {
	names := candidate.DeriveNames(cands)
	nameFor := make(map[string]string, len(cands))
	for i, c := range cands {
		nameFor[c.Ref().String()] = names[i]
	}

	namesOf := func(tokens []string) []string {
		out := make([]string, 0, len(tokens))
		for _, t := range tokens {
			out = append(out, nameFor[t])
		}
		return out
	}

	actorSet := map[string]roles.Actor{
		"builder":   {Agent: "plan-executor", Tier: p.tier, Candidates: candidateEntries(namesOf(p.builder))},
		"librarian": librarianActor(namesOf(p.roles["librarian"]), p.tier),
		"planner":   {Agent: "architect", Candidates: candidateEntries([]string{nameFor[p.planner]})},
		"security":  {Agent: "security-reviewer", Candidates: candidateEntries(namesOf(p.roles["security"]))},
	}
	for id, agent := range map[string]string{"reviewer": "reviewer", "lite-planner": "architect"} {
		if tokens := p.roles[id]; len(tokens) > 0 {
			actorSet[id] = roles.Actor{Agent: agent, Candidates: candidateEntries(namesOf(tokens))}
		}
	}

	actors, err := roles.EncodeActors(actorSet)
	if err != nil {
		return Files{}, fmt.Errorf("marshal actors: %w", err)
	}
	candJSON, err := indent(cands)
	if err != nil {
		return Files{}, fmt.Errorf("marshal candidates: %w", err)
	}
	polJSON, err := indent(policy.Policy{MaxTier: p.tier})
	if err != nil {
		return Files{}, fmt.Errorf("marshal policy: %w", err)
	}
	return Files{
		Kinds:          loggedInKinds(inv),
		Candidates:     candJSON,
		Policy:         polJSON,
		Actors:         actors,
		ActorOrder:     chainOrder,
		CandidateNames: names,
	}, nil
}

// librarianActor is the docs-only writer over the builders' own names. Its
// check stays nil so the writer default (on) applies.
func librarianActor(builderNames []string, tier string) roles.Actor {
	return roles.Actor{
		Agent:      "librarian",
		Tier:       tier,
		Candidates: candidateEntries(builderNames),
		Scope:      &pathscope.Scope{Paths: []string{"@docs"}, Comments: true},
	}
}

// candidateEntries wraps names as an actor's candidates, each on.
func candidateEntries(names []string) []roles.Entry {
	entries := make([]roles.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, roles.Entry{Candidate: name})
	}
	return entries
}

// loggedInKinds are the harness kinds whose CLI is logged in, inventory
// order -- the order harness.All() sorts, which the agent install walks.
func loggedInKinds(inv Inventory) []string {
	kinds := make([]string, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		if s.LoggedIn {
			kinds = append(kinds, s.Harness)
		}
	}
	return kinds
}

// anyLoggedIn reports whether at least one status entry stands logged in.
func anyLoggedIn(inv Inventory) bool {
	for _, s := range inv.Harnesses {
		if s.LoggedIn {
			return true
		}
	}
	return false
}

// notAvailableMessage lists the way in: the login command of every harness
// the probe found, since one of them is the way out.
func notAvailableMessage(inv Inventory) string {
	logins := make([]string, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		logins = append(logins, s.Login)
	}
	return "no harness is logged in; log in with one of: " + strings.Join(logins, ", ")
}

// indent is the section encoding every front end stores: two-space indented
// JSON with a trailing newline.
func indent(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
