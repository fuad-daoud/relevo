package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/view"
)

// Verbs is what a tools/call dispatches to, each returning what the CLI's
// --json would, or an error the caller turns into an isError result. session is
// the calling harness session from the call's _meta, opencode's namespaced
// ai.opencode/sessionID, "" when the harness sends none; only Status resolves
// it, and send, done, show and gate address a binding by name.
type Verbs interface {
	Status(ctx context.Context, session string, a StatusArgs) (any, error)
	Send(ctx context.Context, session string, a SendArgs) (any, error)
	Done(ctx context.Context, session string, a DoneArgs) (any, error)
	Show(ctx context.Context, session string, a ShowArgs) (any, error)
	Gate(ctx context.Context, session string, a GateArgs) (any, error)
	Wait(ctx context.Context, session string, a WaitArgs) (any, error)
}

// RelevoVerbs adapts internal/relevo's functions to Verbs. MasterMind is the
// fallback identity for a harness that carries none per call (Claude Code);
// ResolveSession, when set, maps a call's harness session to a MasterMind id
// (opencode sends its session in _meta under ai.opencode/sessionID). Only
// Status consults either: send, done, show and gate address a binding by name.
type RelevoVerbs struct {
	RT             relevo.Runtime
	MasterMind     string
	ResolveSession func(session string) (string, error)
	WaitInterval   time.Duration
}

// masterMindFor is the identity of one call: the session's mastermind when the
// harness names it, else the process-level fallback. An identity that would be
// "" is an error, never a filter that silently matches nothing.
func (v *RelevoVerbs) masterMindFor(session string) (string, error) {
	if v.ResolveSession != nil {
		if session == "" {
			return "", errors.New("this call carries no OpenCode session (`_meta ai.opencode/sessionID`); cannot tell which MasterMind it belongs to")
		}
		id, err := v.ResolveSession(session)
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", fmt.Errorf("opencode session %s resolved to no MasterMind", session)
		}
		return id, nil
	}
	if v.MasterMind == "" {
		return "", errors.New(`no relevo MasterMind for this session; run "relevo mastermind init"`)
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

// Done calls relevo.Done and reports relevo.DoneText alongside its result. A
// name that is a chain routes to relevo.ChainDone: status shows one synthetic
// row named after the chain, so done on that name releases every member. A
// name that is no chain -- or a store that cannot answer -- falls through to
// the binding path unchanged.
func (v *RelevoVerbs) Done(ctx context.Context, _ string, a DoneArgs) (any, error) {
	if v.RT.Store != nil {
		if _, err := v.RT.Store.Chain(a.Name); err == nil {
			res, cerr := relevo.ChainDone(ctx, v.RT, a.Name)
			if cerr != nil {
				return nil, cerr
			}
			return doneResult{DoneResult: res, Text: relevo.DoneText(a.Name, res)}, nil
		}
	}
	res, err := relevo.Done(ctx, v.RT, a.Name)
	if err != nil {
		return nil, err
	}
	return doneResult{DoneResult: res, Text: relevo.DoneText(a.Name, res)}, nil
}

// Show calls relevo.Show for one round: an empty section reads the prompt, and
// round 0 the newest completed round.
func (v *RelevoVerbs) Show(ctx context.Context, _ string, a ShowArgs) (any, error) {
	section := relevo.ShowSection(a.Section)
	if section == "" {
		section = relevo.ShowPrompt
	}
	return relevo.Show(ctx, v.RT, relevo.ShowOptions{Name: a.Name, Round: a.Round, Section: section})
}
