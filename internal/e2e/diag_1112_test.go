package e2e

import (
	"io/fs"

	"github.com/fuad-daoud/relevo/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// diagDump prints every file under the state root whose path names one of the
// given binding or chain names, so a halted chain on a CI runner shows the
// member's stream, staged seed and chain inputs. Throwaway diagnostics for #1112.
func diagDump(t *testing.T, root string, names ...string) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		hit := false
		for _, n := range names {
			if strings.Contains(rel, n) {
				hit = true
			}
		}
		if !hit {
			return nil
		}
		body, _ := os.ReadFile(path)
		if len(body) > 6000 {
			body = append(body[:6000], []byte("\n...[truncated]")...)
		}
		t.Logf("DIAG1112 file %s (%d bytes):\n%s", rel, len(body), body)
		return nil
	})
}

// diagSeeds prints the state root and every seed the reviewer was sent, read
// through the store so a sealed round still answers, then fails the test so CI
// shows it. Throwaway diagnostics for #1112.
func diagSeeds(t *testing.T, st *store.Store, name string) {
	t.Helper()
	t.Logf("DIAG1112 root %s", st.Root())
	if resolved, err := filepath.EvalSymlinks(st.Root()); err == nil {
		t.Logf("DIAG1112 root resolves to %s", resolved)
	}
	for r := 1; r <= 3; r++ {
		body, err := st.ReadFile(st.PromptPath(name, r))
		if err != nil {
			continue
		}
		t.Logf("DIAG1112 %s round %d seed:\n%s", name, r, body)
		for _, line := range strings.Split(string(body), "\n") {
			i := strings.Index(line, ": /")
			if i < 0 {
				continue
			}
			p := strings.TrimSuffix(line[i+2:], ".")
			_, statErr := os.Stat(p)
			t.Logf("DIAG1112 seed path %s copied=%v exists_now=%v", p, strings.Contains(p, "/inputs/"), statErr == nil)
		}
	}
	t.Errorf("DIAG1112 forced failure so the diagnostics print")
}
