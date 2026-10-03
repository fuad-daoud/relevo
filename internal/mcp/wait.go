package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

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

func formatWaitResult(st *store.Store, name string, round int, res relevo.WaitResult) string {
	firstLine := fmt.Sprintf("%s round %d %s", name, round, waitOutcomeWord(res.Code))

	payload := res.Payload
	if res.Code == relevo.WaitTimeout {
		payload = "round still open, call wait again"
	} else if payload == "" && res.Line != "" && res.Line != "-" && (res.Code == relevo.WaitNeedsYou || res.Code == relevo.WaitNotStarted) {
		payload = res.Line
	}

	if len(payload) > maxWaitOutputBytes {
		hint := fmt.Sprintf("relevo show %s --round %d --report", name, round)
		path := res.Line
		if (path == "" || path == "-") && st != nil {
			path = st.ReportPath(name, round)
		}
		if path != "" && path != "-" {
			return fmt.Sprintf("%s\n%s\n%s", firstLine, path, hint)
		}
		return fmt.Sprintf("%s\n%s", firstLine, hint)
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

// Wait blocks until a round closes, needs attention, or times out.
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
