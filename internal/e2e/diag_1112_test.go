package e2e

import (
	"io/fs"
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
