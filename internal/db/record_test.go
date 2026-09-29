package db

import (
	"testing"
	"time"
)

func TestRecordPutGetList(t *testing.T) {
	d := openTestDB(t)

	beta := testRecord("beta")
	betaID, err := d.RecordPut(beta)
	if err != nil {
		t.Fatalf("RecordPut(beta): %v", err)
	}
	if _, err := d.RecordPut(testRecord("alpha")); err != nil {
		t.Fatalf("RecordPut(alpha): %v", err)
	}

	got, found, err := d.RecordGet("", "beta")
	if err != nil {
		t.Fatalf("RecordGet: %v", err)
	}
	if !found {
		t.Fatal("RecordGet(beta) found = false, want true")
	}
	if got.ID != betaID || got.Name != "beta" || got.State != "active" || got.Round != 1 || got.CWD != "/tmp/beta" {
		t.Errorf("RecordGet(beta) = %+v, want id %s name beta state active round 1 cwd /tmp/beta", got, betaID)
	}

	if _, found, err := d.RecordGet("", "missing"); err != nil || found {
		t.Errorf("RecordGet(missing) = (found %v, err %v), want (false, nil)", found, err)
	}

	list, err := d.RecordList("")
	if err != nil {
		t.Fatalf("RecordList: %v", err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("RecordList names = %v, want [alpha beta]", recordNames(list))
	}

	viewed := time.Now().UTC().Truncate(time.Millisecond)
	if err := d.RecordSetViewed("", "beta", viewed); err != nil {
		t.Fatalf("RecordSetViewed: %v", err)
	}

	// A second put upserts: same id, created_at and viewed_at kept.
	next := testRecord("beta")
	next.State = "needs_you"
	next.Round = 4
	next.CreatedAt = time.Now().UTC().Add(24 * time.Hour).Truncate(time.Millisecond)
	againID, err := d.RecordPut(next)
	if err != nil {
		t.Fatalf("RecordPut(beta) again: %v", err)
	}
	if againID != betaID {
		t.Errorf("second RecordPut id = %s, want the existing id %s", againID, betaID)
	}

	got, found, err = d.RecordGet("", "beta")
	if err != nil || !found {
		t.Fatalf("RecordGet(beta) after put = (found %v, err %v)", found, err)
	}
	if got.State != "needs_you" || got.Round != 4 {
		t.Errorf("after put: state %q round %d, want needs_you 4", got.State, got.Round)
	}
	if !got.CreatedAt.Equal(beta.CreatedAt) {
		t.Errorf("created_at = %v, want the original %v (a put keeps it)", got.CreatedAt, beta.CreatedAt)
	}
	if got.ViewedAt == nil || !got.ViewedAt.Equal(viewed) {
		t.Errorf("viewed_at = %v, want %v (a put keeps it)", got.ViewedAt, viewed)
	}

	// SetViewed on a name with no row is a no-op, not an error.
	if err := d.RecordSetViewed("", "missing", viewed); err != nil {
		t.Errorf("RecordSetViewed(missing) = %v, want nil", err)
	}
}

// TestRecordPutScopesByOwner pins that the same name may be live under two
// owners and neither read sees the other's row.
func TestRecordPutScopesByOwner(t *testing.T) {
	d := openTestDB(t)

	first := testRecord("api")
	first.Owner = "abcdef0123456789"
	firstID, err := d.RecordPut(first)
	if err != nil {
		t.Fatalf("RecordPut(owner A): %v", err)
	}

	second := testRecord("api")
	second.Owner = "0011223344556677"
	second.Round = 3
	secondID, err := d.RecordPut(second)
	if err != nil {
		t.Fatalf("RecordPut(owner B): %v", err)
	}
	if secondID == firstID {
		t.Errorf("a Put under a new owner reused row %s, want a row of its own", firstID)
	}

	gotA, found, err := d.RecordGet("abcdef0123456789", "api")
	if err != nil || !found {
		t.Fatalf("RecordGet(owner A, api) = (found %v, err %v), want (true, nil)", found, err)
	}
	if gotA.ID != firstID || gotA.Owner != "abcdef0123456789" || gotA.Round != 1 {
		t.Errorf("owner A row = %+v, want id %s owner abcdef0123456789 round 1", gotA, firstID)
	}

	gotB, found, err := d.RecordGet("0011223344556677", "api")
	if err != nil || !found {
		t.Fatalf("RecordGet(owner B, api) = (found %v, err %v), want (true, nil)", found, err)
	}
	if gotB.ID != secondID || gotB.Owner != "0011223344556677" || gotB.Round != 3 {
		t.Errorf("owner B row = %+v, want id %s owner 0011223344556677 round 3", gotB, secondID)
	}

	listA, err := d.RecordList("abcdef0123456789")
	if err != nil {
		t.Fatalf("RecordList(owner A): %v", err)
	}
	if len(listA) != 1 || listA[0].Name != "api" {
		t.Fatalf("RecordList(owner A) = %v, want [api]", recordNames(listA))
	}
	listB, err := d.RecordList("0011223344556677")
	if err != nil {
		t.Fatalf("RecordList(owner B): %v", err)
	}
	if len(listB) != 1 || listB[0].Name != "api" {
		t.Fatalf("RecordList(owner B) = %v, want [api]", recordNames(listB))
	}
	if none, err := d.RecordList(""); err != nil || len(none) != 0 {
		t.Fatalf("RecordList(local) = %v (err %v), want none", recordNames(none), err)
	}
}

func TestRecordArchiveFreesName(t *testing.T) {
	d := openTestDB(t)

	firstID, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := d.RecordArchive("", "alpha", at); err != nil {
		t.Fatalf("RecordArchive: %v", err)
	}

	if _, found, err := d.RecordGet("", "alpha"); err != nil || found {
		t.Errorf("RecordGet(archived) = (found %v, err %v), want (false, nil)", found, err)
	}
	list, err := d.RecordList("")
	if err != nil {
		t.Fatalf("RecordList: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("RecordList after archive = %v, want empty", recordNames(list))
	}

	var archived string
	if err := d.sqlDB.QueryRow(`SELECT archived_at FROM binding_record WHERE id = ?`, firstID).Scan(&archived); err != nil {
		t.Fatalf("select archived_at: %v", err)
	}
	if archived == "" {
		t.Error("archived_at is empty on the archived row")
	}

	secondID, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut after archive: %v", err)
	}
	if secondID == firstID {
		t.Errorf("re-put reused archived id %s, want a new row", firstID)
	}

	if err := d.RecordArchive("", "missing", at); err != nil {
		t.Errorf("RecordArchive(missing) = %v, want nil", err)
	}
}

func TestRecordDeleteCascadesEvents(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	if err := d.EventAppend(id, testEvent(1)); err != nil {
		t.Fatalf("EventAppend: %v", err)
	}
	if err := d.EventAppend(id, testEvent(2)); err != nil {
		t.Fatalf("EventAppend: %v", err)
	}

	if err := d.RecordDelete("", "alpha"); err != nil {
		t.Fatalf("RecordDelete: %v", err)
	}
	if _, found, err := d.RecordGet("", "alpha"); err != nil || found {
		t.Errorf("RecordGet after delete = (found %v, err %v), want (false, nil)", found, err)
	}

	var events int
	if err := d.sqlDB.QueryRow(`SELECT COUNT(*) FROM binding_event WHERE record_id = ?`, id).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 0 {
		t.Errorf("events after delete = %d, want 0 (cascade)", events)
	}
}

// TestEventAppendAndReplaceAll pins the event log, including that a duplicate
// seq fails.
func TestEventAppendAndReplaceAll(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}

	if err := d.EventAppend(id, testEvent(1)); err != nil {
		t.Fatalf("EventAppend: %v", err)
	}
	if err := d.EventAppend(id, testEvent(1)); err == nil {
		t.Error("a second append of seq 1 = nil, want a primary-key error")
	}

	if err := d.EventReplaceAll(id, []RecordEvent{testEvent(1), testEvent(2), testEvent(3)}); err != nil {
		t.Fatalf("EventReplaceAll: %v", err)
	}

	all, err := d.EventsOf(id, 0)
	if err != nil {
		t.Fatalf("EventsOf(0): %v", err)
	}
	if len(all) != 3 || all[0].Seq != 1 || all[1].Seq != 2 || all[2].Seq != 3 {
		t.Fatalf("EventsOf(0) seqs = %v, want [1 2 3]", eventSeqs(all))
	}

	after, err := d.EventsOf(id, 1)
	if err != nil {
		t.Fatalf("EventsOf(1): %v", err)
	}
	if len(after) != 2 || after[0].Seq != 2 || after[1].Seq != 3 {
		t.Fatalf("EventsOf(1) seqs = %v, want [2 3]", eventSeqs(after))
	}

	max, err := d.EventMaxSeq(id)
	if err != nil {
		t.Fatalf("EventMaxSeq: %v", err)
	}
	if max != 3 {
		t.Errorf("EventMaxSeq = %d, want 3", max)
	}
	if empty, err := d.EventMaxSeq("no-such-record"); err != nil || empty != 0 {
		t.Errorf("EventMaxSeq(no-such-record) = (%d, %v), want (0, nil)", empty, err)
	}
}

// TestEventConfirmPatchesJSON pins that a confirm patches the entry's JSON, so
// an unknown key survives.
func TestEventConfirmPatchesJSON(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordPut(testRecord("alpha"))
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	if err := d.EventReplaceAll(id, []RecordEvent{testEvent(1), testEvent(2)}); err != nil {
		t.Fatalf("EventReplaceAll: %v", err)
	}

	patched := `{"seq":2,"confirmed":true,"unknown_key":"kept"}`
	at := time.Now().UTC().Truncate(time.Millisecond)
	if err := d.EventConfirm(id, 2, at, "channel", patched); err != nil {
		t.Fatalf("EventConfirm: %v", err)
	}

	all, err := d.EventsOf(id, 0)
	if err != nil {
		t.Fatalf("EventsOf after confirm: %v", err)
	}
	confirmed := all[1]
	if !confirmed.Confirmed {
		t.Error("confirmed = false, want true")
	}
	if confirmed.DeliveredAt == nil || !confirmed.DeliveredAt.Equal(at) {
		t.Errorf("delivered_at = %v, want %v", confirmed.DeliveredAt, at)
	}
	if confirmed.Route != "channel" {
		t.Errorf("route = %q, want channel", confirmed.Route)
	}
	if confirmed.JSON != patched {
		t.Errorf("entry_json = %s, want %s", confirmed.JSON, patched)
	}

	if err := d.EventConfirm(id, 1, at, "", testEvent(1).JSON); err != nil {
		t.Fatalf("EventConfirm(1): %v", err)
	}
	all, err = d.EventsOf(id, 0)
	if err != nil {
		t.Fatalf("EventsOf after second confirm: %v", err)
	}
	if all[0].Route != "" {
		t.Errorf("route = %q, want empty", all[0].Route)
	}
}
