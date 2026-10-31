package view

import (
	"errors"
	"testing"
)

// TestFindingsRound pins the pure resolver behind `show --findings`: a
// requested round within upper is taken as is, one above upper is refused with
// the plan-round count, and no --round scans newest-first for the round that
// holds the consult's findings, ErrNoFindings when none does.
func TestFindingsRound(t *testing.T) {
	t.Parallel()

	existsErr := errors.New("exists failed")
	holds := func(rounds ...int) func(int) (bool, error) {
		return func(round int) (bool, error) {
			for _, r := range rounds {
				if r == round {
					return true, nil
				}
			}
			return false, nil
		}
	}

	cases := []struct {
		name      string
		requested int
		upper     int
		exists    func(int) (bool, error)
		want      int
		wantErr   string
		wantNone  bool
	}{
		{name: "requested round within upper", requested: 1, upper: 1, exists: holds(), want: 1},
		{name: "requested round above upper", requested: 2, upper: 1, exists: holds(), wantErr: "round 2: binding has 1 rounds"},
		{name: "newest held round wins", requested: 0, upper: 2, exists: holds(1), want: 1},
		{name: "newest of two held rounds wins", requested: 0, upper: 2, exists: holds(1, 2), want: 2},
		{name: "no round holds the findings", requested: 0, upper: 2, exists: holds(), wantNone: true},
		{name: "exists error propagates", requested: 0, upper: 2, exists: func(int) (bool, error) { return false, existsErr }, wantErr: "exists failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			round, err := FindingsRound(tc.requested, tc.upper, tc.exists)
			if tc.wantNone {
				if !errors.Is(err, ErrNoFindings) {
					t.Fatalf("FindingsRound(%d, %d) err = %v, want ErrNoFindings", tc.requested, tc.upper, err)
				}
				return
			}
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("FindingsRound(%d, %d) err = %v, want %q", tc.requested, tc.upper, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindingsRound(%d, %d): %v", tc.requested, tc.upper, err)
			}
			if round != tc.want {
				t.Errorf("FindingsRound(%d, %d) = %d, want %d", tc.requested, tc.upper, round, tc.want)
			}
		})
	}
}

// TestFindingsRoundFor pins the shared --findings resolution: the scan runs
// over the higher of the round counter and the plan-round count, a missing
// consult names the consult and the binding, and range and exists errors pass
// through unnamed.
func TestFindingsRoundFor(t *testing.T) {
	t.Parallel()

	existsErr := errors.New("exists failed")
	present := func(round int) (bool, error) { return round == 2, nil }

	if round, err := FindingsRoundFor(2, 3, 1, "shop", "abc", present); err != nil || round != 2 {
		t.Errorf("FindingsRoundFor(requested 2) = (%d, %v), want (2, nil)", round, err)
	}
	if round, err := FindingsRoundFor(0, 1, 3, "shop", "abc", present); err != nil || round != 2 {
		t.Errorf("FindingsRoundFor(scan over plan count) = (%d, %v), want (2, nil)", round, err)
	}

	_, err := FindingsRoundFor(0, 1, 1, "shop", "zzz", present)
	if !errors.Is(err, ErrNoFindings) {
		t.Fatalf("FindingsRoundFor(missing) err = %v, want ErrNoFindings", err)
	}
	if want := "consult zzz on shop: no findings for that consult"; err.Error() != want {
		t.Errorf("FindingsRoundFor(missing) err = %q, want %q", err, want)
	}

	if _, err := FindingsRoundFor(9, 3, 1, "shop", "abc", present); err == nil || err.Error() != "round 9: binding has 3 rounds" {
		t.Errorf("FindingsRoundFor(requested 9) err = %v, want the range refusal", err)
	}
	if _, err := FindingsRoundFor(0, 2, 2, "shop", "abc", func(int) (bool, error) { return false, existsErr }); !errors.Is(err, existsErr) {
		t.Errorf("FindingsRoundFor(exists fails) err = %v, want the exists error", err)
	}
}
