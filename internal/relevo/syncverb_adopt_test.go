package relevo

import (
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/installation"
)

// TestAdoptionIsOnlyBuiltForAnAsEnable pins that a verb without --as carries no
// adoption, and one with it rewrites the installation file beside the database.
func TestAdoptionIsOnlyBuiltForAnAsEnable(t *testing.T) {
	root := t.TempDir()
	if _, err := installation.Load(root); err != nil {
		t.Fatalf("Load: %v", err)
	}
	v := &VerbRunner{Path: filepath.Join(root, "relevo.db")}
	if got := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable}); got != nil {
		t.Fatalf("an enable without --as built an adoption: %+v", got)
	}
	a := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable, AdoptAs: "inst-x"})
	if a == nil || a.ID != "inst-x" {
		t.Fatalf("adoption = %+v, want one for inst-x", a)
	}
	if err := a.Identity("inst-x"); err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if inst, _, err := installation.Read(root); err != nil || inst.ID != "inst-x" {
		t.Errorf("installation file = %+v, %v, want inst-x", inst, err)
	}
	if AdoptedWarning("inst-x") == "" {
		t.Error("the adoption warning is empty")
	}
}
