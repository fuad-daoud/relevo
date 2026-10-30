package chain

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrRunningMember reports a manual send refused because the binding is a
// member of a chain that is still running. The chain owns the member's rounds
// until it halts, stops or finishes; after that a manual send works again.
var ErrRunningMember = errors.New("a running chain owns this binding")

// RefuseRunningMember is the refusal a manual send gets when name belongs
// to a chain with status running. A name that is no chain member -- or whose
// chain is terminal -- passes. It is read-only: it takes the caller's tx, so a
// send under the lock can call it, and RefuseRunningMemberStore is its
// store-reading twin for the lock-free preflight.
func RefuseRunningMember(tx *store.Tx, name string) error {
	c, err := tx.ChainByMember(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return runningChainRefusal(name, c)
}

// RefuseRunningMemberStore is RefuseRunningMember's read-only twin
// for the preflight, which holds no tx.
func RefuseRunningMemberStore(s *store.Store, name string) error {
	c, err := s.ChainByMember(name)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return runningChainRefusal(name, c)
}

// runningChainRefusal is the one wording, shared by the store and tx checks:
// only a chain in status running refuses; a halted, stopped or done chain has
// handed its member back.
func runningChainRefusal(name string, c db.ChainRow) error {
	if c.Status != string(StatusRunning) {
		return nil
	}
	return fmt.Errorf("binding %q belongs to running chain %s; relevo stop %s first: %w", name, c.Name, c.Name, ErrRunningMember)
}

// OwnsMember reports whether name is one of any chain's member bindings.
// The wiring owns a chain member's red gate and its repair rounds, so the
// ordinary paths ask this before acting on their own.
func OwnsMember(tx *store.Tx, name string) bool {
	_, err := tx.ChainByMember(name)
	return err == nil
}
