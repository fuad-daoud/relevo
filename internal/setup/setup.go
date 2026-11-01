// Package setup probes a machine's harnesses, turns the answers its questions
// carry into the starter candidates, policy and actors, and keeps the OS
// SetupEnv the front ends probe and install through.
package setup

import (
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Files is the starter configuration the answers produced, ready to store.
type Files struct {
	Kinds          []string // the harness kinds a CLI is logged in for, in harness.All() order
	Candidates     []byte   // JSON, indented two spaces, trailing newline
	Policy         []byte   // JSON, indented two spaces, trailing newline
	Actors         []byte   // JSON, the written actors
	ActorOrder     []string // the actor names written
	CandidateNames []string // the names DeriveNames gave every candidate
}

// NotAvailableError is what Build returns when no harness is logged in:
// nothing is written until one is, and the front end renders the message
// with the not_available code.
type NotAvailableError struct {
	Message string
}

func (e *NotAvailableError) Error() string { return e.Message }

// OSEnv is the production SetupEnv: the manifest-backed agent install env
// with the OS runner in it. One constructor, so the runner and its deadline
// have one place.
func OSEnv() (SetupEnv, error) {
	install, err := relevo.AgentInstallEnv()
	if err != nil {
		return nil, err
	}
	return runnerEnv{install}, nil
}
