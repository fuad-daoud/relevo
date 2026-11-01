package relevo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// mastermindRoute picks the pending-report route: the live push claim
// first, then the deliverer for the mastermind kind, else pull. A pull
// route is never live: the daemon cannot see whether the background
// wait's `relevo wait` is running, which is why pull is a route and not
// a fault. A live claim for a gone record is still the push route but
// not live: the claim outlives the session's host. A failed record
// lookup keeps the claim's answer, and every failure reads as not live.
//
// loads is the report's bulk claim load; a nil loads, or one with no
// claims map, reads the claim here.
func mastermindRoute(rt Runtime, b store.Binding, loads *rowLoads) (route string, live bool) {
	if b.MasterMindID != "" {
		if claims := loads.claimMap(); claims != nil {
			if c := claims[b.MasterMindID]; c != nil {
				return "push", !loads.recordGone(rt, b.MasterMindID)
			}
		} else if rt.Channels != nil {
			now := time.Now()
			if rt.Now != nil {
				now = rt.Now()
			}
			if c, err := rt.Channels.Live(b.MasterMindID, now); err == nil && c != nil {
				return "push", !loads.recordGone(rt, b.MasterMindID)
			}
		}
	}
	if b.MasterMind.Kind != "" {
		if _, ok := rt.Deliverers[b.MasterMind.Kind]; ok {
			return "deliverer", true
		}
	}
	return "pull", false
}

// waitLive reports whether a `relevo wait` is polling the named binding
// now: the pull route's one liveness fact the route itself cannot carry.
// Every failure reads as not live -- a false positive would hold back an
// escalation on a payload nobody is collecting, while a false negative
// only costs the grace window before the row escalates on its own.
//
// waits is the report's bulk load when the store offers one; nil reads
// the registration here.
func waitLive(rt Runtime, name string, waits map[string]*delivery.WaitClaim) bool {
	if waits != nil {
		return waits[name] != nil
	}
	if rt.Waits == nil {
		return false
	}
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	c, err := rt.Waits.Live(name, now)
	return err == nil && c != nil
}

// statusConfig carries Status's options.
type statusConfig struct {
	detail bool
}

// StatusOption is one option on Status.
type StatusOption func(*statusConfig)

// Detail turns the detail figures -- Live, LiveUsage and Headless.Tail -- on
// or off. They default ON. The fleet is the one caller that passes false,
// because it paints no pixel from any of them while each costs a git diff,
// a usage peek or a log read per row per tick.
func Detail(on bool) StatusOption {
	return func(c *statusConfig) { c.detail = on }
}

// statusConfigOf folds opts over the default-on detail figures.
func statusConfigOf(opts []StatusOption) statusConfig {
	cfg := statusConfig{detail: true}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

// Status builds every row from the store and what relevo can determine
// locally. Only store failures fail the call. Each chain's member rows
// are replaced by the chain's own row.
//
// Detail(false) leaves Live, LiveUsage and Headless.Tail nil on every row.
func Status(ctx context.Context, rt Runtime, opts ...StatusOption) (view.Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}

	rep, err := buildReportWith(ctx, rt, bindings, statusConfigOf(opts))
	if err != nil {
		return view.Report{}, err
	}
	chains, err := rt.Store.Chains()
	if err != nil {
		return view.Report{}, err
	}
	rep = view.ApplyChains(rt.Store, rep, chains)
	rep.HandedOver = handedOverRows(rt)
	return rep, nil
}

// handedOverRows lists the bindings another machine writes now. A failed read
// leaves the list empty: the live rows are the report, and this is a footnote.
func handedOverRows(rt Runtime) []view.HandedOverRow {
	left, err := rt.Store.Left()
	if err != nil || len(left) == 0 {
		return nil
	}
	d, err := rt.Store.DB()
	if err != nil {
		return nil
	}
	labels := map[string]string{}
	if list, lerr := d.InstallationList(); lerr == nil {
		counts := map[string]int{}
		for _, in := range list {
			if in.Label != "" {
				counts[in.Label]++
			}
		}
		for _, in := range list {
			if in.Label != "" && counts[in.Label] == 1 {
				labels[in.ID] = in.Label
			}
		}
	}
	seen := map[string]bool{}
	var out []view.HandedOverRow
	for _, l := range left {
		if seen[l.Name] || l.Owner != rt.Store.Owner() {
			continue
		}
		seen[l.Name] = true
		to := labels[l.ToWriter]
		if to == "" {
			to = l.ToWriter
		}
		pending, _ := d.OutboxPendingFor(l.RootID)
		out = append(out, view.HandedOverRow{Name: l.Name, To: to, Kind: l.Kind, NotExported: pending > 0})
	}
	return out
}

// StatusRow is one binding's row with the detail figures on: the single-row
// counterpart to a Detail(false) fleet report. Only store failures fail
// the call -- a binding the store does not hold is the store's own error,
// so the caller can tell an unresolvable key from a merely thin row.
func StatusRow(ctx context.Context, rt Runtime, name string) (view.BindingStatus, error) {
	b, err := rt.Store.Load(name)
	if err != nil {
		return view.BindingStatus{}, err
	}
	return statusRow(ctx, rt, b, statusConfig{detail: true})
}

// statusRow is one binding's row with no report-wide bulk read: the
// single-row path, which resolves each fact with its own store call.
func statusRow(ctx context.Context, rt Runtime, b store.Binding, cfg statusConfig) (view.BindingStatus, error) {
	return statusRowWith(ctx, rt, b, cfg, nil)
}

// buildReport is buildReportWith with the detail figures on, for callers
// whose rows name a diff, a usage figure and a log tail.
func buildReport(ctx context.Context, rt Runtime, bindings []store.Binding) (view.Report, error) {
	return buildReportWith(ctx, rt, bindings, statusConfig{detail: true})
}

func buildReportWith(ctx context.Context, rt Runtime, bindings []store.Binding, cfg statusConfig) (view.Report, error) {
	loads := loadRows(ctx, rt, bindings)
	rows := make([]view.BindingStatus, 0, len(bindings))
	for _, b := range bindings {
		row, err := statusRowWith(ctx, rt, b, cfg, loads)
		if err != nil {
			return view.Report{}, err
		}
		rows = append(rows, row)
	}

	// The rows sort attention-first -- NEEDS YOU, HELD, ACTIVE, PAUSED,
	// DONE, stale first, newest Last.TS first -- so `status`,
	// `status --json` and the statusline agree.
	rows = view.SortRows(rows, true)

	rep := view.Report{Bindings: rows}
	// The ledger is one kv row projected twice, so it is read and decoded
	// once for the report.
	l := loadLedgerOnce(rt)
	rep.Gated = availability.GatesFrom(AvailabilityDeps(rt), l)
	rep.Unused = UnusedProviderGatesFrom(rt, l)
	return rep, nil
}

// statusRowWith is read-only, so it reaches the store through the
// self-locking *store.Store methods rather than a *store.Tx: there is no
// load-modify-save here for WithLock to protect.
//
// cfg.detail gates the three figures no fleet pixel reads -- Live,
// LiveUsage and the headless log tail -- on the call, not on the value,
// so with them off nothing calls git, the usage reader or the log.
//
// loads is the report's shared bulk read, nil on the single-row path.
func statusRowWith(ctx context.Context, rt Runtime, b store.Binding, cfg statusConfig, loads *rowLoads) (view.BindingStatus, error) {
	row := view.BindingStatus{
		Name: b.Name, CWD: housekeep.RoundDir(b), Round: b.Round,
		State: string(b.State), Display: view.DisplayState(b.State),
		BuilderCandidate: b.BuilderCandidate,
		BuilderAccount:   b.BuilderAccount,
		Role:             b.WriterRole(),
		Shape:            view.BindingShape(b),
		ForkedFrom:       b.ForkedFrom,
		ForkedAtRound:    b.ForkedAtRound,
		Consults:         consult.Running(b),
		Switches:         b.RoundSwitches,
		Branch:           b.Branch,
		MasterMindKind:   b.MasterMind.Kind,
		MasterMindID:     b.MasterMindID,
		BuilderKind:      b.Builder.Kind, BuilderStatus: view.AgentUnknown,
	}

	// BuilderName is set only when the set holds the token; a retired
	// token leaves the field empty and RenderStatus falls back to the
	// token itself.
	if name, ok := rt.Candidates.NameFor(b.BuilderCandidate); ok {
		row.BuilderName = name
	}

	// A custom builder definition is named on the row, while a shipped one
	// leaves the document alone. The definition named is the binding's
	// own role's.
	if kind := b.Builder.Kind; kind != "" {
		if role, ok := rt.RoleRegistry().Role(b.WriterRole()); ok {
			if d, ok := role.Definitions[kind]; ok && d.Custom {
				row.BuilderDefinition = d.Agent
				row.BuilderDefinitionCustom = true
			}
		}
	}

	row.MasterMindRoute, row.MasterMindRouteLive = mastermindRoute(rt, b, loads)
	row.WaitLive = waitLive(rt, b.Name, loads.waitMap())

	// MasterMindName is the record's name, so the row can say "mastermind
	// architect-1" without a second lookup by the reader. No registry or
	// a forgotten record leaves it "".
	row.MasterMindName = loads.masterMindName(rt, b)

	// A binding landed since its last send says so until the branch moves
	// again.
	if !b.LandedAt.IsZero() {
		row.Landed = "landed"
		if b.LandedPR != "" {
			row.Landed = "landed pr " + b.LandedPR
		}
		row.LandedAt = b.LandedAt
		row.LandedPR = b.LandedPR
	}

	if b.Builder.Headless() {
		row.BuilderStatus, row.Headless = headlessStatus(ctx, rt, b, cfg.detail, loads.aliveMap())
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

	// The progress labels: the stale label is the row's own, the working
	// label is a headless row's out of headlessStatus. This runs before the
	// gate override so a gated row still reads "gating ...".
	labelsNow := time.Now()
	if rt.Now != nil {
		labelsNow = rt.Now()
	}
	working, staleLabel := view.LabelsOf(b, labelsNow)
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

	// A gate in flight overrides whatever the builder reports: the round is
	// held on the gate, not on the builder.
	if b.GateRun != nil {
		age := rt.Now().Sub(time.Unix(b.GateRun.StartedAt, 0)).Truncate(time.Second)
		row.BuilderStatus = fmt.Sprintf("gating %s", age)
	}

	// broken means the builder process is gone, whether a clean exit or
	// mid-round. needs_you is unambiguous.
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
	setNextInputs(rt, &row, entries)
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Kind == store.KindDiff {
			row.LastClose = &view.CloseInfo{Round: e.Round, Commits: e.Commits, Tree: e.Tree}
			break
		}
	}
	row.RoundStart, row.RoundEnd, row.RoundUsage = view.RoundFacts(entries)
	row.RoundPriorTokens = store.PriorTokensOf(entries, row.PlanRound)
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
	// A running round gets its figure read live, after Spend is set, so the
	// recorded sums stay exactly what the log entries give. A nil rt.Now
	// falls back to the wall clock.
	now := time.Now()
	if rt.Now != nil {
		now = rt.Now()
	}
	if cfg.detail && !b.Builder.Remote() {
		row.LiveUsage = peekUsage(ctx, rt, b, now)
	}
	row.Dirty = row.LastClose != nil && row.LastClose.Tree == "dirty" && b.RoundStartedAt.IsZero()

	// The live diff: the round's working tree against its baseline while a
	// round is open. liveStat degrades to nil on its own, so this never
	// fails the row.
	if cfg.detail && !b.Builder.Remote() {
		row.Live = liveStat(ctx, rt, b)
	}

	// The quiet age: only for an ACTIVE row with an open round sampled at
	// least once. Reuses the live usage figure's now, so the two clocks in
	// one row never disagree.
	if row.Display == "ACTIVE" && !b.RoundStartedAt.IsZero() && !row.LastProgressAt.IsZero() {
		row.QuietFor = view.AgeText(now.Sub(row.LastProgressAt))
	}

	// The unread marker: the newest report entry is newer than the
	// binding's .viewed stamp, or there is no stamp and a report exists.
	// The stamp rides the binding List/Load already read, so the marker
	// costs no second RecordGet.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != store.KindReport {
			continue
		}
		// A report a running chain consumed is already read: the chain's
		// end delivery is the mastermind's copy, so the member row must
		// not keep painting REPORT IN for a round nobody is waiting on.
		if strings.Contains(entries[i].Note, view.ConsumedNoteMarker) {
			break
		}
		if b.ViewedAt == nil || entries[i].TS.After(*b.ViewedAt) {
			row.Unread = true
		}
		break
	}

	// Pending is the oldest unconfirmed to-mastermind payload, derived from
	// the log in hand rather than a second store read. TS is the pending
	// entry's own write time, so the row can say how long the payload has
	// been waiting for a collector.
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed {
			row.Pending = &view.PendingInfo{Round: e.Round, Kind: e.Kind, TS: e.TS}
			break
		}
	}

	// The newest reviewer verdict, while it judged the round just closed:
	// LastVerdict.Round == b.Round-1 means no later round has closed since.
	// An older verdict stays in `relevo log`.
	if b.LastVerdict != nil && b.LastVerdict.Round == b.Round-1 {
		row.Verdict = fmt.Sprintf("verdict: %s (%d reasons)", b.LastVerdict.Verdict, len(b.LastVerdict.Reasons))
	}

	return row, nil
}
