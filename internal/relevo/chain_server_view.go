package relevo

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// remoteCode reports whether err is the server's own refusal for one code at
// one status, which is how a chain verb tells "nothing to stop" and "already
// running" from an ordinary failure.
func remoteCode(err error, status int, code remote.Code) bool {
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	return httpErr.Status == status && httpErr.Body.Code == code
}

// chainServerStatus is `relevo status <chain>` for a chain that runs on a
// server: the chain's row is the server's view, and every member row stays
// this machine's own. A server this machine cannot read is reported on the
// row's detail rather than guessed at from the mirror.
func chainServerStatus(ctx context.Context, rt Runtime, c db.ChainRow) (view.Report, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return view.Report{}, err
	}
	rep, err := buildReport(ctx, rt, bindings)
	if err != nil {
		return view.Report{}, err
	}

	row := viewChainRow(rt.Store, c)
	if v, gerr := chainGetView(ctx, rt, c); gerr != nil {
		row.Detail = fmt.Sprintf("server %s unreachable: %v", c.Server, gerr)
	} else {
		mirrored, merr := chainRowFromView(c, v, rt.Now().UTC())
		if merr != nil {
			return view.Report{}, merr
		}
		row = viewChainRow(rt.Store, mirrored)
	}

	rows := []view.BindingStatus{row}
	for _, member := range chainMembersOf(c) {
		for _, b := range rep.Bindings {
			if b.Name == member {
				rows = append(rows, b)
				break
			}
		}
	}
	rep.Bindings = rows
	return rep, nil
}

// chainServerTrace is `show <chain> --trace` for a chain that runs on a
// server: the document is the server's own trace, decoded exactly as a local
// trace is, so a trace that cannot be decoded is an error rather than a guess.
func chainServerTrace(ctx context.Context, rt Runtime, c db.ChainRow) (ChainTraceDoc, error) {
	v, err := chainGetView(ctx, rt, c)
	if err != nil {
		return ChainTraceDoc{}, err
	}
	doc := ChainTraceDoc{
		Name: c.Name, Status: chainOr(v.Status, c.Status), Phase: chainOr(v.Phase, c.Phase),
		Step: chainOr(v.Step, c.Step), Plan: chainIntOr(v.Plan, c.Plan),
		Plans: chainIntOr(v.Plans, c.Plans), Corrections: v.Corrections,
	}
	for _, r := range v.Trace {
		// A mirror whose row carries a workflow was written by the engine, so
		// its rows are workflow events; a legacy mirror keeps the fixed state
		// machine's vocabulary.
		if len(c.WorkflowJSON) > 0 {
			fev, ferr := workflow.DecodeEvent(r.Event)
			if ferr != nil {
				return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", c.Name, ferr)
			}
			act, aerr := workflow.DecodeAction(r.Action)
			if aerr != nil {
				return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", c.Name, aerr)
			}
			doc.Events = append(doc.Events, ChainTraceEvent{
				Seq: r.Seq, TS: r.TS, Step: r.Step,
				Member: r.Member, Round: r.Round, Plan: r.Plan, Reason: r.Reason,
				Flow: &fev, FlowAction: &act,
			})
			continue
		}
		ev, err := chain.DecodeEvent(r.Event)
		if err != nil {
			return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", c.Name, err)
		}
		act, err := chain.DecodeAction(r.Action)
		if err != nil {
			return ChainTraceDoc{}, fmt.Errorf("chain %s: %w", c.Name, err)
		}
		doc.Events = append(doc.Events, ChainTraceEvent{
			Seq: r.Seq, TS: r.TS, Phase: r.Phase, Step: r.Step,
			Member: r.Member, Round: r.Round, Plan: r.Plan, Event: ev, Action: act, Reason: r.Reason,
		})
	}
	return doc, nil
}

// chainServerWait is `relevo wait <chain>` for a chain that runs on a server.
// Each poll pulls the mirror unless the daemon is already doing it, then reads
// the mirror; the end is the mirror's own wait, so the one queued delivery is
// pulled exactly as a local chain's is.
func chainServerWait(ctx context.Context, rt Runtime, name string, timeout, interval time.Duration, peek bool) (WaitResult, error) {
	if timeout <= 0 {
		return WaitResult{}, fmt.Errorf("wait: timeout must be positive")
	}
	if interval <= 0 {
		return WaitResult{}, fmt.Errorf("wait: interval must be positive")
	}

	start := rt.Now()
	for {
		if !chainDaemonRunning(rt) {
			c, err := rt.Store.Chain(name)
			if errors.Is(err, store.ErrNotFound) {
				return WaitResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
			}
			if err != nil {
				return WaitResult{}, err
			}
			if err := chainPullOne(ctx, rt, c); err != nil {
				return WaitResult{}, err
			}
		}
		c, err := rt.Store.Chain(name)
		if errors.Is(err, store.ErrNotFound) {
			return WaitResult{}, fmt.Errorf("chain %s: %w", name, store.ErrNotFound)
		}
		if err != nil {
			return WaitResult{}, err
		}
		if chain.Status(c.Status) != chain.StatusRunning {
			return waitChainEnd(ctx, rt, c, peek)
		}
		if rt.Now().Sub(start) >= timeout {
			return WaitResult{Code: WaitTimeout, Done: true}, nil
		}

		select {
		case <-ctx.Done():
			return WaitResult{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// chainServerStop is `relevo stop <chain>` for a chain that runs on a server:
// the server stops it and answers what it did. A chain that is not running is
// the ordinary nothing-to-stop answer.
func chainServerStop(ctx context.Context, rt Runtime, c db.ChainRow) (StopResult, error) {
	if rt.Remote == nil {
		return StopResult{}, ErrRemoteUnavailable
	}
	resp, err := rt.Remote.ChainStop(ctx, c.Server, c.Name)
	if err != nil {
		if remoteCode(err, 409, remote.CodeNothingToStop) {
			return StopResult{}, ErrNothingToStop
		}
		return StopResult{}, err
	}
	out := StopResult{Round: resp.Round, Action: resp.Action}
	if b, lerr := rt.Store.Load(chainMemberName(c, c.AwaitingMember)); lerr == nil {
		out.Shape = b.Shape
	}
	return out, nil
}

// chainServerResume is `relevo chain --resume` for a chain that runs on a
// server: the resolved settings and an explicit gate are posted, then the
// mirror is pulled so the answer is this machine's own view of the resumed
// chain. A chain the server still runs is the running refusal.
//
// The mirror's own queued end payload is confirmed first, exactly as the local
// resume confirms it: the halt it carries says the chain needs the human, and
// that stops being true the moment they resume it -- a later wait or pull must
// not deliver the stale NEEDS YOU.
func chainServerResume(ctx context.Context, rt Runtime, c db.ChainRow, opts ResumeOptions) (ChainResult, error) {
	if rt.Remote == nil {
		return ChainResult{}, ErrRemoteUnavailable
	}
	if err := supersedeChainDelivery(rt, c); err != nil {
		return ChainResult{}, err
	}
	req := remote.ChainResumeRequest{
		MaxCorrections: opts.MaxCorrections,
		ReviewerActor:  opts.ReviewerActor,
		PlannerActor:   opts.PlannerActor,
		SecurityActor:  opts.SecurityActor,
		Security:       opts.Security,
		Regate:         opts.Regate,
	}
	if opts.Gate != "" || opts.NoGate {
		gate := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, roleChecks(rt.RoleRegistry(), "builder"))
		req.Gate = &gate
	}
	if _, err := rt.Remote.ChainResume(ctx, c.Server, c.Name, req); err != nil {
		if remoteCode(err, 409, remote.CodeChainRunning) {
			return ChainResult{}, fmt.Errorf("chain %s is running: %w", c.Name, ErrChainRunning)
		}
		if remoteCode(err, 409, remote.CodeChainDone) {
			return ChainResult{}, fmt.Errorf("chain %s is done: %w", c.Name, ErrChainDone)
		}
		if remoteCode(err, 409, remote.CodeRoundOpen) {
			return ChainResult{}, roundOpenFromWire(err)
		}
		return ChainResult{}, err
	}
	return chainResultFromMirror(ctx, rt, c.Name)
}

// roundOpenFromWire rebuilds the send path's typed round-open refusal from the
// server's 409 round_open, so the CLI prints the conflict and its `relevo stop
// <member>` next line exactly as a local refusal does. The member and round are
// read back from the refusal's own message, whose shape the send path already
// generates; anything unparseable stays the server's error.
func roundOpenFromWire(err error) error {
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) {
		return err
	}
	msg := httpErr.Body.Message
	i := strings.Index(msg, ": round ")
	j := strings.Index(msg, " is still open")
	if i < 0 || j <= i {
		return err
	}
	round, perr := strconv.Atoi(strings.TrimSpace(msg[i+len(": round ") : j]))
	if perr != nil || round <= 0 {
		return err
	}
	return &RoundOpenError{Member: strings.TrimSpace(msg[:i]), Round: round}
}

// chainServerDone is `relevo done <chain>` for a chain that runs on a server:
// the server releases the chain, then each mirror member is released through
// the ordinary done, and the mirror is closed with the chain's own done row.
func chainServerDone(ctx context.Context, rt Runtime, c db.ChainRow) (DoneResult, error) {
	if rt.Remote == nil {
		return DoneResult{}, ErrRemoteUnavailable
	}
	if _, err := chainResultFromMirror(ctx, rt, c.Name); err != nil {
		return DoneResult{}, err
	}
	if err := rt.Remote.ChainDone(ctx, c.Server, c.Name); err != nil {
		if remoteCode(err, 409, remote.CodeChainRunning) {
			return DoneResult{}, fmt.Errorf("chain %s is running; relevo stop %s first: %w", c.Name, c.Name, ErrChainRunning)
		}
		if remoteCode(err, 409, remote.CodeRoundOpen) {
			return DoneResult{}, roundOpenFromWire(err)
		}
		return DoneResult{}, err
	}

	row, err := rt.Store.Chain(c.Name)
	if err != nil {
		return DoneResult{}, err
	}
	var out DoneResult
	first := true
	for _, member := range chainMembersOf(row) {
		res, derr := Done(ctx, rt, member)
		if errors.Is(derr, store.ErrNotFound) {
			continue
		}
		if derr != nil {
			return DoneResult{}, derr
		}
		if first {
			out = res
			first = false
		}
	}

	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Chain(c.Name)
		if err != nil {
			return err
		}
		if cur.Status == string(chain.StatusDone) {
			return nil
		}
		return chainDoneRow(rt, tx, cur)
	}); err != nil {
		return DoneResult{}, err
	}
	return out, nil
}

// chainGetView reads a server chain's view, the one read every server-chain
// read verb makes.
func chainGetView(ctx context.Context, rt Runtime, c db.ChainRow) (remote.ChainView, error) {
	if rt.Remote == nil {
		return remote.ChainView{}, ErrRemoteUnavailable
	}
	return rt.Remote.GetChain(ctx, c.Server, c.Name)
}

// chainResultFromMirror pulls the mirror once and answers with it: the shape a
// resumed or finished server chain returns so its caller sees this machine's
// own record, not the server's raw view.
func chainResultFromMirror(ctx context.Context, rt Runtime, name string) (ChainResult, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return ChainResult{}, fmt.Errorf("chain %s: %w", name, err)
	}
	if err := chainPullOne(ctx, rt, c); err != nil {
		return ChainResult{}, err
	}
	row, err := rt.Store.Chain(name)
	if err != nil {
		return ChainResult{}, err
	}
	members, err := chainStoredMembersOf(rt, row)
	if err != nil {
		return ChainResult{}, err
	}
	return ChainResult{
		Chain: row, Members: members, Plans: row.Plans,
		Check: chainBuilderCheck(members, row.Builder),
	}, nil
}

// chainStoredMembersOf loads the members a row names, skipping a record that
// is gone: a released member is not an error for a read verb.
func chainStoredMembersOf(rt Runtime, c db.ChainRow) ([]store.Binding, error) {
	out := make([]store.Binding, 0, len(chainMembersOf(c)))
	for _, name := range chainMembersOf(c) {
		b, err := rt.Store.Load(name)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// chainDaemonRunning reports whether a daemon holds the store, which a wait
// reads to skip a pull the daemon is already making.
func chainDaemonRunning(rt Runtime) bool {
	running, err := rt.Store.DaemonRunning()
	return err == nil && running
}
