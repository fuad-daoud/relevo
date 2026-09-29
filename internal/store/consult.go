package store

import (
	"encoding/json"
	"time"
)

// DefaultConsultCap bounds how many consults may be RUNNING on one binding at
// once. An idle harness process holds roughly 800 MB, so an unbounded fan-out
// is a memory failure, not a workspace.
const DefaultConsultCap = 8

// ConsultState is where one consult has got to. spawning and running are
// non-terminal and both occupy a cap slot; only done and silent are reapable,
// and a finished record is kept because it is the reap worklist.
type ConsultState string

const (
	ConsultSpawning ConsultState = "spawning" // slot reserved; no process yet
	ConsultRunning  ConsultState = "running"  // spawned; no findings yet
	ConsultDone     ConsultState = "done"     // findings queued to the mastermind
	ConsultSilent   ConsultState = "silent"   // gave up; "no findings" reported
)

// Consult is one ephemeral, read-only, one-shot agent attached to a binding.
//
// It is deliberately not a Binding: a consult has no round counter, no diff
// baseline, no worktree and no persistent session, and modelling it as a
// Binding would leave every one of those fields dead while forcing Status, gc,
// Fork, doctor and the UI to filter it out.
//
// "Read-only" describes how the role is configured, not something relevo
// enforces: a consult's writes are not observable to relevo, so the role is a
// contract with the harness, not a sandbox.
type Consult struct {
	// ID is 8 lowercase hex characters, unique within one binding.
	ID string `json:"id"`

	// Role is the actor-table name that was asked, not the candidate that ran
	// it: the candidate is the mastermind's choice at the time, the role is what
	// was intended.
	Role string `json:"actor"`

	Endpoint Endpoint `json:"endpoint"`

	// Round is the owning binding's round at spawn, a label for audit and
	// filenames only: a consult never advances a round or touches a diff
	// baseline.
	Round int `json:"round"`

	AskPath string `json:"ask_path"`
	// FindingsPath is the round file key read through Store.ReadFile.
	FindingsPath string `json:"findings_path"`

	State ConsultState `json:"state"`

	SpawnedAt time.Time `json:"spawned_at"`

	// NudgedAt is when relevo sent this consult its single nudge; zero means
	// it has not been nudged.
	NudgedAt time.Time `json:"nudged_at"`

	// Note is why a silent consult gave up; empty for running and done.
	Note string `json:"note,omitempty"`
}

// consultAlias is Consult without its methods, for the ordinary decode.
type consultAlias Consult

// UnmarshalJSON reads a consult written by this relevo or by one before the
// A4 rename, mapping a legacy "role" key onto Actor. The new "actor" key wins
// when both are present.
func (c *Consult) UnmarshalJSON(raw []byte) error {
	var alias consultAlias
	if err := json.Unmarshal(raw, &alias); err != nil {
		return err
	}
	var newKeys struct {
		Actor *string `json:"actor"`
	}
	if err := json.Unmarshal(raw, &newKeys); err != nil {
		return err
	}
	out := Consult(alias)
	if newKeys.Actor == nil {
		var old struct {
			Role *string `json:"role"`
		}
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		if old.Role != nil {
			out.Role = *old.Role
		}
	}
	*c = out
	return nil
}
