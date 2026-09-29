// Package consult runs one-shot, read-only consults beside a binding's
// builder: reserving and starting the process, the reconcile tick that turns
// a finished process into findings, and the round-close reviewer.
package consult

import (
	"context"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Git is the slice of the git CLI the round-close reviewer needs: the
// builder's head, and the throwaway worktree the reviewer reads it through.
type Git interface {
	HeadCommit(ctx context.Context, dir string) (string, error)
	AddDetachedWorktree(ctx context.Context, dir, path, commit string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
}

// Deps is the slice of the caller's runtime the consult lifecycle reads. A
// caller builds it once so consult stays unaware of the rest of the runtime.
type Deps struct {
	Store  *store.Store
	Runner spawn.Runner
	Git    Git
	Now    func() time.Time
	NewID  func() string

	// Seen records a process this daemon has observed alive; LostToRestart
	// reports one it never saw and that started before it. A caller with no
	// daemon watch set passes nil for both, which reads as "never seen".
	Seen          func(pid int, startedAt int64)
	LostToRestart func(pid int, startedAt int64) bool

	// Scope builds a scoped spawn's spec from the caller's template, deriving
	// the unit name from kind, owner, name, round and id.
	Scope func(kind, owner, name string, round int, id string) *spawn.ScopeSpec

	// Usage reads what one consult consumed. Nil is never called.
	Usage func(ctx context.Context, b store.Binding, c store.Consult, end time.Time) *usage.Usage

	// Delivery is the queue a finished consult's findings go through.
	Delivery delivery.Deps

	// ResolveReviewer picks the round-close reviewer: its candidate, harness
	// role and permission tier.
	ResolveReviewer func() (candidate.Candidate, harness.RoleSpec, harness.Tier, error)
}
