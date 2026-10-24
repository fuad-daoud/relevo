package main

import (
	"context"
	"os"
	"os/exec"
	"time"

	"github.com/fuad-daoud/relevo/internal/doctor"
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

// diskChecks returns the tmp root row, the Go cache row, and, when the working
// directory is a Go tree, the effective -trimpath row. A go that cannot answer
// is never an error: its cache row is informational.
func diskChecks(ctx context.Context, in diskInputs) []doctor.Check {
	var out []doctor.Check
	if in.TmpRoot != "" {
		u := relevo.TmpUsageOf(in.TmpRoot, diskWalkBudget, diskWalkMaxEntries)
		out = append(out, doctor.TmpRootCheck(u.Rounds, u.Checks, u.Loose, u.Truncated, doctor.DefaultTmpWarnBytes))
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
	out = append(out, doctor.GoCacheCheck(dir, size, truncated, doctor.DefaultGoCacheWarnBytes))
	if in.WorkDir != "" && relevo.IsGoTree(in.WorkDir) {
		out = append(out, doctor.TrimpathCheck(in.Settings.TrimpathOn()))
	}
	return out
}

// runDiskChecks wires diskChecks to the real working directory and go binary.
// A repository setting that will not read leaves the default, which is on.
func runDiskChecks(rt relevo.Runtime) []doctor.Check {
	ctx := context.Background()
	in := diskInputs{TmpRoot: rt.TmpRoot, Run: execRun}
	if wd, err := os.Getwd(); err == nil {
		in.WorkDir = wd
		if settings, serr := relevo.RepoSettingsFor(ctx, rt, wd); serr == nil {
			in.Settings = settings
		}
	}
	return diskChecks(ctx, in)
}
