package availability

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
)

// loadLedger reads the runtime's ledger for assertions.
func loadLedger(t *testing.T, d Deps) Ledger {
	t.Helper()
	l, err := LoadLedger(d.Gates)
	if err != nil {
		t.Fatalf("LoadKV ledger: %v", err)
	}
	return l
}

// loadHistory reads the runtime's history for assertions.
func readHistory(t *testing.T, d Deps) History {
	t.Helper()
	h, err := loadHistory(d.Gates)
	if err != nil {
		t.Fatalf("LoadKV history: %v", err)
	}
	return h
}

// kvRowBytes reads one kv row for a byte-identity assertion.
func kvRowBytes(t *testing.T, d Deps, key string) []byte {
	t.Helper()
	raw, ok, err := d.Gates.KVGet(key)
	if err != nil || !ok {
		t.Fatalf("KVGet(%s) = (_, %v, %v), want the row", key, ok, err)
	}
	return raw
}

// TestRecordSpawnFailureLockedUnderHeldLock pins the fix for the deadlock
// the T2 round-2 report found: switchBuilder runs inside Reconcile's
// Store.WithLock, and recordSpawnFailureLocked must be able to record a
// failed replacement spawn from in there without trying to re-take that
// (non-reentrant) lock. If it ever does, this test hangs until the 5s
// timer fires instead of failing fast, which is why the assertion is a
// select against a timer rather than a bare call.
func TestRecordSpawnFailureLockedUnderHeldLock(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	done := make(chan error, 1)
	go func() {
		done <- d.Store.WithLock(func(*store.Tx) error {
			RecordSpawnFailureLocked(d, testAgyRef, "webshop", errors.New("boom"))
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}

	l := loadLedger(t, d)
	count := 0
	for _, e := range l.Entries {
		if e.Kind == SpawnFailed && e.Subject == testAgyRef {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("spawn_failed entries for %s = %d, want 1", testAgyRef, count)
	}
}

func TestUnavailableRecordsTheProvider(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	provider, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window")
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if e.Kind != RateLimited || e.Subject != "test" {
		t.Errorf("entry = %+v, want RateLimited for provider test", e)
	}
	if !e.Until.IsZero() {
		t.Errorf("Until = %v, want zero", e.Until)
	}
	if e.Note != "5h window" {
		t.Errorf("Note = %q, want %q", e.Note, "5h window")
	}
	if e.Source != "planner" {
		t.Errorf("Source = %q, want mastermind", e.Source)
	}
	if e.Binding != "" {
		t.Errorf("Binding = %q, want empty", e.Binding)
	}
}

func TestUnavailableWithUntil(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	until := baseTime.Add(2 * time.Hour)

	if _, err := Unavailable(d, testClaudeRef, until, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if !l.Entries[0].Until.Equal(until) {
		t.Errorf("Until = %v, want %v", l.Entries[0].Until, until)
	}
}

func TestUnavailableRefusesAnUnknownToken(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, "claude/test/nope", time.Time{}, ""); !errors.Is(err, candidate.ErrUnknownCandidate) {
		t.Errorf("err = %v, want ErrUnknownCandidate", err)
	}
	l := loadLedger(t, d)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0", len(l.Entries))
	}

	if _, err := Unavailable(d, "claude/test", time.Time{}, ""); !errors.Is(err, candidate.ErrBadRef) {
		t.Errorf("err = %v, want ErrBadRef", err)
	}
}

func TestAvailableByTokenAndByProvider(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "first"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "second"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	provider, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 2 {
		t.Errorf("Available(provider) = %q, %d, want test, 2", provider, removed)
	}
	l := loadLedger(t, d)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0", len(l.Entries))
	}

	provider, removed, err = Available(d, testClaudeRef, ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}
}

func TestAvailableLeavesSpawnFailures(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	RecordSpawnFailure(d, testClaudeRef, "webshop", errors.New("boom"))
	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	_, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if l.Entries[0].Kind != SpawnFailed {
		t.Errorf("remaining entry kind = %v, want SpawnFailed", l.Entries[0].Kind)
	}
}

func TestGatesEmptyWhenNoLedger(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if got := Gates(d); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

func TestGatesProjectsOntoCandidates(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	RecordSpawnFailure(d, testAgyRef, "webshop", errors.New("x"))

	gates := Gates(d)
	if len(gates) != 4 {
		t.Fatalf("got %d gates, want 4: %+v", len(gates), gates)
	}

	// The first two gates are both for agy/test/m: one RateLimited (from
	// the provider-wide gate) and one SpawnFailed. Both have Since ==
	// baseTime, so the sort between them is not guaranteed; assert as a set.
	agyKinds := map[Kind]bool{}
	for _, g := range gates[:2] {
		if g.Token != testAgyRef {
			t.Errorf("gate = %+v, want token %q", g, testAgyRef)
		}
		agyKinds[g.Kind] = true
	}
	if !agyKinds[RateLimited] || !agyKinds[SpawnFailed] {
		t.Errorf("first two gates = %+v, want one RateLimited and one SpawnFailed for %q", gates[:2], testAgyRef)
	}

	if gates[2].Token != testClaudeRef || gates[2].Kind != RateLimited {
		t.Errorf("gate 2 = %+v, want RateLimited for %q", gates[2], testClaudeRef)
	}
	if gates[3].Token != testOpencodeRef || gates[3].Kind != RateLimited {
		t.Errorf("gate 3 = %+v, want RateLimited for %q", gates[3], testOpencodeRef)
	}
}

// TestGatesToleratesABadLedger pins that a ledger relevo cannot parse is still
// read as empty with its stderr note, never a crash. A hand-edited row (the
// medium's version of a hand-edited file) is simulated by a KV that returns
// invalid bytes.
func TestGatesToleratesABadLedger(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	d.Gates = badJSONKV{}

	if got := Gates(d); got != nil {
		t.Errorf("Gates() = %+v, want nil", got)
	}
}

// TestMutateLedgerCarriesUnknownEntries: an entry with an
// unknown kind or source rides through the Load -> Prune -> mutate -> Save
// path mutateLedgerLocked takes, untouched.
func TestMutateLedgerCarriesUnknownEntries(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	doc := `{"entries":[
  {"kind":"future_kind","subject":"test","at":"2026-09-11T15:00:00Z","source":"relevo"},
  {"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}
]}`
	if err := d.Gates.KVPut(ledgerKey, []byte(doc)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	err := mutateLedgerLocked(d, func(l Ledger) Ledger {
		return l.Append(Entry{
			Kind:    RateLimited,
			Subject: "test",
			At:      baseTime,
			Source:  "planner",
		})
	})
	if err != nil {
		t.Fatalf("mutateLedgerLocked: %v", err)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 || l.Entries[0].Kind != RateLimited {
		t.Fatalf("Entries = %+v, want the one appended rate_limited", l.Entries)
	}
	if len(l.Other) != 2 {
		t.Fatalf("Other = %v, want the 2 unknown entries preserved", l.Other)
	}
}

// TestGatesIgnoresUnknownEntries pins that the preserved entries in Other are
// invisible to readers: Gates still returns exactly the known gates.
func TestGatesIgnoresUnknownEntries(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	doc := `{"entries":[
  {"kind":"future_kind","subject":"test","at":"2026-09-11T15:00:00Z","source":"relevo"},
  {"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"},
  {"kind":"rate_limited","subject":"test","at":"2026-09-11T15:00:00Z","source":"planner"}
]}`
	if err := d.Gates.KVPut(ledgerKey, []byte(doc)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	gates := Gates(d)
	if len(gates) != 3 {
		t.Fatalf("got %d gates, want 3 (one per candidate on provider test): %+v", len(gates), gates)
	}
	for _, g := range gates {
		if g.Kind != RateLimited {
			t.Errorf("gate = %+v, want only the known rate_limited gate", g)
		}
	}
}

// TestGateKindTextExitedNoReport pins the wording switchBuilder's synthesised
// gate renders through ErrAllGated and skipText: it must never drift
// from "exited without a report".
func TestGateKindTextExitedNoReport(t *testing.T) {
	t.Parallel()

	if got := GateKindText(ExitedNoReport); got != "exited without a report" {
		t.Errorf("GateKindText(ExitedNoReport) = %q, want %q", got, "exited without a report")
	}
}

// TestGateKindTextRolesMissing pins the wording in status, candidates and
// doctor.
func TestGateKindTextRolesMissing(t *testing.T) {
	t.Parallel()

	if got := GateKindText(RolesMissing); got != "agents missing" {
		t.Errorf("GateKindText(RolesMissing) = %q, want %q", got, "agents missing")
	}
}

// TestGateTimeText pins the one formatter every gate time goes through: the
// clock time alone on today's local date, the date as well otherwise.
func TestGateTimeText(t *testing.T) {
	t.Cleanup(SetGateClock(func() time.Time { return time.Date(2026, 9, 23, 14, 0, 0, 0, time.Local) }))

	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"today", time.Date(2026, 9, 23, 22, 16, 0, 0, time.Local), "22:16"},
		{"26 days out", time.Date(2026, 10, 19, 22, 16, 0, 0, time.Local), "Oct 19 22:16"},
		{"tomorrow", time.Date(2026, 9, 24, 0, 5, 0, 0, time.Local), "Sep 24 00:05"},
		{"next year", time.Date(2027, 1, 2, 3, 4, 0, 0, time.Local), "2027-01-02 03:04"},
	}
	for _, tt := range tests {
		if got := GateTimeText(tt.in); got != tt.want {
			t.Errorf("GateTimeText(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGateUntilText(t *testing.T) {
	if got := GateUntilText(time.Time{}); got != "until cleared" {
		t.Errorf("GateUntilText(zero) = %q, want %q", got, "until cleared")
	}

	fixed := baseTime
	t.Cleanup(SetGateClock(func() time.Time { return fixed }))
	want := "until " + fixed.Local().Format("15:04")
	if got := GateUntilText(fixed); got != want {
		t.Errorf("GateUntilText(fixed) = %q, want %q", got, want)
	}

	// A gate 26 days out shows its date: the --for 632h example.
	t.Cleanup(SetGateClock(func() time.Time { return time.Date(2026, 9, 23, 14, 0, 0, 0, time.Local) }))
	if got := GateUntilText(time.Date(2026, 10, 19, 22, 16, 0, 0, time.Local)); got != "until Oct 19 22:16" {
		t.Errorf("GateUntilText(26d) = %q, want %q", got, "until Oct 19 22:16")
	}
}

func TestGatedNoteEmptyWhenNotGated(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if got := gatedNote(d, testClaudeRef); got != "" {
		t.Errorf("gatedNote() = %q, want empty", got)
	}
}

// TestGatesCarryName: every gate Gates hands a renderer carries
// the candidate's short name beside its canonical token, and a token no
// longer configured reads as itself.
func TestGatesCarryName(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	RecordSpawnFailure(d, testAgyRef, "webshop", errors.New("boom"))

	gates := Gates(d)
	byToken := make(map[string]Gate, len(gates))
	for _, g := range gates {
		byToken[g.Token] = g
	}
	if got := byToken[testClaudeRef].Name; got != "claude-m" {
		t.Errorf("claude gate Name = %q, want claude-m", got)
	}
	if got := byToken[testAgyRef].Name; got != "agy-m" {
		t.Errorf("agy gate Name = %q, want agy-m", got)
	}
}

func TestGatedNoteFormatsEveryGate(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	RecordSpawnFailure(d, testClaudeRef, "webshop", errors.New("boom"))

	got := gatedNote(d, testClaudeRef)
	if !strings.HasPrefix(got, "note: claude-m is gated: ") {
		t.Fatalf("gatedNote() = %q, want prefix %q", got, "note: claude-m is gated: ")
	}
	if !strings.HasSuffix(got, "; proceeding") {
		t.Errorf("gatedNote() = %q, want suffix %q", got, "; proceeding")
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("gatedNote() = %q, want one line", got)
	}
	if !strings.Contains(got, "rate-limited") || !strings.Contains(got, "spawn failed") {
		t.Errorf("gatedNote() = %q, want both gate kinds present", got)
	}
	if !strings.Contains(got, "boom") {
		t.Errorf("gatedNote() = %q, want the spawn-failure note included", got)
	}
}

func TestMutateLedgerPrunes(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	expired := Entry{
		Kind:    RateLimited,
		Subject: "stale",
		At:      baseTime.Add(-time.Hour),
		Until:   baseTime.Add(-time.Minute),
		Source:  "planner",
	}
	if err := SaveLedger(d.Gates, Ledger{Entries: []Entry{expired}}); err != nil {
		t.Fatalf("SaveKV: %v", err)
	}

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if l.Entries[0].Subject != "test" {
		t.Errorf("remaining entry subject = %q, want test", l.Entries[0].Subject)
	}
}

func TestUnavailableRecordsHistory(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}

	h := readHistory(t, d)
	if len(h.Events) != 1 {
		t.Fatalf("got %d history events, want 1: %+v", len(h.Events), h.Events)
	}
	want := Event{Kind: RateLimited, Provider: "test", Token: "", Source: "planner", Note: "5h window", At: baseTime}
	if h.Events[0] != want {
		t.Errorf("history event = %+v, want %+v", h.Events[0], want)
	}
}

// TestSwitchSpawnFailureRecordsHistory mirrors
// TestRecordSpawnFailureLockedUnderHeldLock's already-held-lock setup, since
// that is the daemon-switch path recordSpawnFailureLocked serves.
func TestSwitchSpawnFailureRecordsHistory(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	done := make(chan error, 1)
	go func() {
		done <- d.Store.WithLock(func(*store.Tx) error {
			RecordSpawnFailureLocked(d, testAgyRef, "webshop", errors.New("boom"))
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WithLock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlocked")
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}

	h := readHistory(t, d)
	if len(h.Events) != 1 {
		t.Fatalf("got %d history events, want 1: %+v", len(h.Events), h.Events)
	}
	if h.Events[0].Kind != SpawnFailed || h.Events[0].Token != testAgyRef {
		t.Errorf("history event = %+v, want SpawnFailed for %q", h.Events[0], testAgyRef)
	}
}

// TestAvailableRecordsClear: a clear that removed something is an
// observation after all. The history gains a Cleared event whose
// Since is the At of the entry the clear removed, so At - Since is how long
// the provider was blocked.
func TestAvailableRecordsClear(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	d.Now = func() time.Time { return baseTime.Add(5 * time.Hour) }

	provider, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 1 {
		t.Errorf("Available = %q, %d, want test, 1", provider, removed)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 0 {
		t.Errorf("got %d ledger entries, want 0: %+v", len(l.Entries), l.Entries)
	}

	h := readHistory(t, d)
	if len(h.Events) != 2 {
		t.Fatalf("got %d history events, want 2: %+v", len(h.Events), h.Events)
	}
	ev := h.Events[1]
	if ev.Kind != Cleared {
		t.Errorf("kind = %q, want %q", ev.Kind, Cleared)
	}
	if ev.Provider != "test" {
		t.Errorf("provider = %q, want test", ev.Provider)
	}
	if ev.Source != ClearedByMasterMind {
		t.Errorf("source = %q, want %q", ev.Source, ClearedByMasterMind)
	}
	if !ev.Since.Equal(baseTime) {
		t.Errorf("Since = %v, want %v", ev.Since, baseTime)
	}
	if !ev.At.Equal(baseTime.Add(5 * time.Hour)) {
		t.Errorf("At = %v, want %v", ev.At, baseTime.Add(5*time.Hour))
	}
}

// TestAvailableNothingClearedRecordsNothing: zero removed is not an error and
// is not an observation either, so the history stays empty.
func TestAvailableNothingClearedRecordsNothing(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	provider, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 0 {
		t.Errorf("Available = %q, %d, want test, 0", provider, removed)
	}

	h := readHistory(t, d)
	if len(h.Events) != 0 {
		t.Errorf("got %d history events, want 0: %+v", len(h.Events), h.Events)
	}
}

// TestAvailableRefusesUnknownWritesNothing: the refusal has to stop the save,
// so the ledger is not even rewritten to drop its expired entry. The gate is
// given an Until that has passed by the time the clear runs precisely so the
// pruned-and-saved ledger would differ from the file the refusal must leave.
func TestAvailableRefusesUnknownWritesNothing(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, baseTime.Add(time.Hour), "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	d.Now = func() time.Time { return baseTime.Add(5 * time.Hour) }

	beforeLedger := kvRowBytes(t, d, "ledger")
	beforeHistory := kvRowBytes(t, d, "availability")

	if _, _, err := Available(d, "tset", ClearedByMasterMind); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("Available(tset) err = %v, want ErrUnknownProvider", err)
	}

	if got := kvRowBytes(t, d, "ledger"); string(got) != string(beforeLedger) {
		t.Errorf("ledger row = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := kvRowBytes(t, d, "availability"); string(got) != string(beforeHistory) {
		t.Errorf("availability row = %s, want it untouched at %s", got, beforeHistory)
	}
}

// TestAvailableRejectsBadSource: source is one of two constants, and a
// caller that passes anything else has a bug -- nothing is written.
func TestAvailableRejectsBadSource(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	beforeLedger := kvRowBytes(t, d, "ledger")
	beforeHistory := kvRowBytes(t, d, "availability")

	if _, _, err := Available(d, "test", "bogus"); err == nil {
		t.Fatal("Available(source=\"bogus\") err = nil, want an error")
	}

	if got := kvRowBytes(t, d, "ledger"); string(got) != string(beforeLedger) {
		t.Errorf("ledger row = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := kvRowBytes(t, d, "availability"); string(got) != string(beforeHistory) {
		t.Errorf("availability row = %s, want it untouched at %s", got, beforeHistory)
	}
}

func TestHistoryFailureDoesNotFailTheLedger(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	// A KV whose availability write fails while the ledger write succeeds:
	// the ledger write is the one that matters and must still land.
	d.Gates = failPutKV{inner: d.Gates, key: "availability"}

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "reason"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
}

func TestBindingsOnProvider(t *testing.T) {
	t.Parallel()

	bindings := []store.Binding{
		{
			// active + open round on anthropic: in.
			Name: "b-web", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// active + open round, but a different provider: out.
			Name: "google-binding", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/google/gemini",
		},
		{
			// active but no round open: out.
			Name: "no-round", State: store.StateActive, RoundStartedAt: time.Time{},
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// open round on anthropic, but not active: out.
			Name: "needs-you", State: store.StateNeedsYou, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
		{
			// active + open round, but adopted (no candidate): out.
			Name: "adopted", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "",
		},
		{
			// active + open round on anthropic, named so the sort is checked: in.
			Name: "a-api", State: store.StateActive, RoundStartedAt: baseTime,
			BuilderCandidate: "agy/anthropic/sonnet",
		},
	}

	got := BindingsOnProvider(bindings, "anthropic")
	want := []string{"a-api", "b-web"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("BindingsOnProvider() = %v, want %v", got, want)
	}
}

// TestUnavailableByName: a candidate name is accepted, and the
// canonical candidate's provider is what the ledger records.
func TestUnavailableByName(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	provider, err := Unavailable(d, "claude-m", time.Time{}, "quota")
	if err != nil {
		t.Fatalf("Unavailable(claude-m): %v", err)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1", len(l.Entries))
	}
	if l.Entries[0].Subject != "test" {
		t.Errorf("Subject = %q, want test", l.Entries[0].Subject)
	}
}

// TestRolesMissingNoteWording pins the note's fix wording: a shipped path's fix
// is `relevo config agents --kind`, a custom path's is a by-hand install, and a
// mixed list carries both.
func TestRolesMissingNoteWording(t *testing.T) {
	t.Parallel()

	builderDefs := []string{"plan-executor", "researcher"}

	shipped := rolesMissingNote("builder", "claude", builderDefs, []string{".claude/agents/plan-executor.md"})
	if !strings.Contains(shipped, "run relevo config agents --kind claude") {
		t.Errorf("shipped note = %q, want the install fix", shipped)
	}
	if strings.Contains(shipped, "yourself") {
		t.Errorf("shipped note = %q, want no custom fix", shipped)
	}

	custom := rolesMissingNote("builder", "claude", []string{"my-executor"}, []string{".claude/agents/my-executor.md"})
	if !strings.Contains(custom, "run relevo config agents --kind claude for a custom agent relevo renders") || !strings.Contains(custom, "yourself") {
		t.Errorf("custom note = %q, want the custom fix", custom)
	}
	if strings.Contains(custom, "agent install") {
		t.Errorf("custom note = %q, want no install fix", custom)
	}

	mixed := rolesMissingNote("builder", "claude", []string{"plan-executor", "my-executor"},
		[]string{".claude/agents/plan-executor.md", ".claude/agents/my-executor.md"})
	if !strings.Contains(mixed, "run relevo config agents --kind claude") || !strings.Contains(mixed, "yourself") {
		t.Errorf("mixed note = %q, want both fixes", mixed)
	}
}

// TestUnavailableSanitizesReason pins that the reason a caller records is
// sanitised before it reaches the ledger.
func TestUnavailableSanitizesReason(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	if _, err := Unavailable(d, testClaudeRef, time.Time{}, "quota \x1b[2J"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	gates := Gates(d)
	if len(gates) == 0 {
		t.Fatal("Gates = none, want every candidate on the gated provider")
	}
	for _, g := range gates {
		if strings.ContainsRune(g.Note, '\x1b') {
			t.Errorf("Note = %q, want the control byte replaced", g.Note)
		}
		if !strings.Contains(g.Note, "\uFFFD") {
			t.Errorf("Note = %q, want a replacement rune", g.Note)
		}
	}
}
