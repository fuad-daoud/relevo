package db

import "testing"

func TestEpochCutRaisesOnly(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	// The record raises to epoch 2 at sequence 5, refuses to fall to (2, 3)
	// or (2, 5), and rises to epoch 3 at a lower sequence -- an epoch is
	// compared first, so the newer epoch's row replaces the older one whole.
	for _, cut := range []EpochCut{{2, 5}, {2, 3}, {2, 5}, {3, 1}} {
		if err := d.RaiseEpochCut("m1", cut.Epoch, cut.Seq); err != nil {
			t.Fatalf("RaiseEpochCut %d/%d: %v", cut.Epoch, cut.Seq, err)
		}
	}
	got, err := d.EpochCuts()
	if err != nil || got["m1"] != (EpochCut{Epoch: 3, Seq: 1}) {
		t.Fatalf("EpochCuts = %v, %v; want m1 at epoch 3, seq 1", got, err)
	}

	// A row that would lower the cut, at any epoch, never replaces it.
	if err := d.RaiseEpochCut("m1", 3, 0); err != nil {
		t.Fatalf("RaiseEpochCut 3/0: %v", err)
	}
	if err := d.RaiseEpochCut("m1", 2, 9); err != nil {
		t.Fatalf("RaiseEpochCut 2/9: %v", err)
	}
	got, err = d.EpochCuts()
	if err != nil || got["m1"] != (EpochCut{Epoch: 3, Seq: 1}) {
		t.Fatalf("EpochCuts = %v, %v; the cut must never regress", got, err)
	}
}

func TestEpochCutRefusesARecordNoVerifierWrote(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	for _, cut := range []EpochCut{{0, 5}, {1, -1}} {
		if err := d.RaiseEpochCut("m1", cut.Epoch, cut.Seq); err == nil {
			t.Fatalf("RaiseEpochCut %d/%d came back as a success", cut.Epoch, cut.Seq)
		}
	}
}

func TestEpochCutOnAFileWithoutTheTableIsEmpty(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	got, err := d.EpochCuts()
	if err != nil || len(got) != 0 {
		t.Fatalf("EpochCuts = %v, %v; want an empty map", got, err)
	}
}
