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

	if err := ReplayRefusedPass(shared, local); err != nil {
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
	if err := ReplayRefusedPass(shared, local); err != nil {
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

	if err := ReplayRefusedPass(shared, local); err != nil {
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

	if err := ReplayRefusedPass(shared, local); err != nil {
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

	if err := ReplayRefusedPass(shared, local); err != nil {
		t.Fatalf("ReplayRefusedPass: %v", err)
	}

	mark, _, err := shared.ImportMark("m2")
	if err != nil || mark != 0 {
		t.Fatalf("mark = %d, want 0", mark)
	}
}

func TestReplayRefusedPassC2CappedRewindsToZero(t *testing.T) {
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

	if err := ReplayRefusedPass(shared, local); err != nil {
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
