package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// maxWaitOutputBytes caps payload text in wait tool results.
// Derived from 25k tokens at ~4 chars/token (pinned at 96 KiB).
const maxWaitOutputBytes = 96 * 1024

// maxWaitTimeout is the upper bound on wait tool timeout (under ~28h MCP tool timeout).
const maxWaitTimeout = 27 * time.Hour

// maxWaitTimeoutNoProgress is the upper bound on wait timeout without progress token.
const maxWaitTimeoutNoProgress = 25 * time.Minute

// WaitArgs is the wait tool's input: {name?, round?, timeout?}.
type WaitArgs struct {
	Name             string `json:"name,omitempty"`
	Round            int    `json:"round,omitempty"`
	Timeout          string `json:"timeout,omitempty"`
	HasProgressToken bool   `json:"-"`
}

func validateWaitArgs(a WaitArgs) error {
	if a.Round < 0 {
		return errors.New("round must be non-negative")
	}
	if a.Timeout != "" {
		d, err := time.ParseDuration(a.Timeout)
		if err != nil {
			return err
		}
		if d <= 0 {
			return errors.New("timeout must be positive")
		}
	}
	return nil
}

func resolveWaitTimeout(a WaitArgs, st *store.Store) (time.Duration, error) {
	var timeout time.Duration
	if a.Timeout != "" {
		d, err := time.ParseDuration(a.Timeout)
		if err != nil {
			return 0, err
		}
		timeout = d
	} else {
		timeout = 24 * time.Hour
		if a.Name != "" && st != nil {
			if b, err := st.Load(a.Name); err == nil && b.RoundTimeoutMS > 0 {
				timeout = time.Duration(b.RoundTimeoutMS) * time.Millisecond
			}
		}
	}

	if timeout > maxWaitTimeout {
		timeout = maxWaitTimeout
	}
	if !a.HasProgressToken && timeout > maxWaitTimeoutNoProgress {
		timeout = maxWaitTimeoutNoProgress
	}
	return timeout, nil
}

func waitOutcomeWord(code int) string {
	switch code {
	case relevo.WaitClosed:
		return "closed"
	case relevo.WaitUnmarked:
		return "unmarked"
	case relevo.WaitNeedsYou:
		return "needs-you"
	case relevo.WaitGone:
		return "gone"
	case relevo.WaitHalted:
		return "halted"
	case relevo.WaitNotStarted:
		return "not-started"
	case relevo.WaitTimeout:
		return "still-open"
	default:
		return fmt.Sprintf("code-%d", code)
	}
}

// peekRef is the non-claiming command that prints e whole: delivery's own
// `relevo show` form with --peek appended. The peek is what makes the pointer
// safe here -- a plain `show` claims the oldest pending payload of its round,
// so pointing an already-confirmed entry at a claiming command would hand the
// reader the NEXT pending payload of that round as a side effect of reading one
// it already has.
//
// A kind delivery names no section for (a halt entry, whose payload is the
// notification itself and carries its own `relevo status` pointer) is pointed
// at the round's log, which carries the entry and claims nothing.
func peekRef(b store.Binding, e store.LogEntry) string {
	if ref := delivery.LogRef(b, e); ref != "" {
		return ref + " --peek"
	}
	return fmt.Sprintf("relevo show %s --round %d --log --peek", b.Name, e.Round)
}

// oversizeWaitBody renders the over-cap result: one item per entry the delivery
// confirmed, in confirmation order so the waited round is last. An entry whose
// text still fits the budget left is delivered whole; an entry that does not is
// cut to that budget, or -- when too little of it is left for a useful fragment
// -- reduced to its peek pointer. Either way every confirmed entry is
// represented, which is the whole point: confirming every entry and printing a
// path for them all drops the content of each one the caller never sees.
//
// A halt entry is the exception: its text is the reason a human is needed, it is
// short, and a report that eats the budget first would otherwise reduce it to a
// pointer at the exact moment it matters most. So a halt's text is written whole
// whatever budget is left, and it does not spend budget the report still needs.
func oversizeWaitBody(st *store.Store, name string, round int, res relevo.WaitResult) string {
	binding := delivery.BindingFor(st, name)

	delivered := res.Delivered
	if len(delivered) == 0 {
		// Payload only ever comes from a through-pull, which always records per
		// entry, so this cannot happen; and a bare path is what this branch
		// exists to stop. Cut the text rather than name a file.
		delivered = []delivery.Delivered{{
			Entry: store.LogEntry{Round: round, Kind: store.KindReport},
			Text:  res.Payload,
		}}
	}
	waited := delivered[len(delivered)-1].Entry.Round

	budget := maxWaitOutputBytes
	var b strings.Builder
	for i, d := range delivered {
		last := i == len(delivered)-1
		ref := peekRef(binding, d.Entry)
		header := ""
		if len(delivered) > 1 && !last {
			header = delivery.EntryHeader(d.Entry, waited)
		}
		switch text := d.Text; {
		case d.Entry.Kind == store.KindHalt:
			b.WriteString(header)
			b.WriteString(text)
		case len(text) <= budget:
			b.WriteString(header)
			b.WriteString(text)
			budget -= len(text)
		case budget > len(ref)+1:
			b.WriteString(delivery.TruncateTo(text, budget, fmt.Sprintf("%d bytes", budget), ref))
			budget = 0
		default:
			// Too little of the budget left for a useful fragment: the pointer
			// is the whole delivery.
			b.WriteString(ref)
		}
		if !last {
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

func formatWaitResult(st *store.Store, name string, round int, res relevo.WaitResult) string {
	firstLine := fmt.Sprintf("%s round %d %s", name, round, waitOutcomeWord(res.Code))

	payload := res.Payload
	if res.Code == relevo.WaitTimeout {
		payload = "round still open, call wait again"
	} else if payload == "" && res.Line != "" && res.Line != "-" && (res.Code == relevo.WaitNeedsYou || res.Code == relevo.WaitNotStarted) {
		payload = res.Line
	}

	if len(payload) > maxWaitOutputBytes {
		return firstLine + "\n" + oversizeWaitBody(st, name, round, res)
	}

	body := firstLine
	if payload != "" {
		body += "\n" + payload
	}
	if res.DeliverErr != nil {
		body += "\ndelivery error: " + res.DeliverErr.Error()
	}
	return body
}

// waitRoundLine names the round a wait reports on: the one the caller asked
// for, the one Wait resolved, or else the newest planned one on the binding.
func (v *RelevoVerbs) waitRoundLine(name string, asked, resolved int) int {
	if resolved != 0 {
		return resolved
	}
	if asked != 0 {
		return asked
	}
	if name == "" || v.RT.Store == nil {
		return 0
	}
	b, err := v.RT.Store.Load(name)
	if err != nil {
		return 0
	}
	entries, _ := v.RT.Store.ReadLog(name)
	return relevo.DefaultWaitRound(b, entries)
}

// waitOnTarget runs the blocking wait: the named binding, or every active
// binding this MasterMind owns when no name came in.
func (v *RelevoVerbs) waitOnTarget(ctx context.Context, session string, a WaitArgs, timeout, interval time.Duration) (string, relevo.WaitResult, error) {
	if a.Name != "" {
		return relevo.Wait(ctx, v.RT, relevo.WaitOptions{
			Names:    []string{a.Name},
			Round:    a.Round,
			Timeout:  timeout,
			Interval: interval,
		})
	}
	id, err := v.masterMindFor(session)
	if err != nil {
		return "", relevo.WaitResult{}, err
	}
	return relevo.WaitOwned(ctx, v.RT, id, a.Round, timeout, interval)
}

// Wait blocks until a round closes, needs attention, or times out. While this
// MasterMind's push claim is live the holder is writing the report into the
// session, so the wait delivers nothing: it reports the outcome line plus
// "delivered by the mod" and claims or confirms no entry.
func (v *RelevoVerbs) Wait(ctx context.Context, session string, a WaitArgs) (any, error) {
	if err := validateWaitArgs(a); err != nil {
		return nil, err
	}

	if a.Name != "" && v.RT.Store != nil {
		if _, err := v.RT.Store.Chain(a.Name); err == nil {
			return nil, fmt.Errorf("chain %s: wait on chains is CLI-only; run relevo wait --name %s", a.Name, a.Name)
		}
	}

	timeout, err := resolveWaitTimeout(a, v.RT.Store)
	if err != nil {
		return nil, err
	}

	interval := v.WaitInterval
	if interval <= 0 {
		interval = time.Second
	}

	if v.pushClaimLive(session, a.Name) {
		return v.waitDeliveredByMod(ctx, session, a, timeout, interval)
	}

	name, res, err := v.waitOnTarget(ctx, session, a, timeout, interval)
	if errors.Is(err, context.Canceled) {
		target := a.Name
		if target == "" {
			target, _ = v.masterMindFor(session)
		}
		return fmt.Sprintf("%s round %d cancelled", target, v.waitRoundLine(target, a.Round, 0)), nil
	}
	if err != nil {
		return nil, err
	}

	target := name
	if target == "" {
		target = a.Name
	}
	return formatWaitResult(v.RT.Store, target, v.waitRoundLine(target, a.Round, res.Round), res), nil
}

// pushClaimLive reports whether a live push claim covers the wait's target: the
// named binding's mastermind, or this call's own mastermind.
func (v *RelevoVerbs) pushClaimLive(session, name string) bool {
	if v.RT.Channels == nil {
		return false
	}
	id := ""
	if name != "" {
		if v.RT.Store != nil {
			if b, err := v.RT.Store.Load(name); err == nil {
				id = b.MasterMindID
			}
		}
	} else {
		id, _ = v.masterMindFor(session)
	}
	if id == "" {
		return false
	}
	now := time.Now()
	if v.RT.Now != nil {
		now = v.RT.Now()
	}
	c, err := v.RT.Channels.Live(id, now)
	return err == nil && c != nil
}

// waitDeliveredByMod is the wait under a live push claim: it polls through
// relevo.Wait with Peek, so it claims and confirms nothing, and appends the
// note that tells the model the report is already in its transcript.
func (v *RelevoVerbs) waitDeliveredByMod(ctx context.Context, session string, a WaitArgs, timeout, interval time.Duration) (any, error) {
	opts := relevo.WaitOptions{Round: a.Round, Timeout: timeout, Interval: interval, Peek: true}
	if a.Name != "" {
		opts.Names = []string{a.Name}
	} else {
		id, err := v.masterMindFor(session)
		if err != nil {
			return nil, err
		}
		names, err := v.ownedNames(id)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			return nil, fmt.Errorf("no active bindings for mastermind %s", id)
		}
		opts.Names = names
	}

	name, res, err := relevo.Wait(ctx, v.RT, opts)
	if errors.Is(err, context.Canceled) {
		target := a.Name
		if target == "" {
			target, _ = v.masterMindFor(session)
		}
		return fmt.Sprintf("%s round %d cancelled", target, v.waitRoundLine(target, a.Round, 0)), nil
	}
	if err != nil {
		return nil, err
	}

	target := name
	if target == "" {
		target = a.Name
	}
	out := formatWaitResult(v.RT.Store, target, v.waitRoundLine(target, a.Round, res.Round), res)
	if res.Code == relevo.WaitTimeout || res.Code == relevo.WaitGone {
		return out, nil
	}
	return out + "\ndelivered by the mod", nil
}

// ownedNames lists a mastermind's active bindings, the same set WaitOwned
// polls when no name came in.
func (v *RelevoVerbs) ownedNames(masterMindID string) ([]string, error) {
	if v.RT.Store == nil {
		return nil, errors.New("wait: store is required")
	}
	all, err := v.RT.Store.List()
	if err != nil {
		return nil, err
	}
	var names []string
	for _, b := range all {
		if b.MasterMindID == masterMindID && b.State != store.StateDone {
			names = append(names, b.Name)
		}
	}
	return names, nil
}
