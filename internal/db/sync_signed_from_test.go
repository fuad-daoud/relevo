package db

import "testing"

func TestSignedFromRecordsOnlyLower(t *testing.T) {
	t.Parallel()
	d := openTestDB(t)
	for _, seq := range []int{5, 9, 3} {
		if err := d.LowerSignedFrom("m1", seq); err != nil {
			t.Fatalf("LowerSignedFrom %d: %v", seq, err)
		}
	}
	got, err := d.SignedFroms()
	if err != nil || got["m1"] != 3 {
		t.Fatalf("SignedFroms = %v, %v; want m1 at 3", got, err)
	}
	if err := d.ClearSignedFrom(); err != nil {
		t.Fatalf("ClearSignedFrom: %v", err)
	}
	if got, _ := d.SignedFroms(); len(got) != 0 {
		t.Errorf("records after clear = %v", got)
	}
}
