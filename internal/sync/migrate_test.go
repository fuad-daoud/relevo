package sync

import (
	"errors"
	"fmt"
	"testing"
)

// TestMigrateGateRefusesAnUnconfirmedRun pins the confirm rule: without the
// flag the verb must not move a row.
func TestMigrateGateRefusesAnUnconfirmedRun(t *testing.T) {
	t.Parallel()
	if err := MigrateGate(false); !errors.Is(err, ErrMigrateUnconfirmed) {
		t.Fatalf("MigrateGate(false) = %v, want ErrMigrateUnconfirmed", err)
	}
}

// TestMigrateGatePassesAConfirmedRun pins the other way: the flag saying every
// machine already runs this release is the whole of the rule.
func TestMigrateGatePassesAConfirmedRun(t *testing.T) {
	t.Parallel()
	if err := MigrateGate(true); err != nil {
		t.Fatalf("MigrateGate(true) = %v, want nil", err)
	}
}

// TestMigrateUnconfirmedNamesTheFlagAndTheHazard pins the fixed sentence: it
// is written in this tree and names the flag that ends the refusal and the
// loss an unconfirmed run risks, so a reader learns both without a word the
// remote chose.
func TestMigrateUnconfirmedNamesTheFlagAndTheHazard(t *testing.T) {
	t.Parallel()
	want := "sync: refusing to migrate without --confirm-all-upgraded, which says every machine already runs this release: a machine still on the old release cannot read format 6, and a late row it writes can be lost"
	if got := ErrMigrateUnconfirmed.Error(); got != want {
		t.Errorf("ErrMigrateUnconfirmed = %q, want %q", got, want)
	}
}

// TestMigrateIncompleteNamesTheRerun pins the fixed sentence: rows an old
// binary wrote after the read are swept by running the verb again.
func TestMigrateIncompleteNamesTheRerun(t *testing.T) {
	t.Parallel()
	want := "sync: rows arrived while migrating; run `relevo db sync migrate` again"
	if got := ErrMigrateIncomplete.Error(); got != want {
		t.Errorf("ErrMigrateIncomplete = %q, want %q", got, want)
	}
}

// TestMigrateSentinelsAreDistinct pins that the two refusals are different
// faults with different ways out: one needs the operator's confirmation, the
// other one more run.
func TestMigrateSentinelsAreDistinct(t *testing.T) {
	t.Parallel()
	if errors.Is(ErrMigrateUnconfirmed, ErrMigrateIncomplete) {
		t.Error("ErrMigrateUnconfirmed reads as ErrMigrateIncomplete")
	}
	if errors.Is(ErrMigrateIncomplete, ErrMigrateUnconfirmed) {
		t.Error("ErrMigrateIncomplete reads as ErrMigrateUnconfirmed")
	}
}

// TestMigrateSentinelsAreNotPermanentRefusals pins that neither refusal
// latches: an unconfirmed run passes once confirmed, and an incomplete one
// passes on rerun.
func TestMigrateSentinelsAreNotPermanentRefusals(t *testing.T) {
	t.Parallel()
	for _, err := range []error{ErrMigrateUnconfirmed, ErrMigrateIncomplete} {
		if IsPermanentRefusal(err) {
			t.Errorf("%v is a permanent refusal", err)
		}
		if IsPermanentRefusal(fmt.Errorf("migrate: %w", err)) {
			t.Errorf("a wrapped %v is a permanent refusal", err)
		}
	}
}
