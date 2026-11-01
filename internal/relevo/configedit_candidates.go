package relevo

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// AddCandidate validates in, appends it, and returns the candidates section
// alone.
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

// EditCandidate changes the candidate named name to in. The name follows
// the model, and every actor entry that referenced the candidate follows the
// rename.
func EditCandidate(d ConfigDoc, name string, in CandidateInput) (ConfigEdit, error) {
	entry, san, i, err := validateEditCandidate(d, name, in)
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
	acts, actsChanged := repointEditCandidateActors(acts, oldName, oldToken, newName, newToken)
	return encodeEditCandidate(d, next, acts, actsChanged, oldName, newName)
}

func validateEditCandidate(d ConfigDoc, name string, in CandidateInput) (candidate.Candidate, CandidateInput, int, error) {
	i := candidateIndex(d, name)
	if i < 0 {
		return candidate.Candidate{}, in, -1, &FieldError{"", "no candidate named " + name}
	}
	entry := d.Candidates[i]
	if in.Harness == entry.Harness && in.Provider == entry.Provider && in.Model == entry.Model {
		return candidate.Candidate{}, in, -1, ErrNoChange
	}
	san, err := validateCandidateInput(in, d, i)
	if err != nil {
		return candidate.Candidate{}, in, -1, err
	}
	return entry, san, i, nil
}

func repointEditCandidateActors(acts map[string]roles.Actor, oldName, oldToken, newName, newToken string) (map[string]roles.Actor, bool) {
	changed := false
	if newName == oldName && newToken == oldToken {
		return acts, false
	}
	for actorName, a := range acts {
		entries := a.Candidates
		entryChanged := false
		for j, e := range entries {
			if e.Candidate != oldName && e.Candidate != oldToken {
				continue
			}
			if e.Candidate == newName {
				continue
			}
			if !entryChanged {
				entries = append([]roles.Entry(nil), a.Candidates...)
				entryChanged = true
			}
			entries[j].Candidate = newName
		}
		if entryChanged {
			a.Candidates = entries
			acts[actorName] = a
			changed = true
		}
	}
	return acts, changed
}

func encodeEditCandidate(d ConfigDoc, next []candidate.Candidate, acts map[string]roles.Actor, actsChanged bool, oldName, newName string) (ConfigEdit, error) {
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

	message := "edit candidate " + oldName
	if newName != oldName {
		message = "edit candidate " + oldName + " → " + newName
	}
	return ConfigEdit{Sections: sections, Message: message, Name: newName}, nil
}

// PreviewCandidateName is the name in would get: for editing != "" the entry
// named editing takes in's harness/provider/model; for "" in is appended.
// Names are derived as AddCandidate and EditCandidate derive them. No
// validation; an empty model gives "".
func PreviewCandidateName(d ConfigDoc, editing string, in CandidateInput) string {
	if strings.TrimSpace(in.Model) == "" {
		return ""
	}

	entries := append([]candidate.Candidate(nil), d.Candidates...)
	var i int
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

// DeleteCandidate removes the candidate named name and every actor entry
// referencing it. An actor left with no candidates refuses the edit.
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

// validateCandidateInput applies the rules in order. skip is the index an
// edit is replacing, excluded from the duplicate checks, or -1.
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

// checkProvider is the provider lock: the harness must be one relevo knows,
// and the provider a non-empty single word among the harness's providers when
// the harness names any. It is extracted so the cockpit's whole-document check
// can run it over a stored config without going through a form.
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

// dryRun validates the whole post-edit config the way the store will. Any
// error becomes a whole-form FieldError.
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

func candidateIndex(d ConfigDoc, name string) int {
	for i, c := range d.Candidates {
		if c.Name == name {
			return i
		}
	}
	return -1
}

// candidateToken returns the canonical token of the candidate named name,
// "" when no candidate has that name.
func candidateToken(d ConfigDoc, name string) string {
	for _, c := range d.Candidates {
		if c.Name == name {
			return c.Ref().String()
		}
	}
	return ""
}

// resolvesToCandidate reports whether ref names a candidate in d, by name
// or by canonical token.
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
