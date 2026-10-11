package isolate

import (
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestContainerArgvDoesNotMountTheGitShimDir pins the shipped container-mode
// behaviour: a writer round's PATH entry for the host git shim is passed in,
// but the shim directory is not mounted, so inside a container the entry names
// a missing directory the shim's sh skips and the image's own git runs. The
// shim protects local and user-mode rounds only.
func TestContainerArgvDoesNotMountTheGitShimDir(t *testing.T) {
	shimDir := "/state/relevo/shims"
	spec := spawn.ProcSpec{
		Dir:        "/round/tree",
		Argv:       []string{"claude"},
		Env:        []string{"PATH=" + shimDir + ":/usr/bin"},
		StreamPath: "/binding/r1-runner.jsonl",
	}
	got := ContainerArgv(spec, containerSpecFixture())
	if !slices.Contains(got.Argv, "PATH="+shimDir+":/usr/bin") {
		t.Errorf("argv %v lacks the PATH env entry", got.Argv)
	}
	for _, a := range got.Argv {
		if strings.HasPrefix(a, shimDir) {
			t.Errorf("argv mounts %q; the shim dir must stay unmounted", a)
		}
	}
}
