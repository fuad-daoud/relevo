package relevo

import (
	"encoding/json"
	"errors"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
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
	// Servers is the servers section: one entry per remote builder, keyed by
	// the server's short name. Nil when the section is absent.
	Servers map[string]remote.ServerEntry
}

// CandidateInput is what the add/edit candidate form submits.
type CandidateInput struct{ Harness, Provider, Model string }

// FieldError is a validation failure the form shows under one field.
// Field is "harness", "provider", "model" or "" (a whole-form error).
type FieldError struct{ Field, Msg string }

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

	body, ok, err = s.Body(config.Servers)
	if err != nil {
		return ConfigDoc{}, err
	}
	if ok {
		if err := json.Unmarshal(body, &d.Servers); err != nil {
			return ConfigDoc{}, err
		}
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
