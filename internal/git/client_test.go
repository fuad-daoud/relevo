package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// TestCommandEnvCarriesOneHome pins the review fix: a credentialed client's git
// child sees exactly one HOME, the tenant's, even when the daemon's own HOME is
// in the inherited environment. A reader that takes the first match must never
// see the daemon's home.
func TestCommandEnvCarriesOneHome(t *testing.T) {
	t.Setenv("HOME", "/home/daemon")
	t.Setenv("USER", "daemon")
	t.Setenv("LOGNAME", "daemon")

	base := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	tenant := base.WithCredential(1001, 1002, []string{"HOME=/home/alice", "USER=alice", "LOGNAME=alice"})
	cmd := tenant.command(context.Background(), "/round", nil, "status")

	for _, name := range []string{"HOME", "USER", "LOGNAME"} {
		var got []string
		for _, e := range cmd.Env {
			if strings.HasPrefix(e, name+"=") {
				got = append(got, e)
			}
		}
		if len(got) != 1 {
			t.Errorf("%s entries = %v, want exactly one", name, got)
		}
	}
	if !slices.Contains(cmd.Env, "HOME=/home/alice") {
		t.Errorf("child env = %v, want the tenant HOME", cmd.Env)
	}
	if slices.Contains(cmd.Env, "HOME=/home/daemon") {
		t.Errorf("child env kept the daemon HOME: %v", cmd.Env)
	}
}

// TestDiffPatchUsesCommand pins that diffPatch builds its child through the
// shared command: the client's extra env reaches the git process, so a
// user-mode diff runs with the tenant's home.
func TestDiffPatchUsesCommand(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf 'HOME=%s\\n' \"$HOME\"\nprintf 'ARGS=%s\\n' \"$*\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(stub, 5*time.Second, DefaultMaxPatchBytes)
	client.gitEnvExtra = []string{"HOME=/home/tenant"}

	body, truncated, err := client.diffPatch(context.Background(), dir, "from", "to")
	if err != nil {
		t.Fatalf("diffPatch: %v", err)
	}
	if truncated {
		t.Fatal("diffPatch truncated a tiny stub output")
	}
	if !strings.Contains(string(body), "HOME=/home/tenant") {
		t.Errorf("diffPatch child output = %q, want the tenant HOME: diffPatch must build through command()", body)
	}
	if !strings.Contains(string(body), "ARGS=diff from to") {
		t.Errorf("diffPatch child output = %q, want the git args", body)
	}
}

// TestStatusDoesNotTakeIndexLock pins the index.lock hazard. First, a status read
// over an index a plain `git status` would refresh leaves .git/index
// byte-for-byte alone: writing it means taking index.lock, and it is
// GIT_OPTIONAL_LOCKS=0 that makes git skip that write. Second, with index.lock
// held by hand the way a concurrent git write holds it, the read still succeeds.
func TestStatusDoesNotTakeIndexLock(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := initRepoWithCommit(t, "a.txt", "one\n")

	// Backdate the tracked file: the index's recorded stat data is then stale,
	// so a plain `git status` re-hashes the file, finds its content unchanged,
	// and rewrites the index to record the new mtime.
	stale := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(filepath.Join(repoDir, "a.txt"), stale, stale); err != nil {
		t.Fatalf("backdate a.txt: %v", err)
	}

	client := NewClient("git", 5*time.Second, DefaultMaxPatchBytes)
	indexPath := filepath.Join(repoDir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index: %v", err)
	}

	dirty, err := client.Dirty(ctx, repoDir)
	if err != nil {
		t.Fatalf("Dirty with a stale index: %v", err)
	}
	if dirty {
		t.Error("Dirty reported a change, but a.txt's content is unchanged")
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("read .git/index after the status read: %v", err)
	}
	if !bytes.Equal(indexBefore, indexAfter) {
		t.Error("the status read rewrote .git/index, so it took index.lock; a builder's commit in this worktree would fail against it")
	}

	lockPath := filepath.Join(repoDir, ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("hold .git/index.lock: %v", err)
	}
	defer func() { _ = os.Remove(lockPath) }()

	if _, err := client.Dirty(ctx, repoDir); err != nil {
		t.Fatalf("Dirty while .git/index.lock is held: %v", err)
	}
}

// TestRunDeadlineErrorNamesArgv pins that a command killed by the client's
// per-command budget names the argv it was running, and only that: the wrap
// must keep errors.Is(err, context.DeadlineExceeded) true, since a dozen call
// sites probe the timeout with exactly that, and a named error that stopped
// matching would silently turn every timeout into a mystery failure.
//
// The seam is a stub binary plus a 1ns budget: the stub is not git, so no real
// repository, network or slow child is involved, and the budget is already spent
// before the child is ever started.
func TestRunDeadlineErrorNamesArgv(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	client := NewClient(stub, time.Nanosecond, DefaultMaxPatchBytes)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"rev-parse", []string{"rev-parse", "HEAD"}},
		{"bundle create", []string{"bundle", "create", "--all"}},
		{"merge-base", []string{"merge-base", "--is-ancestor", "a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.run(context.Background(), dir, nil, tc.args...)
			if err == nil {
				t.Fatal("run with an expired budget returned no error")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("errors.Is(err, context.DeadlineExceeded) = false for %v; the call sites probing the timeout need it true", err)
			}
			want := "git " + strings.Join(tc.args, " ")
			if !strings.Contains(err.Error(), want) {
				t.Errorf("deadline error = %q, want it to name the argv %q", err, want)
			}
		})
	}
}
