package store

import (
	"encoding/json"
	"testing"
)

// TestSubdirIsEmptyForARecordThatHasNone pins the no-migration rule: a record
// with no subdir key decodes to an empty one, so an existing binding runs
// exactly where it always ran. Defaulting a missing key to anything but ""
// would move every existing round to a different directory.
func TestSubdirIsEmptyForARecordThatHasNone(t *testing.T) {
	raw := []byte(`{"name":"webshop","cwd":"/trees/api","format":16,"runner":{"mode":"headless"},"actor":"builder","shape":"writer"}`)
	var b Binding
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if b.Subdir != "" {
		t.Fatalf("Subdir = %q for a record that has none, want \"\"", b.Subdir)
	}
	if b.CWD != "/trees/api" {
		t.Fatalf("CWD = %q, want the record's own tree", b.CWD)
	}
}
