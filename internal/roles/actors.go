// Package roles parses the cockpit's agents and actors config sections and
// converts them into today's File, so every consumer of
// *Registry keeps working unchanged (cockpit spec §3.2, §3.3; A2 round
// 1). Round 1 is additive: nothing is removed and nothing is migrated.
package roles

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

// ErrBadActors reports an agents or actors section that does not parse or
// validate. FromActors errors wrap ErrBadRoles instead, so config.Load
// handles them exactly as it treats a bad roles file.
var ErrBadActors = errors.New("bad actors")

// agentKeyPattern is the shape of an agent name (cockpit spec §3.2): it
// becomes a file name, so it admits only what a file name needs. It is
// roles.json's agentNamePattern.
var agentKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// actorNamePattern is the shape of an actor name: today's role-name pattern
// (internal/roles/file.go).
var actorNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// AgentEntry is one entry of the `agents` section, keyed by agent name.
// Exactly one of Source and Native is set.
type AgentEntry struct {
	// Source is the agentsrc single-source text; Parse must give Name == the
	// entry's key. Forbidden with Native, which carries its own shape.
	Source string `json:"source,omitempty"`
	// Shape is the native agent's shape, "writer" or "reader". Required with
	// Native (its data does not carry one) and forbidden with Source.
	Shape string `json:"shape,omitempty"`
	// Native maps a harness kind to an existing harness-native agent.
	Native map[string]DefRow `json:"native,omitempty"`
}

// Actor is one entry of the `actors` section: a named agent plus candidates in
// order, each of which may be off, plus a tier and a check.
type Actor struct {
	Agent      string  `json:"agent"`
	Candidates []Entry `json:"candidates,omitempty"`
	Tier       string  `json:"tier,omitempty"`
	// Check is the writer's gate: run the project's check after the round.
	// nil means true for a writer; it is refused on a reader.
	Check *bool `json:"check,omitempty"`
}

// Entry is one of an actor's candidates: JSON is either a string (on) or
// {"candidate": s, "off": true}.
type Entry struct {
	Candidate string
	Off       bool
}

// MarshalJSON writes an on entry as its candidate string and an off entry as
// {"candidate": .., "off": true}.
func (e Entry) MarshalJSON() ([]byte, error) {
	if e.Off {
		return json.Marshal(struct {
			Candidate string `json:"candidate"`
			Off       bool   `json:"off"`
		}{Candidate: e.Candidate, Off: true})
	}
	return json.Marshal(e.Candidate)
}

// UnmarshalJSON reads either entry form.
func (e *Entry) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		e.Candidate = s
		e.Off = false
		return nil
	}
	var obj struct {
		Candidate string `json:"candidate"`
		Off       bool   `json:"off"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	e.Candidate = obj.Candidate
	e.Off = obj.Off
	return nil
}

// ParseAgents reads and validates the `agents` section. The returned map is
// keyed by agent name; the warnings are unused in round 1 and always nil.
func ParseAgents(body []byte) (map[string]AgentEntry, []string, error) {
	var entries map[string]AgentEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, nil, fmt.Errorf("agents: %v: %w", err, ErrBadActors)
	}
	if entries == nil {
		// A top-level null decodes into a nil map without an error; the
		// section's top level must be an object.
		return nil, nil, fmt.Errorf("agents: top-level value must be an object: %w", ErrBadActors)
	}
	for _, name := range sortedKeys(entries) {
		if err := validateAgent(name, entries[name]); err != nil {
			return nil, nil, err
		}
	}
	return entries, nil, nil
}

// validateAgent applies §3.1's rules to one agents entry.
func validateAgent(name string, e AgentEntry) error {
	if !agentKeyPattern.MatchString(name) {
		return fmt.Errorf("agents: %s: bad agent name: %w", name, ErrBadActors)
	}
	if _, shipped := Shipped(name); shipped {
		return fmt.Errorf("agents: %s: name is a shipped agent; duplicate it under another name: %w", name, ErrBadActors)
	}

	hasSource := e.Source != ""
	hasNative := e.Native != nil
	switch {
	case hasSource && hasNative:
		return fmt.Errorf("agents: %s: source and native are mutually exclusive: %w", name, ErrBadActors)
	case !hasSource && !hasNative:
		return fmt.Errorf("agents: %s: one of source or native is required: %w", name, ErrBadActors)
	case hasSource:
		if e.Shape != "" {
			return fmt.Errorf("agents: %s: shape is only for a native entry: %w", name, ErrBadActors)
		}
		src, err := agentsrc.Parse([]byte(e.Source))
		if err != nil {
			return fmt.Errorf("agents: %s: %v: %w", name, err, ErrBadActors)
		}
		if src.Name != name {
			return fmt.Errorf("agents: %s: source name is %q: %w", name, src.Name, ErrBadActors)
		}
		return nil
	default:
		if e.Shape != "writer" && e.Shape != "reader" {
			return fmt.Errorf("agents: %s: native needs shape writer or reader: %w", name, ErrBadActors)
		}
		for _, kind := range sortedKeys(e.Native) {
			def := e.Native[kind]
			if _, ok := harness.Lookup(kind); !ok {
				return fmt.Errorf("agents: %s: native.%s: unknown harness kind: %w", name, kind, ErrBadActors)
			}
			if !agentKeyPattern.MatchString(def.Agent) {
				return fmt.Errorf("agents: %s: native.%s.agent: bad name %q: %w", name, kind, def.Agent, ErrBadActors)
			}
			for _, req := range def.Requires {
				if !agentKeyPattern.MatchString(req) {
					return fmt.Errorf("agents: %s: native.%s.requires: bad name %q: %w", name, kind, req, ErrBadActors)
				}
			}
		}
		return nil
	}
}

// ParseActors reads and validates the `actors` section. The returned map is
// keyed by actor name; the warnings are unused in round 1 and always nil.
func ParseActors(body []byte) (map[string]Actor, []string, error) {
	var actors map[string]Actor
	if err := json.Unmarshal(body, &actors); err != nil {
		return nil, nil, fmt.Errorf("actors: %v: %w", err, ErrBadActors)
	}
	if actors == nil {
		return nil, nil, fmt.Errorf("actors: top-level value must be an object: %w", ErrBadActors)
	}
	for _, name := range sortedKeys(actors) {
		if err := validateActor(name, actors[name]); err != nil {
			return nil, nil, err
		}
	}
	return actors, nil, nil
}

// validateActor applies §3.2's rules to one actors entry. The agent's shape is
// not known here, so the reader/check rule lives in FromActors.
func validateActor(name string, a Actor) error {
	if !actorNamePattern.MatchString(name) {
		return fmt.Errorf("actors: %s: bad actor name: %w", name, ErrBadActors)
	}
	if a.Agent == "" {
		return fmt.Errorf("actors: %s.agent: required: %w", name, ErrBadActors)
	}

	seen := make(map[string]bool, len(a.Candidates))
	for i, e := range a.Candidates {
		tok := e.Candidate
		if !candidate.IsName(tok) {
			if _, err := candidate.ParseRef(tok); err != nil {
				return fmt.Errorf("actors: %s.candidates[%d]: %q: want a candidate name or harness/provider/model: %w", name, i, tok, ErrBadActors)
			}
		}
		if seen[tok] {
			return fmt.Errorf("actors: %s.candidates[%d]: duplicate token %q: %w", name, i, tok, ErrBadActors)
		}
		seen[tok] = true
	}

	if a.Tier != "" {
		if _, err := harness.ParseTier(a.Tier); err != nil {
			return fmt.Errorf("actors: %s.tier: %v: %w", name, err, ErrBadActors)
		}
	}
	return nil
}

// EncodeAgents renders the agents section: keys sorted, two-space indent, a
// trailing newline (encoding/json sorts map keys already).
func EncodeAgents(agents map[string]AgentEntry) ([]byte, error) {
	return encode(agents)
}

// EncodeActors renders the actors section with the same rules as EncodeAgents.
func EncodeActors(actors map[string]Actor) ([]byte, error) {
	return encode(actors)
}

// encode is the one encoder both EncodeAgents and EncodeActors call.
func encode(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// sortedKeys returns a string-keyed map's keys, sorted, so validation and
// encoding are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
