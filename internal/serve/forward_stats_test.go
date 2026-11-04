package serve

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// forwardedNotes returns the send notes round n's head log carries: the queue
// entries with the forwarded prefix.
func forwardedNotes(t *testing.T, env *testEnv, name string) []store.LogEntry {
	t.Helper()
	entries, err := env.runtime(t).Store.ReadLog(name)
	if err != nil {
		t.Fatalf("read log %s: %v", name, err)
	}
	var out []store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindQueue && strings.HasPrefix(e.Note, store.ForwardedNotePrefix) {
			out = append(out, e)
		}
	}
	return out
}

// TestBurstSendConfirmWritesColdNote pins the first confirmed send over a
// fresh node binding: one queue entry carries "forwarded to <node> (cold)",
// the round it confirms, the confirm time, and nothing a reader must confirm.
// It is red before the write: the forward path appended no entry at all.
func TestBurstSendConfirmWritesColdNote(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
		createStatus: http.StatusCreated,
		startStatus:  http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	env, _, _ := answerSetup(t, script, clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	notes := forwardedNotes(t, env, "api")
	if len(notes) != 1 {
		t.Fatalf("send notes = %+v, want exactly one", notes)
	}
	e := notes[0]
	if e.Note != "forwarded to b1 (cold)" || e.Round != 1 || !e.TS.Equal(clock) {
		t.Errorf("send note = %+v, want %q on round 1 at %v", e, "forwarded to b1 (cold)", clock)
	}
	if e.Kind != store.KindQueue || e.Direction != store.DirToMasterMind || !e.Confirmed {
		t.Errorf("send entry = kind %q dir %q confirmed %v, want a confirmed %q to_planner entry",
			e.Kind, e.Direction, e.Confirmed, store.KindQueue)
	}
}

// TestBurstSendSecondRoundWritesWarmNote pins the warm word: a second send
// over a node binding the head already shipped to reads warm. The first note
// stays cold, so the two rounds keep their own start facts.
func TestBurstSendSecondRoundWritesWarmNote(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
		createStatus: http.StatusCreated,
		startStatus:  http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	env, _, _ := answerSetup(t, script, clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	ctx := context.Background()
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")
	// The binding now stands on the node: a later round reaches it through
	// the same binding, so the second send creates nothing.
	script.getStatus = http.StatusOK
	script.getView = stubRoundView("nb-api", 1, remote.RoundRunning)

	// Bring the first round home the way an ack does, then queue the next:
	// the same binding takes it, so the warm claim is on the second send.
	b.Burst.Forward = nil
	b.Round = 2
	b.QueuedAt = clock.Add(2 * time.Minute)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("advance to round 2: %v", err)
	}
	writeHeadPlan(t, env, "api", 2)
	if got := env.srv.placeForwards(clock.Add(2 * time.Minute)); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	replaced := loadBurstBinding(t, rt, "api")
	replaced.Burst.Forward.NodeBinding = "nb-api"
	if err := rt.Store.Save(replaced); err != nil {
		t.Fatalf("rename node binding: %v", err)
	}
	if got := env.srv.sendForwards(ctx, clock.Add(3*time.Minute)); got != 1 {
		t.Fatalf("confirmed round 2 = %d, want 1", got)
	}
	notes := forwardedNotes(t, env, "api")
	if len(notes) != 2 {
		t.Fatalf("send notes = %+v, want two", notes)
	}
	if notes[0].Note != "forwarded to b1 (cold)" || notes[0].Round != 1 {
		t.Errorf("first note = %+v, want %q on round 1", notes[0], "forwarded to b1 (cold)")
	}
	if notes[1].Note != "forwarded to b1 (warm)" || notes[1].Round != 2 {
		t.Errorf("second note = %+v, want %q on round 2", notes[1], "forwarded to b1 (warm)")
	}
}

// TestBurstSendRetryAfterCreateStaysCold pins that the cold claim survives a
// retry: the first call creates the binding and its start fails, so nothing
// is confirmed; the next tick finds the binding standing (not created this
// call) and must still read cold, because no send to this node was ever
// confirmed for this binding.
func TestBurstSendRetryAfterCreateStaysCold(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
		createStatus: http.StatusCreated,
		startStatus:  http.StatusInternalServerError, startCode: remote.Code("internal"), startMsg: "boom",
	}
	env, _, _ := answerSetup(t, script, clock)
	ctx := context.Background()
	if got := env.srv.sendForwards(ctx, clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0 for the failed start", got)
	}
	if notes := forwardedNotes(t, env, "api"); len(notes) != 0 {
		t.Fatalf("send notes = %+v, want none for an unconfirmed send", notes)
	}

	// The retry: the node binding now answers, so this call creates nothing.
	script.getStatus = http.StatusOK
	script.getView = stubRoundView("nb-api", 1, remote.RoundRunning)
	script.startStatus = http.StatusCreated
	script.startView = stubRoundView("nb-api", 1, remote.RoundRunning)
	if got := env.srv.sendForwards(ctx, clock.Add(time.Minute)); got != 1 {
		t.Fatalf("confirmed on retry = %d, want 1", got)
	}
	notes := forwardedNotes(t, env, "api")
	if len(notes) != 1 {
		t.Fatalf("send notes = %+v, want the retry's one note", notes)
	}
	if notes[0].Note != "forwarded to b1 (cold)" {
		t.Errorf("retry note = %q, want %q: the binding this head created stays cold",
			notes[0].Note, "forwarded to b1 (cold)")
	}
}

// TestBurstSendRefusalWritesNoNote pins that the note rides a confirmation
// only: a refused send requeues the round and leaves the head log without a
// forwarded entry, so ingest never reads a refusal as a start.
func TestBurstSendRefusalWritesNoNote(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusOK, getView: stubRoundView("nb-api", 1, remote.RoundRunning),
		startStatus: http.StatusUnprocessableEntity, startCode: remote.CodeInvalid, startMsg: "tier above max",
	}
	env, _, fwd := answerSetup(t, script, clock)
	if got := env.srv.sendForwards(context.Background(), clock); got != 0 {
		t.Fatalf("confirmed = %d, want 0", got)
	}
	requireRequeued(t, env, fwd)
	if notes := forwardedNotes(t, env, "api"); len(notes) != 0 {
		t.Errorf("send notes = %+v, want none for a refused send", notes)
	}
}

// TestBurstSendFailedNoteAppendStillConfirms pins that the note never gates
// the send: with the binding's log at the store's cap the append fails, and
// the confirm still lands -- the round is on the node whatever the log says.
// The fill drives a real store refusal rather than a stub.
func TestBurstSendFailedNoteAppendStillConfirms(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	script := &nodeScript{
		getStatus: http.StatusNotFound, getCode: remote.CodeNotFound, getMsg: "not found",
		createStatus: http.StatusCreated,
		startStatus:  http.StatusCreated, startView: stubRoundView("nb-api", 1, remote.RoundRunning),
	}
	env, _, _ := answerSetup(t, script, clock)
	rt := env.runtime(t)
	b := loadBurstBinding(t, rt, "api")

	// store's log cap (maxLogEntries) refuses any append once the binding
	// carries that many entries; fill with batched writes, not a loop of
	// single appends, to keep the fill inside one transaction per batch.
	const logCap = 10000
	for {
		entries, err := rt.Store.ReadLog("api")
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if len(entries) >= logCap {
			break
		}
		n := logCap - len(entries)
		if n > 1000 {
			n = 1000
		}
		fill := make([]store.LogEntry, 0, n)
		for i := 0; i < n; i++ {
			fill = append(fill, store.LogEntry{
				TS: clock, Round: 1, Direction: store.DirToMasterMind,
				Kind: store.KindQueue, Confirmed: true, Note: "fill",
			})
		}
		if err := rt.Store.WithLock(func(tx *store.Tx) error {
			return tx.SaveWithLog(b, fill...)
		}); err != nil {
			t.Fatalf("fill log to %d: %v", logCap, err)
		}
	}

	if got := env.srv.sendForwards(context.Background(), clock.Add(time.Minute)); got != 1 {
		t.Fatalf("confirmed = %d, want 1: a failed note append never fails the send", got)
	}
	back := loadBurstBinding(t, rt, "api")
	if have := burstStoredForward(back); have == nil || have.Phase != "running" {
		t.Errorf("forward = %+v, want the confirm to have landed despite the failed append", have)
	}
	for _, e := range forwardedNotes(t, env, "api") {
		if strings.HasPrefix(e.Note, store.ForwardedNotePrefix) {
			t.Errorf("send note = %q, want none: the append was refused", e.Note)
		}
	}
}

// TestBurstAdoptRunningWritesAdoptedNote pins the restart claim: a
// round the node held across a restart is not a local start, so the adopt
// writes "forwarded to <node> (adopted)" at the claim time the node's answer
// proves. A superseded claim applies nothing and writes nothing.
func TestBurstAdoptRunningWritesAdoptedNote(t *testing.T) {
	clock := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	env := forwardEnv(t, headKP)
	since := clock.Add(-time.Minute)
	saveLostForward(t, env, "fq", "b1", "sending", 1, since)
	rt := env.runtime(t)
	fwd := *burstStoredForward(loadBurstBinding(t, rt, "fq"))
	ownerPath := headOwnerPath(t, env)

	env.srv.adoptRunning(ownerPath, "fq", fwd)

	back := loadBurstBinding(t, rt, "fq")
	if have := burstStoredForward(back); have == nil || have.Phase != "running" {
		t.Fatalf("forward = %+v, want adopted running", have)
	}
	notes := forwardedNotes(t, env, "fq")
	if len(notes) != 1 {
		t.Fatalf("send notes = %+v, want the adopted one", notes)
	}
	if notes[0].Note != "forwarded to b1 (adopted)" || notes[0].Round != 1 {
		t.Errorf("adopted note = %+v, want %q on round 1", notes[0], "forwarded to b1 (adopted)")
	}
	if !notes[0].TS.Equal(fwd.ClaimedAt) {
		t.Errorf("adopted note TS = %v, want the claim time %v", notes[0].TS, fwd.ClaimedAt)
	}

	// A second pass over the now-running forward applies nothing and writes
	// nothing: the note is one per adopted round.
	env.srv.adoptRunning(ownerPath, "fq", fwd)
	if notes := forwardedNotes(t, env, "fq"); len(notes) != 1 {
		t.Errorf("send notes after retry = %+v, want still one", notes)
	}
}

// TestForwardedRoundStatsEndToEnd runs one binding through two forwarded
// rounds on a real node and reads what each machine recorded: the head log
// carries the cold send note for round 1 and the warm one for round 2, each
// close lands exactly one report entry with the node's outcome and usage and
// Confirmed false, the round advances and the forward clears, the other
// owner's log gains nothing, and the owner daemon's ingest pass reads those
// entries into round rows with start kinds and measured waits. The script
// runner reports no rusage, so the wire carries none and the head entry
// keeps the columns absent; step-2 fixtures pin the rusage copy itself.
func TestForwardedRoundStatsEndToEnd(t *testing.T) {
	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pem, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}
	// The clock starts at the instant the wire requests sign with, so the
	// nonce window holds, and moves only through the times the passes are
	// handed: accept at it, each send two minutes on.
	clock := time.Now().UTC()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.Burst = burstTestPolicy()
		cfg.ClientKey = pem
		cfg.Provider = &recordingBurstProvider{}
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	nodeSrv, nodeTS, nodeFP := startBurstNodeWithRunner(t, &headKP)
	runner, ok := nodeSrv.cfg.Runner.(*scriptRunner)
	if !ok {
		t.Fatalf("node runner = %T, want *scriptRunner", nodeSrv.cfg.Runner)
	}
	nodeDir, ok := remote.IDOf(headKP.Public).Dir()
	if !ok {
		t.Fatal("head id dir: malformed")
	}
	nodeRoot := filepath.Join(nodeSrv.cfg.Root, "bindings", nodeDir)

	// A second owner, enrolled and holding one idle binding: every entry
	// this flow writes must land in the forwarding owner's history.
	bob := addOwner(t, env, "bob")
	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name: "web", RepoID: bob.repoID, BaseCommit: bob.headSHA, Role: "builder",
	})
	resp, body := doSigned(t, env.ts, bob.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create web status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}

	ctx := context.Background()
	row := stubRow(t, "b1", nodeTS.URL, nodeFP)
	fwd := queueForward(t, env, "api", row, clock)
	if got := env.srv.sendForwards(ctx, clock.Add(2*time.Minute)); got != 1 {
		t.Fatalf("confirmed = %d, want 1", got)
	}
	notes := forwardedNotes(t, env, "api")
	if len(notes) != 1 || notes[0].Note != "forwarded to b1 (cold)" || notes[0].Round != 1 {
		t.Fatalf("send notes = %+v, want the cold note on round 1", notes)
	}

	closeNodeRound(t, nodeSrv, runner, nodeRoot, fwd.NodeBinding, 1)
	if got := env.srv.observeForwards(ctx); got != 1 {
		t.Fatalf("observe closed = %d, want 1", got)
	}
	rt := env.runtime(t)
	back, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if back.Round != 2 {
		t.Errorf("head Round = %d, want 2 after the close", back.Round)
	}
	if burstStoredForward(back) != nil {
		t.Errorf("forward = %+v, want it cleared by the ack", back.Burst)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	headReport := requireOneReport(t, entries, 1)
	if headReport.Confirmed {
		t.Error("head report entry Confirmed = true, want false: the owner daemon delivers it")
	}
	if !strings.Contains(headReport.Payload, "# Report") {
		t.Errorf("head report payload = %q, want the node's report body", headReport.Payload)
	}
	// The node's own report entry is what the head copied: compare the two
	// machine's facts field by field on the wire-bearing names.
	nodeSt := nodeSrv.ownerStore(nodeRoot)
	nodeEntries, err := nodeSt.ReadLog(fwd.NodeBinding)
	if err != nil {
		t.Fatal(err)
	}
	nodeReport := requireOneReport(t, nodeEntries, 1)
	if headReport.Outcome != nodeReport.Outcome {
		t.Errorf("head report outcome %q, want the node's %q", headReport.Outcome, nodeReport.Outcome)
	}
	if (headReport.Usage == nil) != (nodeReport.Usage == nil) ||
		(headReport.Usage != nil && *headReport.Usage != *nodeReport.Usage) {
		t.Errorf("head report usage %v, want the node's %v verbatim", headReport.Usage, nodeReport.Usage)
	}
	if headReport.Rusage != nil {
		t.Errorf("head report rusage = %v, want nil: the script runner measures none, so the wire carries none", headReport.Rusage)
	}

	// Round 2 over the same node binding: the ship base stands, so the
	// second confirmed send reads warm. The accept census entry the wire
	// would have written is appended by hand, the round is queued and
	// placed like any other.
	q2 := clock.Add(10 * time.Minute)
	next := loadBurstBinding(t, rt, "api")
	next.QueuedAt = q2
	if err := rt.Store.Save(next); err != nil {
		t.Fatalf("queue round 2: %v", err)
	}
	writeHeadPlan(t, env, "api", 2)
	if err := rt.Store.AppendLog("api", store.LogEntry{
		TS: q2, Round: 2, Direction: store.DirToMasterMind,
		Kind: store.KindQueue, Confirmed: true, Note: "queued (1/1 builders busy)",
	}); err != nil {
		t.Fatalf("accept census round 2: %v", err)
	}
	if got := env.srv.placeForwards(q2); got != 1 {
		t.Fatalf("placed = %d, want 1", got)
	}
	replaced := loadBurstBinding(t, rt, "api")
	replaced.Burst.Forward.NodeBinding = fwd.NodeBinding
	if err := rt.Store.Save(replaced); err != nil {
		t.Fatalf("reuse node binding: %v", err)
	}
	if got := env.srv.sendForwards(ctx, q2.Add(time.Minute)); got != 1 {
		t.Fatalf("confirmed round 2 = %d, want 1", got)
	}
	notes = forwardedNotes(t, env, "api")
	if len(notes) != 2 || notes[1].Note != "forwarded to b1 (warm)" || notes[1].Round != 2 {
		t.Fatalf("send notes = %+v, want cold then warm on round 2", notes)
	}

	closeNodeRound(t, nodeSrv, runner, nodeRoot, fwd.NodeBinding, 2)
	if got := env.srv.observeForwards(ctx); got != 1 {
		t.Fatalf("observe closed round 2 = %d, want 1", got)
	}
	entries, err = rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	requireOneReport(t, entries, 2)

	// The idle owner's binding: its log holds nothing this flow wrote.
	rtBob, err := env.srv.runtime(bob.id)
	if err != nil {
		t.Fatalf("runtime(bob): %v", err)
	}
	entriesBob, err := rtBob.Store.ReadLog("web")
	if err != nil {
		t.Fatalf("read web log: %v", err)
	}
	if len(entriesBob) != 0 {
		t.Errorf("web log = %+v, want nothing: entries land in the forwarding owner's store", entriesBob)
	}

	// The wiring the tick depends on: the forwarding owner's runtime
	// carries the shared machine database the head's store writes to.
	if rt := env.srv.runtimeAt(headOwnerPath(t, env)); rt.DB != env.srv.cfg.DB {
		t.Fatal("owner runtime DB is not the shared machine database: the tick would ingest nothing")
	}
	// The production call chain: the owner daemon's tick ingests every live
	// binding over the now-wired runtime.
	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}
	// The tick's non-ingest phases must not move the flow: the rounds stay
	// closed, no new send lands, both reports still exist.
	if back := loadBurstBinding(t, rt, "api"); back.Round != 3 {
		t.Errorf("head Round = %d, want 3 after the tick", back.Round)
	}
	if notes := forwardedNotes(t, env, "api"); len(notes) != 2 {
		t.Errorf("send notes after tick = %+v, want still the cold and warm ones", notes)
	}
	if entries, err := rt.Store.ReadLog("api"); err != nil {
		t.Fatalf("read log after tick: %v", err)
	} else {
		requireOneReport(t, entries, 1)
		requireOneReport(t, entries, 2)
	}
	rows, err := env.srv.cfg.DB.Query(db.Filter{Binding: "api"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no round rows for api after ingest")
	}
	rounds, err := env.srv.cfg.DB.Rounds(rows[0].BindingID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]struct {
		kind string
		wait int64
	}{1: {kind: "cold", wait: 2 * 60 * 1000}, 2: {kind: "warm", wait: 60 * 1000}}
	for n, w := range want {
		var found *db.Round
		for i := range rounds {
			if rounds[i].Number == n {
				found = &rounds[i]
			}
		}
		if found == nil {
			t.Fatalf("no round %d row, want it ingested", n)
		}
		if found.StartKind == nil || *found.StartKind != w.kind {
			t.Errorf("round %d start kind = %v, want %q", n, found.StartKind, w.kind)
		}
		if found.QueueWaitMS == nil {
			t.Errorf("round %d wait = nil, want %d ms", n, w.wait)
		} else if *found.QueueWaitMS != w.wait {
			t.Errorf("round %d wait = %d ms, want %d", n, *found.QueueWaitMS, w.wait)
		}
	}
}

// requireOneReport returns round n's single report entry.
func requireOneReport(t *testing.T, entries []store.LogEntry, n int) store.LogEntry {
	t.Helper()
	var report store.LogEntry
	count := 0
	for _, e := range entries {
		if e.Round == n && e.Kind == store.KindReport {
			report = e
			count++
		}
	}
	if count != 1 {
		t.Fatalf("round %d report entries = %d, want exactly one (%+v)", n, count, entries)
	}
	return report
}
