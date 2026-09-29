package relevo

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// ConfigDoc is the editable config: the stored sections decoded, candidates in
// stored order.
type ConfigDoc struct {
	Candidates []candidate.Candidate
	Actors     map[string]roles.Actor
	Agents     map[string]roles.AgentEntry
	Policy     policy.Policy
	PolicyRaw  json.RawMessage // the stored policy body, verbatim; nil when the section is absent
}

// CandidateInput is what the add/edit candidate form submits.
type CandidateInput struct{ Harness, Provider, Model string }

// FieldError is a validation failure the form shows under one field.
// Field is "harness", "provider", "model" or "" (a whole-form error).
type FieldError struct{ Field, Msg string }

// Error returns the message the form shows.
func (e *FieldError) Error() string { return e.Msg }

// ConfigEdit is one validated change, ready for WriteConfigEdit.
type ConfigEdit struct {
	Sections map[config.Section]json.RawMessage // only the sections the edit changes
	Message  string                             // revision message, e.g. "add candidate glm-5.3-flash"
	Name     string                             // the name the edit produced or touched (the new name after a rename)
}

// ActorSlot is one place a candidate sits in an actor's list.
type ActorSlot struct {
	Actor    string
	Position int // 1-based
	Off      bool
}

// ErrNoChange reports an edit whose input equals what is already stored: there
// is nothing to save.
var ErrNoChange = errors.New("nothing changed")

// LoadConfigDoc reads the editable sections through the store. A missing
// section is empty.
func LoadConfigDoc(s *config.Store) (ConfigDoc, error) {
	d := ConfigDoc{
		Actors: map[string]roles.Actor{},
		Agents: map[string]roles.AgentEntry{},
	}

	body, ok, err := s.Body(config.Candidates)
	if err != nil {
		return ConfigDoc{}, err
	}
	if ok {
		if err := json.Unmarshal(body, &d.Candidates); err != nil {
			return ConfigDoc{}, err
		}
	}

	body, ok, err = s.Body(config.Actors)
	if err != nil {
		return ConfigDoc{}, err
	}
	if ok {
		a, _, err := roles.ParseActors(body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Actors = a
	}

	body, ok, err = s.Body(config.Agents)
	if err != nil {
		return ConfigDoc{}, err
	}
	if ok {
		a, _, err := roles.ParseAgents(body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Agents = a
	}

	body, ok, err = s.Body(config.Policy)
	if err != nil {
		return ConfigDoc{}, err
	}
	if ok {
		p, _, err := policy.Parse(config.FileName(config.Policy), body)
		if err != nil {
			return ConfigDoc{}, err
		}
		d.Policy = p
		d.PolicyRaw = append(json.RawMessage(nil), body...)
	}

	return d, nil
}

// AddCandidate validates in, appends it, derives its name and returns the
// candidates section alone.
func AddCandidate(d ConfigDoc, in CandidateInput) (ConfigEdit, error) {
	san, err := validateCandidateInput(in, d, -1)
	if err != nil {
		return ConfigEdit{}, err
	}

	next := append(append([]candidate.Candidate(nil), d.Candidates...), candidate.Candidate{
		Harness:  san.Harness,
		Provider: san.Provider,
		Model:    san.Model,
	})
	next[len(next)-1].Name = candidate.DeriveNames(next)[len(next)-1]

	body, err := encodeCandidates(next)
	if err != nil {
		return ConfigEdit{}, err
	}
	if err := dryRun(next, d.Actors, d.Agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}

	name := next[len(next)-1].Name
	return ConfigEdit{
		Sections: map[config.Section]json.RawMessage{config.Candidates: body},
		Message:  "add candidate " + name,
		Name:     name,
	}, nil
}

// EditCandidate changes the candidate named name to in. The name follows the
// model, and every actor entry that referenced the candidate follows the
// rename. Every other field of the entry is kept.
func EditCandidate(d ConfigDoc, name string, in CandidateInput) (ConfigEdit, error) {
	i := candidateIndex(d, name)
	if i < 0 {
		return ConfigEdit{}, &FieldError{"", "no candidate named " + name}
	}
	entry := d.Candidates[i]
	if in.Harness == entry.Harness && in.Provider == entry.Provider && in.Model == entry.Model {
		return ConfigEdit{}, ErrNoChange
	}
	san, err := validateCandidateInput(in, d, i)
	if err != nil {
		return ConfigEdit{}, err
	}

	oldName := entry.Name
	oldToken := entry.Ref().String()

	next := append([]candidate.Candidate(nil), d.Candidates...)
	next[i].Harness = san.Harness
	next[i].Provider = san.Provider
	next[i].Model = san.Model
	next[i].Name = ""
	next[i].Name = candidate.DeriveNames(next)[i]
	newName := next[i].Name
	newToken := next[i].Ref().String()

	acts := copyActors(d.Actors)
	actsChanged := false
	if newName != oldName || newToken != oldToken {
		for actorName, a := range acts {
			entries := a.Candidates
			changed := false
			for j, e := range entries {
				if e.Candidate != oldName && e.Candidate != oldToken {
					continue
				}
				if e.Candidate == newName {
					continue
				}
				if !changed {
					entries = append([]roles.Entry(nil), a.Candidates...)
					changed = true
				}
				entries[j].Candidate = newName
			}
			if changed {
				a.Candidates = entries
				acts[actorName] = a
				actsChanged = true
			}
		}
	}

	body, err := encodeCandidates(next)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Candidates: body}
	if actsChanged {
		actBody, err := roles.EncodeActors(acts)
		if err != nil {
			return ConfigEdit{}, err
		}
		sections[config.Actors] = actBody
	}
	if err := dryRun(next, acts, d.Agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}

	message := "edit candidate " + name
	if newName != oldName {
		message = "edit candidate " + oldName + " → " + newName
	}
	return ConfigEdit{Sections: sections, Message: message, Name: newName}, nil
}

// PreviewCandidateName is the name in would get: for editing != "" the entry
// named editing takes in's harness/provider/model; for "" in is appended.
// Names are derived as AddCandidate/EditCandidate derive them. No validation;
// an empty model gives "".
func PreviewCandidateName(d ConfigDoc, editing string, in CandidateInput) string {
	if strings.TrimSpace(in.Model) == "" {
		return ""
	}

	entries := append([]candidate.Candidate(nil), d.Candidates...)
	i := -1
	if editing == "" {
		entries = append(entries, candidate.Candidate{})
		i = len(entries) - 1
	} else {
		i = candidateIndex(d, editing)
		if i < 0 {
			return ""
		}
	}

	entries[i].Harness = in.Harness
	entries[i].Provider = in.Provider
	entries[i].Model = in.Model
	entries[i].Name = ""
	return candidate.DeriveNames(entries)[i]
}

// DeleteCandidate removes the candidate named name and every actor entry that
// references it. An actor left with no candidates refuses the edit.
func DeleteCandidate(d ConfigDoc, name string) (ConfigEdit, error) {
	i := candidateIndex(d, name)
	if i < 0 {
		return ConfigEdit{}, &FieldError{"", "no candidate named " + name}
	}
	token := d.Candidates[i].Ref().String()

	next := make([]candidate.Candidate, 0, len(d.Candidates))
	next = append(next, d.Candidates[:i]...)
	next = append(next, d.Candidates[i+1:]...)

	acts := copyActors(d.Actors)
	actsChanged := false
	for _, actorName := range sortedActorNames(acts) {
		a := acts[actorName]
		if len(a.Candidates) == 0 {
			continue
		}
		kept := make([]roles.Entry, 0, len(a.Candidates))
		removed := false
		for _, e := range a.Candidates {
			if e.Candidate == name || e.Candidate == token {
				removed = true
				continue
			}
			kept = append(kept, e)
		}
		if !removed {
			continue
		}
		if len(kept) == 0 {
			return ConfigEdit{}, &FieldError{"", actorName + " has no other candidate; add one in :actors first"}
		}
		a.Candidates = kept
		acts[actorName] = a
		actsChanged = true
	}

	body, err := encodeCandidates(next)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Candidates: body}
	if actsChanged {
		actBody, err := roles.EncodeActors(acts)
		if err != nil {
			return ConfigEdit{}, err
		}
		sections[config.Actors] = actBody
	}
	if err := dryRun(next, acts, d.Agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{Sections: sections, Message: "delete candidate " + name, Name: name}, nil
}

// CandidateSlots returns every actor slot that references the candidate named
// name, by name or token, sorted by actor name.
func CandidateSlots(d ConfigDoc, name string) []ActorSlot {
	token := candidateToken(d, name)
	var slots []ActorSlot
	for _, actorName := range sortedActorNames(d.Actors) {
		for i, e := range d.Actors[actorName].Candidates {
			if e.Candidate == name || (token != "" && e.Candidate == token) {
				slots = append(slots, ActorSlot{Actor: actorName, Position: i + 1, Off: e.Off})
			}
		}
	}
	return slots
}

// SetActorEntries replaces actor's candidate list with entries. It covers
// reorder, on/off, add and remove in one call.
func SetActorEntries(d ConfigDoc, actor string, entries []roles.Entry) (ConfigEdit, error) {
	a, ok := d.Actors[actor]
	if !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + actor}
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if !resolvesToCandidate(d, e.Candidate) {
			return ConfigEdit{}, &FieldError{"", "no candidate named " + e.Candidate}
		}
		if seen[e.Candidate] {
			return ConfigEdit{}, &FieldError{"", "duplicate candidate " + e.Candidate}
		}
		seen[e.Candidate] = true
	}

	a.Candidates = append([]roles.Entry(nil), entries...)
	acts := copyActors(d.Actors)
	acts[actor] = a
	return actorEdit(d, acts, actor, "edit actor "+actor+" candidates")
}

// EditActor sets actor's agent, tier and check. A reader agent stores no
// check, whatever check says.
func EditActor(d ConfigDoc, actor, agent, tier string, check bool) (ConfigEdit, error) {
	a, ok := d.Actors[actor]
	if !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + actor}
	}
	if _, shipped := roles.Shipped(agent); !shipped {
		if _, ok := d.Agents[agent]; !ok {
			return ConfigEdit{}, &FieldError{"agent", "no agent named " + agent}
		}
	}
	if tier != "" {
		if _, err := harness.ParseTier(tier); err != nil {
			return ConfigEdit{}, &FieldError{"tier", err.Error()}
		}
	}
	shape, err := AgentShape(d, agent)
	if err != nil {
		return ConfigEdit{}, err
	}

	a.Agent = agent
	a.Tier = tier
	if shape == string(agentsrc.ShapeReader) {
		a.Check = nil
	} else {
		a.Check = &check
	}
	acts := copyActors(d.Actors)
	acts[actor] = a
	return actorEdit(d, acts, actor, "edit actor "+actor)
}

// AddActor creates an actor with no candidates, no tier and no check.
func AddActor(d ConfigDoc, name, agent string) (ConfigEdit, error) {
	body, err := roles.EncodeActors(map[string]roles.Actor{name: {Agent: agent}})
	if err != nil {
		return ConfigEdit{}, err
	}
	if _, _, err := roles.ParseActors(body); err != nil {
		return ConfigEdit{}, &FieldError{"", err.Error()}
	}
	if _, taken := d.Actors[name]; taken {
		return ConfigEdit{}, &FieldError{"", "actor " + name + " already exists"}
	}
	if _, shipped := roles.Shipped(agent); !shipped {
		if _, ok := d.Agents[agent]; !ok {
			return ConfigEdit{}, &FieldError{"agent", "no agent named " + agent}
		}
	}

	acts := copyActors(d.Actors)
	acts[name] = roles.Actor{Agent: agent}
	return actorEdit(d, acts, name, "add actor "+name)
}

// DeleteActor removes the actor named name. A builtin actor name is refused.
func DeleteActor(d ConfigDoc, name string) (ConfigEdit, error) {
	if _, ok := harness.RoleByName(name); ok {
		return ConfigEdit{}, &FieldError{"", name + " is built in; it can't be deleted"}
	}
	if _, ok := d.Actors[name]; !ok {
		return ConfigEdit{}, &FieldError{"", "no actor named " + name}
	}

	acts := copyActors(d.Actors)
	delete(acts, name)
	return actorEdit(d, acts, name, "delete actor "+name)
}

// DeleteAgent removes the custom agent named name. A shipped agent and an
// agent any actor uses are both refused. Only the agents section changes.
func DeleteAgent(d ConfigDoc, name string) (ConfigEdit, error) {
	if _, shipped := roles.Shipped(name); shipped {
		return ConfigEdit{}, &FieldError{"", name + " ships with relevo; it can't be deleted"}
	}
	var users []string
	for _, actorName := range sortedActorNames(d.Actors) {
		if d.Actors[actorName].Agent == name {
			users = append(users, actorName)
		}
	}
	if len(users) > 0 {
		return ConfigEdit{}, &FieldError{"", "used by " + strings.Join(users, ", ") + "; point it at another agent in :actors first"}
	}
	if _, ok := d.Agents[name]; !ok {
		return ConfigEdit{}, &FieldError{"", "no agent named " + name}
	}

	agents := make(map[string]roles.AgentEntry, len(d.Agents))
	for k, v := range d.Agents {
		if k == name {
			continue
		}
		agents[k] = v
	}
	body, err := roles.EncodeAgents(agents)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Agents: body}
	if err := dryRun(d.Candidates, d.Actors, agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{Sections: sections, Message: "delete agent " + name, Name: name}, nil
}

// WriteConfigEdit stores e as one revision labelled source "ui". Warnings are
// ignored.
func WriteConfigEdit(s *config.Store, e ConfigEdit) error {
	_, err := s.As("ui", e.Message).PutDoc(e.Sections)
	return err
}

// ReloadConfig returns rt with the sections ConfigWatcher.Refresh replaces
// refreshed from the store.
func ReloadConfig(rt Runtime) (Runtime, error) {
	if rt.Config == nil {
		return rt, errors.New("no config store")
	}
	L, err := rt.Config.Load()
	if err != nil {
		return rt, err
	}
	rt.Candidates = L.Candidates
	rt.Policy = L.Policy
	rt.Registry = L.Registry
	rt.ConfigWarnings = L.Warnings
	return rt, nil
}

// actorEdit builds an actors-only edit from acts and dry-runs it.
func actorEdit(d ConfigDoc, acts map[string]roles.Actor, name, message string) (ConfigEdit, error) {
	body, err := roles.EncodeActors(acts)
	if err != nil {
		return ConfigEdit{}, err
	}
	sections := map[config.Section]json.RawMessage{config.Actors: body}
	if err := dryRun(d.Candidates, acts, d.Agents, d.Policy); err != nil {
		return ConfigEdit{}, err
	}
	return ConfigEdit{Sections: sections, Message: message, Name: name}, nil
}

// validateCandidateInput applies §4.1's rules in order. skip is the index of
// the entry an edit is replacing, excluded from the duplicate checks, or -1.
func validateCandidateInput(in CandidateInput, d ConfigDoc, skip int) (CandidateInput, error) {
	if err := checkProvider(in.Harness, in.Provider); err != nil {
		return in, err
	}
	if strings.TrimSpace(in.Model) == "" {
		return in, &FieldError{"model", "a model is required"}
	}
	in.Model = strings.TrimSpace(in.Model)
	in.Provider = strings.TrimSpace(in.Provider)

	for i, c := range d.Candidates {
		if i == skip {
			continue
		}
		if c.Harness == in.Harness && c.Provider == in.Provider && c.Model == in.Model {
			return in, &FieldError{"model", "already a candidate: " + c.Name}
		}
	}
	for i, c := range d.Candidates {
		if i == skip {
			continue
		}
		if c.Name == in.Provider {
			return in, &FieldError{"provider", in.Provider + " is already a candidate's name"}
		}
	}
	return in, nil
}

// checkProvider is the provider lock: the harness must be one relevo knows, and
// the provider a non-empty single word among the harness's providers when the
// harness names any. It is validateCandidateInput's first four rules, extracted
// so the cockpit's whole-document check can run them over a stored config
// without going through a form.
func checkProvider(harnessKind, provider string) error {
	h, ok := harness.Lookup(harnessKind)
	if !ok {
		return &FieldError{"harness", "pick a harness"}
	}
	if provider == "" {
		return &FieldError{"provider", "a provider is required"}
	}
	if strings.Contains(provider, "/") {
		return &FieldError{"provider", "a provider is one word, with no /"}
	}
	if h.Providers != nil && !slices.Contains(h.Providers, provider) {
		return &FieldError{"provider", "pick one of " + harnessKind + "'s providers"}
	}
	return nil
}

// dryRun validates the whole post-edit config the way the store will: it
// validates every section, converts the agents and actors to a roles file, and
// builds the registry over the candidate set. Any error becomes a whole-form
// FieldError.
func dryRun(cands []candidate.Candidate, acts map[string]roles.Actor, agents map[string]roles.AgentEntry, pol policy.Policy) error {
	if acts == nil {
		acts = map[string]roles.Actor{}
	}
	if agents == nil {
		agents = map[string]roles.AgentEntry{}
	}

	candBody, err := encodeCandidates(cands)
	if err != nil {
		return &FieldError{"", err.Error()}
	}
	if _, err := config.Validate(config.Candidates, candBody); err != nil {
		return &FieldError{"", err.Error()}
	}

	actBody, err := roles.EncodeActors(acts)
	if err != nil {
		return &FieldError{"", err.Error()}
	}
	if _, err := config.Validate(config.Actors, actBody); err != nil {
		return &FieldError{"", err.Error()}
	}

	agentsBody, err := roles.EncodeAgents(agents)
	if err != nil {
		return &FieldError{"", err.Error()}
	}
	if _, err := config.Validate(config.Agents, agentsBody); err != nil {
		return &FieldError{"", err.Error()}
	}

	rf, _, err := roles.FromActors(agents, acts)
	if err != nil {
		return &FieldError{"", err.Error()}
	}
	set, _, err := candidate.Parse(config.FileName(config.Candidates), candBody)
	if err != nil {
		return &FieldError{"", err.Error()}
	}
	if _, err := roles.Build(rf, set, pol); err != nil {
		return &FieldError{"", err.Error()}
	}
	return nil
}

// encodeCandidates renders the candidates section the way the store does:
// two-space indent and a trailing newline.
func encodeCandidates(cands []candidate.Candidate) ([]byte, error) {
	data, err := json.MarshalIndent(cands, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// candidateIndex returns the index of the candidate named name, or -1.
func candidateIndex(d ConfigDoc, name string) int {
	for i, c := range d.Candidates {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// candidateToken returns the canonical token of the candidate named name, or
// "" when no candidate has that name.
func candidateToken(d ConfigDoc, name string) string {
	for _, c := range d.Candidates {
		if c.Name == name {
			return c.Ref().String()
		}
	}
	return ""
}

// resolvesToCandidate reports whether ref names a candidate in d, by name or
// by canonical token.
func resolvesToCandidate(d ConfigDoc, ref string) bool {
	for _, c := range d.Candidates {
		if c.Name == ref || c.Ref().String() == ref {
			return true
		}
	}
	return false
}

// AgentShape returns the shape word ("writer"/"reader") of the agent named
// name: a shipped agent's shape, or a custom entry's own.
func AgentShape(d ConfigDoc, name string) (string, error) {
	if s, ok := roles.Shipped(name); ok {
		return string(s.Shape), nil
	}
	e, ok := d.Agents[name]
	if !ok {
		return "", &FieldError{"agent", "no agent named " + name}
	}
	if e.Native != nil {
		return e.Shape, nil
	}
	src, err := agentsrc.Parse([]byte(e.Source))
	if err != nil {
		return "", &FieldError{"", err.Error()}
	}
	return string(src.Shape), nil
}

// copyActors returns a shallow copy of m: the map is fresh, the Actor values
// are shared until a caller replaces one.
func copyActors(m map[string]roles.Actor) map[string]roles.Actor {
	out := make(map[string]roles.Actor, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// sortedActorNames returns m's keys, sorted.
func sortedActorNames(m map[string]roles.Actor) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
