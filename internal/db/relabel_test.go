package db

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var relabelCreated = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

// seedRelabel adds a binding and its live record, whose JSON created_at matches
// the binding's. feature is empty for a binding with no label.
func seedRelabel(t *testing.T, d *DB, name string, created time.Time, feature, recordJSON string) (bindingID, recordID string) {
	t.Helper()
	b := newTestBinding(name, created)
	if feature != "" {
		b.Feature = ptr(feature)
	}
	bindingID = upsertBinding(t, d, b)
	r := testRecord(name)
	r.JSON = recordJSON
	id, err := d.RecordPut(r)
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	return bindingID, id
}

func recordJSONFor(name string, created time.Time, extra string) string {
	return fmt.Sprintf(`{"name":%q,"created_at":%q%s}`, name, created.Format(time.RFC3339), extra)
}

func relabelOne(id, feature string) RelabelParams {
	return RelabelParams{Entries: []RelabelEntry{{ID: id, Feature: feature, Line: 1}}}
}

func bindingFeature(t *testing.T, d *DB, id string) (feature, origin string) {
	t.Helper()
	got := queryStrings(t, d, `SELECT COALESCE(feature, '<null>') || '|' || origin FROM binding WHERE id = ?`, id)
	if len(got) != 1 {
		t.Fatalf("binding %s rows = %v", id, got)
	}
	feature, origin, _ = strings.Cut(got[0], "|")
	return feature, origin
}

func recordJSONOf(t *testing.T, d *DB, id string) (json, origin string) {
	t.Helper()
	got := queryStrings(t, d, `SELECT record_json || char(1) || origin FROM binding_record WHERE id = ?`, id)
	if len(got) != 1 {
		t.Fatalf("record %s rows = %v", id, got)
	}
	json, origin, _ = strings.Cut(got[0], "\x01")
	return json, origin
}

func TestRelabelFillsNullFeature(t *testing.T) {
	d := renameDB(t)
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "", recordJSONFor("b1", relabelCreated, ""))
	execRaw(t, d, `UPDATE binding SET origin = '' WHERE id = ?`, bid)
	c, err := d.Relabel(relabelOne(bid, "alpha"))
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if c.Requested != 1 || c.Labelled != 1 || c.Unchanged != 0 || c.SkippedLabelled != 0 || c.RecordsRewritten != 1 {
		t.Errorf("counts = %+v, want one binding labelled and one record rewritten", c)
	}
	if f, o := bindingFeature(t, d, bid); f != "alpha" || o != renameOrigin {
		t.Errorf("binding feature=%q origin=%q, want alpha stamped %s", f, o, renameOrigin)
	}
	js, o := recordJSONOf(t, d, rid)
	if !strings.Contains(js, `"feature":"alpha"`) || o != renameOrigin {
		t.Errorf("record = %s origin=%q, want the label and the stamp", js, o)
	}
}

func TestRelabelSkipsLabelledBinding(t *testing.T) {
	d := renameDB(t)
	rj := recordJSONFor("b1", relabelCreated, `,"feature":"old"`)
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "old", rj)
	before := dumpDB(t, d)
	c, err := d.Relabel(relabelOne(bid, "new"))
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if c.SkippedLabelled != 1 || c.Labelled != 0 || c.RecordsRewritten != 0 {
		t.Errorf("counts = %+v, want the labelled binding skipped", c)
	}
	if f, _ := bindingFeature(t, d, bid); f != "old" {
		t.Errorf("feature = %q, want old", f)
	}
	if js, _ := recordJSONOf(t, d, rid); js != rj {
		t.Errorf("record bytes changed: %s", js)
	}
	if dumpDB(t, d) != before {
		t.Errorf("a skipped binding changed the database")
	}
}

func TestRelabelOverwriteReplacesLabel(t *testing.T) {
	d := renameDB(t)
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "old", recordJSONFor("b1", relabelCreated, `,"feature":"old"`))
	p := relabelOne(bid, "new")
	p.Overwrite = true
	c, err := d.Relabel(p)
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if c.Labelled != 1 || c.RecordsRewritten != 1 {
		t.Errorf("counts = %+v, want the label replaced", c)
	}
	if f, _ := bindingFeature(t, d, bid); f != "new" {
		t.Errorf("feature = %q, want new", f)
	}
	if js, _ := recordJSONOf(t, d, rid); !strings.Contains(js, `"feature":"new"`) || strings.Contains(js, "old") {
		t.Errorf("record = %s, want the new label only", js)
	}
}

func TestRelabelUnchangedWritesNothing(t *testing.T) {
	d := renameDB(t)
	bid, _ := seedRelabel(t, d, "b1", relabelCreated, "", recordJSONFor("b1", relabelCreated, ""))
	p := relabelOne(bid, "alpha")
	p.Overwrite = true
	if _, err := d.Relabel(p); err != nil {
		t.Fatalf("first Relabel: %v", err)
	}
	before := dumpDB(t, d)
	c, err := d.Relabel(p)
	if err != nil {
		t.Fatalf("second Relabel: %v", err)
	}
	if c.Unchanged != 1 || c.Labelled != 0 || c.SkippedLabelled != 0 || c.RecordsRewritten != 0 {
		t.Errorf("counts = %+v, want one unchanged", c)
	}
	if dumpDB(t, d) != before {
		t.Errorf("running the same relabel twice changed the second time")
	}
}

func TestRelabelRefusesUnknownIDWritesNothing(t *testing.T) {
	d := renameDB(t)
	a, _ := seedRelabel(t, d, "a", relabelCreated, "", recordJSONFor("a", relabelCreated, ""))
	b, _ := seedRelabel(t, d, "b", relabelCreated.Add(time.Hour), "", recordJSONFor("b", relabelCreated.Add(time.Hour), ""))
	before := dumpDB(t, d)
	_, err := d.Relabel(RelabelParams{Entries: []RelabelEntry{
		{ID: a, Feature: "x", Line: 1}, {ID: "01NOSUCHID", Feature: "x", Line: 2}, {ID: b, Feature: "x", Line: 3},
	}})
	if !errors.Is(err, ErrRelabelRefused) || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "01NOSUCHID") {
		t.Fatalf("err = %v, want a refusal naming line 2 and the id", err)
	}
	if dumpDB(t, d) != before {
		t.Errorf("a refused batch wrote to the valid ids")
	}
}

func TestRelabelRefusesOtherOriginID(t *testing.T) {
	d := renameDB(t)
	bid, _ := seedRelabel(t, d, "b1", relabelCreated, "", recordJSONFor("b1", relabelCreated, ""))
	execRaw(t, d, `UPDATE binding SET origin = '01OTHER' WHERE id = ?`, bid)
	before := dumpDB(t, d)
	_, err := d.Relabel(relabelOne(bid, "x"))
	if !errors.Is(err, ErrRelabelRefused) || !strings.Contains(err.Error(), "01OTHER") {
		t.Fatalf("err = %v, want a refusal naming the other origin", err)
	}
	if dumpDB(t, d) != before {
		t.Errorf("a refused batch wrote")
	}
}

func TestRelabelRewritesRecordJSONBytePreserving(t *testing.T) {
	d := renameDB(t)
	rj := `{"name":"b1","big":9007199254740993,"created_at":"2026-09-01T10:00:00Z","unknown":{"a":1.50,"h":"<&>"},"feature":"old","tail":[1,2]}`
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "", rj)
	if _, err := d.Relabel(relabelOne(bid, "new")); err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	want := strings.Replace(rj, `"feature":"old"`, `"feature":"new"`, 1)
	if js, _ := recordJSONOf(t, d, rid); js != want {
		t.Errorf("record = %s\nwant     %s", js, want)
	}
}

func TestRelabelAppendsMissingFeatureKey(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"non-empty": {`{"a":1}`, `{"a":1,"feature":"x"}`},
		"empty":     {`{}`, `{"feature":"x"}`},
	} {
		t.Run(name, func(t *testing.T) {
			got, changed := setRecordFeature([]byte(tc.in), "x")
			if !changed || string(got) != tc.want {
				t.Errorf("setRecordFeature(%s) = %s, %v; want %s", tc.in, got, changed, tc.want)
			}
		})
	}
	if _, changed := setRecordFeature([]byte(`[1]`), "x"); changed {
		t.Errorf("a non-object was rewritten")
	}
}

func TestRelabelRewritesOnlyTheNamedBindingsRecord(t *testing.T) {
	d := renameDB(t)
	oldCreated, newCreated := relabelCreated, relabelCreated.Add(48*time.Hour)
	oldBID, oldRID := seedRelabel(t, d, "reused", oldCreated, "", recordJSONFor("reused", oldCreated, ""))
	if err := d.RecordArchive("", "reused", oldCreated.Add(time.Hour)); err != nil {
		t.Fatalf("RecordArchive: %v", err)
	}
	// The name is free again once the first record is archived.
	newJSON := recordJSONFor("reused", newCreated, "")
	r := testRecord("reused")
	r.JSON = newJSON
	newRID, err := d.RecordPut(r)
	if err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
	newBID := upsertBinding(t, d, newTestBinding("reused", newCreated))
	if _, err := d.Relabel(relabelOne(oldBID, "alpha")); err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if js, _ := recordJSONOf(t, d, oldRID); !strings.Contains(js, `"feature":"alpha"`) {
		t.Errorf("archived record = %s, want the label", js)
	}
	if js, _ := recordJSONOf(t, d, newRID); js != newJSON {
		t.Errorf("live record of the reused name changed: %s", js)
	}
	if f, _ := bindingFeature(t, d, newBID); f != "<null>" {
		t.Errorf("new binding feature = %q, want untouched", f)
	}
}

func TestRelabelColumnOnlyWhenRecordHasNoCreatedAt(t *testing.T) {
	d := renameDB(t)
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "", `{"name":"b1"}`)
	c, err := d.Relabel(relabelOne(bid, "x"))
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if c.Labelled != 1 || c.RecordsRewritten != 0 {
		t.Errorf("counts = %+v, want the column alone", c)
	}
	if js, _ := recordJSONOf(t, d, rid); js != `{"name":"b1"}` {
		t.Errorf("unmatched record changed: %s", js)
	}
}

func TestRelabelDryRunWritesNothing(t *testing.T) {
	d := renameDB(t)
	a, _ := seedRelabel(t, d, "a", relabelCreated, "", recordJSONFor("a", relabelCreated, ""))
	b, _ := seedRelabel(t, d, "b", relabelCreated.Add(time.Hour), "keep", recordJSONFor("b", relabelCreated.Add(time.Hour), ""))
	before := dumpDB(t, d)
	p := RelabelParams{DryRun: true, Entries: []RelabelEntry{{ID: a, Feature: "x", Line: 1}, {ID: b, Feature: "y", Line: 2}}}
	c, err := d.Relabel(p)
	if err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	if !c.DryRun || c.Labelled != 1 || c.SkippedLabelled != 1 || c.RecordsRewritten != 1 || len(c.Changes) != 1 {
		t.Errorf("dry-run counts = %+v, want the counts an apply would produce", c)
	}
	if dumpDB(t, d) != before {
		t.Errorf("a dry run changed a table or sync_outbox")
	}
	p.DryRun = false
	applied, err := d.Relabel(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	c.DryRun = false
	if applied.Labelled != c.Labelled || applied.SkippedLabelled != c.SkippedLabelled || applied.RecordsRewritten != c.RecordsRewritten {
		t.Errorf("apply counts = %+v, dry-run counts = %+v", applied, c)
	}
}

func TestRelabelStampsOutboxEntries(t *testing.T) {
	d := renameDB(t)
	bid, rid := seedRelabel(t, d, "b1", relabelCreated, "", recordJSONFor("b1", relabelCreated, ""))
	execRaw(t, d, `UPDATE binding SET origin = '' WHERE id = ?`, bid)
	execRaw(t, d, `UPDATE binding_record SET origin = '' WHERE id = ?`, rid)
	var mark int
	if err := d.sqlDB.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM sync_outbox`).Scan(&mark); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, err := d.Relabel(relabelOne(bid, "x")); err != nil {
		t.Fatalf("Relabel: %v", err)
	}
	got := queryStrings(t, d, `SELECT tbl || '|' || COALESCE(origin, '<null>') FROM sync_outbox WHERE seq > ? ORDER BY seq`, mark)
	want := []string{"binding|" + renameOrigin, "binding_record|" + renameOrigin}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("outbox entries = %v, want %v", got, want)
	}
}
