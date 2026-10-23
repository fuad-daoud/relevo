package availability

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
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
	h, err := LoadHistory(d)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
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

// historyRowIDs is the identity of every availability_event row the shared file
// holds, for an assertion that a refused call recorded nothing. The rows are
// the history now, so this is the "the availability row is untouched" check the
// retired kv document answered.
func historyRowIDs(t *testing.T, d Deps) []string {
	t.Helper()
	rows, err := d.History.AvailabilityEvents(0)
	if err != nil {
		t.Fatalf("AvailabilityEvents: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
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

// TestUnavailableTakesTheSubjectsGateClearTakes pins that a gate takes what a
// clear takes: the halt texts tell a reader to run `relevo gate --clear
// <provider>`, so `gate <provider>` has to gate it rather than refuse it as an
// unknown candidate. A subject nothing knows is still refused, and the refusal
// is the caller's own argument rather than a defect.
func TestUnavailableTakesTheSubjectsGateClearTakes(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		name    string
		set     *candidate.Set
		ledger  Ledger
		subject string
		want    string
		wantKey string
		wantErr error
	}{
		{name: "bare provider", set: candidateSet(t, availTwoProviderJSON),
			subject: "cline-pass", want: "cline-pass", wantKey: "cline-pass"},
		{name: "candidate name", set: candidateSet(t, availTwoProviderJSON),
			subject: "claude-m", want: "test", wantKey: "test"},
		{name: "candidate token", set: candidateSet(t, availTwoProviderJSON),
			subject: "opencode/test/m", want: "test", wantKey: "test"},
		{name: "token with an effort suffix", set: candidateSet(t, availTwoProviderJSON),
			subject: "opencode/test/m#high", want: "test", wantKey: "test"},
		{name: "account gate key", set: candidateSet(t, availTwoProviderJSON),
			subject: "test@cp1", want: "test", wantKey: "test@cp1"},
		{name: "provider a gate left behind", set: candidateSet(t, testCandidatesJSON),
			ledger:  Ledger{Entries: []Entry{{Kind: RateLimited, Subject: "gone", At: baseTime, Source: "planner"}}},
			subject: "gone", want: "gone", wantKey: "gone"},
		{name: "unknown provider", set: candidateSet(t, availTwoProviderJSON),
			subject: "clinepass", wantErr: ErrUnknownProvider},
		{name: "unknown token", set: candidateSet(t, availTwoProviderJSON),
			subject: "opencode/test/nope", wantErr: candidate.ErrUnknownCandidate},
		{name: "malformed ref", set: candidateSet(t, availTwoProviderJSON),
			subject: "opencode/test", wantErr: candidate.ErrBadRef},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			d := testDepsWith(t, c.set)
			if err := SaveLedger(d.Gates, c.ledger); err != nil {
				t.Fatalf("SaveLedger: %v", err)
			}

			got, err := Unavailable(d, c.subject, baseTime.Add(time.Hour), "reason")
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("Unavailable(%q) err = %v, want %v", c.subject, err, c.wantErr)
				}
				if len(loadLedger(t, d).Entries) != len(c.ledger.Entries) {
					t.Errorf("a refused subject wrote to the ledger: %+v", loadLedger(t, d).Entries)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unavailable(%q): %v", c.subject, err)
			}
			if got != c.want {
				t.Errorf("provider = %q, want %q", got, c.want)
			}

			entries := loadLedger(t, d).Entries
			if len(entries) != len(c.ledger.Entries)+1 {
				t.Fatalf("got %d entries, want %d: %+v", len(entries), len(c.ledger.Entries)+1, entries)
			}
			last := entries[len(entries)-1]
			if last.Kind != RateLimited || last.Subject != c.wantKey {
				t.Errorf("entry = %+v, want RateLimited on %q", last, c.wantKey)
			}
		})
	}
}

// TestGateProviderIsResolveClearSubjectsRule pins that the two verbs share one
// accepted set rather than two that drift: every subject ResolveClearSubject
// accepts, GateProvider accepts, and it refuses exactly what it refuses.
func TestGateProviderIsResolveClearSubjectsRule(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, availTwoProviderJSON)
	for _, subject := range []string{
		"opencode/test/m", "opencode/test/m#high", "opencode-m",
		"test", "cline-pass", "test@cp1",
	} {
		clearProvider, cerr := ResolveClearSubject(set, Ledger{}, subject)
		if cerr != nil {
			t.Fatalf("ResolveClearSubject(%q): %v", subject, cerr)
		}
		gateProvider, _, gerr := GateProvider(set, Ledger{}, subject, nil)
		if gerr != nil {
			t.Fatalf("GateProvider(%q): %v", subject, gerr)
		}
		if gateProvider != clearProvider {
			t.Errorf("GateProvider(%q) = %q, clear = %q", subject, gateProvider, clearProvider)
		}
	}
	for _, subject := range []string{"clinepass", "opencode/test/nope", "opencode/test"} {
		if _, _, err := GateProvider(set, Ledger{}, subject, nil); err == nil {
			t.Errorf("GateProvider(%q) = nil, want a refusal", subject)
		}
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

	err := mutateLedgerLocked(d, nil, func(l Ledger) Ledger {
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

func TestGatedNoteKeepsKindSinceUntilOrder(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.Local)
	t.Cleanup(SetGateClock(func() time.Time { return now }))

	d := testDeps(t)
	d.Now = func() time.Time { return now }
	until := time.Date(2026, 9, 23, 14, 0, 0, 0, time.Local)
	if _, err := Unavailable(d, testClaudeRef, until, "5h window"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	want := "note: claude-m is gated: rate-limited since 10:00 until 14:00: 5h window; proceeding"
	if got := gatedNote(d, testClaudeRef); got != want {
		t.Errorf("gatedNote() = %q, want %q", got, want)
	}
}

func TestGatedNoteScheduledIsJustTheNote(t *testing.T) {
	d := windowDeps(t, `{"only":[{"from":"14:00Z","to":"00:00Z"}]}`, "2026-10-23T10:00:00Z")

	got := gatedNote(d, windowRef)
	if !strings.HasSuffix(got, "outside window 14:00Z-00:00Z; proceeding") {
		t.Errorf("gatedNote() = %q, want suffix %q", got, "outside window 14:00Z-00:00Z; proceeding")
	}
	if strings.Contains(got, "since") {
		t.Errorf("gatedNote() = %q, want no since clause", got)
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
	beforeHistory := historyRowIDs(t, d)

	if _, _, err := Available(d, "tset", ClearedByMasterMind); !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("Available(tset) err = %v, want ErrUnknownProvider", err)
	}

	if got := kvRowBytes(t, d, "ledger"); string(got) != string(beforeLedger) {
		t.Errorf("ledger row = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := historyRowIDs(t, d); !slices.Equal(got, beforeHistory) {
		t.Errorf("history rows = %v, want them untouched at %v", got, beforeHistory)
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
	beforeHistory := historyRowIDs(t, d)

	if _, _, err := Available(d, "test", "bogus"); err == nil {
		t.Fatal("Available(source=\"bogus\") err = nil, want an error")
	}

	if got := kvRowBytes(t, d, "ledger"); string(got) != string(beforeLedger) {
		t.Errorf("ledger row = %s, want it untouched at %s", got, beforeLedger)
	}
	if got := historyRowIDs(t, d); !slices.Equal(got, beforeHistory) {
		t.Errorf("history rows = %v, want them untouched at %v", got, beforeHistory)
	}
}

func TestHistoryFailureDoesNotFailTheLedger(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	// A shared file whose history INSERT fails while the ledger write succeeds:
	// the ledger write is the one that matters and must still land. The file is
	// closed, which is the failure a broken handle produces and the one
	// recordEvent prints and drops.
	broken := d.History
	if err := broken.Close(); err != nil {
		t.Fatalf("close the shared file: %v", err)
	}

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

	shipped := rolesMissingNote("builder", "claude", builderDefs, []string{".claude/agents/plan-executor.md"}, "")
	if !strings.Contains(shipped, "run relevo config agents --kind claude") {
		t.Errorf("shipped note = %q, want the install fix", shipped)
	}
	if strings.Contains(shipped, "yourself") {
		t.Errorf("shipped note = %q, want no custom fix", shipped)
	}

	custom := rolesMissingNote("builder", "claude", []string{"my-executor"}, []string{".claude/agents/my-executor.md"}, "")
	if !strings.Contains(custom, "run relevo config agents --kind claude for a custom agent relevo renders") || !strings.Contains(custom, "yourself") {
		t.Errorf("custom note = %q, want the custom fix", custom)
	}
	if strings.Contains(custom, "agent install") {
		t.Errorf("custom note = %q, want no install fix", custom)
	}

	mixed := rolesMissingNote("builder", "claude", []string{"plan-executor", "my-executor"},
		[]string{".claude/agents/plan-executor.md", ".claude/agents/my-executor.md"}, "")
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

// TestAvailableClearsEveryAccountGateByGroup: `gate --clear <group>` lifts the
// bare group entry and every group@account entry, and leaves other kinds alone.
func TestAvailableClearsEveryAccountGateByGroup(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	l := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "test", At: baseTime, Source: "planner"},
		{Kind: RateLimited, Subject: "test@cp1", At: baseTime, Source: "planner"},
		{Kind: RateLimited, Subject: "test@cp2", At: baseTime, Source: "planner"},
		{Kind: SpawnFailed, Subject: testClaudeRef, At: baseTime, Source: "relevo"},
	}}
	if err := SaveLedger(d.Gates, l); err != nil {
		t.Fatalf("SaveLedger: %v", err)
	}

	provider, removed, err := Available(d, "test", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 3 {
		t.Errorf("Available(test) = %q, %d, want test, 3", provider, removed)
	}

	got := loadLedger(t, d)
	if len(got.Entries) != 1 || got.Entries[0].Kind != SpawnFailed {
		t.Errorf("remaining ledger = %+v, want only the spawn failure", got.Entries)
	}
}

// TestAvailableClearsOneAccountGate: `gate --clear <group>@<account>` lifts only
// that key, and records the account on the history event.
func TestAvailableClearsOneAccountGate(t *testing.T) {
	t.Parallel()

	d := testDeps(t)
	l := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "test@cp1", At: baseTime, Source: "planner"},
		{Kind: RateLimited, Subject: "test@cp2", At: baseTime, Source: "planner"},
	}}
	if err := SaveLedger(d.Gates, l); err != nil {
		t.Fatalf("SaveLedger: %v", err)
	}

	provider, removed, err := Available(d, "test@cp1", ClearedByMasterMind)
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if provider != "test" || removed != 1 {
		t.Errorf("Available(test@cp1) = %q, %d, want test, 1", provider, removed)
	}

	got := loadLedger(t, d)
	if len(got.Entries) != 1 || got.Entries[0].Subject != "test@cp2" {
		t.Errorf("remaining ledger = %+v, want only test@cp2", got.Entries)
	}

	h := readHistory(t, d)
	if len(h.Events) != 1 {
		t.Fatalf("history events = %+v, want one", h.Events)
	}
	if h.Events[0].Provider != "test" || h.Events[0].Account != "cp1" {
		t.Errorf("clear event = %+v, want provider test account cp1", h.Events[0])
	}
}

// TestUnavailableRecordsAccountKeys: the keys a caller resolves are recorded as
// group@account entries, and mirrored into the history with the account.
func TestUnavailableRecordsAccountKeys(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	provider, err := Unavailable(d, testClaudeRef, time.Time{}, "window", "test@cp1")
	if err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if provider != "test" {
		t.Errorf("provider = %q, want test", provider)
	}

	l := loadLedger(t, d)
	if len(l.Entries) != 1 || l.Entries[0].Subject != "test@cp1" {
		t.Errorf("ledger = %+v, want one entry for test@cp1", l.Entries)
	}

	h := readHistory(t, d)
	if len(h.Events) != 1 || h.Events[0].Provider != "test" || h.Events[0].Account != "cp1" {
		t.Errorf("history = %+v, want provider test account cp1", h.Events)
	}
}

// TestGateAccountsForPicksTheUngatedAccount: the pure resolver names the first
// account of the pool no gate covers, and nil when no account serves the token.
func TestGateAccountsForPicksTheUngatedAccount(t *testing.T) {
	t.Parallel()

	set := account.Set{
		{Name: "cp1", Harness: account.OpenCode, Groups: []string{"cline-pass"}},
		{Name: "cp2", Harness: account.OpenCode, Groups: []string{"cline-pass"}},
	}
	token := "opencode/cline-pass/m"

	if names := GateAccountsFor(token, nil, set, nil, account.Failover); len(names) != 1 || names[0] != "cp1" {
		t.Errorf("GateAccountsFor = %v, want [cp1]", names)
	}
	if names := GateAccountsFor(token, nil, set, []string{"cline-pass@cp1"}, account.Failover); len(names) != 1 || names[0] != "cp2" {
		t.Errorf("GateAccountsFor with cp1 gated = %v, want [cp2]", names)
	}
	if names := GateAccountsFor(token, nil, account.Set{}, nil, account.Failover); names != nil {
		t.Errorf("GateAccountsFor with no accounts = %v, want nil", names)
	}
}

// TestGateWithAnImmediateResetKeepsTheLedgerValid: a lifted-now gate arrives
// with an until a hair before the write's At -- the scan that parsed the reset
// ran first -- and decode refuses "until precedes at", taking the whole ledger
// with it. Unavailable clamps such an until to At, so the entry reads as valid
// and expires immediately: a session limit recorded at its own reset time
// gates for zero time.
func TestGateWithAnImmediateResetKeepsTheLedgerValid(t *testing.T) {
	t.Parallel()

	d := testDeps(t)

	// A reset half an hour ago, recorded now, is a gate lifting now.
	if _, err := Unavailable(d, testClaudeRef, baseTime.Add(-30*time.Minute), "session limit reached"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// The ledger must still decode: without the clamp the entry carries
	// until precedes at and LoadLedger takes the whole ledger down.
	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	e := l.Entries[0]
	if !e.Until.Equal(e.At) {
		t.Errorf("entry Until = %v, At = %v, want Until == At (the clamp)", e.Until, e.At)
	}
	if !e.Until.Equal(baseTime) {
		t.Errorf("entry Until = %v, want %v", e.Until, baseTime)
	}

	// And the gate lifts at once: zero time, no live gate on the provider.
	if gates := Gates(d); len(gates) != 0 {
		t.Errorf("Gates = %+v, want none: a reset at now gates for zero time", gates)
	}
}

// TestReasonResetAtNowLiftsTheGate: a reason naming a clock reset at or just
// past now is the provider saying the limit is lifting right now, so the gate
// recorded from that reason ends at now -- zero time, valid in the ledger --
// rather than sitting until tomorrow.
func TestReasonResetAtNowLiftsTheGate(t *testing.T) {
	t.Parallel()

	now := baseTime
	reason := "RESOURCE_EXHAUSTED 429: session limit reached. Resets 15:33"

	until, ok := ResetFromReason(reason, now)
	if !ok {
		t.Fatal("ResetFromReason ok = false, want the lifted-now parse")
	}
	if !until.Equal(now) {
		t.Errorf("Until = %v, want %v: the reset at now lifts the gate now", until, now)
	}

	d := testDeps(t)
	if _, err := Unavailable(d, testClaudeRef, until, reason); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	// until == at is a valid entry -- decode refuses only until BEFORE at --
	// and it is already expired: the gate lifts now.
	l := loadLedger(t, d)
	if len(l.Entries) != 1 {
		t.Fatalf("got %d ledger entries, want 1: %+v", len(l.Entries), l.Entries)
	}
	if !l.Entries[0].Until.Equal(l.Entries[0].At) {
		t.Errorf("entry Until = %v, At = %v, want Until == At", l.Entries[0].Until, l.Entries[0].At)
	}
	if gates := Gates(d); len(gates) != 0 {
		t.Errorf("Gates = %+v, want none: the reason's reset lifts the gate now", gates)
	}
}

func TestSkillMissingTextIsTheNote(t *testing.T) {
	t.Parallel()
	if got := GateText(SkillMissing, time.Time{}, "skill x not found"); got != "skill x not found" {
		t.Errorf("GateText = %q, want the note", got)
	}
	if got := GateKindText(SkillMissing); got != "skill missing" {
		t.Errorf("GateKindText = %q", got)
	}
}
