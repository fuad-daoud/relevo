package sync

import (
	"encoding/json"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

func TestReplayRefusedPassRunsOnce(t *testing.T) {
	_, shared, local := openSplit(t)

	// Set initial mark for remote origin m2.
	if err := shared.SetImportMark("m2", 5000); err != nil {
		t.Fatalf("set import mark: %v", err)
	}

	drop := synclog.Drop{Origin: "m2", Seq: 5000, Reason: "constraint refused"}.String()
	body, _ := json.Marshal(Trouble{Dropped: []string{drop}})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	wantRewound := 5000 - 4*synclog.DefaultBatchSize
	mark, _, err := shared.ImportMark("m2")
	if err != nil || mark != wantRewound {
		t.Fatalf("mark = %d, want %d", mark, wantRewound)
	}

	// Verify marker written.
	var gen string
	if err := marker(local, KeyReplayRefused, &gen); err != nil || gen != "1" {
		t.Fatalf("marker = %q, %v, want '1'", gen, err)
	}

	// Move mark forward again.
	if err := shared.SetImportMark("m2", 6000); err != nil {
		t.Fatalf("advance mark: %v", err)
	}

	// Second run should be a no-op because marker is set.
	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	mark, _, err = shared.ImportMark("m2")
	if err != nil || mark != 6000 {
		t.Fatalf("mark after second pass = %d, want 6000", mark)
	}
}

func TestReplayRefusedPassSmallestSeq(t *testing.T) {
	_, shared, local := openSplit(t)

	if err := shared.SetImportMark("m2", 3000); err != nil {
		t.Fatalf("set mark: %v", err)
	}

	d1 := synclog.Drop{Origin: "m2", Seq: 2000, Reason: "err"}.String()
	d2 := synclog.Drop{Origin: "m2", Seq: 1500, Reason: "err"}.String()
	d3 := synclog.Drop{Origin: "m2", Seq: 2500, Reason: "err"}.String()
	body, _ := json.Marshal(Trouble{Dropped: []string{d1, d2, d3}})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	want := 1500 - 4*synclog.DefaultBatchSize
	mark, _, err := shared.ImportMark("m2")
	if err != nil || mark != want {
		t.Fatalf("mark = %d, want %d", mark, want)
	}
}

func TestReplayRefusedPassNoDroppedLines(t *testing.T) {
	_, shared, local := openSplit(t)

	if err := shared.SetImportMark("m2", 100); err != nil {
		t.Fatalf("set mark: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	mark, _, err := shared.ImportMark("m2")
	if err != nil || mark != 100 {
		t.Fatalf("mark = %d, want 100 unchanged", mark)
	}

	var gen string
	if err := marker(local, KeyReplayRefused, &gen); err != nil || gen != "1" {
		t.Fatalf("marker = %q, want '1'", gen)
	}
}

func TestReplayRefusedPassNeverBelowZero(t *testing.T) {
	_, shared, local := openSplit(t)

	if err := shared.SetImportMark("m2", 50); err != nil {
		t.Fatalf("set mark: %v", err)
	}

	d := synclog.Drop{Origin: "m2", Seq: 50, Reason: "err"}.String()
	body, _ := json.Marshal(Trouble{Dropped: []string{d}})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	mark, _, err := shared.ImportMark("m2")
	if err != nil || mark != 0 {
		t.Fatalf("mark = %d, want 0", mark)
	}
}

func TestReplayRefusedPassCappedRewindsToZero(t *testing.T) {
	_, shared, local := openSplit(t)

	if err := shared.SetImportMark("m2", 5000); err != nil {
		t.Fatalf("set mark m2: %v", err)
	}
	if err := shared.SetImportMark("m3", 5000); err != nil {
		t.Fatalf("set mark m3: %v", err)
	}

	var drops []string
	// m2 has maxDroppedReports (50) drops, oldest is 4000.
	for i := 0; i < maxDroppedReports; i++ {
		drops = append(drops, synclog.Drop{Origin: "m2", Seq: 4000 + i, Reason: "err"}.String())
	}
	// m3 has 2 drops under the cap, smallest is 3000.
	drops = append(drops, synclog.Drop{Origin: "m3", Seq: 3000, Reason: "err"}.String())
	drops = append(drops, synclog.Drop{Origin: "m3", Seq: 3100, Reason: "err"}.String())

	body, _ := json.Marshal(Trouble{Dropped: drops})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, nil); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	// m2 was at the cap, so it must rewind to 0.
	mark2, _, err := shared.ImportMark("m2")
	if err != nil || mark2 != 0 {
		t.Fatalf("m2 mark = %d, want 0 (capped)", mark2)
	}

	// m3 was under the cap, so it must rewind to Nmin - 4*256 = 3000 - 1024 = 1976.
	want3 := 3000 - 4*synclog.DefaultBatchSize
	mark3, _, err := shared.ImportMark("m3")
	if err != nil || mark3 != want3 {
		t.Fatalf("m3 mark = %d, want %d", mark3, want3)
	}
}

func TestReplayRefusedPassResolvesBatchBoundary(t *testing.T) {
	_, shared, local := openSplit(t)
	tLog := synclog.NewMemTransport("m1")

	// Multi-entry batches:
	// Batch 1: Seq 1..3, Batch 1
	// Batch 2: Seq 4..5, Batch 4
	b1 := []synclog.Entry{
		{Origin: "m1", Table: "binding", PK: `["b1"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b1","name":"b1","cwd":"/1","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b2"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b2","name":"b2","cwd":"/2","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b3"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b3","name":"b3","cwd":"/3","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
	}
	b2 := []synclog.Entry{
		{Origin: "m1", Table: "binding", PK: `["b4"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b4","name":"b4","cwd":"/4","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b5"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b5","name":"b5","cwd":"/5","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
	}

	if _, err := tLog.Append(b1); err != nil {
		t.Fatalf("append b1: %v", err)
	}
	if _, err := tLog.Append(b2); err != nil {
		t.Fatalf("append b2: %v", err)
	}

	// Advance mark to 5 (head).
	if err := shared.SetImportMark("m1", 5); err != nil {
		t.Fatalf("set import mark: %v", err)
	}

	// Drop report targeting sequence 1 (in the middle of batch 1):
	// target = Nmin - 4*256. With Nmin = 1 + 4*256 = 1025, target is 1.
	drop := synclog.Drop{Origin: "m1", Seq: 1 + 4*synclog.DefaultBatchSize, Reason: "constraint refused"}.String()
	body, _ := json.Marshal(Trouble{Dropped: []string{drop}})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, tLog); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	// Target was 1 (middle of batch 1). First entry with Seq > 1 is Seq 2 (Batch 1).
	// Its batch start minus one is 1 - 1 = 0, so mark must rewind to boundary 0.

	// Import from mark 0: no gaps, dropped rows arrive, mark reaches head 5.
	imp := synclog.NewImporter(shared, tLog.OnLog(shared.Origin()))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("gaps = %+v, want no gaps", res.Gaps)
	}
	if len(res.Dropped) != 0 {
		t.Fatalf("dropped = %+v, want no drops", res.Dropped)
	}
	if res.Applied == 0 {
		t.Fatalf("applied = 0, want dropped rows to arrive")
	}

	mark, _, err := shared.ImportMark("m1")
	if err != nil || mark != 5 {
		t.Fatalf("mark after import = %d, want head 5", mark)
	}
}

func TestReplayRefusedPassTargetBelowFloorTakesSnapshotPath(t *testing.T) {
	_, shared, local := openSplit(t)
	tLog := synclog.NewMemTransport("m1")

	// Append batches and compact up to floor 3.
	b1 := []synclog.Entry{
		{Origin: "m1", Table: "binding", PK: `["b1"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b1","name":"b1","cwd":"/1","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b2"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b2","name":"b2","cwd":"/2","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b3"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b3","name":"b3","cwd":"/3","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
	}
	b2 := []synclog.Entry{
		{Origin: "m1", Table: "binding", PK: `["b4"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b4","name":"b4","cwd":"/4","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
		{Origin: "m1", Table: "binding", PK: `["b5"]`, Op: synclog.OpUpsert, SchemaVersion: 1, Body: []byte(`{"id":"b5","name":"b5","cwd":"/5","builder_mode":"local","created_at":"t","ingest_source":"manual","origin":"m1"}`)},
	}
	if _, err := tLog.Append(b1); err != nil {
		t.Fatalf("append b1: %v", err)
	}
	if _, err := tLog.Append(b2); err != nil {
		t.Fatalf("append b2: %v", err)
	}
	if _, err := tLog.Compact("m1", 3); err != nil {
		t.Fatalf("compact: %v", err)
	}

	if err := shared.SetImportMark("m1", 5); err != nil {
		t.Fatalf("set import mark: %v", err)
	}

	// Target 1 is below compaction floor 3.
	drop := synclog.Drop{Origin: "m1", Seq: 1 + 4*synclog.DefaultBatchSize, Reason: "err"}.String()
	body, _ := json.Marshal(Trouble{Dropped: []string{drop}})
	if err := local.KVPut(KeyTrouble, body); err != nil {
		t.Fatalf("write trouble: %v", err)
	}

	if err := ReplayRefusedPass(shared, local, tLog); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	mark, _, err := shared.ImportMark("m1")
	if err != nil || mark >= 3 {
		t.Fatalf("mark = %d, want < floor 3", mark)
	}

	imp := synclog.NewImporter(shared, tLog.OnLog(shared.Origin()))
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if res.Snapshots == 0 {
		t.Fatalf("res.Snapshots = 0, want snapshot path taken")
	}
	if len(res.Held) != 0 {
		t.Fatalf("held = %+v, want no gaps", res.Held)
	}

	mark, _, err = shared.ImportMark("m1")
	if err != nil || mark != 5 {
		t.Fatalf("mark after snapshot import = %d, want head 5", mark)
	}
}
