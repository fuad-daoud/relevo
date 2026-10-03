package relevo

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Scope is what one status-shaped surface shows: which mastermind's rows, and
// which of them survive the DONE rule. An empty MasterMindID is every
// mastermind's, which is what --all-masterminds asks for. Named marks a view
// that already answered by name, so no scope narrowing applies to it: asking
// for one binding or chain is a request for that specific thing.
type Scope struct {
	MasterMindID string
	All          bool
	Named        bool
}

// ScopeReport is the one scope rule a built report is narrowed by: keep the
// scope's mastermind's rows, then hide DONE unless the scope keeps them. It is
// pure, so every surface's row set can be compared without a harness.
func ScopeReport(rep view.Report, sc Scope) view.Report {
	if !sc.Named && sc.MasterMindID != "" {
		kept := make([]view.BindingStatus, 0, len(rep.Bindings))
		for _, b := range rep.Bindings {
			if b.MasterMindID == sc.MasterMindID {
				kept = append(kept, b)
			}
		}
		rep.Bindings = kept
	}
	if sc.Named || sc.All {
		return rep
	}
	return view.HideDone(rep)
}

// scopeBinding is ScopeReport's predicate one step earlier, where the rows do
// not exist yet: MasterMindStatus narrows the store before it builds anything,
// and has to reach the same verdict. A chain's own row is synthesised after
// this, so a chain that is itself over still reads on the surfaces that show
// chains.
func scopeBinding(b store.Binding, sc Scope) bool {
	if sc.Named {
		return true
	}
	if sc.MasterMindID != "" && b.MasterMindID != sc.MasterMindID {
		return false
	}
	return sc.All || b.State != store.StateDone
}

// ScopeRefusal is an identity a status surface could not resolve. It is a
// refusal, never a fallback to "every mastermind" or to no rows at all: the
// next step is the flag or the command that settles it.
type ScopeRefusal struct {
	Cause error
	Next  string
}

func (e ScopeRefusal) Error() string {
	return fmt.Sprintf("status shows one mastermind's bindings, and no mastermind resolved (%v); pass --mastermind <name|id>, or --all-masterminds to show every mastermind's", e.Cause)
}

func (e ScopeRefusal) Unwrap() error { return e.Cause }

// scopeNext is the working next step for a refusal. Two live sessions in one
// directory are settled by naming which one; everything else -- an unknown or
// stale ref, a session with no record, no detection at all -- is settled by
// looking at what is registered.
func scopeNext(err error) string {
	var ambiguous mastermind.ErrAmbiguousOpencodeSession
	if errors.As(err, &ambiguous) {
		return "relevo status --mastermind <name|id>"
	}
	return "relevo mastermind list"
}

// ResolveScope turns the scope flags a status verb was given into the one
// Scope, resolving identity through resolve. The record comes back too, and is
// nil only for --all-masterminds, because the statusline names its owner on its
// first line and every mastermind has no single name to print.
//
// resolve is injected so the rule needs no registry to test; in production it
// closes over a Runtime and calls mastermind.Resolve.
func ResolveScope(mastermindRef string, allMasterMinds, allDone, named bool,
	resolve func(ref string) (mastermind.Record, error)) (Scope, *mastermind.Record, error) {

	if allMasterMinds && mastermindRef != "" {
		return Scope{}, nil, ScopeRefusal{
			Cause: errors.New("--all-masterminds and --mastermind are exclusive"),
			Next:  "relevo help",
		}
	}
	sc := Scope{All: allDone, Named: named}
	if allMasterMinds {
		return sc, nil, nil
	}
	// A named view answers by name, so it needs no identity: a session that
	// resolves to none is not a fault on a path that never asked.
	if named && mastermindRef == "" {
		return sc, nil, nil
	}
	rec, err := resolve(mastermindRef)
	if err != nil {
		return Scope{}, nil, ScopeRefusal{Cause: err, Next: scopeNext(err)}
	}
	sc.MasterMindID = rec.ID
	return sc, &rec, nil
}
