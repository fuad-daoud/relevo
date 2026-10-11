//go:build linux

package upgrade

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	childEnv = "RELEVO_UPGRADE_SELF_IMAGE_CHILD"
	pathEnv  = "RELEVO_UPGRADE_SELF_IMAGE_PATH"
)

// childReport is what the re-executed copy says about itself: the identity it
// reads for the image it is running, and the one it reads for the path it was
// started from.
type childReport struct {
	Image    store.FileID
	Path     store.FileID
	ImageErr string
	PathErr  string
}

// TestSelfImageIdentityIsTheRunningImageNotThePath pins the baseline source
// itself. A copy of this binary is exec'd from a temp path, a different file is
// renamed over that path while the copy runs, and the copy must still read its
// own image as the copy it started as, while the path reads as the
// replacement. Only Linux names the running image independently of the path,
// and that independence is the whole of the daemon's baseline: a baseline read
// from the path adopts the replacement and leaves the watcher nothing to fire
// on. Mutation: SelfImageIdentity falls back to a stat of the resolved path.
func TestSelfImageIdentityIsTheRunningImageNotThePath(t *testing.T) {
	if os.Getenv(childEnv) == "1" {
		selfImageChild()
		return
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "relevo")
	copySelf(t, path)

	original, err := ExeIdentity(path)
	if err != nil {
		t.Fatalf("ExeIdentity of the copy: %v", err)
	}

	cmd := exec.Command(path, "-test.run=^"+t.Name()+"$")
	cmd.Env = append(os.Environ(), childEnv+"=1", pathEnv+"="+path)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the copy: %v", err)
	}
	out := bufio.NewReader(stdout)

	if line, err := readLine(out); err != nil || line != "ready" {
		t.Fatalf("child never reported ready (line %q, err %v)", line, err)
	}

	// The rename lands while the copy runs, which is the window the daemon's
	// baseline capture sits in.
	replacement := filepath.Join(dir, "relevo.new")
	if err := os.WriteFile(replacement, []byte("a different image"), 0o755); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("rename over the running copy: %v", err)
	}

	if _, err := io.WriteString(stdin, "go\n"); err != nil {
		t.Fatalf("ask the child to report: %v", err)
	}
	line, err := readLine(out)
	if err != nil {
		t.Fatalf("read the child's report: %v", err)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("the copy exited badly: %v", err)
	}

	var got childReport
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("decode the child's report %q: %v", line, err)
	}
	if got.ImageErr != "" || got.PathErr != "" {
		t.Fatalf("the copy could not read itself: image %q, path %q", got.ImageErr, got.PathErr)
	}

	if got.Image != original {
		t.Errorf("running image = %+v, want the copy's own identity %+v; a baseline read from the path adopts the replacement instead",
			got.Image, original)
	}
	if got.Path == original {
		t.Errorf("path identity = %+v, unchanged after a rename over it", got.Path)
	}
}

// TestSelfImageIdentityFailureIsNoBaseline is the disabled-re-exec case: a self
// image that cannot be read is an error carrying no identity, so the daemon
// warns and runs on rather than watching from an identity that means nothing.
// Mutation: swallow the stat error and return the zero value with nil error,
// which reads as a usable baseline and leaves re-exec enabled on a daemon that
// never identified itself.
func TestSelfImageIdentityFailureIsNoBaseline(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-image")

	got, err := selfImageIdentity(missing)
	if err == nil {
		t.Error("selfImageIdentity on a missing link returned no error, so a failure would be mistaken for a baseline")
	}
	if got != (store.FileID{}) {
		t.Errorf("selfImageIdentity on a missing link = %+v, want the zero identity", got)
	}

	// The zero value is only safe because no real identity equals it, so a
	// failed read can never be read back as "still ours".
	image, err := SelfImageIdentity()
	if err != nil {
		t.Fatalf("SelfImageIdentity: %v", err)
	}
	if image == (store.FileID{}) {
		t.Error("a real image identity equals the zero value a failed read returns")
	}
}

// selfImageChild is the re-executed copy: it prints one ready line, waits for
// the parent to rename over its path, then reports both identities.
func selfImageChild() {
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		fmt.Fprintln(os.Stderr, "child: ready:", err)
		os.Exit(2)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		fmt.Fprintln(os.Stderr, "child: no go signal:", err)
		os.Exit(2)
	}

	image, imageErr := SelfImageIdentity()
	path, pathErr := ExeIdentity(os.Getenv(pathEnv))
	rep := childReport{Image: image, Path: path}
	if imageErr != nil {
		rep.ImageErr = imageErr.Error()
	}
	if pathErr != nil {
		rep.PathErr = pathErr.Error()
	}
	raw, err := json.Marshal(rep)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: encode:", err)
		os.Exit(2)
	}
	if _, err := fmt.Fprintln(os.Stdout, string(raw)); err != nil {
		fmt.Fprintln(os.Stderr, "child: report:", err)
		os.Exit(2)
	}
	os.Exit(0)
}

// copySelf puts a runnable copy of this test binary at path, the way a daemon
// starts from an installed binary.
func copySelf(t *testing.T, path string) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatalf("open the test binary: %v", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create the copy: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy the test binary: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close the copy: %v", err)
	}
}

// readLine reads one line, without the newline.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	return strings.TrimRight(line, "\n"), err
}
