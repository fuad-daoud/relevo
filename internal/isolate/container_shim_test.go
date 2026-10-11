package isolate

import (
	"slices"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

const testShimDir = "/state/relevo/.shims"

func shimSpec(path string) spawn.ProcSpec {
	return spawn.ProcSpec{
		Dir:        "/round/tree",
		Argv:       []string{"claude"},
		Env:        []string{"PATH=" + path},
		StreamPath: "/binding/r1-runner.jsonl",
	}
}

func shimMounts(argv []string) []string {
	var got []string
	for i, a := range argv {
		if a == "-v" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], testShimDir) {
			got = append(got, argv[i+1])
		}
	}
	return got
}

// TestContainerArgvMountsTheGitShimForAWriterRound: inside a container the
// round still runs worktrees of one repository, so a writer round's shim must
// be reachable there, read-only so the builder cannot rewrite it.
func TestContainerArgvMountsTheGitShimForAWriterRound(t *testing.T) {
	c := containerSpecFixture()
	c.GitShimDir = testShimDir
	got := ContainerArgv(shimSpec(testShimDir+":/usr/bin"), c)
	want := []string{testShimDir + ":" + testShimDir + ":ro"}
	if m := shimMounts(got.Argv); !slices.Equal(m, want) {
		t.Fatalf("shim mounts = %v, want %v (argv %v)", m, want, got.Argv)
	}
	tmpfs := slices.Index(got.Argv, "--tmpfs")
	if shim := slices.Index(got.Argv, want[0]); shim > tmpfs {
		t.Errorf("shim mount at %d comes after --tmpfs at %d", shim, tmpfs)
	}
}

// TestContainerArgvSkipsTheGitShimWhenPATHLacksIt: a reader round's PATH
// does not carry the shim, and on a server that has never run a writer round
// the directory does not exist, which podman refuses to bind.
func TestContainerArgvSkipsTheGitShimWhenPATHLacksIt(t *testing.T) {
	cases := map[string]struct {
		shimDir string
		path    string
	}{
		"reader PATH":         {testShimDir, "/usr/bin"},
		"shim not first":      {testShimDir, "/usr/bin:" + testShimDir},
		"no shim dir in spec": {"", testShimDir + ":/usr/bin"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := containerSpecFixture()
			c.GitShimDir = tc.shimDir
			got := ContainerArgv(shimSpec(tc.path), c)
			if m := shimMounts(got.Argv); len(m) != 0 {
				t.Errorf("shim mounts = %v, want none", m)
			}
		})
	}
}
