package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
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

// pushConfirmPollEvery is how often awaitConfirm re-reads the entry it wrote.
// Only the one-shot ack verb can confirm an admitted entry while this claim is
// live, so the entry's own Confirmed flag is the whole signal -- no message
// channel is needed.
const pushConfirmPollEvery = 100 * time.Millisecond

// errPushStop ends the drain cleanly: the context was cancelled or the claim
// was lost to another holder. It is never returned to a caller.
var errPushStop = errors.New("push: stopped")

// pushAdmit is one entry RunPush wrote but has not seen acked; on exit its
// admit is cleared so the entry is pending again.
type pushAdmit struct {
	binding string
	idx     int
}

// RunPush holds mastermindID's push claim and drains its mailbox to out as
// NDJSON lines, waiting for each entry to be confirmed by `relevo push --ack`.
// It reads no stdin: the ack is a separate process, so the mod never has to
// drive a pipe. ctx cancellation, losing the claim or an error ends it
// cleanly, and the claim is released. A second holder gets ErrClaimHeld from
// the claim write and must not drain.
func RunPush(ctx context.Context, d Deps, mastermindID string, out io.Writer) error {
	return runPush(ctx, d, mastermindID, out, nil)
}

// runPush is RunPush with a test seam: claimSeam, when non-nil, runs between a
// claim and the write of its line, where a split scan and admit would leave the
// entry claimable by a reader. Production passes nil.
func runPush(ctx context.Context, d Deps, mastermindID string, out io.Writer, claimSeam func()) error {
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
		unacked:      map[pushAdmit]struct{}{},
		last:         map[string]store.State{},
		claimSeam:    claimSeam,
	}
	// The admits this holder wrote are cleared on the way out -- except when it
	// stops because it lost the claim. Then a successor may already have run
	// clearStaleAdmits and be writing a line for the very same entry, and
	// clearing it here would make that entry claimable while the successor is
	// delivering it. The successor's own clear, or clearOrphanAdmit once no
	// claim is live, returns it to pending instead.
	defer func() {
		if !p.claimLost {
			clearUnackedAdmits(d, p.unacked)
		}
	}()
	return p.run(runCtx)
}

// pushRun is one push holder's drain state.
type pushRun struct {
	d            Deps
	mastermindID string
	out          io.Writer
	unacked      map[pushAdmit]struct{}
	// last is each own binding's last-seen state, so a state line marks a
	// transition rather than every poll.
	last map[string]store.State
	// claimSeam, nil in production, runs between a claim and its write. A test
	// uses it to try a reader pull in the gap a split scan and admit would open.
	claimSeam func()
	// claimLost records that the run ended on losing the claim rather than on
	// ctx or an error, which is the one exit that must not clear its own admits.
	claimLost bool
}

// run drains entries until ctx is done or the claim is lost. Each entry is
// admitted and written before the loop blocks for its confirm, so an entry is
// never claimable by a reader while the mod is about to read it.
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
		case <-ticker.C:
			if !p.claimHeld() {
				p.claimLost = true
				return nil
			}
		}
	}
}

// step sends at most one claimable entry, blocking for its confirm; when none is
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
	return true, p.awaitConfirm(ctx, b.Name, idx)
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

// awaitConfirm polls the entry this holder wrote until it is confirmed by the
// ack verb, its admit is cleared by someone else, or the run stops. It never
// confirms: the entry is the ack verb's to settle, and the holder only waits
// for that answer to arrive.
//
// The poll ends on AdmittedAt == nil as well as Confirmed, because an admit
// cleared under the holder (the orphan clear, another writer) makes the entry
// claimable again and every further ack for it would be refused as not
// admitted. Dropping it from unacked lets the next step re-admit and re-send it
// with a fresh line.
func (p *pushRun) awaitConfirm(ctx context.Context, name string, idx int) error {
	ticker := time.NewTicker(pushConfirmPollEvery)
	defer ticker.Stop()

	for {
		entries, err := p.d.Store.ReadLog(name)
		if err != nil {
			return err
		}
		if idx < len(entries) {
			if entries[idx].Confirmed || entries[idx].AdmittedAt == nil {
				delete(p.unacked, pushAdmit{binding: name, idx: idx})
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return errPushStop
		case <-ticker.C:
			if !p.claimHeld() {
				p.claimLost = true
				return errPushStop
			}
		}
	}
}

// claimHeld reports whether this process still holds the push claim. A Live
// error is treated as held: a transient store failure must not end a live
// holder's drain, and the claim's own TTL is what decides a real loss.
func (p *pushRun) claimHeld() bool {
	if p.d.Channels == nil {
		return false
	}
	var now time.Time
	if p.d.Now != nil {
		now = p.d.Now()
	}
	c, err := p.d.Channels.Live(p.mastermindID, now)
	return err != nil || (c != nil && c.PID == os.Getpid())
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
