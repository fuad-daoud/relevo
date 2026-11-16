package relevo

import (
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store/storetest"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// TestMirrorDocNamesMatchTheBoundDocuments pins the mirrored names against the
// documents a machine actually binds: the list a none-history reader follows
// must be the config and the secrets as sharedDocs binds them, in that order,
// or the reader would mirror a document the machine does not have.
func TestMirrorDocNamesMatchTheBoundDocuments(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	shared, err := st.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	want := []string{
		machineConfig(shared).Doc().Name(),
		machineConfig(shared).SecretsDoc().Name(),
	}
	got := relevosync.MirrorDocNames()
	if !slices.Equal(got, want) {
		t.Errorf("MirrorDocNames() = %v, want the bound documents %v", got, want)
	}
	bound := sharedDocs(shared)
	for _, name := range got {
		found := false
		for _, d := range bound {
			if d.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the mirrored document %q is not one sharedDocs binds", name)
		}
	}
}
