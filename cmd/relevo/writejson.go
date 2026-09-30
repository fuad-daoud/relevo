package main

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// The write verbs' result documents. Each is built by a pure
// …DocOf function over the values the verb already computed for its human
// line, so the line and the document can never disagree, and the shape
// is golden-tested without a harness, a network or a state directory.
//
// A document is only printed on success: --json never leaves a document on
// stdout before report renders a failure.

// BindDoc is `relevo bind --json` on both routes: the binding it produced, the
// directory it works in, the actor and candidate it resolved, the tier the
// round runs under (the empty stored tier is the harness default), and whether
// the run resumed an existing binding.
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

// bindDocOf is bind's document. dir is the worktree the add route created, or
// the tree the bind route adopted; actor and candidate are the resolved pair
// the human line names; resumed is true on the --resume route.
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

// SendDoc is `relevo send --json`: the round it opened, the candidate and tier
// the round runs under, and whether the round was deferred. The CLI never
// defers -- only the server does -- so deferred is false for this verb.
type SendDoc struct {
	Name      string `json:"name"`
	Round     int    `json:"round"`
	Candidate string `json:"candidate"`
	Tier      string `json:"tier"`
	Deferred  bool   `json:"deferred"`
}

// sendDocOf is send's document: the round the send opened, the candidate and
// tier it will run under, and whether it was staged without a start.
func sendDocOf(name string, round int, candidate, tier string, deferred bool) SendDoc {
	return SendDoc{
		Name:      name,
		Round:     round,
		Candidate: candidate,
		Tier:      tierOrHarness(tier),
		Deferred:  deferred,
	}
}

// StopDoc is `relevo stop --json`: the round that was ended and the action
// relevo took. action is one of "killed", "reaped", "gone" or "dequeued" for a
// member's round, "nothing" when there was no open round, and "stopped" when the
// name was a chain whose awaited member had no open round (the chain was still
// marked stopped). chain is set when the name resolved to a chain rather than a
// binding.
type StopDoc struct {
	Name   string `json:"name"`
	Round  int    `json:"round"`
	Killed bool   `json:"killed"`
	Action string `json:"action"`
	Chain  bool   `json:"chain,omitempty"`
}

// stopDocOf is stop's document. An empty StopResult.Action is the "no open
// round" answer, which the document names "nothing".
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

// chainStopDocOf is stop's document for a name that is a chain: the same shape,
// with chain carried so a script can tell the two apart.
func chainStopDocOf(name string, res relevo.StopResult) StopDoc {
	doc := stopDocOf(name, res)
	doc.Chain = true
	return doc
}

// DoneDoc is `relevo done --json`: whether the worktree was released, which
// worktree the run touched, the branch it names, and why it was kept when it
// was. chain is set when the name resolved to a chain rather than a binding, in
// which case the worktree and branch are the chain's own tree.
type DoneDoc struct {
	Name       string `json:"name"`
	Released   bool   `json:"released"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch"`
	KeptReason string `json:"kept_reason"`
	Chain      bool   `json:"chain,omitempty"`
}

// doneDocOf is done's document: released is true only when relevo removed the
// worktree, and worktree names whichever of removed/kept/gone the run produced.
func doneDocOf(name string, r relevo.DoneResult) DoneDoc {
	return DoneDoc{
		Name:       name,
		Released:   r.WorktreeRemoved != "",
		Worktree:   firstNonEmpty(r.WorktreeRemoved, r.WorktreeKept, r.WorktreeGone),
		Branch:     r.Branch,
		KeptReason: r.KeptReason,
	}
}

// chainDoneDocOf is done's document for a name that is a chain: the same shape,
// with chain carried so a script can tell the two apart.
func chainDoneDocOf(name string, r relevo.DoneResult) DoneDoc {
	doc := doneDocOf(name, r)
	doc.Chain = true
	return doc
}

// UnbindDoc is `relevo unbind --json`: whether the binding was archived rather
// than deleted, whether its worktree was released, which worktree the run
// touched, and the builder process it stopped.
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
		Worktree:       firstNonEmpty(res.WorktreeRemoved, res.WorktreeKept, res.WorktreeGone),
		KeptReason:     res.KeptReason,
		ProcessStopped: res.ProcessStopped,
	}
}

// gcDoc is `relevo unbind --done --json`: what the sweep would clear or did,
// with one relevo.GCResult per binding. The slice is never nil, so an empty
// sweep marshals [] rather than null.
type gcDoc struct {
	DryRun   bool              `json:"dry_run"`
	Bindings []relevo.GCResult `json:"bindings"`
}

// gcDocOf is the --done route's document.
func gcDocOf(dryRun bool, bindings []relevo.GCResult) gcDoc {
	if bindings == nil {
		bindings = []relevo.GCResult{}
	}
	return gcDoc{DryRun: dryRun, Bindings: bindings}
}

// sweepDoc is `relevo unbind --sweep --json`: the directory swept and one
// relevo.RefOutcome per relevo ref. The slice is never nil, so an empty sweep
// marshals [] rather than null.
type sweepDoc struct {
	DryRun bool                `json:"dry_run"`
	Dir    string              `json:"dir"`
	Refs   []relevo.RefOutcome `json:"refs"`
}

// sweepDocOf is the --sweep route's document.
func sweepDocOf(dryRun bool, res relevo.SweepResult) sweepDoc {
	refs := res.Refs
	if refs == nil {
		refs = []relevo.RefOutcome{}
	}
	return sweepDoc{DryRun: dryRun, Dir: res.Dir, Refs: refs}
}

// GateDoc is `relevo gate <token> --json` and `relevo gate --clear --json`, on
// either ledger: the provider the gate is on, the expiry as RFC3339 ("" when
// the gate is open-ended), how many configured candidates the provider serves,
// and the mode. removed is set only by --clear, and is what tells "nothing was
// gating X" from "cleared X (N)": it is a pointer so a real zero still prints.
type GateDoc struct {
	Subject    string `json:"subject"`
	Until      string `json:"until"`
	Candidates int    `json:"candidates"`
	Mode       string `json:"mode"`
	Removed    *int   `json:"removed,omitempty"`
}

// gateSetDocOf is the gating document.
func gateSetDocOf(provider string, until time.Time, candidates int) GateDoc {
	doc := GateDoc{Subject: provider, Candidates: candidates, Mode: "gated"}
	if !until.IsZero() {
		doc.Until = until.UTC().Format(time.RFC3339)
	}
	return doc
}

// gateClearDocOf is the clearing document: gateSetDocOf's shape plus removed,
// which counts the entries the clear lifted.
func gateClearDocOf(provider string, candidates, removed int) GateDoc {
	return GateDoc{Subject: provider, Candidates: candidates, Mode: "gated", Removed: &removed}
}

// candidateLabel is the candidate a result document names: its short name when
// the configured set has one, else the token itself, the same rule the human
// lines already follow.
func candidateLabel(rt relevo.Runtime, token string) string {
	if name := rt.Candidates.NameOf(token); name != "" {
		return name
	}
	return token
}

// tierOrHarness is the tier a document prints: the stored tier, or the
// harness default the CLI's tier chain falls back to.
func tierOrHarness(tier string) string {
	if tier == "" {
		return "harness"
	}
	return tier
}

// firstNonEmpty returns the first non-empty string, or "" when every one is.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
