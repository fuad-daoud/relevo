package ui

import (
	"os/exec"
	"strings"
	"testing"
)

// TestUIPackageDoesNotImportTheSyncWorker pins the one-writer boundary: every
// cockpit sync action sends its verb to the daemon over the owner socket, so
// this package never reaches for the pipe client or the worker process. A
// direct import is what would put a second sync engine in the cockpit's process
// on the same replica the daemon's worker holds, and go list sees it even when
// the import is aliased.
func TestUIPackageDoesNotImportTheSyncWorker(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, "github.com/fuad-daoud/relevo/internal/ui").Output()
	if err != nil {
		t.Fatalf("go list -f imports: %v", err)
	}
	for _, forbidden := range []string{
		"github.com/fuad-daoud/relevo/internal/syncpipe",
		"github.com/fuad-daoud/relevo/internal/syncworker",
	} {
		for _, line := range strings.Fields(string(out)) {
			if line == forbidden || strings.HasPrefix(line, forbidden+"/") {
				t.Errorf("internal/ui imports %s; a cockpit action must reach the daemon over the owner socket instead of building a worker", line)
			}
		}
	}
}
