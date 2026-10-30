package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const recordingGit = `#!/bin/sh
echo "real git: $*"
cat
exit 7
`

// shimTools is a directory holding only the utilities the shim and the fake git
// call, so no system git can be found behind the fake one.
func shimTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"dirname", "basename", "cat"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s: %v", tool, err)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// shimETXTBSYWait is how long a test keeps rebuilding and re-execing a freshly
// written shim before it gives up. The window the retry covers is one fork
// holding a write descriptor across its own exec, so a few seconds of 5ms
// steps suffices and a longer wait would hide real breakage.
const shimETXTBSYWait = 3 * time.Second

// isETXTBSY reports the only failure worth retrying: the kernel's refusal to
// exec a binary another process still holds open for writing. A shim that
// started and refused its command answers with an *exec.ExitError, which does
// not match, so real refusals and every other error come back unretried.
func isETXTBSY(err error) bool {
	return errors.Is(err, syscall.ETXTBSY)
}

// retryShimETXTBSY runs the command build constructs with op -- cmd.Run,
// cmd.Output or cmd.CombinedOutput -- and retries while op fails with
// ETXTBSY. A Cmd cannot be rerun, so every attempt gets a fresh one from
// build. The wait is a parameter so the helper's own test can exhaust it in
// milliseconds. An error that is not ETXTBSY returns at once; exhausting the
// wait returns the last error and the calling test fails on it as it would
// have. Like pollUntil, the loop also stops short of the test's own deadline.
func retryShimETXTBSY(t *testing.T, wait time.Duration, build func() *exec.Cmd, op func(*exec.Cmd) error) error {
	t.Helper()
	end := time.Now().Add(wait)
	if dl, ok := t.Deadline(); ok && dl.Before(end) {
		end = dl
	}
	for {
		err := op(build())
		if !isETXTBSY(err) || time.Now().After(end) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shimRun runs the installed shim with PATH = pathDirs (joined) plus the tools
// directory, and returns stdout, stderr and the exit code.
func shimRun(t *testing.T, shim string, pathDirs []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	path := "PATH=" + strings.Join(append(pathDirs, shimTools(t)), ":")
	var out, errb bytes.Buffer
	err := retryShimETXTBSY(t, shimETXTBSYWait, func() *exec.Cmd {
		cmd := exec.Command(shim, args...)
		cmd.Env = []string{path}
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Stdout, cmd.Stderr = &out, &errb
		return cmd
	}, (*exec.Cmd).Run)
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run shim: %v", err)
	}
	return out.String(), errb.String(), code
}

// TestRetryShimETXTBSYRetriesOnlyETXTBSY pins the helper's three rules with
// fake operations, no real ETXTBSY provoked: an ETXTBSY failure is retried on
// a freshly built command until the operation succeeds, an *exec.ExitError --
// what a shim that ran and refused its command answers with -- is returned
// after a single attempt, and exhausting the wait returns the last error.
func TestRetryShimETXTBSYRetriesOnlyETXTBSY(t *testing.T) {
	fake := func() *exec.Cmd { return &exec.Cmd{Path: "fake-shim"} }
	etxtbsy := func() error {
		return &os.PathError{Op: "fork/exec", Path: "fake-shim", Err: syscall.ETXTBSY}
	}

	attempts, builds := 0, 0
	err := retryShimETXTBSY(t, time.Second, func() *exec.Cmd {
		builds++
		return fake()
	}, func(*exec.Cmd) error {
		attempts++
		if attempts == 1 {
			return etxtbsy()
		}
		return nil
	})
	if err != nil || attempts != 2 || builds != attempts {
		t.Errorf("etxtbsy then success: err %v attempts %d builds %d, want success on the second fresh command", err, attempts, builds)
	}

	_, exitErr := exec.Command("sh", "-c", "exit 7").Output()
	var ee *exec.ExitError
	if !errors.As(exitErr, &ee) {
		t.Fatalf("sh -c 'exit 7' gave %v, want a real *exec.ExitError to pin against", exitErr)
	}
	attempts = 0
	err = retryShimETXTBSY(t, time.Second, fake, func(*exec.Cmd) error {
		attempts++
		return ee
	})
	if !errors.Is(err, ee) || attempts != 1 {
		t.Errorf("exit error: err %v attempts %d, want it back unretried after one attempt", err, attempts)
	}

	attempts = 0
	err = retryShimETXTBSY(t, 200*time.Millisecond, fake, func(*exec.Cmd) error {
		attempts++
		return fmt.Errorf("attempt %d: %w", attempts, etxtbsy())
	})
	if !errors.Is(err, syscall.ETXTBSY) || attempts < 2 {
		t.Errorf("exhaustion: err %v after %d attempts, want ETXTBSY retried to the bound", err, attempts)
	} else if want := fmt.Sprintf("attempt %d", attempts); !strings.Contains(err.Error(), want) {
		t.Errorf("exhaustion: err %q, want the error of the last attempt %q", err, want)
	}
}

func shimFixture(t *testing.T) (shimDir, realDir string) {
	t.Helper()
	root := t.TempDir()
	shimDir = filepath.Join(root, "shims")
	realDir = filepath.Join(root, "real")
	if err := EnsureShim(shimDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "git"), []byte(recordingGit), 0o755); err != nil {
		t.Fatal(err)
	}
	return shimDir, realDir
}

func TestGitShimRefusesEveryStashForm(t *testing.T) {
	t.Parallel()
	shimDir, realDir := shimFixture(t)
	shim := filepath.Join(shimDir, "git")
	forms := [][]string{
		{"stash"},
		{"stash", "push", "-m", "x"},
		{"stash", "list"},
		{"-C", "dir", "stash"},
		{"-c", "k=v", "stash", "push"},
		{"--git-dir=x", "--work-tree=y", "stash"},
		{"--git-dir", "x", "--work-tree", "y", "stash"},
		{"--no-pager", "stash", "list"},
		{"-p", "-P", "--bare", "--literal-pathspecs", "stash"},
		{"--config-env", "k=ENV", "stash"},
		{"--exec-path=/x", "stash"},
	}
	for _, args := range forms {
		stdout, stderr, code := shimRun(t, shim, []string{shimDir, realDir}, "", args...)
		if code == 0 || stdout != "" {
			t.Errorf("git %v: code %d stdout %q, want refusal", args, code, stdout)
		}
		line := strings.TrimSuffix(stderr, "\n")
		if strings.Contains(line, "\n") || !strings.Contains(line, "WIP commit") || !strings.Contains(line, "outside the repo") {
			t.Errorf("git %v: stderr %q, want one line naming WIP commit and copies outside the repo", args, stderr)
		}
	}
}

func TestGitShimPassesEverythingElseThrough(t *testing.T) {
	t.Parallel()
	shimDir, realDir := shimFixture(t)
	shim := filepath.Join(shimDir, "git")
	forms := [][]string{
		{"status"},
		{"log", "--oneline"},
		{},
		{"-C", "stash", "status"},
		{"-c", "k=stash", "log"},
		{"commit", "-m", "stash"},
		{"--no-pager", "diff"},
	}
	for _, args := range forms {
		stdout, _, code := shimRun(t, shim, []string{shimDir, realDir}, "piped\n", args...)
		want := "real git: " + strings.Join(args, " ") + "\npiped\n"
		if stdout != want || code != 7 {
			t.Errorf("git %v: stdout %q code %d, want %q code 7", args, stdout, code, want)
		}
	}
}

func TestGitShimRewritesNetworkURLsForEveryPushForm(t *testing.T) {
	t.Parallel()
	shimDir, realDir := shimFixture(t)
	shim := filepath.Join(shimDir, "git")
	r := "url.relevo-refuses-pushes-from-a-round://.pushInsteadOf="
	rewrite := "-c " + r + "https:// -c " + r + "http:// -c " + r + "ssh:// -c " + r + "git:// -c " + r + "git@ "
	forms := [][]string{
		{"push"},
		{"push", "origin", "HEAD:main"},
		{"-C", "dir", "push", "--delete", "origin", "x"},
		{"--no-pager", "push", "-f"},
	}
	for _, args := range forms {
		stdout, _, code := shimRun(t, shim, []string{shimDir, realDir}, "", args...)
		want := "real git: " + rewrite + strings.Join(args, " ") + "\n"
		if stdout != want || code != 7 {
			t.Errorf("git %v: stdout %q code %d, want %q code 7", args, stdout, code, want)
		}
	}
}

// TestGitShimRefusesANetworkPushButNotALocalOne runs the real git behind the
// shim: a push to a network URL fails before connecting, whether it names a
// remote or the URL itself, while a push to a local bare repository lands.
func TestGitShimRefusesANetworkPushButNotALocalOne(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("no git: %v", err)
	}
	root := t.TempDir()
	shimDir := filepath.Join(root, "shims")
	if err := EnsureShim(shimDir); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + shimDir + ":" + os.Getenv("PATH"),
		"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
	repo := filepath.Join(root, "repo")
	git := func(args ...string) (string, error) {
		var out []byte
		err := retryShimETXTBSY(t, shimETXTBSYWait, func() *exec.Cmd {
			cmd := exec.Command(filepath.Join(shimDir, "git"), args...)
			cmd.Env = env
			cmd.Dir = root
			return cmd
		}, func(cmd *exec.Cmd) error {
			var err error
			out, err = cmd.CombinedOutput()
			return err
		})
		return string(out), err
	}
	for _, args := range [][]string{
		{"init", "-q", "--bare", filepath.Join(root, "local.git")},
		{"init", "-q", repo},
		{"-C", repo, "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", repo, "remote", "add", "origin", "https://example.invalid/r.git"},
		{"-C", repo, "remote", "add", "scp", "git@example.invalid:r.git"},
		{"-C", repo, "remote", "add", "local", filepath.Join(root, "local.git")},
	} {
		if out, err := git(args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, target := range []string{"origin", "scp", "https://example.invalid/d.git"} {
		out, err := git("-C", repo, "push", target, "HEAD:refs/heads/main")
		if err == nil || !strings.Contains(out, "relevo-refuses-pushes-from-a-round") {
			t.Errorf("push %s: err %v, output %q; want a refusal naming relevo-refuses-pushes-from-a-round", target, err, out)
		}
	}
	if out, err := git("-C", repo, "push", "local", "HEAD:refs/heads/main"); err != nil {
		t.Errorf("push to a local bare repository: %v\n%s", err, out)
	}
}

func TestGitShimSkipsItselfWhenListedTwiceOrSymlinked(t *testing.T) {
	t.Parallel()
	shimDir, realDir := shimFixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(shimDir, link); err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(shimDir, "git")
	for _, dirs := range [][]string{
		{shimDir, shimDir, realDir},
		{link, shimDir, realDir},
		{shimDir, link, realDir},
	} {
		stdout, _, code := shimRun(t, shim, dirs, "", "status")
		if stdout != "real git: status\n" || code != 7 {
			t.Errorf("PATH %v: stdout %q code %d, want the real git's", dirs, stdout, code)
		}
	}
	_, stderr, code := shimRun(t, shim, []string{shimDir, shimDir}, "", "status")
	if code != 127 || !strings.Contains(stderr, "not found") {
		t.Errorf("no real git: code %d stderr %q, want 127 and a not-found line", code, stderr)
	}
}

func TestGitShimNestedUnderAnotherShimReachesTheRealGit(t *testing.T) {
	t.Parallel()
	outerDir, realDir := shimFixture(t)
	innerDir := filepath.Join(t.TempDir(), "inner")
	if err := EnsureShim(innerDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := "PATH=" + strings.Join([]string{innerDir, outerDir, realDir, shimTools(t)}, ":")
	var out []byte
	err := retryShimETXTBSY(t, shimETXTBSYWait, func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, filepath.Join(innerDir, "git"), "status")
		cmd.Env = []string{path}
		return cmd
	}, func(cmd *exec.Cmd) error {
		var err error
		out, err = cmd.Output()
		return err
	})
	if ctx.Err() != nil {
		t.Fatalf("nested shims did not return within 10s: the two shims exec each other")
	}
	if string(out) != "real git: status\n" {
		t.Errorf("stdout %q err %v, want the real git's", out, err)
	}
}

func TestEnsureGitShimIsIdempotent(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "shims")
	if err := EnsureShim(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "git")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(path)
	if err := EnsureShim(dir); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	fi2, _ := os.Stat(path)
	if !bytes.Equal(first, second) || !os.SameFile(fi1, fi2) || fi2.Mode().Perm() != 0o755 {
		t.Errorf("second ensure changed the shim")
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureShim(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != ShimSource {
		t.Errorf("ensure did not restore a changed shim")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestGitShimPATH(t *testing.T) {
	t.Parallel()
	if got := ShimPATH("", "/s"); got != "/s" {
		t.Errorf("empty PATH = %q, want /s with no trailing colon", got)
	}
	if got := ShimPATH("/a:/b", "/s"); got != "/s:/a:/b" {
		t.Errorf("PATH = %q, want /s:/a:/b", got)
	}
}
