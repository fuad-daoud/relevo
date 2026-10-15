// Package setup probes a machine's harnesses, turns the answers its questions
// carry into the starter candidates, policy and actors, and keeps the OS
// SetupEnv the front ends probe and install through.
package setup

import (
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Files is the starter configuration the answers produced, ready to store:
// what the candidates, policy and actors sections carry, the harnesses agent
// definitions are installed for, the order the actor summary reads, and the
// names every candidate got.
type Files struct {
	Kinds          []string // the harness kinds a CLI is logged in for, in harness.All() order
	Candidates     []byte   // JSON, indented two spaces, trailing newline
	Policy         []byte   // JSON, indented two spaces, trailing newline
	Actors         []byte   // JSON, the builders plus librarian, planner and the chain's reader actors
	ActorOrder     []string // the actor names written: builder, librarian, planner, reviewer, security, lite-planner
	CandidateNames []string // the names DeriveNames gave every candidate, in candidate order
}

// NotAvailable is what Build returns when no harness is logged in: nothing is
// written until one is, and the front end renders the message's login
// commands with the not_available code.
type NotAvailableError struct {
	Message string
}

func (e *NotAvailableError) Error() string { return e.Message }

// OSEnv is the production SetupEnv, what `relevo setup` and `config init`
// both probe and install through: the manifest-backed agent install env every
// other surface already uses, with the OS runner in it. One constructor, so
// the runner and its deadline have one place.
func OSEnv() (SetupEnv, error) {
	install, err := relevo.AgentInstallEnv()
	if err != nil {
		return nil, err
	}
	return runnerEnv{install}, nil
}
