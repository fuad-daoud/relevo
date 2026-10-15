package availability

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// A gates test needs a real machine-local file: the ledger row, the write
// stamps and the export signal all live in it, and a kv double could not prove
// that one write reaches all three.
func gatesFixture(t *testing.T) (*db.DB, *GatesDoc) {
	t.Helper()
	local := openGatesDB(t)
	return local, OpenGates(local, func() time.Time { return baseTime })
}

// openGatesDB is one machine-local file, closed when the test ends.
func openGatesDB(t *testing.T) *db.DB {
	t.Helper()
	local, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = local.Close() })
	return local
}

// rateLimit is one live rate-limit entry on subject.
func rateLimit(subject string, at, until time.Time) Entry {
	return Entry{Kind: RateLimited, Subject: subject, At: at, Until: until, Source: "planner"}
}

// gatesDeps is the runtime a gates write runs against: the fixture's file as
// the ledger and as the file the stamps and the signal live in, plus a counter
// for the attempts the write asks for.
func gatesDeps(t *testing.T, local *db.DB) (Deps, *int) {
	t.Helper()
	synced := 0
	d := testDeps(t)
	d.Gates = local
	d.Local = local
	d.SyncNow = func() { synced++ }
	return d, &synced
}

// seedLedger writes a chosen ledger straight into the file, so a test starts
// from one it decided rather than from one a write path produced.
func seedLedger(t *testing.T, local *db.DB, entries []Entry, other ...string) {
	t.Helper()
	l := Ledger{Entries: entries}
	for _, raw := range other {
		l.Other = append(l.Other, json.RawMessage(raw))
	}
	if err := local.KVPut(ledgerKey, mustLedger(t, l)); err != nil {
		t.Fatalf("seed the ledger: %v", err)
	}
}

// mustLedger encodes a ledger the way SaveLedger does, so a seeded row decodes
// as the entries it names and the raw ones survive verbatim.
func mustLedger(t *testing.T, l Ledger) []byte {
	t.Helper()
	var entries []json.RawMessage
	for _, e := range l.Entries {
		raw, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal entry: %v", err)
		}
		entries = append(entries, raw)
	}
	entries = append(entries, l.Other...)
	doc, err := json.MarshalIndent(struct {
		Entries []json.RawMessage `json:"entries"`
	}{Entries: entries}, "", "  ")
	if err != nil {
		t.Fatalf("marshal ledger: %v", err)
	}
	return doc
}

// applyIn is one arriving item, applied the way the importer applies it: in the
// importer's own transaction, whose outcome the caller inspects.
func applyIn(t *testing.T, local *db.DB, doc *GatesDoc, key string, value json.RawMessage) (refusal, err error) {
	t.Helper()
	err = local.Tx(func(tx *db.Tx) error {
		refusal, err = doc.ApplyIn(tx, key, value, "peer")
		return err
	})
	return refusal, err
}

// gateValue reads one subject's exported value back as entries.
func gateValue(t *testing.T, items map[string]json.RawMessage, subject string) []Entry {
	t.Helper()
	raw, ok := items[subject]
	if !ok {
		t.Fatalf("Items() has no key %q, want one (keys %v)", subject, sortedGateKeys(items))
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decode %q's value: %v", subject, err)
	}
	return entries
}

// loadGatesLedger reads the ledger straight out of the file, so a test never
// asserts through the handle it also writes.
func loadGatesLedger(t *testing.T, local *db.DB) Ledger {
	t.Helper()
	l, err := LoadLedger(local)
	if err != nil {
		t.Fatalf("LoadLedger: %v", err)
	}
	return l
}

// subjectEntries is the ledger's entries naming subject, in order.
func subjectEntries(l Ledger, subject string) []Entry {
	var out []Entry
	for _, e := range l.Entries {
		if e.Subject == subject {
			out = append(out, e)
		}
	}
	return out
}

// rateLimitedOf is the subject's rate-limit entries, in order.
func rateLimitedOf(l Ledger, subject string) []Entry {
	var out []Entry
	for _, e := range subjectEntries(l, subject) {
		if e.Kind == RateLimited {
			out = append(out, e)
		}
	}
	return out
}

// spawnFailuresOf is the subject's spawn-failure entries, in order.
func spawnFailuresOf(l Ledger, subject string) []Entry {
	var out []Entry
	for _, e := range subjectEntries(l, subject) {
		if e.Kind == SpawnFailed {
			out = append(out, e)
		}
	}
	return out
}

// gateSignals is the export signals naming the gates document. The fixture's
// store holds a config document too, so the count is over gates signals alone.
func gateSignals(t *testing.T, local *db.DB) []string {
	t.Helper()
	signals, err := local.DocSignals()
	if err != nil {
		t.Fatalf("DocSignals: %v", err)
	}
	var out []string
	for _, sig := range signals {
		if sig.Doc == GatesName {
			out = append(out, sig.Doc)
		}
	}
	return out
}

// sameOtherEntry reports whether the stored raw entry still says what the
// seeded one said. The ledger writer re-indents a raw entry as it writes it
// back, so the two are compared decoded rather than byte for byte.
func sameOtherEntry(t *testing.T, stored json.RawMessage, want string) bool {
	t.Helper()
	var got, expect any
	if err := json.Unmarshal(stored, &got); err != nil {
		t.Fatalf("decode the stored raw entry: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &expect); err != nil {
		t.Fatalf("decode the seeded raw entry: %v", err)
	}
	return fmt.Sprint(got) == fmt.Sprint(expect)
}

// sortedGateKeys is the document's keys in order, for a failure message.
func sortedGateKeys(items map[string]json.RawMessage) []string {
	out := make([]string, 0, len(items))
	for k := range items {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// gatesValue is one subject's arriving entries, as a peer would write them.
func gatesValue(t *testing.T, subject string, entries ...Entry) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal the arriving value: %v", err)
	}
	return raw
}

// TestGatesItemsGroupBySubject pins that the document is one key per subject
// holding that subject's live rate-limit entries, and that an expired entry is
// not one of them: a gate nobody can act on any more is not worth another
// machine's round trip, and exporting one would keep a key alive forever.
func TestGatesItemsGroupBySubject(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)

	later := baseTime.Add(time.Minute)
	seedLedger(t, local, []Entry{
		rateLimit("test", baseTime, later),
		rateLimit("test", later, later.Add(time.Hour)),
		rateLimit("other", baseTime.Add(-2*time.Hour), baseTime.Add(-time.Second)),
		{Kind: SpawnFailed, Subject: testAgyRef, At: baseTime, Until: later, Source: "relevo"},
	})

	items, err := doc.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if _, held := items["other"]; held {
		t.Errorf("Items() = %v, want the expired subject gone", sortedGateKeys(items))
	}
	if _, held := items[testAgyRef]; held {
		t.Errorf("Items() = %v, want no key for the spawn-failed token", sortedGateKeys(items))
	}
	got := gateValue(t, items, "test")
	if len(got) != 2 {
		t.Fatalf("the subject's entries = %+v, want both live ones", got)
	}
	if !got[0].At.Equal(baseTime) || !got[1].At.Equal(later) {
		t.Errorf("the subject's entries = %+v, want them in ledger order", got)
	}
}

// TestSpawnFailuresNeverLeaveTheMachine pins the rule that separates the two
// kinds: a spawn failure is about this binary on this host and travels nowhere,
// while a rate limit is about the provider and travels. Both directions are
// checked, because a document that exported spawn failures and a document that
// dropped the rate limits with them would each pass the other half.
func TestSpawnFailuresNeverLeaveTheMachine(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)

	seedLedger(t, local, []Entry{
		rateLimit("test", baseTime, baseTime.Add(time.Hour)),
		{Kind: SpawnFailed, Subject: testAgyRef, At: baseTime, Source: "relevo"},
	}, `{"kind":"quota_shared","subject":"test","at":"2030-01-01T00:00:00Z"}`)

	items, err := doc.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("Items() = %v, want only the rate-limited subject", sortedGateKeys(items))
	}
	if _, held := items[testAgyRef]; held {
		t.Errorf("Items() exported the spawn-failed token %q", testAgyRef)
	}

	// The other direction: an entry naming a spawn failure is refused rather
	// than applied, so no peer can make this machine gate a candidate for a
	// start that failed over there.
	peer := gatesValue(t, testAgyRef, Entry{
		Kind: SpawnFailed, Subject: testAgyRef, At: baseTime, Source: "relevo"})
	refusal, err := applyIn(t, local, doc, testAgyRef, peer)
	if refusal == nil && err == nil {
		t.Fatal("ApplyIn accepted a spawn-failure entry from a peer")
	}
	if l := loadGatesLedger(t, local); len(subjectEntries(l, testAgyRef)) != 1 {
		t.Errorf("the ledger after the refusal = %+v, want only this machine's own entry", l.Entries)
	}
}

// TestGatesApplyReplacesSubjectKeepsOther pins that an arriving value replaces
// the subject's rate-limit entries wholesale and touches nothing else. The two
// kinds it must keep are the ones this machine cannot reconstruct from the
// wire: its own spawn failures, and an entry a newer binary wrote that this one
// has no reader for.
func TestGatesApplyReplacesSubjectKeepsOther(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)
	raw := `{"kind":"quota_shared","subject":"test","at":"2030-01-01T00:00:00Z"}`
	seedLedger(t, local, []Entry{
		rateLimit("test", baseTime, baseTime.Add(time.Hour)),
		rateLimit("test", baseTime.Add(time.Minute), baseTime.Add(2*time.Hour)),
		rateLimit("other", baseTime, baseTime.Add(time.Hour)),
		{Kind: SpawnFailed, Subject: testAgyRef, At: baseTime, Source: "relevo"},
		// A spawn failure on the very subject the arriving value replaces: the
		// kind that does not travel can still share a subject with one that
		// does, and replacing the subject's entries must not take it along.
		{Kind: SpawnFailed, Subject: "test", At: baseTime, Source: "relevo"},
	}, raw)

	replacement := baseTime.Add(3 * time.Hour)
	if err := doc.Apply("test", gatesValue(t, "test", rateLimit("test", replacement, replacement.Add(time.Hour)))); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	l := loadGatesLedger(t, local)
	if got := rateLimitedOf(l, "test"); len(got) != 1 || !got[0].At.Equal(replacement) {
		t.Errorf("the subject's rate-limit entries after the apply = %+v, want only the arriving one", got)
	}
	if got := spawnFailuresOf(l, "test"); len(got) != 1 {
		t.Errorf("the subject's spawn-failure entries = %+v, want the local one kept", got)
	}
	if got := subjectEntries(l, "other"); len(got) != 1 {
		t.Errorf("the other subject's entries = %+v, want them untouched", got)
	}
	if got := subjectEntries(l, testAgyRef); len(got) != 1 || got[0].Kind != SpawnFailed {
		t.Errorf("the spawn-failed token's entries = %+v, want the local one kept", got)
	}
	if len(l.Other) != 1 || !sameOtherEntry(t, l.Other[0], raw) {
		t.Errorf("Other = %s, want the newer binary's entry kept verbatim", l.Other)
	}
}

// TestGatesApplyRefusesUnknownKind pins that an arriving entry of a kind, a
// source or a shape this build has no rule for is refused rather than applied.
// The ledger keeps unknown kinds raw so a newer binary's records survive a
// rollback; applying one as though it were a gate would do the opposite, acting
// on a peer's rule this build has never read.
func TestGatesApplyRefusesUnknownKind(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)
	seedLedger(t, local, []Entry{rateLimit("test", baseTime, baseTime.Add(time.Hour))})

	for name, value := range map[string]json.RawMessage{
		"an unknown kind":     json.RawMessage(`[{"kind":"quota_shared","subject":"test","at":"2030-01-01T00:00:00Z","source":"planner"}]`),
		"an unknown source":   json.RawMessage(`[{"kind":"rate_limited","subject":"test","at":"2030-01-01T00:00:00Z","source":"newer"}]`),
		"another subject":     json.RawMessage(`[{"kind":"rate_limited","subject":"elsewhere","at":"2030-01-01T00:00:00Z","source":"planner"}]`),
		"an until before at":  json.RawMessage(`[{"kind":"rate_limited","subject":"test","at":"2030-01-01T00:00:00Z","until":"2029-01-01T00:00:00Z","source":"planner"}]`),
		"a zero at":           json.RawMessage(`[{"kind":"rate_limited","subject":"test","at":"0001-01-01T00:00:00Z","source":"planner"}]`),
		"an object not array": json.RawMessage(`{"kind":"rate_limited"}`),
		"malformed json":      json.RawMessage(`not json`),
	} {
		refusal, err := applyIn(t, local, doc, "test", value)
		if refusal == nil && err == nil {
			t.Errorf("ApplyIn accepted %s", name)
		}
		if got := subjectEntries(loadGatesLedger(t, local), "test"); len(got) != 1 || !got[0].At.Equal(baseTime) {
			t.Fatalf("the ledger after refusing %s = %+v, want it unchanged", name, got)
		}
	}
}

// TestGatesClearIsATombstone pins what a tombstone means here: it lifts the
// subject's rate-limit gate everywhere, and it lifts nothing else. A peer's
// clear must not take this machine's spawn failures with it, and must not
// erase an entry the ledger holds for a newer binary.
func TestGatesClearIsATombstone(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)
	seedLedger(t, local, []Entry{
		rateLimit("test", baseTime, baseTime.Add(time.Hour)),
		rateLimit("test", baseTime.Add(time.Minute), baseTime.Add(2*time.Hour)),
		rateLimit("other", baseTime, baseTime.Add(time.Hour)),
		{Kind: SpawnFailed, Subject: testAgyRef, At: baseTime, Source: "relevo"},
		{Kind: SpawnFailed, Subject: "test", At: baseTime, Source: "relevo"},
	}, `{"kind":"quota_shared","subject":"test","at":"2030-01-01T00:00:00Z"}`)

	if refusal, err := applyIn(t, local, doc, "test", nil); refusal != nil || err != nil {
		t.Fatalf("ApplyIn(nil) = (%v, %v), want the clear to land", refusal, err)
	}

	l := loadGatesLedger(t, local)
	if got := rateLimitedOf(l, "test"); len(got) != 0 {
		t.Errorf("the cleared subject's rate-limit entries = %+v, want none", got)
	}
	if got := spawnFailuresOf(l, "test"); len(got) != 1 {
		t.Errorf("the cleared subject's spawn-failure entries = %+v, want the local one kept", got)
	}
	if got := subjectEntries(l, "other"); len(got) != 1 {
		t.Errorf("the other subject's entries = %+v, want them kept", got)
	}
	if got := subjectEntries(l, testAgyRef); len(got) != 1 {
		t.Errorf("the spawn-failed token's entries = %+v, want them kept", got)
	}
	if len(l.Other) != 1 {
		t.Errorf("Other = %s, want the newer binary's entry kept", l.Other)
	}

	items, err := doc.Items()
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	if _, held := items["test"]; held {
		t.Errorf("Items() = %v, want the cleared subject gone", sortedGateKeys(items))
	}
}

// TestGatesOversizeValueIsRefused pins the cap on one exported value. The cap
// sits above any real ledger and below the point where an entry could be a
// payload rather than a gate, and it is per doc rather than per entry because
// the value that crosses the log is the whole array.
func TestGatesOversizeValueIsRefused(t *testing.T) {
	t.Parallel()
	_, doc := gatesFixture(t)

	huge := Entry{Kind: RateLimited, Subject: "test", At: baseTime, Source: "planner",
		Note: strings.Repeat("x", gatesValueMax)}
	items, err := gateItems(Ledger{Entries: []Entry{huge}})
	if !errors.Is(err, db.ErrInvalid) {
		t.Fatalf("gateItems on a %d byte value = (%v, %v), want a refusal", gatesValueMax, items, err)
	}
	if len(items) != 0 {
		t.Errorf("gateItems returned %v alongside its refusal", sortedGateKeys(items))
	}

	// The same cap on the way in, so a peer cannot hand over an entry the
	// exporter would then refuse to write back out.
	if err := doc.Apply("test", gatesValue(t, "test", Entry{
		Kind: RateLimited, Subject: "test", At: baseTime, Source: "planner",
		Note: strings.Repeat("y", gatesValueMax),
	})); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("Apply of an over-cap value = %v, want a refusal", err)
	}
}

// TestGateWriteStampsSignalsAndSyncsNow pins that recording a rate limit as a
// person does leaves all three marks behind: a write stamp naming the subject,
// the export signal the exporter watches, and a request for an attempt now.
// Each answers a different question -- when the key was written here, that it
// changed at all, and whether anyone is going to send it soon -- so a write
// leaving one of them behind is only half exported.
func TestGateWriteStampsSignalsAndSyncsNow(t *testing.T) {
	t.Parallel()
	local := openGatesDB(t)
	d, synced := gatesDeps(t, local)

	if _, err := Unavailable(d, "test", baseTime.Add(time.Hour), "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	if *synced != 1 {
		t.Errorf("SyncNow called %d times, want once for the gate write", *synced)
	}
	if got := gateSignals(t, local); len(got) != 1 {
		t.Errorf("the gates signals = %+v, want one", got)
	}
	if _, ok, err := OpenGates(local, nil).ItemWriteStamp("test"); err != nil || !ok {
		t.Errorf("the subject's write stamp = (ok %v, %v), want one", ok, err)
	}
	if _, ok, err := OpenGates(local, nil).ItemWriteStamp("other"); err != nil || ok {
		t.Errorf("an untouched subject's write stamp = (ok %v, %v), want none", ok, err)
	}
}

// TestSeedGateWriteIsUnstamped pins the other side of the same rule: a write
// that is not a person's edit of a gate leaves the ledger changed and every
// sync mark alone. The spawn failure is the case that matters, because it is
// recorded on the ordinary path and its kind is the one that never travels.
func TestSeedGateWriteIsUnstamped(t *testing.T) {
	t.Parallel()
	local := openGatesDB(t)
	d, synced := gatesDeps(t, local)

	RecordSpawnFailure(d, testAgyRef, "webshop", fmt.Errorf("boom"))

	if *synced != 0 {
		t.Errorf("SyncNow called %d times for a spawn failure, want none", *synced)
	}
	if got := gateSignals(t, local); len(got) != 0 {
		t.Errorf("the gates signals = %+v, want none for a spawn failure", got)
	}
	if _, ok, err := OpenGates(local, nil).ItemWriteStamp(testAgyRef); err != nil || ok {
		t.Errorf("the token's write stamp = (ok %v, %v), want none", ok, err)
	}
	if l := loadGatesLedger(t, local); len(subjectEntries(l, testAgyRef)) != 1 {
		t.Errorf("the ledger = %+v, want the spawn failure recorded", l.Entries)
	}
}

// TestGateClearStampsTheClearedSubjects pins the other direction of the same
// rule for a person's clear: the subjects it lifted are stamped, so the peers
// that still hold those gates are told to lift them too.
func TestGateClearStampsTheClearedSubjects(t *testing.T) {
	t.Parallel()
	local := openGatesDB(t)
	d, synced := gatesDeps(t, local)

	if _, err := Unavailable(d, "test", baseTime.Add(time.Hour), "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}
	if _, removed, err := Available(d, "test", ClearedByMasterMind); err != nil || removed == 0 {
		t.Fatalf("Available = (%d, %v), want the gate lifted", removed, err)
	}

	doc := OpenGates(local, nil)
	if _, ok, err := doc.ItemWriteStamp("test"); err != nil || !ok {
		t.Errorf("the cleared subject's write stamp = (ok %v, %v), want one", ok, err)
	}
	if *synced != 2 {
		t.Errorf("SyncNow called %d times, want one for the set and one for the clear", *synced)
	}
}

// TestAppliedGateGatesTheRunningDaemon pins that a gate applied from a peer
// gates this machine's own candidate switching on the next decision, with no
// restart and no invalidation step: the ledger is read from the file on every
// decision rather than held in memory, so an applied entry is a gate the moment
// its transaction commits. The assertion therefore reads through the projection
// the picker uses, not through the document that wrote it.
func TestAppliedGateGatesTheRunningDaemon(t *testing.T) {
	t.Parallel()
	local, doc := gatesFixture(t)
	set := candidateSet(t, testTwoProviderJSON)
	d, synced := gatesDeps(t, local)
	d.Candidates = set

	if gates := LedgerGates(d, set.Refs()); len(gates) != 0 {
		t.Fatalf("the machine starts gated by %+v", gates)
	}

	until := baseTime.Add(time.Hour)
	value := gatesValue(t, "other", rateLimit("other", baseTime, until))
	if refusal, err := applyIn(t, local, doc, "other", value); refusal != nil || err != nil {
		t.Fatalf("ApplyIn = (%v, %v), want the gate to land", refusal, err)
	}

	gates := LedgerGates(d, set.Refs())
	if len(gates) != 1 {
		t.Fatalf("the gates after the apply = %+v, want the provider gated", gates)
	}
	if gates[0].Token != testOtherProviderRef || gates[0].Kind != RateLimited {
		t.Errorf("the gate = %+v, want %q rate-limited", gates[0], testOtherProviderRef)
	}
	if *synced != 0 {
		t.Errorf("SyncNow called %d times by an arriving apply, want none: it is not a local edit", *synced)
	}
}

// TestNoConfigSectionIsADocStampPrefix pins that the write stamps of every
// document can share one table with the config's bare keys. They can, because a
// document's stamps are prefixed and the two sets cannot meet: no config
// section is named after a document, so a section can never be read back as a
// document's stamp key or the reverse. The prefixes are the documents this sync
// round writes stamps for.
func TestNoConfigSectionIsADocStampPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{GatesName, "consent", "session_consent"} {
		for _, sec := range config.Sections {
			if string(sec) == prefix || strings.HasPrefix(string(sec), prefix+"/") {
				t.Errorf("the config section %q meets the document stamp prefix %q", sec, prefix)
			}
		}
	}
}
