package availability

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// historyNow is the fixed clock every test in this package reasons from.
var historyNow = time.Date(2026, 9, 11, 21, 15, 0, 0, time.UTC)

// testHistoryKV is a real t.TempDir() database, the medium the history lives in.
func testHistoryKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestRoundTrip(t *testing.T) {
	kv := testHistoryKV(t)

	h := History{}.
		Append(Event{At: historyNow, Kind: RateLimited, Provider: "anthropic", Source: "planner", Note: "5h"}).
		Append(Event{At: historyNow.Add(time.Minute), Kind: SpawnFailed, Provider: "anthropic", Token: "claude/anthropic/sonnet", Source: "relevo", Binding: "webshop"})

	if err := SaveHistory(kv, h); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}

	got, err := loadHistory(kv)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(got.Events) != len(h.Events) {
		t.Fatalf("got %d events, want %d: %+v", len(got.Events), len(h.Events), got.Events)
	}
	for i, e := range got.Events {
		want := h.Events[i]
		if !e.At.Equal(want.At) {
			t.Errorf("event %d At = %v, want %v", i, e.At, want.At)
		}
		if e.Kind != want.Kind || e.Provider != want.Provider || e.Token != want.Token ||
			e.Source != want.Source || e.Binding != want.Binding || e.Note != want.Note {
			t.Errorf("event %d = %+v, want %+v", i, e, want)
		}
	}
}

func TestLoadKVMissingPath(t *testing.T) {
	kv := testHistoryKV(t)

	got, err := loadHistory(kv)
	if err != nil {
		t.Fatalf("loadHistory(missing): %v", err)
	}
	if len(got.Events) != 0 {
		t.Errorf("got %d events, want 0: %+v", len(got.Events), got.Events)
	}
}

func TestPruneWindow(t *testing.T) {
	kept := Event{At: historyNow.Add(-HistoryRetainWindow), Kind: RateLimited, Provider: "test", Source: "planner"}
	dropped := Event{At: historyNow.Add(-HistoryRetainWindow - time.Second), Kind: RateLimited, Provider: "test", Source: "planner"}

	h := History{}.Append(kept).Append(dropped)
	pruned := h.Prune(historyNow)

	if len(pruned.Events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(pruned.Events), pruned.Events)
	}
	if !pruned.Events[0].At.Equal(kept.At) {
		t.Errorf("kept event At = %v, want %v", pruned.Events[0].At, kept.At)
	}
}

func TestFromEntry(t *testing.T) {
	rateLimited := Entry{Kind: RateLimited, Subject: "anthropic", At: historyNow, Source: "planner", Note: "5h"}
	got := FromEntry(rateLimited, func(string) string { return "" })
	want := Event{Provider: "anthropic", Token: "", Kind: RateLimited, Source: "planner", Note: "5h", At: historyNow}
	if got != want {
		t.Errorf("FromEntry(rate_limited) = %+v, want %+v", got, want)
	}

	spawnFailed := Entry{Kind: SpawnFailed, Subject: "claude/anthropic/sonnet", Binding: "webshop", Source: "relevo"}
	got = FromEntry(spawnFailed, func(string) string { return "anthropic" })
	want = Event{Provider: "anthropic", Token: "claude/anthropic/sonnet", Kind: SpawnFailed, Binding: "webshop", Source: "relevo"}
	if got != want {
		t.Errorf("FromEntry(spawn_failed) = %+v, want %+v", got, want)
	}

	accountGate := Entry{Kind: RateLimited, Subject: "anthropic@work", At: historyNow, Source: "planner", Note: "5h"}
	got = FromEntry(accountGate, func(string) string { return "" })
	want = Event{Provider: "anthropic", Account: "work", Kind: RateLimited, Source: "planner", Note: "5h", At: historyNow}
	if got != want {
		t.Errorf("FromEntry(rate_limited group@account) = %+v, want %+v", got, want)
	}
}

func TestHourCounts(t *testing.T) {
	at2130 := time.Date(2026, 9, 11, 21, 30, 0, 0, time.UTC)
	at2159 := time.Date(2026, 9, 11, 21, 59, 0, 0, time.UTC)
	at2200 := time.Date(2026, 9, 11, 22, 0, 0, 0, time.UTC)
	otherProvider := time.Date(2026, 9, 11, 21, 10, 0, 0, time.UTC)
	otherKind := time.Date(2026, 9, 11, 21, 20, 0, 0, time.UTC)

	h := History{}.
		Append(Event{At: at2130, Kind: RateLimited, Provider: "test"}).
		Append(Event{At: at2159, Kind: RateLimited, Provider: "test"}).
		Append(Event{At: at2200, Kind: RateLimited, Provider: "test"}).
		Append(Event{At: otherProvider, Kind: RateLimited, Provider: "other"}).
		Append(Event{At: otherKind, Kind: SpawnFailed, Provider: "test"})

	counts := HourCounts(h, "test", RateLimited, time.UTC)
	for hour, c := range counts {
		want := 0
		switch hour {
		case 21:
			want = 2
		case 22:
			want = 1
		}
		if c != want {
			t.Errorf("UTC counts[%d] = %d, want %d", hour, c, want)
		}
	}

	plus1 := time.FixedZone("plus1", 3600)
	counts = HourCounts(h, "test", RateLimited, plus1)
	for hour, c := range counts {
		want := 0
		switch hour {
		case 22:
			want = 2
		case 23:
			want = 1
		}
		if c != want {
			t.Errorf("plus1 counts[%d] = %d, want %d", hour, c, want)
		}
	}
}

// TestBlockedDurations: how long a block lasted is At - Since on the Cleared
// event. A Cleared event with no Since (an older file, or a clear that
// removed nothing), a RateLimited event and another provider's clear are all
// skipped.
func TestBlockedDurations(t *testing.T) {
	h := History{}.
		Append(Event{At: historyNow, Kind: RateLimited, Provider: "test"}).
		Append(Event{At: historyNow, Kind: Cleared, Provider: "test"}).
		Append(Event{At: historyNow, Kind: Cleared, Provider: "other", Since: historyNow.Add(-2 * time.Hour)}).
		Append(Event{At: historyNow, Kind: Cleared, Provider: "test", Since: historyNow.Add(-5 * time.Hour)})

	got := BlockedDurations(h, "test")
	want := []time.Duration{5 * time.Hour}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("BlockedDurations(test) = %v, want %v", got, want)
	}

	if got := BlockedDurations(h, "nobody"); got != nil {
		t.Errorf("BlockedDurations(nobody) = %v, want nil", got)
	}
}

// TestSinceOmittedWhenZero: Since is a Cleared-only field, so every other
// kind marshals without the key; a Cleared event that has one survives
// Save/Load unchanged.
func TestSinceOmittedWhenZero(t *testing.T) {
	plain, err := json.Marshal(Event{At: historyNow, Kind: RateLimited, Provider: "test", Source: "planner"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(plain), `"since"`) {
		t.Errorf("RateLimited event marshalled with a since key: %s", plain)
	}

	kv := testHistoryKV(t)
	h := History{}.Append(Event{
		At:       historyNow,
		Kind:     Cleared,
		Provider: "test",
		Source:   "planner",
		Since:    historyNow.Add(-5 * time.Hour),
	})
	if err := SaveHistory(kv, h); err != nil {
		t.Fatalf("SaveHistory: %v", err)
	}

	got, err := loadHistory(kv)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(got.Events), got.Events)
	}
	if !got.Events[0].Since.Equal(h.Events[0].Since) {
		t.Errorf("Since = %v, want %v", got.Events[0].Since, h.Events[0].Since)
	}
	if got.Events[0] != h.Events[0] {
		t.Errorf("event = %+v, want %+v", got.Events[0], h.Events[0])
	}
}
