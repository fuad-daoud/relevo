package store

import "testing"

// TestListForMasterMindReadsOnlyThatMasterMindsLiveBindings pins the query's
// filter against what Save actually writes: a renamed JSON key for
// MasterMindID would make the narrowed list come back empty.
func TestListForMasterMindReadsOnlyThatMasterMindsLiveBindings(t *testing.T) {
	s := New(t.TempDir())
	for _, b := range []struct{ name, mm string }{
		{"mine-b", "pl_mine"},
		{"mine-a", "pl_mine"},
		{"theirs", "pl_theirs"},
		{"nobody", ""},
		{"mine-archived", "pl_mine"},
	} {
		binding := newBinding(b.name, "/repo/"+b.name)
		binding.MasterMindID = b.mm
		if err := s.Save(binding); err != nil {
			t.Fatalf("Save %s: %v", b.name, err)
		}
	}
	if _, err := s.Archive("mine-archived"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	got, err := s.ListForMasterMind("pl_mine")
	if err != nil {
		t.Fatalf("ListForMasterMind: %v", err)
	}
	var names []string
	for _, b := range got {
		if b.MasterMindID != "pl_mine" {
			t.Errorf("%s has MasterMindID %q", b.Name, b.MasterMindID)
		}
		names = append(names, b.Name)
	}
	if len(names) != 2 || names[0] != "mine-a" || names[1] != "mine-b" {
		t.Errorf("ListForMasterMind(pl_mine) = %v, want [mine-a mine-b]", names)
	}

	if got, err := New(t.TempDir()).ListForMasterMind("pl_mine"); err != nil || len(got) != 0 {
		t.Errorf("an empty root = (%v, %v), want none and no error", got, err)
	}
}
