package store

import (
	"encoding/json"
	"testing"
)

// TestBindingAccountRoundTrips pins that the login a round drew from is
// written and read back, and that a record from before the field decodes with
// none, so an old binding is not mistaken for one on a login.
func TestBindingAccountRoundTrips(t *testing.T) {
	s := New(t.TempDir())
	b := newBinding("webshop", "/repo")
	b.BuilderAccount = "cp2"
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.BuilderAccount != "cp2" {
		t.Errorf("BuilderAccount = %q, want cp2", got.BuilderAccount)
	}

	var old Binding
	if err := json.Unmarshal([]byte(`{"format":10,"name":"old","cwd":"/repo"}`), &old); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if old.BuilderAccount != "" {
		t.Errorf("a format-10 record kept account %q, want empty", old.BuilderAccount)
	}
}
