package delivery

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// PushEvent is one NDJSON line `relevo push` writes: the entry's identity and
// the text the mod should act on. A state line carries Seq 0 and Kind "state"
// with the transition's State and OldState instead of a log entry.
type PushEvent struct {
	Seq      int    `json:"seq"`
	Binding  string `json:"binding"`
	Round    int    `json:"round"`
	Kind     string `json:"kind"`
	State    string `json:"state,omitempty"`
	OldState string `json:"old_state,omitempty"`
	Text     string `json:"text"`
}

// pushRefreshEvery is how often a held claim's SeenAt is refreshed: a third of
// ClaimTTL, so two missed ticks still beat the expiry.
const pushRefreshEvery = 3 * time.Second

// pushPollEvery is how often RunPush rescans its bindings for a claimable entry.
const pushPollEvery = 500 * time.Millisecond

// errPushStop ends the drain cleanly: stdin reached EOF or the context was
// cancelled. It is never returned to a caller.
var errPushStop = errors.New("push: stdin closed")

// pushAdmit is one entry RunPush wrote but has not seen acked; on exit its
// admit is cleared so the entry is pending again.
type pushAdmit struct {
	binding string
	idx     int
}

// RunPush holds mastermindID's push claim and drains its mailbox to out as
// NDJSON lines, confirming each entry on the matching `ack <seq>` line from
// in. stdin EOF, ctx cancellation or an error ends it cleanly: the admits it
// wrote but never saw acked are cleared so the entries are pending again, and
// the claim is released. A second holder gets ErrClaimHeld from the claim
// write and must not drain.
func RunPush(ctx context.Context, d Deps, mastermindID string, in io.Reader, out io.Writer) error {
	return runPush(ctx, d, mastermindID, in, out, nil)
}

// runPush is RunPush with a test seam: claimSeam, when non-nil, runs between a
// claim and the write of its line, where a split scan and admit would leave the
// entry claimable by a reader. Production passes nil.
func runPush(ctx context.Context, d Deps, mastermindID string, in io.Reader, out io.Writer, claimSeam func()) error {
	if d.Store == nil {
		return errors.New("push: nil store")
	}
	if mastermindID == "" {
		return errors.New("push: empty mastermind")
	}
	if d.Channels == nil {
		return errors.New("push: no claim store configured")
	}

	now := d.Now()
	claim := Claim{MasterMind: mastermindID, PID: os.Getpid(), StartedAt: now, SeenAt: now}
	if cwd, err := os.Getwd(); err == nil {
		claim.CWD = cwd
	}
	if err := d.Channels.Write(claim, now); err != nil {
		return err
	}
	defer func() { _ = d.Channels.Remove(mastermindID, os.Getpid()) }()

	// The holder now owns the claim, so an entry an earlier dead holder
	// admitted for a no-deliverer mastermind is orphaned and must go back to
	// pending before the drain below re-sends it.
	if err := clearStaleAdmits(d, mastermindID); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go refreshPushClaim(runCtx, d, claim)

	p := &pushRun{
		d:            d,
		mastermindID: mastermindID,
		out:          out,
		lines:        pushLines(runCtx, in),
		unacked:      map[pushAdmit]struct{}{},
		last:         map[string]store.State{},
		claimSeam:    claimSeam,
	}
	defer clearUnackedAdmits(d, p.unacked)
	return p.run(runCtx)
}

// pushRun is one push holder's drain state.
type pushRun struct {
	d            Deps
	mastermindID string
	out          io.Writer
	lines        <-chan string
	unacked      map[pushAdmit]struct{}
	// last is each own binding's last-seen state, so a state line marks a
	// transition rather than every poll.
	last map[string]store.State
	// claimSeam, nil in production, runs between a claim and its write. A test
	// uses it to try a reader pull in the gap a split scan and admit would open.
	claimSeam func()
}

// run drains entries until stdin EOF or ctx is done. Each entry is admitted
// and written before the loop blocks for its ack, so an entry is never
// claimable by a reader while the mod is about to read it.
func (p *pushRun) run(ctx context.Context) error {
	ticker := time.NewTicker(pushPollEvery)
	defer ticker.Stop()

	for {
		sent, err := p.step(ctx)
		if errors.Is(err, errPushStop) {
			return nil
		}
		if err != nil {
			return err
		}
		if sent {
			continue
		}

		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-p.lines:
			if !ok {
				return nil
			}
		case <-ticker.C:
		}
	}
}

// step sends at most one claimable entry, blocking for its ack; when none is
// claimable it writes the state transitions it saw. sent reports whether an
// entry was found.
func (p *pushRun) step(ctx context.Context) (sent bool, err error) {
	b, entry, idx, found, err := nextAdmitted(p.d, p.mastermindID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, p.writeStateLines()
	}
	if p.claimSeam != nil {
		p.claimSeam()
	}
	if err := p.writeEntry(b, entry, idx); err != nil {
		return true, err
	}
	return true, p.awaitAck(ctx, b.Name, idx, entry.Seq)
}

// writeEntry writes one already-admitted entry's NDJSON line and remembers it
// as unacked. The admit happened before this write, so a crash between the two
// leaves the entry admitted rather than claimable by a reader who has not seen
// it.
func (p *pushRun) writeEntry(b store.Binding, e store.LogEntry, idx int) error {
	text, _ := PushText(e, b, p.d.Store.ReadFile)
	text = truncatePushText(text, LogRef(b, e))
	raw, err := json.Marshal(PushEvent{Seq: e.Seq, Binding: b.Name, Round: e.Round, Kind: string(e.Kind), Text: text})
	if err != nil {
		return err
	}
	if _, err := p.out.Write(append(raw, '\n')); err != nil {
		return err
	}
	p.unacked[pushAdmit{binding: b.Name, idx: idx}] = struct{}{}
	return nil
}

// writeStateLines writes one line for each own binding that just entered
// needs_you or broken, and remembers every own binding's state for the next
// step. It runs only between entries -- step reaches it when nothing is
// claimable, never while awaitAck waits -- so the loop stays the one writer. A
// binding that disappeared is dropped from memory: recreated under the same
// name, it announces its state fresh.
func (p *pushRun) writeStateLines() error {
	all, err := p.d.Store.List()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, b := range all {
		if b.MasterMindID != p.mastermindID || b.Owner != "" {
			continue
		}
		seen[b.Name] = true
		prev, wasSeen := p.last[b.Name]
		if (!wasSeen || prev != b.State) && isStateEvent(b.State) {
			if err := p.writeState(b); err != nil {
				return err
			}
		}
		p.last[b.Name] = b.State
	}
	for name := range p.last {
		if !seen[name] {
			delete(p.last, name)
		}
	}
	return nil
}

// writeState writes one state line. It carries seq 0 and kind state: it expects
// no ack, is not a log entry and confirms nothing.
func (p *pushRun) writeState(b store.Binding) error {
	raw, err := json.Marshal(PushEvent{
		Seq:      0,
		Binding:  b.Name,
		Round:    b.Round,
		Kind:     "state",
		State:    string(b.State),
		OldState: string(p.last[b.Name]),
		Text:     stateEventContent(b),
	})
	if err != nil {
		return err
	}
	_, err = p.out.Write(append(raw, '\n'))
	return err
}

// awaitAck waits for the `ack <seq>` line matching seq and confirms the entry
// with route "push". Any other line is ignored.
func (p *pushRun) awaitAck(ctx context.Context, name string, idx, seq int) error {
	for {
		select {
		case <-ctx.Done():
			return errPushStop
		case line, ok := <-p.lines:
			if !ok {
				return errPushStop
			}
			if n, match := ackSeq(line); match && n == seq {
				if err := p.d.Store.ConfirmIndex(name, idx, "push"); err != nil {
					return err
				}
				delete(p.unacked, pushAdmit{binding: name, idx: idx})
				return nil
			}
		}
	}
}

// refreshPushClaim keeps the held claim live for as long as the holder runs.
func refreshPushClaim(ctx context.Context, d Deps, claim Claim) {
	t := time.NewTicker(pushRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			claim.SeenAt = d.Now()
			_ = d.Channels.Write(claim, claim.SeenAt)
		}
	}
}

// pushLines reads in line by line into a channel; the channel closes at EOF.
func pushLines(ctx context.Context, in io.Reader) <-chan string {
	out := make(chan string)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			select {
			case out <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// nextAdmitted returns the oldest claimable entry across the mastermind's own
// bindings, in binding-name order, with the entry already admitted: the scan
// and the admit run in one lock, so no reader can confirm the entry between
// finding it and hiding it from the claimable scans.
func nextAdmitted(d Deps, mastermindID string) (store.Binding, store.LogEntry, int, bool, error) {
	all, err := d.Store.List()
	if err != nil {
		return store.Binding{}, store.LogEntry{}, 0, false, err
	}
	var mine []store.Binding
	for _, b := range all {
		if b.MasterMindID == mastermindID && b.Owner == "" {
			mine = append(mine, b)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })

	for _, b := range mine {
		entry, idx, found, err := claimAndAdmitOne(d, b.Name)
		if err != nil {
			return store.Binding{}, store.LogEntry{}, 0, false, err
		}
		if found {
			return b, entry, idx, true, nil
		}
	}
	return store.Binding{}, store.LogEntry{}, 0, false, nil
}

// claimAndAdmitOne reads one binding's oldest claimable entry and admits it
// under one lock: the read and the admit are a single transaction, so a reader
// can never claim and confirm the entry between them.
func claimAndAdmitOne(d Deps, name string) (store.LogEntry, int, bool, error) {
	var (
		entry store.LogEntry
		idx   int
		found bool
	)
	err := d.Store.WithLock(func(tx *store.Tx) error {
		var err error
		entry, idx, found, err = tx.ClaimableForMasterMind(name)
		if err != nil || !found {
			return err
		}
		return tx.AdmitIndex(name, idx)
	})
	return entry, idx, found, err
}

// clearStaleAdmits clears every admitted entry of a no-deliverer mastermind:
// the holder now owns the claim, so a stamp a dead holder left is orphaned and
// the entry must be pending for the drain.
func clearStaleAdmits(d Deps, mastermindID string) error {
	all, err := d.Store.List()
	if err != nil {
		return err
	}
	for _, b := range all {
		if b.MasterMindID != mastermindID || b.Owner != "" {
			continue
		}
		if kind := b.MasterMind.Kind; kind != "" {
			if _, ok := d.Deliverers[kind]; ok {
				continue
			}
		}
		if err := clearAdmitsFor(d, b.Name); err != nil {
			return err
		}
	}
	return nil
}

// clearAdmitsFor clears every admitted, unconfirmed mastermind-bound entry of
// one binding.
func clearAdmitsFor(d Deps, name string) error {
	entries, err := d.Store.ReadLog(name)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if e.Direction == store.DirToMasterMind && !e.Confirmed && e.AdmittedAt != nil {
			if err := d.Store.ClearAdmitIndex(name, i); err != nil {
				return err
			}
		}
	}
	return nil
}

// clearUnackedAdmits clears the admits this holder wrote but never saw acked.
func clearUnackedAdmits(d Deps, unacked map[pushAdmit]struct{}) {
	for a := range unacked {
		_ = d.Store.ClearAdmitIndex(a.binding, a.idx)
	}
}

// ackSeq parses an `ack <seq>` stdin line.
func ackSeq(line string) (int, bool) {
	f := strings.Fields(line)
	if len(f) != 2 || f[0] != "ack" {
		return 0, false
	}
	n, err := strconv.Atoi(f[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// clearOrphanAdmit clears an admitted entry no route can settle: the
// mastermind's kind has no deliverer and its push claim is not live, so the
// entry is orphaned and goes back to pending for `relevo wait` or a new
// holder. While the claim IS live the holder owns the entry and it is left.
func clearOrphanAdmit(d Deps, tx *store.Tx, b store.Binding, pending store.LogEntry, idx int) error {
	if pending.AdmittedAt == nil {
		return nil
	}
	if kind := b.MasterMind.Kind; kind != "" {
		if _, ok := d.Deliverers[kind]; ok {
			return nil
		}
	}
	if d.Channels != nil && b.MasterMindID != "" {
		if c, err := d.Channels.Live(b.MasterMindID, d.Now()); err == nil && c != nil {
			return nil
		}
	}
	return tx.ClearAdmitIndex(b.Name, idx)
}
