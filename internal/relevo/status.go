package relevo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// mastermindRoute decides how a pending report reaches a binding mastermind:
// the live channel claim first, then the configured
// deliverer for the mastermind kind, else "pull".
//
// live reports whether that route can push right now. A pull route is never
// live: the daemon cannot see whether the background wait's `relevo wait` is
// running, which is exactly why pull is a route and not a fault.
func mastermindRoute(rt Runtime, b store.Binding) (route string, live bool) {
	if rt.Channels != nil && b.MasterMindID != "" {
		now := time.Now()
		if rt.Now != nil {
			now = rt.Now()
		}
		if c, err := rt.Channels.Live(b.MasterMindID, now); err == nil && c != nil {
			return "channel", true
		}
	}
	if b.MasterMind.Kind != "" {
		if _, ok := rt.Deliverers[b.MasterMind.Kind]; ok {
			return "deliverer", true
		}
	}
	return "pull", false
}

// Status builds every row from the store and what relevo can determine
// locally: the mastermind record, a live channel claim and the configured
// deliverers. Only store failures fail the call. Each live chain's member rows
// are replaced by the chain's own row (applyChains).
func Status(ctx context.Context, rt Runtime) (view.Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}

	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return view.Report{}, err
	}
	return applyChains(rep, chains), nil
}

func buildReport(ctx context.Context, rt Runtime, bindings []store.Binding) (view.Report, error) {
	rows := make([]view.BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRow(ctx, rt, b)
		if err != nil {
			return view.Report{}, err
		}
		rows = append(rows, row)
	}

	// status now uses the same attention-first order ui already did --
	// NEEDS YOU, HELD, ACTIVE, PAUSED, DONE, stale first, newest Last.TS
	// first -- so `status`, `status --json` and the statusline agree with
	// `ui` instead of the plain name order this used to be.
	rows = view.SortRows(rows, true)

	rep := view.Report{Bindings: rows}
	rep.Gated = availability.Gates(AvailabilityDeps(rt))
	rep.Unused = UnusedProviderGates(rt)
	return rep, nil
}

// bindingShape is the shape a status row carries: store.ShapeReader for a
// reader, "" for a writer. A writer's row keeps the document it always had --
// an absent shape reads as a writer everywhere -- while a reader names itself
// so the row's word can follow it.
func bindingShape(b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return store.ShapeReader
	}
	return ""
}

// statusRow is read-only, so it reaches the store through the self-locking
// *store.Store methods directly rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
func statusRow(ctx context.Context, rt Runtime, b store.Binding) (view.BindingStatus, error) {
	row := view.BindingStatus{
		Name: b.Name, CWD: b.CWD, Round: b.Round,
		State: string(b.State), Display: view.DisplayState(b.State),
		BuilderCandidate: b.BuilderCandidate,
		BuilderAccount:   b.BuilderAccount,
		Role:             bindingRole(b),
		Shape:            bindingShape(b),
		ForkedFrom:       b.ForkedFrom,
		ForkedAtRound:    b.ForkedAtRound,
		Consults:         consult.Running(b),
		Switches:         b.RoundSwitches,
		Branch:           b.Branch,
		MasterMindKind:   b.MasterMind.Kind,
		MasterMindID:     b.MasterMindID,
		BuilderKind:      b.Builder.Kind, BuilderStatus: view.AgentUnknown,
	}

	// BuilderName is set only when the set actually holds the token. A retired
	// token leaves the field empty, and RenderStatus falls back to printing
	// the token itself.
	if name, ok := rt.Candidates.NameFor(b.BuilderCandidate); ok {
		row.BuilderName = name
	}

	// A custom builder definition is named on the row -- and so in
	// `status --json` -- while a shipped one leaves today's document alone.
	// The definition named is the binding's own role's.
	if kind := b.Builder.Kind; kind != "" {
		if role, ok := rt.RoleRegistry().Role(bindingRole(b)); ok {
			if d, ok := role.Definitions[kind]; ok && d.Custom {
				row.BuilderDefinition = d.Agent
				row.BuilderDefinitionCustom = true
			}
		}
	}

	row.MasterMindRoute, row.MasterMindRouteLive = mastermindRoute(rt, b)

	// MasterMindName is the record's name, so `status --json` and a status row
	// can say "mastermind architect-1" without a second lookup by the reader.
	// A Runtime with no registry (tests) or a forgotten record leaves it "".
	if b.MasterMindID != "" && rt.MasterMinds != nil {
		if rec, err := rt.MasterMinds.Get(b.MasterMindID); err == nil {
			row.MasterMindName = rec.Name
		}
	}

	// A binding landed since its last send says so until the branch
	// moves again.
	if !b.LandedAt.IsZero() {
		row.Landed = "landed"
		if b.LandedPR != "" {
			row.Landed = "landed pr " + b.LandedPR
		}
		row.LandedAt = b.LandedAt
		row.LandedPR = b.LandedPR
	}

	if b.Builder.Headless() {
		row.BuilderStatus, row.Headless = headlessStatus(ctx, rt, b)
	} else if b.Builder.Remote() {
		row.Server = b.Builder.Server
		row.BuilderStatus = b.Builder.RemoteStatus
		if row.BuilderStatus == "" {
			row.BuilderStatus = "unknown"
		}
		if row.BuilderStatus == string(remote.RoundQueued) {
			row.BuilderStatus = view.QueueText(b.Builder.RemoteQueue, b.Builder.Server, rt.Now())
		}
		if b.Builder.RemoteStatus == string(remote.RoundRunning) && b.Builder.RemoteLive != nil {
			view.ApplyRemoteLive(&row, b.Builder.RemoteLive, !b.StalledSince.IsZero(), rt.Store.BuilderLogPath(b.Name, b.Round), rt.Now())
		}
		if !b.StalledSince.IsZero() && b.Builder.RemoteStatus == string(remote.RoundRunning) {
			row.BuilderStatus = "stalled " + view.AgeText(rt.Now().Sub(b.StalledSince))
		}
	}

	// The progress labels. The stale label is the row's own; the working
	// label is a headless row's, already carried out of headlessStatus (a
	// remote row's status comes from the server). This runs before the gate
	// override so a gated row still reads "gating ...".
	labelsNow := time.Now()
	if rt.Now != nil {
		labelsNow = rt.Now()
	}
	working, staleLabel := labelsOf(b, labelsNow)
	row.Stale = staleLabel
	if b.Progress != nil {
		row.LastProgressAt = b.Progress.TreeAt
		if b.Progress.OutputAt.After(row.LastProgressAt) {
			row.LastProgressAt = b.Progress.OutputAt
		}
	}
	switch {
	case strings.HasPrefix(working, "stalled "):
		row.Stall = working
	case strings.HasPrefix(working, "exploring "):
		row.Exploring = working
	}

	// A gate in flight overrides whatever the builder itself reports: the
	// round is held on the gate, not on the builder, which the marker already
	// confirmed finished.
	if b.GateRun != nil {
		age := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0)).Truncate(time.Second)
		row.BuilderStatus = fmt.Sprintf("gating %s", age)
	}

	// broken is overloaded: it means the builder process is gone, which
	// covers both a clean exit and one mid-round. needs_you is unambiguous.
	if b.State == store.StateBroken {
		row.Detail = view.DiagnoseBuilder(b).Detail(b.Round)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return view.BindingStatus{}, err
	}
	for _, e := range entries {
		if store.IsPromptKind(e.Kind) && e.Round > row.PlanRound {
			row.PlanRound = e.Round
		}
	}
	if w, ok := view.WaitingOn(b, entries, questionFirstLine(rt)); ok {
		row.Waiting = &w
	}
	if n := len(entries); n > 0 {
		last := entries[n-1]
		row.LastSeq = last.Seq
		row.Last = &view.LastEvent{
			TS: last.TS, Round: last.Round,
			Direction: last.Direction, Kind: last.Kind,
			Note: last.Note, Outcome: last.Outcome,
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; view.IsPayloadKind(e.Kind) {
			row.LastPayload = &view.LastEvent{
				TS: e.TS, Round: e.Round,
				Direction: e.Direction, Kind: e.Kind,
				Note: e.Note, Outcome: e.Outcome,
			}
			break
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Kind == store.KindDiff {
			row.LastClose = &view.CloseInfo{Round: e.Round, Commits: e.Commits, Tree: e.Tree}
			break
		}
	}
	row.RoundStart, row.RoundEnd, row.RoundUsage = view.RoundFacts(entries)
	row.RoundPriorTokens = view.PriorTokensOf(entries, row.PlanRound)
	if b.Builder.Remote() && row.RoundEnd.IsZero() && b.Builder.RemoteLive != nil {
		row.RoundPriorTokens = b.Builder.RemoteLive.PriorTokens
	}
	var usages []usage.Usage
	var consults []bool
	var switches []usage.Usage
	var reportPriors []usage.Tokens
	for _, e := range entries {
		if e.Kind == store.KindReport {
			if e.Usage != nil {
				u := *e.Usage
				row.LastUsage = &u
			}
			if e.PriorTokens != nil {
				reportPriors = append(reportPriors, *e.PriorTokens)
			}
		}
		if e.Kind == store.KindSwitch && e.Usage != nil {
			switches = append(switches, *e.Usage)
		}
		if e.Usage == nil || (e.Kind != store.KindReport && e.Kind != store.KindFindings) {
			continue
		}
		usages = append(usages, *e.Usage)
		consults = append(consults, e.Kind == store.KindFindings)
	}
	if len(usages) > 0 || len(switches) > 0 || len(reportPriors) > 0 {
		s := usage.Sum(usages, consults)
		for _, sw := range switches {
			s = s.AddSegment(sw)
		}
		for _, p := range reportPriors {
			s.Tokens = s.Tokens.Add(p)
		}
		row.Spend = &s
	}
	// A round that is still running gets its figure read live:
	// after Spend is set, so the recorded sums stay exactly what the log
	// entries give. rt.Now is nil in some test runtimes; the clock falls
	// back to the wall.
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	if !b.Builder.Remote() {
		row.LiveUsage = peekUsage(ctx, rt, b, now)
	}
	row.Dirty = row.LastClose != nil && row.LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()

	// The live diff: the round's working tree against its baseline,
	// while a round is open. liveStat degrades to nil on its own -- no Git
	// wired, no baseline recorded, or the git read failed -- so this never
	// fails the row.
	if !b.Builder.Remote() {
		row.Live = liveStat(ctx, rt, b)
	}

	// The quiet age: only for an ACTIVE row with an open round that has
	// been sampled at least once. Reuses the same now the live usage figure
	// just used, so the two clocks in one row never disagree.
	if row.Display == "ACTIVE" && !b.RoundStartedAt.IsZero() && !row.LastProgressAt.IsZero() {
		row.QuietFor = view.AgeText(now.Sub(row.LastProgressAt))
	}

	// The unread marker: the newest report entry is newer than the
	// binding's .viewed stamp, or there is no stamp at all and a report
	// exists.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != store.KindReport {
			continue
		}
		viewedAt, ok := rt.Store.ViewedAt(b.Name)
		if !ok || entries[i].TS.After(viewedAt) {
			row.Unread = true
		}
		break
	}

	pending, found, err := rt.Store.PendingForMasterMind(b.Name)
	if err != nil {
		return view.BindingStatus{}, err
	}
	if found {
		row.Pending = &view.PendingInfo{Round: pending.Round, Kind: pending.Kind}
	}

	// The newest reviewer verdict, while it judged the round just closed:
	// LastVerdict.Round == b.Round-1 means no later round has closed since.
	// A verdict for an older round is history, and `relevo log` has it.
	if b.LastVerdict != nil && b.LastVerdict.Round == b.Round-1 {
		row.Verdict = fmt.Sprintf("verdict: %s (%d reasons)", b.LastVerdict.Verdict, len(b.LastVerdict.Reasons))
	}

	return row, nil
}
