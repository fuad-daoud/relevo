package ui

import (
	"os/exec"
	"strings"
	"testing"
)

// TestUIPackageDoesNotImportTheSyncWorker pins the one-writer boundary: every
// cockpit sync action sends its verb to the daemon over the owner socket, so
// this package never reaches for the pipe client or the worker process. The
// walk is over the transitive dependency set, not the direct imports, because
// a helper package this one imports and that imports the pipe would put a
// second sync engine in the cockpit's process on the same replica the daemon's
// worker holds -- and go list -deps sees it even when the import is aliased.
func TestUIPackageDoesNotImportTheSyncWorker(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "github.com/fuad-daoud/relevo/internal/ui").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, forbidden := range []string{
		"github.com/fuad-daoud/relevo/internal/syncpipe",
		"github.com/fuad-daoud/relevo/internal/syncworker",
	} {
		for _, line := range strings.Fields(string(out)) {
			if line == forbidden || strings.HasPrefix(line, forbidden+"/") {
				t.Errorf("internal/ui reaches %s transitively; a cockpit action must reach the daemon over the owner socket instead of building a worker", line)
			}
		}
	}
}
