package store

import "testing"

// TestListWithCheckRunReadsOnlyCheckCarriers pins the query's filter against
// what Save actually writes: a renamed JSON key for CheckRun would make the
// narrowed list come back empty.
func TestListWithCheckRunReadsOnlyCheckCarriers(t *testing.T) {
	s := newStore(t)
	if err := s.Save(newBinding("plain", "/repo/plain")); err != nil {
		t.Fatalf("Save plain: %v", err)
	}
	running := newBinding("running", "/repo/running")
	running.CheckRun = &CheckRun{ID: "c1", Command: "make check", Round: 1}
	if err := s.Save(running); err != nil {
		t.Fatalf("Save running: %v", err)
	}
	settled := newBinding("settled", "/repo/settled")
	settled.CheckRun = &CheckRun{ID: "c0", Command: "make check", Round: 1, Result: "pass"}
	if err := s.Save(settled); err != nil {
		t.Fatalf("Save settled: %v", err)
	}

	got, err := s.ListWithCheckRun()
	if err != nil {
		t.Fatalf("ListWithCheckRun: %v", err)
	}
	var names []string
	for _, b := range got {
		if b.CheckRun == nil {
			t.Errorf("%s has no CheckRun", b.Name)
		}
		names = append(names, b.Name)
	}
	if len(names) != 2 || names[0] != "running" || names[1] != "settled" {
		t.Errorf("ListWithCheckRun = %v, want [running settled]", names)
	}

	if got, err := newStore(t).ListWithCheckRun(); err != nil || len(got) != 0 {
		t.Errorf("an empty root = (%v, %v), want none and no error", got, err)
	}
}
