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

// PushEvent is one NDJSON line `relevo push` writes; a state line carries Seq 0
// and Kind "state" instead of a log entry.
type PushEvent struct {
	Seq      int    `json:"seq"`
	Binding  string `json:"binding"`
	Round    int    `json:"round"`
	Kind     string `json:"kind"`
	State    string `json:"state,omitempty"`
	OldState string `json:"old_state,omitempty"`
	Text     string `json:"text"`
}

// pushRefreshEvery refreshes a held claim's SeenAt: a third of ClaimTTL, so two
// missed ticks still beat expiry.
const pushRefreshEvery = 3 * time.Second

// pushPollEvery is how often RunPush rescans for a claimable entry.
const pushPollEvery = 500 * time.Millisecond

// pushConfirmPollEvery is how often awaitConfirm re-reads its entry; the
// entry's Confirmed flag is the whole signal, no channel needed.
const pushConfirmPollEvery = 100 * time.Millisecond

// errPushStop ends the drain on cancel or a lost claim; never returned to a caller.
var errPushStop = errors.New("push: stopped")

// pushAdmit is one written-but-unacked entry; cleared on exit so it pends again.
type pushAdmit struct {
	binding string
	idx     int
}

// RunPush holds mastermindID's claim and drains its mailbox as NDJSON, waiting
// for each entry's `relevo push --ack`. It reads no stdin: the ack is a
// separate process. A second holder gets ErrClaimHeld and must not drain.
func RunPush(ctx context.Context, d Deps, mastermindID string, out io.Writer) error {
	return runPush(ctx, d, mastermindID, out, nil)
}

// runPush is RunPush with a test seam; production passes nil.
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

	// The holder owns the claim now, so a dead holder's orphaned admit goes back
	// to pending before the drain re-sends it.
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
	// This holder's admits clear on the way out -- unless it lost the claim: a
	// successor may already be delivering the same entry, and clearing here
	// would make it claimable mid-delivery.
	defer func() {
		if !p.claimLost {
			clearUnackedAdmits(d, p.unacked)
		}
	}()
	return p.run(runCtx)
}

// pushRun is one holder's drain state.
type pushRun struct {
	d            Deps
	mastermindID string
	out          io.Writer
	unacked      map[pushAdmit]struct{}
	// last is each own binding's last-seen state, so a line marks a transition.
	last map[string]store.State
	// claimSeam, nil in production, runs between a claim and its write.
	claimSeam func()
	// claimLost records a claim-loss exit, the one exit that keeps its admits.
	claimLost bool
}

// run drains entries; each is admitted and written before blocking for its
// confirm, so no reader claims it mid-write.
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

// step sends at most one claimable entry, blocking for its confirm, else
// writes the state transitions it saw.
func (p *pushRun) step(ctx context.Context) (sent bool, err error) {
	mine, err := ownBindings(p.d, p.mastermindID)
	if err != nil {
		return false, err
	}
	b, entry, idx, found, err := nextAdmitted(p.d, mine)
	if err != nil {
		return false, err
	}
	if !found {
		return false, p.writeStateLines(mine)
	}
	if p.claimSeam != nil {
		p.claimSeam()
	}
	if err := p.writeEntry(b, entry, idx); err != nil {
		return true, err
	}
	return true, p.awaitConfirm(ctx, b.Name, idx)
}

// writeEntry writes one admitted entry's line and remembers it as unacked; the
// admit precedes the write, so a crash between them leaves it admitted, never
// claimable unseen.
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

// writeStateLines writes one line per binding that just entered needs_you or
// broken, between entries only, so the loop stays the one writer. A vanished
// binding is forgotten, so a same-named recreation announces fresh.
func (p *pushRun) writeStateLines(mine []store.Binding) error {
	seen := make(map[string]bool)
	for _, b := range mine {
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

// writeState writes one state line: seq 0, kind state, no ack, confirms nothing.
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

// awaitConfirm polls this holder's written entry until the ack verb confirms it,
// its admit clears, or the run stops; it never confirms itself. A cleared admit
// drops from unacked, so the next step re-admits and re-sends it fresh.
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

// claimHeld reports whether this process still holds the claim; a Live error
// counts as held, so a transient store failure never ends a live drain.
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

// refreshPushClaim keeps the held claim live while the holder runs.
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

// ownBindings returns the mastermind's own bindings in name order, reading only
// that mastermind's records: holders poll twice a second, so a full listing
// would be the daemon's largest idle load.
func ownBindings(d Deps, mastermindID string) ([]store.Binding, error) {
	listed, err := d.Store.ListForMasterMind(mastermindID)
	if err != nil {
		return nil, err
	}
	var mine []store.Binding
	for _, b := range listed {
		if b.MasterMindID == mastermindID && b.Owner == "" {
			mine = append(mine, b)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].Name < mine[j].Name })
	return mine, nil
}

// nextAdmitted returns the oldest claimable entry across mine, already
// admitted: scan and admit run in one lock, so no reader slips between them.
func nextAdmitted(d Deps, mine []store.Binding) (store.Binding, store.LogEntry, int, bool, error) {
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

// claimAndAdmitOne admits one binding's oldest claimable entry under one lock,
// so no reader claims it between read and admit.
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

// clearStaleAdmits clears a no-deliverer mastermind's admitted entries: the new
// holder owns the claim, so a dead holder's stamps are orphaned.
func clearStaleAdmits(d Deps, mastermindID string) error {
	mine, err := ownBindings(d, mastermindID)
	if err != nil {
		return err
	}
	for _, b := range mine {
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

// clearAdmitsFor clears one binding's admitted, unconfirmed mastermind-bound entries.
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

// clearUnackedAdmits clears this holder's written-but-unacked admits.
func clearUnackedAdmits(d Deps, unacked map[pushAdmit]struct{}) {
	for a := range unacked {
		_ = d.Store.ClearAdmitIndex(a.binding, a.idx)
	}
}

// clearOrphanAdmit clears an admitted entry no route can settle -- no deliverer
// and no live claim -- back to pending. While the claim is live the holder
// owns the entry and it is left.
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
