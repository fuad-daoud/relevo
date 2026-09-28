package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Verbs is what a tools/call dispatches to, each returning what the CLI's
// --json would, or an error the caller turns into an isError result. session
// is the calling harness session from the call's _meta, "" when the harness
// sends none; only Status needs it.
type Verbs interface {
	Status(ctx context.Context, session string, a StatusArgs) (any, error)
	Send(ctx context.Context, session string, a SendArgs) (any, error)
	Done(ctx context.Context, session string, a DoneArgs) (any, error)
}

// RelevoVerbs adapts internal/relevo's functions to Verbs. MasterMind is the
// fallback identity for a harness that carries none per call (Claude Code);
// ResolveSession, when set, maps a call's harness session to a MasterMind id
// (opencode sends its session in _meta.sessionID).
type RelevoVerbs struct {
	RT             relevo.Runtime
	MasterMind     string
	ResolveSession func(session string) (string, error)
}

// masterMindFor is the identity of one call: the session's mastermind when the
// harness names it, else the process-level fallback.
func (v *RelevoVerbs) masterMindFor(session string) (string, error) {
	if v.ResolveSession != nil && session != "" {
		return v.ResolveSession(session)
	}
	return v.MasterMind, nil
}

// Status filters relevo.Status to this mastermind's bindings (unless a.All), narrowed to a.Name.
func (v *RelevoVerbs) Status(ctx context.Context, session string, a StatusArgs) (any, error) {
	rep, err := relevo.Status(ctx, v.RT)
	if err != nil {
		return nil, err
	}

	if !a.All {
		id, err := v.masterMindFor(session)
		if err != nil {
			return nil, err
		}
		kept := rep.Bindings[:0:0]
		for _, b := range rep.Bindings {
			if b.MasterMindID == id {
				kept = append(kept, b)
			}
		}
		rep.Bindings = kept
	}

	if a.Name != "" {
		var found *view.BindingStatus
		for i := range rep.Bindings {
			if rep.Bindings[i].Name == a.Name {
				found = &rep.Bindings[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("no binding named %s", a.Name)
		}
		rep.Bindings = []view.BindingStatus{*found}
	}

	if a.Name == "" && !a.All {
		rep = view.HideDone(rep)
	}

	return rep, nil
}

// sendResult is relevo.SendResult plus the tools-mode wait budget, empty on a dry run.
type sendResult struct {
	relevo.SendResult
	WaitBudget string `json:"wait_budget,omitempty"`
}

// waitBudget renders roundTimeoutMS for `relevo wait --timeout`; non-positive reads as "".
func waitBudget(roundTimeoutMS int) string {
	if roundTimeoutMS <= 0 {
		return ""
	}
	return (time.Duration(roundTimeoutMS) * time.Millisecond).String()
}

// budgetOf pulls the wait budget out of a Send result; a non-sendResult has none.
func budgetOf(res any) string {
	if sr, ok := res.(sendResult); ok {
		return sr.WaitBudget
	}
	return ""
}

// Send calls relevo.Send, or relevo.SendDryRun when a.DryRun; AllowYolo is always false.
func (v *RelevoVerbs) Send(ctx context.Context, _ string, a SendArgs) (any, error) {
	opts := relevo.SendOptions{
		Tier:      a.Tier,
		AllowYolo: false,
		Builder:   a.Candidate,
		Regate:    a.Regate,
		Verify:    a.Verify,
	}

	if a.DryRun {
		d, err := relevo.SendDryRun(ctx, v.RT, a.Name, a.File, opts)
		if err != nil {
			return nil, err
		}
		return d, nil
	}

	res, err := relevo.Send(ctx, v.RT, a.Name, a.File, opts)
	if err != nil {
		return nil, err
	}
	out := sendResult{SendResult: res}
	// Reload so a binding with no --timeout still reports store's 24h default.
	if v.RT.Store != nil {
		if b, lerr := v.RT.Store.Load(a.Name); lerr == nil {
			out.WaitBudget = waitBudget(b.RoundTimeoutMS)
		}
	}
	return out, nil
}

type doneResult struct {
	relevo.DoneResult
	Text string `json:"text"`
}

// Done calls relevo.Done and reports relevo.DoneText alongside its result.
func (v *RelevoVerbs) Done(ctx context.Context, _ string, a DoneArgs) (any, error) {
	res, err := relevo.Done(ctx, v.RT, a.Name)
	if err != nil {
		return nil, err
	}
	return doneResult{DoneResult: res, Text: relevo.DoneText(a.Name, res)}, nil
}
