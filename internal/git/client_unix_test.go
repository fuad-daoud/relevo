//go:build unix

package git

import (
	"context"
	"slices"
	"testing"
	"time"
)

// TestCommandAppliesCredential pins the identity and environment a git child
// runs with: a credentialed client's command carries the tenant uid/gid (no
// extra groups) and the tenant env, and WithCredential leaves the receiver
// untouched so a none-mode owner keeps running as the serve uid.
func TestCommandAppliesCredential(t *testing.T) {
	base := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	tenant := base.WithCredential(1001, 1002, []string{"HOME=/home/alice"})

	cmd := tenant.command(context.Background(), "/round", []string{"GIT_INDEX_FILE=/tmp/x"}, "status")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatalf("command SysProcAttr = %+v, want the tenant credential", cmd.SysProcAttr)
	}
	if cred := cmd.SysProcAttr.Credential; cred.Uid != 1001 || cred.Gid != 1002 || len(cred.Groups) != 0 {
		t.Errorf("credential = %+v, want uid 1001 gid 1002 with no groups", cred)
	}
	if !slices.Contains(cmd.Env, "HOME=/home/alice") {
		t.Errorf("child env lacks the tenant HOME: %v", cmd.Env)
	}
	if !slices.Contains(cmd.Env, "GIT_INDEX_FILE=/tmp/x") {
		t.Errorf("child env lacks the per-call env: %v", cmd.Env)
	}
	if cmd.Dir != "/round" {
		t.Errorf("command Dir = %q, want /round", cmd.Dir)
	}

	plain := base.command(context.Background(), "/round", nil, "status")
	if plain.SysProcAttr != nil && plain.SysProcAttr.Credential != nil {
		t.Errorf("the original client gained a credential: %+v", plain.SysProcAttr)
	}
	if slices.Contains(plain.Env, "HOME=/home/alice") {
		t.Errorf("the original client gained the tenant env: %v", plain.Env)
	}
}
