package delivery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// Pusher is the channel's outbound side; internal/mcp implements it by
// writing a notifications/claude/channel line to its stdio transport.
type Pusher interface {
	Push(ctx context.Context, content string, meta map[string]string) error
}

// DrainState is one relevo mcp poll loop's memory across polls: which mastermind
// it drains, and each of that mastermind's bindings' last-seen State, so Drain
// knows when a state event is a transition rather than a repeat.
type DrainState struct {
	MasterMind string
	Last       map[string]store.State // binding name -> last-seen state; nil until first poll
}

// DrainResult is what one Drain call did.
type DrainResult struct {
	Pushed int      // payload events pushed and confirmed
	States int      // state events pushed
	Failed []string // binding names whose push returned an error (left pending)
}

// Drain runs one poll over one mastermind's bindings: for each binding whose
// mastermind is st.MasterMind and which is not remote-owned, it pushes the oldest
// pending mastermind payload (confirming only after the push succeeds), then
// pushes a state event on selected state transitions.
func Drain(ctx context.Context, d Deps, st *DrainState, p Pusher) (DrainResult, error) {
	if st.MasterMind == "" {
		return DrainResult{}, fmt.Errorf("drain: empty mastermind")
	}
	if d.Store == nil {
		return DrainResult{}, fmt.Errorf("drain: nil store")
	}

	bindings, err := d.Store.List()
	if err != nil {
		return DrainResult{}, err
	}

	var mine []store.Binding
	for _, b := range bindings {
		if b.MasterMindID == st.MasterMind && b.Owner == "" {
			mine = append(mine, b)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })

	if st.Last == nil {
		st.Last = map[string]store.State{}
	}

	var res DrainResult
	seen := make(map[string]bool, len(mine))
	for _, b := range mine {
		seen[b.Name] = true
		if err := drainOne(ctx, d, p, st, b, &res); err != nil {
			return res, err
		}
	}

	// Bindings that disappeared from the store are dropped from memory: a
	// deleted-and-recreated binding of the same name announces its state
	// fresh, the same as a restarted relevo mcp would.
	for name := range st.Last {
		if !seen[name] {
			delete(st.Last, name)
		}
	}

	return res, nil
}

// drainOne pushes one binding's pending payload and its state transition. A
// refused push is recorded in res; a store error is returned.
func drainOne(ctx context.Context, d Deps, p Pusher, st *DrainState, b store.Binding, res *DrainResult) error {
	entry, idx, found, err := pendingFor(d, b.Name)
	if err != nil {
		return err
	}
	if found {
		if err := pushPayload(ctx, d, p, b, entry, idx, res); err != nil {
			return err
		}
	}
	pushState(ctx, p, st, b, res)
	return nil
}

// pendingFor reads one binding's oldest claimable mastermind entry under the
// state lock. It skips an entry a push route already admitted, so the channel
// never pushes a payload another route holds.
func pendingFor(d Deps, name string) (store.LogEntry, int, bool, error) {
	var (
		entry store.LogEntry
		idx   int
		found bool
	)
	err := d.Store.WithLock(func(tx *store.Tx) error {
		var err error
		entry, idx, found, err = tx.ClaimableForMasterMind(name)
		return err
	})
	return entry, idx, found, err
}

// pushPayload pushes one entry and confirms it only once the push succeeded.
// A refused push names the binding in res.Failed and leaves the entry pending.
func pushPayload(ctx context.Context, d Deps, p Pusher, b store.Binding, entry store.LogEntry, idx int, res *DrainResult) error {
	meta := map[string]string{
		"binding": b.Name,
		"round":   strconv.Itoa(entry.Round),
		"kind":    string(entry.Kind),
		"seq":     strconv.Itoa(entry.Seq),
	}
	if show := LogRef(b, entry); show != "" {
		meta["show"] = show
	}

	content, _ := PushText(entry, b, d.Store.ReadFile)
	if err := p.Push(ctx, content, meta); err != nil {
		res.Failed = append(res.Failed, b.Name)
		slog.Info("channel push failed; entry stays pending", "binding", b.Name, "round", entry.Round, "error", err)
		return nil
	}
	if err := d.Store.WithLock(func(tx *store.Tx) error {
		return tx.ConfirmIndex(b.Name, idx, "channel")
	}); err != nil {
		return err
	}
	res.Pushed++
	return nil
}

// pushState pushes a state event when b's state is a transition into a state
// worth announcing, and records the state as last-seen either way.
func pushState(ctx context.Context, p Pusher, st *DrainState, b store.Binding, res *DrainResult) {
	prev, wasSeen := st.Last[b.Name]
	if (!wasSeen || prev != b.State) && isChannelState(b.State) {
		oldState := ""
		if wasSeen {
			oldState = string(prev)
		}
		meta := map[string]string{
			"binding":   b.Name,
			"round":     strconv.Itoa(b.Round),
			"kind":      "state",
			"state":     string(b.State),
			"old_state": oldState,
		}
		if err := p.Push(ctx, stateEventContent(b), meta); err != nil {
			slog.Info("channel state push failed", "binding", b.Name, "state", b.State, "error", err)
		} else {
			res.States++
		}
	}
	st.Last[b.Name] = b.State
}

// isChannelState reports whether s is one of the states worth a push:
// needs_you and broken.
func isChannelState(s store.State) bool {
	switch s {
	case store.StateNeedsYou, store.StateBroken:
		return true
	default:
		return false
	}
}

// stateEventContent is the human-readable body of a state event: the state in
// the reader's words, plus the binding's Halt reason when it has one, plus the
// next steps.
func stateEventContent(b store.Binding) string {
	var reason string
	if b.Halt != "" {
		reason = " -- " + b.Halt
	}
	return fmt.Sprintf("%s round %d: %s%s\nrun relevo status --name %s, then send or stop.",
		b.Name, b.Round, channelStateLabel(b.State), reason, b.Name)
}

func channelStateLabel(s store.State) string {
	switch s {
	case store.StateNeedsYou:
		return "NEEDS YOU"
	case store.StateBroken:
		return "BROKEN"
	default:
		return strings.ToUpper(string(s))
	}
}
