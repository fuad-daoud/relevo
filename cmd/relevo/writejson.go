package main

import (
	"cmp"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The write verbs' result documents: each built by a pure …DocOf function over
// the values the verb already computed, so the human line and the document can
// never disagree. A document is only printed on success.

// BindDoc is `relevo bind --json` on both routes: the binding, its directory,
// the resolved actor and candidate, the tier, and whether the run resumed.
type BindDoc struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Actor     string `json:"actor"`
	Candidate string `json:"candidate"`
	Tier      string `json:"tier"`
	State     string `json:"state"`
	Resumed   bool   `json:"resumed"`
	Round     int    `json:"round"`
}

// bindDocOf is bind's document; dir is the worktree the add route created, or
// the tree the bind route adopted.
func bindDocOf(b store.Binding, dir, actor, candidate string, resumed bool) BindDoc {
	return BindDoc{
		Name:      b.Name,
		Dir:       dir,
		Actor:     actor,
		Candidate: candidate,
		Tier:      tierOrHarness(b.Tier),
		State:     string(b.State),
		Resumed:   resumed,
		Round:     b.Round,
	}
}

// SendDoc is `relevo send --json`: the round opened, the candidate and tier it
// runs under, and whether it was deferred. The CLI never defers, so deferred
// is false here.
type SendDoc struct {
	Name      string `json:"name"`
	Round     int    `json:"round"`
	Candidate string `json:"candidate"`
	Tier      string `json:"tier"`
	Deferred  bool   `json:"deferred"`
}

// sendDocOf is send's document.
func sendDocOf(name string, round int, candidate, tier string, deferred bool) SendDoc {
	return SendDoc{
		Name:      name,
		Round:     round,
		Candidate: candidate,
		Tier:      tierOrHarness(tier),
		Deferred:  deferred,
	}
}

// StopDoc is `relevo stop --json`: the round ended and the action taken --
// "killed", "reaped", "gone" or "dequeued", "nothing" with no open round,
// "stopped" for a chain whose awaited member had none. chain is set when the
// name was a chain.
type StopDoc struct {
	Name   string `json:"name"`
	Round  int    `json:"round"`
	Killed bool   `json:"killed"`
	Action string `json:"action"`
	Chain  bool   `json:"chain,omitempty"`
}

// stopDocOf is stop's document; an empty action is the "no open round"
// answer, named "nothing".
func stopDocOf(name string, res relevo.StopResult) StopDoc {
	action := res.Action
	if action == "" {
		action = "nothing"
	}
	return StopDoc{
		Name:   name,
		Round:  res.Round,
		Killed: action == "killed",
		Action: action,
	}
}

// chainStopDocOf is stop's document for a chain: the same shape, with chain
// carried.
func chainStopDocOf(name string, res relevo.StopResult) StopDoc {
	doc := stopDocOf(name, res)
	doc.Chain = true
	return doc
}

// DoneDoc is `relevo done --json`: whether the worktree was released, which
// worktree the run touched, the branch, and why it was kept. chain is set when
// the name was a chain.
type DoneDoc struct {
	Name       string `json:"name"`
	Released   bool   `json:"released"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch"`
	KeptReason string `json:"kept_reason"`
	Chain      bool   `json:"chain,omitempty"`
}

// doneDocOf is done's document: released is true only when relevo removed the
// worktree.
func doneDocOf(name string, r relevo.DoneResult) DoneDoc {
	return DoneDoc{
		Name:       name,
		Released:   r.WorktreeRemoved != "",
		Worktree:   cmp.Or(r.WorktreeRemoved, r.WorktreeKept, r.WorktreeGone),
		Branch:     r.Branch,
		KeptReason: r.KeptReason,
	}
}

// chainDoneDocOf is done's document for a chain: the same shape, with chain
// carried.
func chainDoneDocOf(name string, r relevo.DoneResult) DoneDoc {
	doc := doneDocOf(name, r)
	doc.Chain = true
	return doc
}

// UnbindDoc is `relevo unbind --json`: whether the binding was archived,
// whether its worktree was released, which worktree it touched, and the
// process stopped.
type UnbindDoc struct {
	Name           string `json:"name"`
	Released       bool   `json:"released"`
	Archived       bool   `json:"archived"`
	Worktree       string `json:"worktree"`
	KeptReason     string `json:"kept_reason"`
	ProcessStopped int    `json:"process_stopped"`
}

// unbindDocOf is unbind's document, the same fields UnbindText renders.
func unbindDocOf(name string, res relevo.UnbindResult) UnbindDoc {
	return UnbindDoc{
		Name:           name,
		Released:       res.WorktreeRemoved != "",
		Archived:       res.Archived,
		Worktree:       cmp.Or(res.WorktreeRemoved, res.WorktreeKept, res.WorktreeGone),
		KeptReason:     res.KeptReason,
		ProcessStopped: res.ProcessStopped,
	}
}

// gcDoc is `relevo unbind --done --json`. Bindings is never nil, so an empty
// sweep marshals [] rather than null.
type gcDoc struct {
	DryRun   bool                 `json:"dry_run"`
	Bindings []housekeep.GCResult `json:"bindings"`
}

func gcDocOf(dryRun bool, bindings []housekeep.GCResult) gcDoc {
	if bindings == nil {
		bindings = []housekeep.GCResult{}
	}
	return gcDoc{DryRun: dryRun, Bindings: bindings}
}

// sweepDoc is `relevo unbind --sweep --json`. Refs is never nil, so an empty
// sweep marshals [] rather than null.
type sweepDoc struct {
	DryRun bool                   `json:"dry_run"`
	Dir    string                 `json:"dir"`
	Refs   []housekeep.RefOutcome `json:"refs"`
}

func sweepDocOf(dryRun bool, res housekeep.SweepResult) sweepDoc {
	refs := res.Refs
	if refs == nil {
		refs = []housekeep.RefOutcome{}
	}
	return sweepDoc{DryRun: dryRun, Dir: res.Dir, Refs: refs}
}

// GateDoc is the gate set/clear document: the subject, the expiry as RFC3339
// ("" when open-ended), and the mode. removed is set only by --clear, and is a
// pointer so a real zero still prints.
type GateDoc struct {
	Subject    string `json:"subject"`
	Until      string `json:"until"`
	Candidates int    `json:"candidates"`
	Mode       string `json:"mode"`
	Removed    *int   `json:"removed,omitempty"`
}

func gateSetDocOf(provider string, until time.Time, candidates int) GateDoc {
	doc := GateDoc{Subject: provider, Candidates: candidates, Mode: "gated"}
	if !until.IsZero() {
		doc.Until = until.UTC().Format(time.RFC3339)
	}
	return doc
}

// gateClearDocOf is the clearing document: gateSetDocOf's shape plus removed.
func gateClearDocOf(provider string, candidates, removed int) GateDoc {
	return GateDoc{Subject: provider, Candidates: candidates, Mode: "gated", Removed: &removed}
}

// PushAckDoc is `relevo push --ack --json`: the entry settled, the route, and
// already_confirmed, true only on the idempotent retry -- false when this run
// wrote the confirmation.
type PushAckDoc struct {
	Binding          string `json:"binding"`
	Seq              int    `json:"seq"`
	Route            string `json:"route"`
	AlreadyConfirmed bool   `json:"already_confirmed"`
}

// pushAckDocOf is the ack document; the long form needs none, its stream
// already NDJSON.
func pushAckDocOf(res delivery.AckResult) PushAckDoc {
	return PushAckDoc{
		Binding:          res.Binding,
		Seq:              res.Seq,
		Route:            res.Route,
		AlreadyConfirmed: res.AlreadyConfirmed,
	}
}

// candidateLabel is the candidate a result document names: its short name when
// the set has one, else the token itself.
func candidateLabel(rt relevo.Runtime, token string) string {
	if name := rt.Candidates.NameOf(token); name != "" {
		return name
	}
	return token
}

// tierOrHarness is the tier a document prints: the stored tier, or "harness"
// when empty.
func tierOrHarness(tier string) string {
	if tier == "" {
		return "harness"
	}
	return tier
}

// ServiceDoc is `relevo service install|uninstall --json`: which verb ran, the
// unit file it acted on, and what happened to it: wrote, unchanged, removed or
// absent.
type ServiceDoc struct {
	Action  string `json:"action"`
	Unit    string `json:"unit"`
	Outcome string `json:"outcome"`
}

// serviceDocOf is the service document; the human line prints the same fields.
func serviceDocOf(action, unit, outcome string) ServiceDoc {
	return ServiceDoc{Action: action, Unit: unit, Outcome: outcome}
}
