package db

import (
	"testing"
	"time"
)

// TestUpsertOfAnUnchangedRowQueuesNothing pins that writing a binding or a
// round with the values it already holds records no outbox entry, while a
// changed value still does: the daemon re-ingests a binding whenever its
// revision moves, and every entry is one more row a synced machine exports.
func TestUpsertOfAnUnchangedRowQueuesNothing(t *testing.T) {
	d, sqlDB := truncateDB(t)
	server := "zen"
	binding := Binding{
		Name: "w3-c5", CWD: "/home/x/w3", BuilderMode: "headless", Server: &server,
		CreatedAt: time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), IngestSource: IngestLive,
	}
	bindingID, err := d.UpsertBinding(binding)
	if err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	started := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	closed := started.Add(time.Hour)
	cost := 0.42
	round := Round{
		BindingID: bindingID, Number: 1, StartedAt: started, ClosedAt: &closed,
		Outcome: OutcomeReported, CostUSD: &cost,
	}
	if _, err := d.UpsertRound(round); err != nil {
		t.Fatalf("UpsertRound: %v", err)
	}
	if err := d.TruncateOutbox(); err != nil {
		t.Fatalf("TruncateOutbox: %v", err)
	}

	if _, err := d.UpsertBinding(binding); err != nil {
		t.Fatalf("UpsertBinding again: %v", err)
	}
	if _, err := d.UpsertRound(round); err != nil {
		t.Fatalf("UpsertRound again: %v", err)
	}
	if got := outboxRows(t, sqlDB, 0); len(got) != 0 {
		t.Fatalf("an unchanged re-upsert recorded %d entries, want none: %+v", len(got), got)
	}

	done := "done"
	binding.FinalState = &done
	if _, err := d.UpsertBinding(binding); err != nil {
		t.Fatalf("UpsertBinding changed: %v", err)
	}
	round.Switches = 1
	if _, err := d.UpsertRound(round); err != nil {
		t.Fatalf("UpsertRound changed: %v", err)
	}
	got := outboxRows(t, sqlDB, 0)
	if len(got) != 2 || got[0].tbl != "binding" || got[1].tbl != "round" {
		t.Errorf("a changed re-upsert recorded %+v, want one binding and one round entry", got)
	}
}
