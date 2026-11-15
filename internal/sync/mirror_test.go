package sync

import (
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestMirrorDocNamesAreTheConfigThenTheSecrets pins the fixed order -- the
// config first, then the secrets that carry the R2 credentials -- and that a
// caller's edit of the returned slice never changes the next call's list.
func TestMirrorDocNamesAreTheConfigThenTheSecrets(t *testing.T) {
	t.Parallel()
	want := []string{ConfigDocName, SecretsDocName}
	got := MirrorDocNames()
	if !slices.Equal(got, want) {
		t.Fatalf("MirrorDocNames() = %v, want %v", got, want)
	}
	got[0] = "edited"
	if again := MirrorDocNames(); !slices.Equal(again, want) {
		t.Errorf("after a caller's edit MirrorDocNames() = %v, want %v", again, want)
	}
}

// TestMirrorTablesCarryTheDocPrefix pins that each mirrored name travels under
// its doc: table, in MirrorDocNames's order.
func TestMirrorTablesCarryTheDocPrefix(t *testing.T) {
	t.Parallel()
	want := []string{synclog.DocPrefix + ConfigDocName, synclog.DocPrefix + SecretsDocName}
	if got := MirrorTables(); !slices.Equal(got, want) {
		t.Errorf("MirrorTables() = %v, want %v", got, want)
	}
}
