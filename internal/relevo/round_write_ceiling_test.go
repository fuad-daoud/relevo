package relevo

// Per-round synced-write ceiling: a running round rewrites the binding's
// stream cursor, its progress clocks and a served poll's live copy on every
// tick, and the ingest mirror reads the binding again on every save. None of
// those is a synced fact, so none may queue a binding_record or mastermind
// outbox row; only transitions may. The counts below are enumerations, not
// guesses: each row names the transition that writes it, and any row beyond
// them is the per-tick rewrite the machine-local overlay exists to stop.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	// The round's binding_record rows, counted from the window's truncate:
	// start (the save that opens round 1), close (the save that files the
	// report entry), deliver (the save that settles the binding done).
	// A tick moves only overlay fields and writes no shared row at all.
	ceilingRoundBindingRecords = 3
	// The mastermind rows the round may queue: the ingest mirror carries the
	// stored last_seen on a mirror run, so its upsert changes nothing and
	// fires no sync trigger.
	ceilingRoundMastermindRows = 0

	ceilingTicks         = 20 // ticks the loop drives per round
	ceilingStreamLines   = 20 // lines appended per tick when streaming (400 in all)
	ceilingQuietLines    = 0  // the control round streams nothing
	ceilingBinding       = "webshop"
	ceilingMasterMindID  = "pl_ceiling"
	ceilingMasterSession = "sess-ceiling"

	// A claude stream line that names no session: the drain advances the
	// cursor past it without recording a session id, which is a synced field.
	ceilingStreamLine = `{"type":"assistant","message":{"content":[{"type":"text","text":"ceiling tick line"}]}}` + "\n"
)

// ceilingWorld is one round's counted world: the store, its machine database,
// and the store revision the mirror runs on, exactly as the daemon's tick
// gates it. seenBR/seenMM are the counted rows so far, so every assertion
// below is about the rows a moment of the round newly queued.
type ceilingWorld struct {
	t      *testing.T
	ctx    context.Context
	rt     Runtime
	s      *store.Store
	d      *db.DB
	rev    string
	seenBR int
	seenMM int
}

// TestRunningRoundQueuesNoSyncedWritePerTick is the deterministic core of the
// write ceiling: one whole round -- open, a streaming tick loop, close,
// deliver -- driven through the real drain and progress helpers and the real
// ingest mirror. Every tick must queue zero binding_record and mastermind
// rows, the round must queue only its transition rows, and the same round
// streamed with hundreds of lines across the same ticks must queue exactly
// what the quiet round queues.
func TestRunningRoundQueuesNoSyncedWritePerTick(t *testing.T) {
	quiet := runRoundWriteCeiling(t, ceilingQuietLines)
	streamed := runRoundWriteCeiling(t, ceilingStreamLines)

	if streamed.bindingRecords != quiet.bindingRecords || streamed.mastermindRows != quiet.mastermindRows {
		t.Errorf("round streaming %d lines across %d ticks queued %d binding_record and %d mastermind rows, "+
			"same round streaming %d lines queued %d and %d: stream volume must not be a variable in the synced-write count",
			ceilingStreamLines*ceilingTicks, ceilingTicks, streamed.bindingRecords, streamed.mastermindRows,
			ceilingQuietLines, quiet.bindingRecords, quiet.mastermindRows)
	}
	if quiet.bindingRecords != ceilingRoundBindingRecords {
		t.Errorf("the round queued %d binding_record rows, want the pinned %d (start, close, deliver): "+
			"any row beyond the transitions is a per-tick shared-row rewrite",
			quiet.bindingRecords, ceilingRoundBindingRecords)
	}
	if quiet.mastermindRows != ceilingRoundMastermindRows {
		t.Errorf("the round queued %d mastermind rows, want the pinned %d: a mirror run is not a new sighting",
			quiet.mastermindRows, ceilingRoundMastermindRows)
	}
}

// roundWriteCount is the outbox cost of one whole round, per counted table.
type roundWriteCount struct {
	bindingRecords int
	mastermindRows int
}

func runRoundWriteCeiling(t *testing.T, linesPerTick int) roundWriteCount {
	t.Helper()
	c := &ceilingWorld{t: t, ctx: context.Background(), rt: newRuntime(t)}
	c.s = c.rt.Store
	d, err := c.s.DB()
	if err != nil {
		t.Fatalf("store DB: %v", err)
	}
	c.d = d

	// Everything outside the counted window happens before the truncate: the
	// binding is added, its mastermind is registered, and one mirror run
	// primes the rows the round's mirror passes will then leave unchanged.
	if err := c.s.Save(ceilingBound()); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	if _, err := d.UpsertMasterMind(db.MasterMind{
		ID: ceilingMasterMindID, HarnessKind: "claude", SessionID: ceilingMasterSession,
	}); err != nil {
		t.Fatalf("register mastermind: %v", err)
	}
	rev, err := c.s.Revision(ceilingBinding)
	if err != nil {
		t.Fatalf("revision: %v", err)
	}
	c.rev = rev
	c.mirror()
	if err := c.d.TruncateOutbox(); err != nil {
		t.Fatalf("truncate outbox: %v", err)
	}

	c.openRound()
	c.mirror()
	startBR, startMM := c.rows()
	if startBR != 1 || startMM != 0 {
		c.t.Errorf("opening the round queued %d binding_record and %d mastermind rows, want the one transition write and no mastermind row", startBR, startMM)
	}
	c.seenBR, c.seenMM = startBR, startMM
	for i := 1; i <= ceilingTicks; i++ {
		c.tick(i, linesPerTick)
	}
	c.closeRound()
	c.mirror()
	c.deliver()
	c.mirror()

	br, mm := c.rows()
	return roundWriteCount{bindingRecords: br, mastermindRows: mm}
}

// ceilingBound is a headless builder's binding before its first round, served
// in flavour so a RemoteLive refresh belongs on its ticks the way
// remote_sync.go writes one on every poll of a running served round.
func ceilingBound() store.Binding {
	return store.Binding{
		Name: ceilingBinding, CWD: "/repo", State: store.StateActive, RoundCap: 20,
		MasterMind:   store.Endpoint{Kind: "claude", SessionID: ceilingMasterSession},
		MasterMindID: ceilingMasterMindID,
		Builder: store.Endpoint{
			Kind: "claude", Mode: store.ModeHeadless, Server: "http://served.invalid",
		},
	}
}

// openRound is the send transition: round 1 opens with its prompt entry, and
// the first progress observation is folded into the same save because a fresh
// observation carries a synced field (the clock's home object) the first time.
func (c *ceilingWorld) openRound() {
	c.t.Helper()
	b, err := c.s.Load(ceilingBinding)
	if err != nil {
		c.t.Fatalf("load for start: %v", err)
	}
	b.Round = 1
	b.RoundStartedAt = baseTime
	b.Builder.StreamRound = 1
	b.Builder.StreamStart = 0
	b.Progress = &store.Progress{SampledAt: baseTime, TreeAt: baseTime, OutputAt: baseTime}
	if err := c.s.Save(b); err != nil {
		c.t.Fatalf("start round: %v", err)
	}
}

// tick drives one running-round tick through the real helpers: the drain
// advances the cursor over any new stream lines, progressStep samples the
// clocks at a fresh instant past the interval, and the served live copy is
// refreshed; the whole save goes through the store's synced-write path.
func (c *ceilingWorld) tick(i, linesPerTick int) {
	c.t.Helper()
	if linesPerTick > 0 {
		streamWrite(c.t, c.rt, strings.Repeat(ceilingStreamLine, linesPerTick))
	}
	b, err := c.s.Load(ceilingBinding)
	if err != nil {
		c.t.Fatalf("load for tick %d: %v", i, err)
	}
	b = drainStream(c.rt, b)
	now := baseTime.Add(time.Duration(i) * 31 * time.Second)
	b = progressStep(c.rt, b, now, sampleSignals(c.ctx, c.rt, b))
	b.Builder.RemoteLive = &store.LiveFacts{
		At: now, PID: 4711, Tail: []string{"served poll", strings.Repeat("x", i)},
	}
	if err := c.s.Save(b); err != nil {
		c.t.Fatalf("save tick %d: %v", i, err)
	}
	c.mirror()

	br, mm := c.rows()
	if br-c.seenBR != 0 || mm-c.seenMM != 0 {
		c.t.Errorf("tick %d queued %d binding_record and %d mastermind outbox rows, want 0 and 0: "+
			"a tick moves the cursor, the clocks and the served live copy, none of which is a synced fact",
			i, br-c.seenBR, mm-c.seenMM)
	}
	c.seenBR, c.seenMM = br, mm
}

// closeRound is the marker close as the store sees it: the round's report
// entry is filed in the same transaction as the binding's save.
func (c *ceilingWorld) closeRound() {
	c.t.Helper()
	err := c.s.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(ceilingBinding)
		if err != nil {
			return err
		}
		return tx.SaveWithLog(b, store.LogEntry{
			TS: baseTime.Add(time.Duration(ceilingTicks) * 31 * time.Second), Round: 1,
			Direction: store.DirToMasterMind, Kind: store.KindReport,
			Path: c.s.ReportPath(ceilingBinding, 1),
		})
	})
	if err != nil {
		c.t.Fatalf("close round: %v", err)
	}
}

// deliver is the round's delivery transition: the report is taken and the
// binding settles done.
func (c *ceilingWorld) deliver() {
	c.t.Helper()
	b, err := c.s.Load(ceilingBinding)
	if err != nil {
		c.t.Fatalf("load for deliver: %v", err)
	}
	b.State = store.StateDone
	if err := c.s.Save(b); err != nil {
		c.t.Fatalf("deliver: %v", err)
	}
}

// mirror runs the ingest pass the daemon's tick runs after its saves: an
// unchanged store revision is skipped, and a mirror run over an unchanged
// binding must leave the mastermind row untouched.
func (c *ceilingWorld) mirror() {
	c.t.Helper()
	rev, err := c.s.Revision(ceilingBinding)
	if err != nil || rev == c.rev {
		return
	}
	if _, err := ingest.Ingest(c.ctx, ingest.StoreSource(c.s, ceilingBinding), c.d, IngestDeps(c.rt)); err != nil {
		c.t.Fatalf("ingest mirror: %v", err)
	}
	c.rev = rev
}

// rows counts the binding_record and mastermind entries waiting in the shared
// outbox; reading the log does not drain it.
func (c *ceilingWorld) rows() (int, int) {
	c.t.Helper()
	entries, err := c.d.DrainOutbox(100000)
	if err != nil {
		c.t.Fatalf("read outbox: %v", err)
	}
	var br, mm int
	for _, e := range entries {
		switch e.Table {
		case "binding_record":
			br++
		case "mastermind":
			mm++
		}
	}
	return br, mm
}
