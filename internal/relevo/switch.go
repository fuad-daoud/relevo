package relevo

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// gatedBuilder reports the first live rate-limit gate on b's own builder
// candidate, if any. Pure over the ledger and b's own token: a token the
// configured set no longer holds (the candidate was edited or deleted
// mid-round) is still checked, because the running process is on that triple.
//
// SpawnFailed gates are ignored: a running builder is not a failed spawn, so
// a spawn-failure gate recorded against this same token by an earlier switch
// attempt must not itself trigger another switch.
func gatedBuilder(rt Runtime, b store.Binding) (availability.Gate, bool) {
	for _, g := range availability.LedgerGates(AvailabilityDeps(rt), []string{b.BuilderCandidate}) {
		if g.Token == b.BuilderCandidate && g.Kind == availability.RateLimited {
			return g, true
		}
	}
	return availability.Gate{}, false
}

// roundExclusionGates is one ledger.Gate per token in b.RoundExcluded --
// candidates that exited without a report during the CURRENT round (#191).
// Pure. switchBuilder folds these into the live ledger gates it passes to
// resolveCandidate, so a mid-round switch never lands the pick back on a
// builder that already proved it cannot finish this round. gatedBuilder is
// NOT changed to look at these: it looks only at RateLimited, so an
// exclusion never triggers a switch by itself -- only the switch's own
// resolution sees it.
func roundExclusionGates(b store.Binding) []availability.Gate {
	gates := make([]availability.Gate, 0, len(b.RoundExcluded))
	for _, t := range b.RoundExcluded {
		gates = append(gates, availability.Gate{
			Token:   t,
			Kind:    availability.ExitedNoReport,
			Note:    "round " + strconv.Itoa(b.Round),
			Binding: b.Name,
		})
	}
	return gates
}

// nextBuilder returns the same candidate on the next ungated account of its
// pool, when a limit gated the account the binding is on and another login is
// free. The walk starts after the current login and wraps, so a pool rotates
// through its logins rather than restarting at the first one. It is decided
// before the actor order is walked, so a limit rotates within the pool before
// it ever changes candidate. Pure.
func nextBuilder(b store.Binding, pick AccountPick) (account.Account, bool) {
	if len(pick.Set) == 0 || b.BuilderAccount == "" {
		return account.Account{}, false
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return account.Account{}, false
	}
	pool := pick.Set.Pool(account.Kind(ref.Harness), ref.Provider)
	cur, ok := accountByName(pool, b.BuilderAccount)
	if !ok || !account.Gated(cur, pick.Gates) {
		// The login in use is not gated: this switch has another cause and
		// the actor order decides it as it always did.
		return account.Account{}, false
	}
	start := 0
	for i, a := range pool {
		if a.Name == cur.Name {
			start = i + 1
			break
		}
	}
	for i := 0; i < len(pool); i++ {
		a := pool[(start+i)%len(pool)]
		if !account.Gated(a, pick.Gates) {
			return a, true
		}
	}
	return account.Account{}, false
}

// poolForPick is the pool a pick drew from, for the opencode flip's drift
// check. Nil when the token does not parse or the pool is empty.
func poolForPick(pick AccountPick, token string) []account.Account {
	ref, err := candidate.ParseRef(token)
	if err != nil {
		return nil
	}
	return pick.Set.Pool(account.Kind(ref.Harness), ref.Provider)
}

// switchLinePrefix is what marks a payload line as one round's switch line, so
// payloadNamesSwitches reads a payload the way a reader of it does.
const switchLinePrefix = "Switch: "

// payloadNamesSwitches reports whether a payload already carries switch lines
// rendered from a closed round's own switch history. Pure.
func payloadNamesSwitches(payload string) bool {
	for _, line := range strings.Split(payload, "\n") {
		if strings.HasPrefix(line, switchLinePrefix) {
			return true
		}
	}
	return false
}

// switchLine renders one switch entry's report line from that entry's own
// note -- the reason and the resolution switchEntry already stores, and nothing
// else. Every path that records a switch (a rate limit, an exit without a
// report, a lost daemon, a nudge's resume, a remote server's own switch) writes
// the whole fact into the note, so rendering the note renders the switch
// whatever shape it took; a note this round has never seen is still named rather
// than dropped. "" for an entry with no note: a switch with nothing recorded
// about it has nothing to say.
func switchLine(note string) string {
	if note == "" {
		return ""
	}
	return switchLinePrefix + note
}

// switchLines renders the switch entries of one round, in log order, as the
// lines queueReport appends to that round's report payload. A round that
// switched twice reports both switches, in the order they happened.
//
// Only the round being closed counts: a switch on an earlier round was named in
// that round's report, and one on a later round is not this payload's fact.
// Nudge entries are excluded by note, the same exclusion store.HasKind applies,
// so a nudge is never mistaken for a switch.
//
// A remote round's switches are the server's record, not the log's: the closed
// round's view carries the round's whole history and the caller's payload has
// already named it. The entry a poll writes when it observes the server running
// a different candidate is that same switch as this client happened to see it,
// so naming it here too says the switch twice -- an A-B-A four times. Such an
// entry is excluded by note prefix, the way the nudge is excluded by note, and
// only when the payload already names a switch: a server that ships no switch
// history leaves the poll's entry as the only record there is. Pure.
func switchLines(entries []store.LogEntry, round int, serverSwitch bool) []string {
	var out []string
	for _, e := range entries {
		if e.Round != round || e.Kind != store.KindSwitch || e.Note == nudgeNote {
			continue
		}
		if serverSwitch && strings.HasPrefix(e.Note, pollSwitchNotePrefix) {
			continue
		}
		if line := switchLine(e.Note); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// switchEntry is the log record of one builder switch: why the switch
// happened, and what ExplainResolution says about the pick that replaced
// the builder (spec §3.3, §4.4). Always Confirmed and DirToMasterMind, the same
// reasoning as pickEntry: a switch is never a pending payload.
//
// The rule that follows from Confirmed is the whole of a switch's delivery: a
// switch needs no action, because the round already moved on to the new
// builder and the next tick continues from there. Queuing one anyway would buy
// a mid-round turn on a runner and a second payload in the same delivery the
// report arrives in, for a fact the report already carries. So a switch entry
// is status, and its trace to the mastermind is the switch line in the closing
// round's report -- switchLine below, appended by queueReport. No new stored
// fact is added for it: the note written here is what that line renders.
func switchEntry(now time.Time, round int, reason string, res Resolution, u *usage.Usage) store.LogEntry {
	return store.LogEntry{
		TS: now.UTC(), Round: round, Direction: store.DirToMasterMind,
		Kind: store.KindSwitch, Confirmed: true,
		Usage: u,
		Note:  "switched builder (" + reason + "): " + ExplainResolution("builder", res),
	}
}

// switchBuilder replaces b's builder mid-round with the next candidate the
// policy order and the ledger's live gates pick, and hands it the SAME
// round's plan. The round number does not change -- the new builder
// inherits the partial diff capture.RoundDiff already handles -- but
// RoundStartedAt is restarted, so the replacement gets its own startGrace
// before a nudge and its own round budget, exactly like a fresh handoff.
//
// The replacement is a new process started on the same round's prompt.
// closeOld kills the process it replaces; the gone trigger has nothing left
// to kill and passes closeOld=false, the gated trigger's process is still
// running and passes true.
//
// resolveBuilder's own Resolution is always HowExplicit -- it is handed the
// already-chosen token -- and is discarded. The Resolution switchBuilder
// records in the switch log entry is the one it makes itself, by calling
// resolveCandidate with an omitted token, so the audit trail explains the
// real policy decision rather than the trivial "explicit" one. This is the
// same rule Add and Fork follow (policy-order spec §4.3).
//
// A switch tick returns without deliverAndSettle, like the halt paths in
// Reconcile: replacing a builder is the only thing that tick does to this
// binding.
//
// counted controls whether the switch advances b.RoundSwitches. A rate-limit
// switch -- whether the gate came from a pattern match or from a human's
// `relevo gate <token>` -- passes false: max_switches counts builders that
// fail, not providers that close, and counting one trigger but not the
// other would make the halt depend on who noticed the gate first. The
// `>= limit` check above is unaffected either way: `0` still disables
// switching, and a binding already at the limit still halts instead of
// switching again.
func switchBuilder(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, reason string, closeOld, counted bool) (store.Binding, error) {
	limit := rt.Policy.SwitchLimit()
	if b.RoundSwitches >= limit {
		if b.Builder.PID == 0 {
			b = abandonSession(b)
		}
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
			"%s: builder %s (%s); already switched %d time(s) this round (max_switches %d)",
			b.Name, reason, b.BuilderCandidate, b.RoundSwitches, limit))
	}

	pick := pickFor(rt)
	gates := append(availability.Gates(AvailabilityDeps(rt)), roundExclusionGates(b)...)

	// A limit gated the account this binding is on: retry the same candidate
	// on the next ungated login of its pool before the actor order is walked
	// at all. The rotation is decided here, purely.
	var (
		res     Resolution
		next    account.Account
		rotated bool
	)
	if a, ok := nextBuilder(b, pick); ok {
		if c, cerr := rt.Candidates.Resolve(b.BuilderCandidate); cerr == nil {
			res = Resolution{Candidate: c, How: HowRotation, Account: a.Name}
			next, rotated = a, true
		}
	}
	if !rotated {
		var err error
		res, err = resolveRole(rt.RoleRegistry(), rt.Candidates, gates, "", bindingRole(b), pick)
		if err != nil {
			if b.Builder.PID == 0 {
				b = abandonSession(b)
			}
			return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
				"%s: builder %s (%s); cannot switch: %v",
				b.Name, reason, b.BuilderCandidate, err))
		}
	}

	if closeOld {
		// The one place besides done/unbind where relevo stops a process it
		// started (#99): the mastermind gated the provider while the round's
		// process was still running.
		if b.Builder.PID != 0 && rt.Runner != nil {
			if err := rt.Runner.Kill(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); err != nil {
				return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
					"%s: builder %s; could not stop its process %d to replace it: %v",
					b.Name, reason, b.Builder.PID, err))
			}
		}
	}

	b = abandonSession(b)
	old := b.BuilderCandidate
	now := rt.Now().UTC()
	prior := peekUsage(ctx, rt, b, now)

	// The replacement inherits the mode (spec §5.4): a headless binding gets
	// a headless endpoint, which startRound below fills in.
	ep, _, err := resolveBuilder(ctx, rt, tx, BindOptions{
		Candidate: res.Token(),
		CWD:       b.CWD,
		Headless:  b.Builder.Headless(),
		Tier:      string(effectiveTier(b)),
		Role:      b.Role,
	}, b.Name)
	if err != nil {
		// resolveBuilder already recorded spawn_failed for the pick, which
		// gates it for the next resolution -- so the next human send picks a
		// different candidate. What does not happen is a retry: with the old
		// pane closed (or already gone) and no replacement process, the next
		// tick returns at reconcileHeadless' PID == 0 branch without acting,
		// and view.WaitingOn reads the binding as broken rather than waiting
		// on the daemon. So this binding stops here, and the spawn_failed
		// entry is what keeps the bad candidate out of the next send.
		if counted && !rotated {
			b.RoundSwitches++
		}
		b.State = store.StateBroken
		slog.Warn("builder switch failed", "binding", b.Name, "round", b.Round, "pick", res.Token(), "err", err)
		reason := fmt.Sprintf("%s: builder %s; switching to %s failed: %v", b.Name, b.BuilderCandidate, res.Token(), err)
		// A broken binding no route will fix owes the MasterMind the same entry
		// a halt does; queueBrokenHalt skips only the ones a later tick can
		// still switch, and without a process this is not one of them.
		next, queued, qerr := queueBrokenHalt(ctx, rt, tx, b, reason)
		if qerr != nil {
			// The switch reason rides the error: the entry cannot be queued
			// here, so Admit's own spawn-failure halt is what runs, and a bare
			// queue error left that halt naming only the failed append rather
			// than the switch the round actually broke on.
			//
			// Halt is written only once the entry is queued. A binding that
			// came back halted with the entry unwritten is one Admit reads as
			// already halted -- it keeps its reason and queues nothing -- and
			// nothing else retries it, so the reason the switch recorded would
			// be the only account of the fault, in the state word alone.
			return b, fmt.Errorf("%s: %w", reason, qerr)
		}
		// The binding keeps the reason, name-stripped the way haltBinding
		// records one, only when the entry that carries it was queued. A
		// served binding ships its Halt to its owner, so an empty one left the
		// owner told only that the round broke: not which switch failed, not
		// why, and nothing for the human to act on beyond the state word
		// itself. A skipped binding keeps whatever reason it already had.
		if queued {
			next.Halt = strings.TrimPrefix(reason, b.Name+": ")
		}
		return deliverAndSettle(ctx, rt, tx, next)
	}

	// The whole install shares one opencode login, so a rotation must move the
	// active row itself; claude and codex carry their login per process and
	// need nothing here.
	if rotated {
		if err := switchOpencodeActive(ctx, rt, next, poolForPick(pick, res.Token()), pick.Gates); err != nil {
			slog.Warn("could not flip the opencode active account", "binding", b.Name, "account", next.Name, "err", err)
		}
	}

	b.Builder = carryStream(b.Builder, ep)
	b.BuilderCandidate = res.Token()
	b.BuilderAccount = res.Account
	// A rotation never counts: the pool bounds it naturally -- every login it
	// leaves is gated -- so it must not consume the max_switches budget.
	if counted && !rotated {
		b.RoundSwitches++
	}
	b.BuilderMissingSince = time.Time{}
	b.BuilderScreen = ""
	b.BuilderScreenAt = time.Time{}
	b.State = store.StateActive

	if err := tx.AppendLog(b.Name, switchEntry(now, b.Round, reason, res, prior)); err != nil {
		return b, err
	}

	text := roundPrompt(rt, tx, b, rt.Store.PromptPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round))
	started, err := startRound(ctx, rt, tx, b, text, false)
	if err != nil {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
			"%s: switched builder to %s but could not start round %d: %v",
			b.Name, res.Token(), b.Round, err))
	}
	b = started

	b.RoundStartedAt = now

	slog.Info("builder switched", "binding", b.Name, "round", b.Round,
		"from", old, "to", res.Token(), "reason", reason, "switches", b.RoundSwitches)

	return b, nil
}

// limitText is the text a decision point scans for rate-limit patterns: the
// tail of the current builder process's output -- its log when the round has
// one, otherwise the bytes it appended to the round's stream -- keeping only
// the lines the harness itself wrote. A local builder is always headless since
// #303.
func limitText(ctx context.Context, rt Runtime, b store.Binding) string {
	return currentBuilderScanText(rt, b, availability.LimitScanLines)
}

// gateOnLimit is the one helper every decision point calls (spec §4.4).
// Preconditions: the round is open and the caller holds the store lock.
//
// It applies the switchable guard itself -- the same one the existing
// gatedBuilder triggers use -- and returns handled=false without reading the
// ledger when it fails: an adopted builder is never gated by relevo, and no
// call site has to repeat the check.
//
// It checks the credit class before MatchLimit, because the two differ in kind
// and not only in duration: a credit exhaustion gates until a human tops the
// account up and clears it, while every other limit gates until a reset the
// provider named. Everything after the match is the same either way -- one
// rate_limited ledger entry, the report-on-disk branch, then an uncounted
// switch -- so a credit tail that also carries a reset, a clock or a date takes
// the credit gate and never the timed one.
//
// On a match it records one rate_limited ledger entry (source relevo), warns,
// marks a headless builder's log, then checks whether this round already has
// a report on disk: if so the gate is recorded but the round is left for the
// caller to close as it would have (handled=false, m.Line set); otherwise it
// switches the builder uncounted (handled=true) and returns the replacement.
func gateOnLimit(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string, closeOld bool) (next store.Binding, m availability.LimitMatch, handled bool, err error) {
	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if !switchable {
		return b, availability.LimitMatch{}, false, nil
	}

	now := rt.Now()
	credit := false
	if line, ok := availability.MatchCredit(text); ok {
		credit, m.Line = true, line
	} else {
		patterns := availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate)
		m, ok = availability.MatchLimit(text, patterns, now, rt.Policy.LimitGateDefault())
		if !ok {
			return b, availability.LimitMatch{}, false, nil
		}
	}

	// A limit is recorded against the login that hit it, so another login of
	// the same provider stays usable; a binding with no account records the
	// bare group exactly as before.
	subject := availability.ProviderOf(b.BuilderCandidate)
	if b.BuilderAccount != "" && subject != "" {
		subject = account.GateKey(subject, b.BuilderAccount)
	}
	entry := availability.Entry{
		Kind:    availability.RateLimited,
		Subject: subject,
		At:      now,
		Until:   m.Until,
		Note:    m.Line,
		Source:  "relevo",
		Binding: b.Name,
	}
	reason := "rate-limited: " + m.Line
	if credit {
		// The zero Until is the whole point of the class: the gate lifts when a
		// human tops the account up and clears it. The note names that, because
		// the line itself names only a limit that ran out.
		entry.Until = time.Time{}
		entry.Note = m.Line + " -- out of credits, top up the account and clear the gate"
		reason = "out of credits: " + m.Line
		slog.Warn("provider out of credits",
			"binding", b.Name, "round", b.Round, "provider", entry.Subject,
			"until", "cleared", "line", m.Line)
	} else {
		slog.Warn("provider rate-limited",
			"binding", b.Name, "round", b.Round, "provider", entry.Subject,
			"until", m.Until, "parsed", m.Parsed, "line", m.Line)
	}
	if err := availability.AppendEntryLocked(AvailabilityDeps(rt), entry); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record rate limit gate: %v\n", err)
	}

	if _, _, ok, _ := rt.Store.StatFile(rt.Store.ReportPath(b.Name, b.Round)); ok {
		return b, m, false, nil
	}

	next, err = switchBuilder(ctx, rt, tx, b, reason, closeOld, false)
	return next, m, true, err
}

// gateOnOutage is the exit path's answer to a provider-side outage (#931): the
// builder's own tail says the PROVIDER failed, not the builder, so the exit
// earns the same timed gate a rate limit earns -- one rate_limited ledger entry
// whose note keeps the cause, then the builder switched uncounted, for the same
// reason gateOnLimit does not count: a provider that cannot serve is not a
// builder that failed, so it must not spend the round's switch budget. The
// candidate stays eligible once the provider recovers, where the until-cleared
// exclusion would keep locking it out of the round.
//
// handled is false when the tail names no provider outage. That is the whole
// point of the split: the caller's own classification of the exit -- for the
// headless path, the until-cleared #191 exclusion a genuine crash earns -- is
// left exactly as it was, which is why the gate is offered only at that
// decision and never earlier in the precedence order.
//
// Unlike gateOnLimit there is no report-on-disk branch: every call site is
// already past the report close, so a report never reaches here and the same
// observation cannot be recorded twice. A ledger write that fails is reported
// and the switch proceeds anyway, exactly as gateOnLimit does.
func gateOnOutage(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, text string) (next store.Binding, handled bool, err error) {
	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if !switchable {
		return b, false, nil
	}

	now := rt.Now()
	m, ok := availability.MatchOutage(text, now, rt.Policy.LimitGateDefault())
	if !ok {
		return b, false, nil
	}

	// The gate lands on the login that hit it, exactly as a limit does, so
	// another login of the same provider stays usable.
	subject := availability.ProviderOf(b.BuilderCandidate)
	if b.BuilderAccount != "" && subject != "" {
		subject = account.GateKey(subject, b.BuilderAccount)
	}
	entry := availability.Entry{
		Kind:    availability.RateLimited,
		Subject: subject,
		At:      now,
		Until:   m.Until,
		Note:    m.Line,
		Source:  "relevo",
		Binding: b.Name,
	}
	if err := availability.AppendEntryLocked(AvailabilityDeps(rt), entry); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record provider outage gate: %v\n", err)
	}

	slog.Warn("provider outage",
		"binding", b.Name, "round", b.Round, "provider", entry.Subject,
		"until", m.Until, "parsed", m.Parsed, "line", m.Line)

	next, err = switchBuilder(ctx, rt, tx, b, "provider outage: "+m.Line, false, false)
	return next, true, err
}
