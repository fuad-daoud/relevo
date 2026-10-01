package main

import (
	"context"
	"os/exec"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestApplyCredential pins binExec's identity helper: a nil credential leaves
// the command as the serve uid, and a set one applies the tenant uid and gid.
func TestApplyCredential(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")

	applyCredential(cmd, nil)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.Credential != nil {
		t.Fatalf("applyCredential(nil) set a credential: %+v", cmd.SysProcAttr)
	}

	applyCredential(cmd, &spawn.Credential{UID: 1001, GID: 1002})
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("applyCredential did not apply the credential")
	}
	cred := cmd.SysProcAttr.Credential
	if cred.Uid != 1001 || cred.Gid != 1002 {
		t.Errorf("credential = uid %d gid %d, want 1001:1002", cred.Uid, cred.Gid)
	}
}
