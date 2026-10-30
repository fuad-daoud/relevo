package doctor

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// The doctor's disk walks stay cheap: whichever budget runs out first ends
// the walk and the row says its numbers are a floor.
const (
	diskWalkBudget     = 2 * time.Second
	diskWalkMaxEntries = 500000
)

// diskInputs are the seams diskChecks reads, so a test names its own paths and
// never starts a go process.
type diskInputs struct {
	TmpRoot  string
	WorkDir  string
	Settings relevo.RepoSettings
	Run      func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// DiskRun is how the doctor asks go for its cache. It is a variable so tests
// answer for go instead of starting it.
var DiskRun = execRun

// diskChecks returns the tmp root row, the Go cache row, and, when the working
// directory is a Go tree, the effective -trimpath row. A go that cannot answer
// is never an error: its cache row is informational.
func diskChecks(ctx context.Context, in diskInputs) []Check {
	var out []Check
	if in.TmpRoot != "" {
		u := relevo.TmpUsageOf(in.TmpRoot, diskWalkBudget, diskWalkMaxEntries)
		out = append(out, TmpRootCheck(u.Rounds, u.Checks, u.Loose, u.Truncated, DefaultTmpWarnBytes))
	}
	dir, err := relevo.GoCacheDir(ctx, in.Run)
	if err != nil {
		dir = ""
	}
	var size int64
	var truncated bool
	if dir != "" {
		size, truncated = relevo.GoCacheUsage(dir, diskWalkBudget, diskWalkMaxEntries)
	}
	out = append(out, GoCacheCheck(dir, size, truncated, DefaultGoCacheWarnBytes))
	if in.WorkDir != "" && relevo.IsGoTree(in.WorkDir) {
		out = append(out, TrimpathCheck(in.Settings.TrimpathOn()))
	}
	return out
}

// runDiskChecks wires diskChecks to the real working directory and go binary.
// A repository setting that will not read leaves the default, which is on.
func runDiskChecks(rt relevo.Runtime) []Check {
	ctx := context.Background()
	in := diskInputs{TmpRoot: rt.TmpRoot, Run: DiskRun}
	if wd, err := os.Getwd(); err == nil {
		in.WorkDir = wd
		if settings, serr := relevo.RepoSettingsFor(ctx, rt, wd); serr == nil {
			in.Settings = settings
		}
	}
	return diskChecks(ctx, in)
}
