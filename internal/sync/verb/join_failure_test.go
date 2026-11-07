package verb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// pullRefused is a log whose pulls refuse, so a join writes its marker and
// then fails in the import. Range walks still delegate, so the secrets
// pre-pass succeeds and the failure is the pipeline's own.
type pullRefused struct {
	*synclog.MemTransport
}

func (l *pullRefused) Pull(map[string]int) ([]synclog.Entry, error) {
	return nil, errors.New("test: the pull refuses")
}

// A failing pipeline leaves two traces for nobody reading: the marker set and
// a failure record carrying the refusal's time and message, plus the log line.
func TestAFailingJoinLeavesItsMarkerAndItsFailure(t *testing.T) {
	runner, local, log := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		return &pullRefused{MemTransport: log}, nil
	}
	buf, restore := captureSyncLogs(t)
	defer restore()

	res := runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	if res.OK {
		t.Fatal("a join whose pull refuses was accepted")
	}
	if _, marked, err := relevosync.ReadJoin(local); err != nil || !marked {
		t.Fatalf("the join marker = (marked %v, err %v), want it left behind", marked, err)
	}
	failure, ok, err := relevosync.ReadJoinFailure(local)
	if err != nil || !ok {
		t.Fatalf("ReadJoinFailure = (%+v, %v, %v), want the recorded failure", failure, ok, err)
	}
	if failure.At.IsZero() {
		t.Error("the failure carries no time")
	}
	if failure.Message != res.Message {
		t.Errorf("failure message = %q, want the refusal's %q", failure.Message, res.Message)
	}
	if logged := buf.String(); !strings.Contains(logged, "the join failed") {
		t.Errorf("the log carries no failure line:\n%s", logged)
	}
	if logged := buf.String(); strings.Contains(logged, joinToken) || strings.Contains(logged, joinPassphrase) {
		t.Errorf("a secret reached the log:\n%s", logged)
	}
}

// A plain enable refused before any marker records nothing: no token, no
// preflight and no quota guard ever started a join, so status must not say one
// failed.
func TestARefusedEnableWithNoMarkerLeavesNoRecord(t *testing.T) {
	t.Parallel()

	runner, local := joinRunner(t)
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable, RemoteURL: joinRemote},
		[]byte(verbFixtureToken))
	if res.OK || res.Code != wire.SyncCodeNoR2 {
		t.Fatalf("enable = (ok %v, code %q), want refused as %q", res.OK, res.Code, wire.SyncCodeNoR2)
	}
	if _, marked, err := relevosync.ReadJoin(local); err != nil || marked {
		t.Fatalf("a refused enable wrote a join marker: (marked %v, err %v)", marked, err)
	}
	if _, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
		t.Errorf("a refused enable recorded a failure: (ok %v, err %v)", ok, err)
	}
}

// A resumed join that then fails the passphrase check records the failure too:
// beginJoin cleared the old record, so without this the marker would read as a
// join in progress while nothing runs.
func TestAResumedJoinRefusedAtVerifyStillRecordsItsFailure(t *testing.T) {
	t.Parallel()

	runner, local, _ := joinMachine(t, joinPassphrase)
	if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	wrong := "not the passphrase"
	res := runner.Run(context.Background(), joinVerb(t, wrong), []byte(joinToken+wrong))
	if res.OK {
		t.Fatal("a join with the wrong passphrase was accepted")
	}
	if _, marked, err := relevosync.ReadJoin(local); err != nil || !marked {
		t.Fatalf("the join marker = (marked %v, err %v), want it still set", marked, err)
	}
	failure, ok, err := relevosync.ReadJoinFailure(local)
	if err != nil || !ok {
		t.Fatalf("ReadJoinFailure = (%+v, %v, %v), want the resumed run's failure", failure, ok, err)
	}
	if failure.Message != res.Message {
		t.Errorf("failure message = %q, want the refusal's %q", failure.Message, res.Message)
	}
}

// A later enable starts clear of the last run's failure: a planted record is
// gone after a run that succeeds, which is why a success writes no separate
// clear.
func TestALaterEnableClearsAPlantedFailureAndProceeds(t *testing.T) {
	t.Parallel()

	runner, local := joinRunner(t)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	if err := relevosync.WriteJoinFailure(local, time.Unix(0, 0).UTC(), "sync: enable: import: an old failure"); err != nil {
		t.Fatalf("WriteJoinFailure: %v", err)
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbEnable, RemoteURL: "libsql://join.invalid"},
		[]byte(verbFixtureToken))
	if !res.OK {
		t.Fatalf("enable refused: %s", res.Message)
	}
	if _, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
		t.Errorf("the old failure survived a later enable: (ok %v, err %v)", ok, err)
	}
}

// Clearing a join that never had a local file is a no-op rather than a panic.
func TestBeginJoinWithNoLocalFileIsANoOp(t *testing.T) {
	t.Parallel()

	(&Runner{Local: nil}).beginJoin()
}

// Recording a join failure with no local file is a no-op rather than a panic:
// there is no marker to read and no record to write, so both a nil runner and
// a runner holding no file return quietly.
func TestRecordJoinFailureWithNoLocalFileIsANoOp(t *testing.T) {
	t.Parallel()

	(&Runner{Local: nil}).recordJoinFailure(errors.New("test: a failure with nowhere to go"))
	(*Runner)(nil).recordJoinFailure(errors.New("test: a failure with nowhere to go"))
}

// panicStats is a log that panics where the join's import reads, carrying a
// token-shaped value so the test can tell whether the value leaked.
type panicStats struct {
	*synclog.MemTransport
}

func (l *panicStats) Stats() (synclog.Stats, error) {
	panic("test-panic-token-shaped-value")
}

// A panic on the enable path records a fixed-text failure and re-panics, so
// the owner still answers as it does today. The panic value stays out of both
// the record and the log because a token can be on that stack.
func TestAJoinPanicRecordsAFixedFailureAndRepanics(t *testing.T) {
	runner, local, log := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		return &panicStats{MemTransport: log}, nil
	}
	buf, restore := captureSyncLogs(t)
	defer restore()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	}()
	if recovered != "test-panic-token-shaped-value" {
		t.Fatalf("recovered = %v, want the panic to reach the caller", recovered)
	}
	failure, ok, err := relevosync.ReadJoinFailure(local)
	if err != nil || !ok {
		t.Fatalf("ReadJoinFailure = (%+v, %v, %v), want the panic's failure", failure, ok, err)
	}
	if failure.Message != "sync: the enable did not finish" {
		t.Errorf("failure message = %q, want the fixed text", failure.Message)
	}
	if logged := buf.String(); !strings.Contains(logged, "the join panicked") {
		t.Errorf("the log carries no panic line:\n%s", logged)
	}
	if logged := buf.String(); strings.Contains(logged, "test-panic-token-shaped-value") {
		t.Errorf("the panic value reached the log:\n%s", logged)
	}
	if strings.Contains(failure.Message, "test-panic-token-shaped-value") {
		t.Error("the panic value reached the failure record")
	}
}

// A panic with no marker records nothing: no join wrote its mark, so a record
// would blame a join that never started, and the log stays quiet for the same
// reason the record does.
func TestAJoinPanicWithNoMarkerRecordsNothing(t *testing.T) {
	runner, local, log := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		return &panicStats{MemTransport: log}, nil
	}
	buf, restore := captureSyncLogs(t)
	defer restore()

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		runner.Run(context.Background(), joinVerb(t, joinPassphrase), []byte(joinToken+joinPassphrase))
	}()
	if recovered != "test-panic-token-shaped-value" {
		t.Fatalf("recovered = %v, want the panic to reach the caller", recovered)
	}
	if _, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
		t.Errorf("ReadJoinFailure = (ok %v, err %v), want no record without a marker", ok, err)
	}
	if logged := buf.String(); strings.Contains(logged, "the join panicked") {
		t.Errorf("the log carries a panic line with no marker:\n%s", logged)
	}
	if logged := buf.String(); strings.Contains(logged, "test-panic-token-shaped-value") {
		t.Errorf("the panic value reached the log:\n%s", logged)
	}
}

// A plain enable refused at verify with a marker set records the failure too:
// the marker says a join is in progress, and the planted record names an older
// run, so without this status would blame the wrong failure.
func TestAPlainEnableRefusedAtVerifyStillRecordsItsFailure(t *testing.T) {
	t.Parallel()

	runner, local, _ := joinMachine(t, joinPassphrase)
	if err := relevosync.SetR2(local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	if err := relevosync.WriteJoinFailure(local, time.Unix(0, 0).UTC(), "sync: enable: import: an old failure"); err != nil {
		t.Fatalf("WriteJoinFailure: %v", err)
	}
	wrong := "not the passphrase"
	verb := &wire.SyncVerb{
		Verb:          wire.SyncVerbEnable,
		RemoteURL:     joinRemote,
		PassphraseLen: len(wrong),
		R2Endpoint:    verbFixtureR2.Endpoint,
		R2Bucket:      verbFixtureR2.Bucket,
		R2KeyID:       verbFixtureR2.KeyID,
		R2SecretLen:   len(verbFixtureR2.Secret),
	}
	res := runner.Run(context.Background(), verb, []byte(joinToken+wrong+verbFixtureR2.Secret))
	if res.OK {
		t.Fatal("a plain enable with the wrong passphrase was accepted")
	}
	if _, marked, err := relevosync.ReadJoin(local); err != nil || !marked {
		t.Fatalf("the join marker = (marked %v, err %v), want it still set", marked, err)
	}
	failure, ok, err := relevosync.ReadJoinFailure(local)
	if err != nil || !ok {
		t.Fatalf("ReadJoinFailure = (%+v, %v, %v), want the refused run's failure", failure, ok, err)
	}
	if failure.Message != res.Message {
		t.Errorf("failure message = %q, want the refusal's %q", failure.Message, res.Message)
	}
}

// A machine already on, still sending a join's owed history, keeps running when
// another enable is refused, so the refusal must not read as a dead join.
func TestARefusedEnableOnARunningJoinLeavesNoRecord(t *testing.T) {
	t.Parallel()

	runner, local := joinRunner(t)
	if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	runner.recordJoinFailure(relevosync.ErrAlreadyEnabled)

	if failure, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
		t.Errorf("ReadJoinFailure = (%+v, %v, %v), want no record on a machine that is on", failure, ok, err)
	}
}

// A stop is what a disable asked for, not a failure to report.
func TestAStoppedEnableLeavesNoRecord(t *testing.T) {
	t.Parallel()

	for _, stop := range []error{relevosync.ErrEnableStopped, relevosync.ErrStopped} {
		runner, local := joinRunner(t)
		if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("WriteJoin: %v", err)
		}

		runner.recordJoinFailure(fmt.Errorf("sync: enable: %w", stop))

		if failure, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
			t.Errorf("after %v: ReadJoinFailure = (%+v, %v, %v), want no record", stop, failure, ok, err)
		}
	}
}

// A panic on a machine already on is no dead join either.
func TestAPanicOnARunningJoinRecordsNothing(t *testing.T) {
	t.Parallel()

	runner, local := joinRunner(t)
	if err := relevosync.WriteJoin(local, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("WriteJoin: %v", err)
	}
	if err := relevosync.MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}

	func() {
		defer func() { _ = recover() }()
		defer runner.recordJoinPanic()
		panic("boom")
	}()

	if failure, ok, err := relevosync.ReadJoinFailure(local); err != nil || ok {
		t.Errorf("ReadJoinFailure = (%+v, %v, %v), want no record on a machine that is on", failure, ok, err)
	}
}
