package availability

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/db"
)

// testLedgerKV is a real t.TempDir() database, the medium the ledger lives in.
func testLedgerKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		until time.Time
		want  bool
	}{
		{"zero Until", time.Time{}, false},
		{"Until in future", now.Add(time.Minute), false},
		{"Until equals now", now, true},
		{"Until in past", now.Add(-time.Minute), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Entry{Until: tt.until}
			if got := e.Expired(now); got != tt.want {
				t.Errorf("Expired(%v) with Until %v = %v, want %v", now, tt.until, got, tt.want)
			}
		})
	}
}

func TestPruneAppendClearArePure(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	e1 := Entry{
		Kind:    SpawnFailed,
		Subject: "claude/anthropic/sonnet",
		At:      now.Add(-2 * time.Hour),
		Until:   now.Add(time.Hour),
		Source:  "relevo",
	}
	e2 := Entry{
		Kind:    RateLimited,
		Subject: "anthropic",
		At:      now.Add(-2 * time.Hour),
		Until:   now.Add(-time.Hour),
		Source:  "planner",
	}
	e3 := Entry{
		Kind:    SpawnFailed,
		Subject: "opencode/openrouter/deepseek",
		At:      now.Add(-time.Hour),
		Source:  "relevo",
	}

	origEntries := []Entry{e1, e2, e3}
	l := Ledger{Entries: origEntries}
	origCopy := append([]Entry(nil), origEntries...)

	// Prune drops expired entry e2
	pruned := l.Prune(now)
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Prune mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantPruned := []Entry{e1, e3}
	if !reflect.DeepEqual(pruned.Entries, wantPruned) {
		t.Errorf("Prune() = %v, want %v", pruned.Entries, wantPruned)
	}

	// Append adds e4 at the end without mutating original
	e4 := Entry{
		Kind:    RateLimited,
		Subject: "google",
		At:      now,
		Source:  "planner",
	}
	appended := l.Append(e4)
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Append mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantAppended := []Entry{e1, e2, e3, e4}
	if !reflect.DeepEqual(appended.Entries, wantAppended) {
		t.Errorf("Append() = %v, want %v", appended.Entries, wantAppended)
	}

	// Clear removes e1 matching SpawnFailed and subject
	cleared := l.Clear(SpawnFailed, "claude/anthropic/sonnet")
	if !reflect.DeepEqual(l.Entries, origCopy) {
		t.Fatalf("Clear mutated receiver entries: got %v, want %v", l.Entries, origCopy)
	}
	wantCleared := []Entry{e2, e3}
	if !reflect.DeepEqual(cleared.Entries, wantCleared) {
		t.Errorf("Clear() = %v, want %v", cleared.Entries, wantCleared)
	}
}

func TestClearMatchesKindAndSubject(t *testing.T) {
	e1 := Entry{Kind: RateLimited, Subject: "anthropic"}
	e2 := Entry{Kind: RateLimited, Subject: "google"}
	e3 := Entry{Kind: SpawnFailed, Subject: "anthropic"}

	l := Ledger{Entries: []Entry{e1, e2, e3}}
	got := l.Clear(RateLimited, "anthropic")

	want := []Entry{e2, e3}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Errorf("Clear(RateLimited, anthropic) = %v, want %v", got.Entries, want)
	}
}

func TestLoadLedgerMissingIsEmpty(t *testing.T) {
	kv := testLedgerKV(t)
	l, err := LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger() unexpected error: %v", err)
	}
	if len(l.Entries) != 0 {
		t.Errorf("LoadLedger() got %d entries, want 0", len(l.Entries))
	}
}

func TestSaveLedgerLoadLedgerRoundTrip(t *testing.T) {
	kv := testLedgerKV(t)
	tz := time.FixedZone("EST", -5*3600)
	orig := Ledger{
		Entries: []Entry{
			{
				Kind:    SpawnFailed,
				Subject: "claude/anthropic/sonnet",
				At:      time.Date(2026, 9, 11, 14, 0, 0, 0, tz),
				Until:   time.Date(2026, 9, 11, 14, 10, 0, 0, tz),
				Note:    "exit 1",
				Source:  "relevo",
				Binding: "cand-a",
			},
			{
				Kind:    RateLimited,
				Subject: "anthropic",
				At:      time.Date(2026, 9, 11, 15, 0, 0, 0, tz),
				Until:   time.Time{},
				Note:    "5-hour window hit",
				Source:  "planner",
				Binding: "cand-b",
			},
		},
	}

	if err := SaveLedger(kv, orig); err != nil {
		t.Fatalf("SaveLedger failed: %v", err)
	}

	loaded, err := LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger failed: %v", err)
	}

	norm := func(l Ledger) Ledger {
		entries := make([]Entry, len(l.Entries))
		for i, e := range l.Entries {
			e.At = e.At.UTC()
			if !e.Until.IsZero() {
				e.Until = e.Until.UTC()
			}
			entries[i] = e
		}
		return Ledger{Entries: entries}
	}

	if !reflect.DeepEqual(norm(orig), norm(loaded)) {
		t.Errorf("round-trip mismatch:\ngot:  %+v\nwant: %+v", norm(loaded), norm(orig))
	}
}

func TestLoadKVValidation(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantWhy string
	}{
		{
			name: "subject is empty",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "",
      "at": "2026-09-11T15:00:00Z",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "subject is empty",
		},
		{
			name: "at is zero",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "anthropic",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "at is zero",
		},
		{
			name: "until precedes at",
			json: `{
  "entries": [
    {
      "kind": "spawn_failed",
      "subject": "anthropic",
      "at": "2026-09-11T15:00:00Z",
      "until": "2026-09-11T14:00:00Z",
      "source": "relevo"
    }
  ]
}`,
			wantWhy: "until precedes at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kv := testLedgerKV(t)
			if err := kv.KVPut(ledgerKey, []byte(tt.json)); err != nil {
				t.Fatalf("KVPut: %v", err)
			}
			_, err := LoadLedger(kv)
			if err == nil {
				t.Fatalf("LoadLedger() expected error, got nil")
			}
			if !errors.Is(err, ErrBadEntry) {
				t.Errorf("LoadLedger() err = %v, want errors.Is(..., ErrBadEntry)", err)
			}
			if !strings.Contains(err.Error(), "entry 0") {
				t.Errorf("LoadLedger() err %q does not contain %q", err.Error(), "entry 0")
			}
			if !strings.Contains(err.Error(), tt.wantWhy) {
				t.Errorf("LoadLedger() err %q does not contain %q", err.Error(), tt.wantWhy)
			}
		})
	}
}

// TestLoadKVUnknownKindOrSourceIsPreserved pins that an entry whose kind
// or source this binary does not know is kept raw in Other instead of failing
// the whole ledger, so a record a newer relevo wrote survives a rollback.
func TestLoadKVUnknownKindOrSourceIsPreserved(t *testing.T) {
	unknownKind := `{"kind":"future_kind","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"relevo"}`
	unknownSource := `{"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}`
	known := `{"kind":"rate_limited","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"planner"}`

	kv := testLedgerKV(t)
	if err := kv.KVPut(ledgerKey, []byte(`{"entries":[`+unknownKind+`,`+unknownSource+`,`+known+`]}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	l, err := LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger() unexpected error: %v", err)
	}
	if len(l.Entries) != 1 || l.Entries[0].Kind != RateLimited {
		t.Fatalf("Entries = %+v, want the one known rate_limited entry", l.Entries)
	}
	if len(l.Other) != 2 {
		t.Fatalf("Other has %d entries, want 2: %v", len(l.Other), l.Other)
	}
	if !sameJSON(t, l.Other[0], unknownKind) || !sameJSON(t, l.Other[1], unknownSource) {
		t.Errorf("Other = %v, want the unknown entries preserved verbatim", l.Other)
	}
}

// TestSaveKVCarriesOtherThroughMutation is the survival test: Other must ride
// through LoadLedger -> Prune/Append -> SaveLedger untouched. Drop Other from
// SaveLedger and this test fails.
func TestSaveKVCarriesOtherThroughMutation(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	unknownKind := `{"kind":"future_kind","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"relevo"}`
	unknownSource := `{"kind":"spawn_failed","subject":"future/subject","at":"2026-09-11T15:00:00Z","source":"future_source"}`
	known := `{"kind":"rate_limited","subject":"anthropic","at":"2026-09-11T15:00:00Z","source":"planner"}`

	kv := testLedgerKV(t)
	if err := kv.KVPut(ledgerKey, []byte(`{"entries":[`+unknownKind+`,`+unknownSource+`,`+known+`]}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}

	l, err := LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger() unexpected error: %v", err)
	}

	appended := Entry{
		Kind:    RateLimited,
		Subject: "google",
		At:      now,
		Source:  "planner",
	}
	if err := SaveLedger(kv, l.Prune(now).Append(appended)); err != nil {
		t.Fatalf("SaveLedger() failed: %v", err)
	}

	reloaded, err := LoadLedger(kv)
	if err != nil {
		t.Fatalf("LoadLedger() after SaveLedger unexpected error: %v", err)
	}
	if len(reloaded.Entries) != 2 {
		t.Fatalf("Entries after SaveLedger = %+v, want the 2 known entries", reloaded.Entries)
	}
	if len(reloaded.Other) != 2 {
		t.Fatalf("Other after SaveLedger has %d entries, want 2: %v", len(reloaded.Other), reloaded.Other)
	}
	if !sameJSON(t, reloaded.Other[0], unknownKind) || !sameJSON(t, reloaded.Other[1], unknownSource) {
		t.Errorf("Other after SaveLedger = %v, want the unknown entries preserved verbatim", reloaded.Other)
	}
}

// sameJSON reports whether raw and want encode the same JSON value, ignoring
// formatting differences.
func sameJSON(t *testing.T, raw json.RawMessage, want string) bool {
	t.Helper()
	var got, exp any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal raw %q: %v", raw, err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatalf("unmarshal want %q: %v", want, err)
	}
	return reflect.DeepEqual(got, exp)
}

func TestGated(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{
		"agy/google/m",
		"claude/anthropic/opus",
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/m",
	}
	providerOf := func(token string) string {
		return strings.Split(token, "/")[1]
	}

	l := Ledger{
		Entries: []Entry{
			{Kind: RateLimited, Subject: "anthropic", At: now, Source: "planner"},
			{Kind: SpawnFailed, Subject: "agy/google/m", At: now, Until: now.Add(10 * time.Minute), Source: "relevo"},
			{Kind: SpawnFailed, Subject: "opencode/openrouter/z-ai/m", At: now, Until: now.Add(-time.Minute), Source: "relevo"},
			{Kind: SpawnFailed, Subject: "claude/anthropic/haiku", At: now, Until: now.Add(10 * time.Minute), Source: "relevo"},
		},
	}

	got := Gated(l, refs, providerOf, now)

	wantTokens := []string{"agy/google/m", "claude/anthropic/opus", "claude/anthropic/sonnet"}
	if len(got) != len(wantTokens) {
		t.Fatalf("Gated() returned %d gates, want %d: %+v", len(got), len(wantTokens), got)
	}
	for i, tok := range wantTokens {
		if got[i].Token != tok {
			t.Errorf("gate %d token = %q, want %q", i, got[i].Token, tok)
		}
	}
	if got[0].Kind != SpawnFailed {
		t.Errorf("gate 0 kind = %v, want SpawnFailed", got[0].Kind)
	}
	if got[1].Kind != RateLimited || got[2].Kind != RateLimited {
		t.Errorf("gates 1,2 kind = %v, %v, want RateLimited", got[1].Kind, got[2].Kind)
	}

	if empty := Gated(Ledger{}, refs, providerOf, now); empty != nil {
		t.Errorf("Gated() on empty ledger = %v, want nil", empty)
	}
}

func TestGatedOrdersByTokenThenSince(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{"claude/anthropic/sonnet"}
	providerOf := func(token string) string {
		return strings.Split(token, "/")[1]
	}

	l := Ledger{
		Entries: []Entry{
			{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet", At: now, Source: "relevo"},
			{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet", At: now.Add(-time.Hour), Source: "relevo"},
		},
	}

	got := Gated(l, refs, providerOf, now)
	if len(got) != 2 {
		t.Fatalf("Gated() returned %d gates, want 2: %+v", len(got), got)
	}
	if !got[0].Since.Equal(now.Add(-time.Hour)) || !got[1].Since.Equal(now) {
		t.Errorf("gates not ordered by Since: got %v, %v", got[0].Since, got[1].Since)
	}
}

// clinePassPool is a two-account pool for the group cline-pass, the shape every
// account-pool assertion below needs.
func clinePassPool() account.Set {
	return account.Set{
		{Name: "cp1", Harness: account.OpenCode, Groups: []string{"cline-pass"}},
		{Name: "cp2", Harness: account.OpenCode, Groups: []string{"cline-pass"}},
	}
}

// TestGatedAccountPool pins the rule that a group@account entry gates its group
// only once every account in the pool is gated: until then the pick has
// somewhere to go, so the token is not gated.
func TestGatedAccountPool(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{"opencode/cline-pass/m"}
	providerOf := func(token string) string { return strings.Split(token, "/")[1] }
	set := clinePassPool()

	partial := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "cline-pass@cp1", At: now, Source: "planner"},
	}}
	if got := Gated(partial, refs, providerOf, now, set); len(got) != 0 {
		t.Errorf("Gated() on a partially gated pool = %+v, want no gates", got)
	}

	full := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "cline-pass@cp1", At: now, Source: "planner"},
		{Kind: RateLimited, Subject: "cline-pass@cp2", At: now, Source: "planner"},
	}}
	got := Gated(full, refs, providerOf, now, set)
	if len(got) != 2 {
		t.Fatalf("Gated() on a fully gated pool returned %d gates, want one per account entry: %+v", len(got), got)
	}
	for i, g := range got {
		if g.Token != refs[0] || g.Kind != RateLimited {
			t.Errorf("gate %d = %+v, want RateLimited for %s", i, g, refs[0])
		}
	}
}

// TestGatedBareGroupWithAccounts: a bare group entry still gates every
// candidate of the group, whatever the pool holds.
func TestGatedBareGroupWithAccounts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	refs := []string{"opencode/cline-pass/m"}
	providerOf := func(token string) string { return strings.Split(token, "/")[1] }

	l := Ledger{Entries: []Entry{
		{Kind: RateLimited, Subject: "cline-pass", At: now, Source: "planner"},
	}}
	got := Gated(l, refs, providerOf, now, clinePassPool())
	if len(got) != 1 || got[0].Token != refs[0] {
		t.Errorf("Gated() with a bare group entry = %+v, want one gate for %s", got, refs[0])
	}
}

// TestOldFormatLedgerWithAccounts: an old-format ledger (bare group subjects,
// no "@") decodes and behaves unchanged even when accounts are configured.
func TestOldFormatLedgerWithAccounts(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	doc := []byte(`{"entries":[{"kind":"rate_limited","subject":"cline-pass","at":"2026-09-11T15:00:00Z","source":"planner"}]}`)
	l, err := decode(doc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	refs := []string{"opencode/cline-pass/m"}
	providerOf := func(token string) string { return strings.Split(token, "/")[1] }
	got := Gated(l, refs, providerOf, now, clinePassPool())
	if len(got) != 1 || got[0].Token != refs[0] {
		t.Errorf("Gated() on an old-format ledger = %+v, want one gate for %s", got, refs[0])
	}
}
