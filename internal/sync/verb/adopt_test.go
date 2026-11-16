package verb

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/installation"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// TestAdoptionIsOnlyBuiltForAnAsEnable pins that a verb without --as carries no
// adoption, and one with it rewrites the installation file beside the database.
func TestAdoptionIsOnlyBuiltForAnAsEnable(t *testing.T) {
	root := t.TempDir()
	if _, err := installation.Load(root); err != nil {
		t.Fatalf("Load: %v", err)
	}
	v := &Runner{Path: filepath.Join(root, "relevo.db")}
	if got, err := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable}); err != nil || got != nil {
		t.Fatalf("an enable without --as built an adoption: %+v (%v)", got, err)
	}
	a, err := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable, AdoptAs: "inst-x"})
	if err != nil || a == nil || a.ID != "inst-x" {
		t.Fatalf("adoption = %+v (%v), want one for inst-x", a, err)
	}
	if err := a.Identity("inst-x"); err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if inst, _, err := installation.Read(root); err != nil || inst.ID != "inst-x" {
		t.Errorf("installation file = %+v, %v, want inst-x", inst, err)
	}
	if adoptedWarning("inst-x") == "" {
		t.Error("the adoption warning is empty")
	}
}

// TestEnableAdoptionBesideHistoryNoneRefused pins that an enable combining --as
// and --history none is refused with the fix named.
func TestEnableAdoptionBesideHistoryNoneRefused(t *testing.T) {
	runner, _ := joinRunner(t)
	res := runner.Run(context.Background(), &wire.SyncVerb{
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://join.invalid",
		AdoptAs:   "inst-x",
		History:   relevosync.HistoryNone,
	}, []byte(verbFixtureToken))
	if res.OK {
		t.Fatal("enable with --as and --history none accepted, want refusal")
	}
	if res.Code != wire.SyncCodeInvalid {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
	}
	if !strings.Contains(res.Message, "--history none") {
		t.Errorf("message = %q, want the fix named", res.Message)
	}
}
