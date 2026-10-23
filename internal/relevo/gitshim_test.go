package relevo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/store"
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

// shimRun runs the installed shim with PATH = pathDirs (joined) plus the tools
// directory, and returns stdout, stderr and the exit code.
func shimRun(t *testing.T, shim string, pathDirs []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(shim, args...)
	cmd.Env = []string{"PATH=" + strings.Join(append(pathDirs, shimTools(t)), ":")}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run shim: %v", err)
	}
	return out.String(), errb.String(), code
}

func shimFixture(t *testing.T) (shimDir, realDir string) {
	t.Helper()
	root := t.TempDir()
	shimDir = filepath.Join(root, "shims")
	realDir = filepath.Join(root, "real")
	if err := ensureGitShim(shimDir); err != nil {
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
	if err := ensureGitShim(shimDir); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=" + shimDir + ":" + os.Getenv("PATH"),
		"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	}
	repo := filepath.Join(root, "repo")
	git := func(args ...string) (string, error) {
		cmd := exec.Command(filepath.Join(shimDir, "git"), args...)
		cmd.Env = env
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
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
	if err := ensureGitShim(innerDir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(innerDir, "git"), "status")
	cmd.Env = []string{"PATH=" + strings.Join([]string{innerDir, outerDir, realDir, shimTools(t)}, ":")}
	out, err := cmd.Output()
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
	if err := ensureGitShim(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "git")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi1, _ := os.Stat(path)
	if err := ensureGitShim(dir); err != nil {
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
	if err := ensureGitShim(dir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != gitShimSource {
		t.Errorf("ensure did not restore a changed shim")
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755", fi.Mode().Perm())
	}
}

func TestGitShimPATH(t *testing.T) {
	t.Parallel()
	if got := gitShimPATH("", "/s"); got != "/s" {
		t.Errorf("empty PATH = %q, want /s with no trailing colon", got)
	}
	if got := gitShimPATH("/a:/b", "/s"); got != "/s:/a:/b" {
		t.Errorf("PATH = %q, want /s:/a:/b", got)
	}
}

func startSpecEnv(t *testing.T, rt Runtime, fr *fakeRunner, b store.Binding) []string {
	t.Helper()
	b.Round, b.CWD = 1, t.TempDir()
	b.Builder = store.Endpoint{Mode: store.ModeHeadless}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := startProcess(context.Background(), rt, tx, b, []string{"echo"}, candidate.Candidate{Harness: "agy"})
		return err
	})
	if err != nil {
		t.Fatalf("startProcess: %v", err)
	}
	return fr.specs[len(fr.specs)-1].Env
}

func TestStartProcessPutsTheShimFirstOnAWriterPATH(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	env := startSpecEnv(t, rt, fr, store.Binding{Name: "w", Role: "builder", Shape: store.ShapeWriter})
	want := "PATH=" + rt.Store.GitShimDir() + ":/usr/bin:/bin"
	if !reflect.DeepEqual(env, []string{"RELEVO_RUNNER=w", want}) {
		t.Errorf("writer env = %v, want runner marker then %q", env, want)
	}
	if _, err := os.Stat(filepath.Join(rt.Store.GitShimDir(), "git")); err != nil {
		t.Errorf("shim not installed: %v", err)
	}
}

func TestStartProcessShimFollowsTheShapeNotTheActorName(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	cases := []struct {
		role, shape string
		shim        bool
	}{
		{"live-builder", store.ShapeWriter, true},
		{"librarian", store.ShapeWriter, true},
		{"builder", store.ShapeReader, false},
		{"reviewer", store.ShapeReader, false},
	}
	for _, tc := range cases {
		b := store.Binding{Name: "x", Role: tc.role, Shape: tc.shape}
		env, err := gitShimEnv(rt, b)
		if err != nil || (len(env) == 1) != tc.shim {
			t.Errorf("role %q shape %q: env %v err %v, shim wanted = %v", tc.role, tc.shape, env, err, tc.shim)
		}
		if tc.shim {
			if got := startSpecEnv(t, rt, fr, b); len(got) != 2 {
				t.Errorf("role %q: spawn env %v, want the shim PATH entry", tc.role, got)
			}
		}
	}
}

func TestStartProcessRefusesTheRoundWhenTheShimCannotBeWritten(t *testing.T) {
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	if err := os.WriteFile(rt.Store.GitShimDir(), []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := store.Binding{Name: "w", Round: 1, CWD: t.TempDir(), Shape: store.ShapeWriter}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := startProcess(context.Background(), rt, tx, b, []string{"echo"}, candidate.Candidate{Harness: "agy"})
		return err
	})
	if err == nil {
		t.Fatal("startProcess succeeded with an unwritable shim dir")
	}
	if len(fr.specs) != 0 {
		t.Errorf("spawned %d processes, want none", len(fr.specs))
	}
}

func TestGateProcessCarriesNoEnvAndReadersGetNoShim(t *testing.T) {
	t.Parallel()
	fr := newFakeRunner()
	rt := newRuntime(t)
	rt.Runner = fr
	dir := t.TempDir()
	if _, _, err := startGateProc(context.Background(), rt, gateProcSpec{Dir: dir, Command: "true", LogPath: filepath.Join(dir, "g.log")}, 0); err != nil {
		t.Fatal(err)
	}
	if len(fr.specs) != 1 || fr.specs[0].Env != nil {
		t.Errorf("gate specs = %+v, want one with nil Env", fr.specs)
	}
	for _, b := range []store.Binding{{Name: "r", Shape: store.ShapeReader}, {Name: "r"}} {
		if needsGitShim(b) {
			t.Errorf("needsGitShim(%+v) = true, want false for a non-writer", b)
		}
	}
}
