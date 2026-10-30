package main

import (
	"flag"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// doctorFlagValues holds the pointer doctor parses into.
type doctorFlagValues struct {
	asJSON *bool
}

// doctorFlagSet defines that flag on fs and returns what it parses into, so
// the registry's parity test finds exactly one installer per verb.
func doctorFlagSet(fs *flag.FlagSet) *doctorFlagValues {
	v := &doctorFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the report as a JSON document")
	return v
}

// doctorDeps gathers the cmd-owned values the doctor report reads: the build
// facts and helpers that stay in cmd/relevo because other commands use them.
func doctorDeps(rt relevo.Runtime) doctor.Deps {
	d := doctor.Deps{
		Env:              unitEnv{Env: doctor.NewEnv(rt.Store, releaseInputs())},
		Version:          buildVersion(),
		ChatResolver:     chatResolver(),
		RestartCheck:     restartCheck,
		InsertRestartRow: insertRestartRow,
		SyncMeter: func(rt relevo.Runtime) (relevosync.QuotaReading, bool, relevosync.ExportHour, bool) {
			return syncMeter(rt.Store, time.Now())
		},
		RepoOf:    mastermindRepoOf,
		RepoKnown: mastermindRepoKnown,
	}
	if root, err := userConfigRoot(); err == nil {
		d.ConfigRoot = root
	}
	return d
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("relevo doctor", flag.ContinueOnError)
	v := doctorFlagSet(fs)
	asJSON := v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}

	rep, err := doctor.RunReport(rt, L, doctorDeps(rt))
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	if *asJSON {
		if err := printDoc(doctorDocOf(rep)); err != nil {
			return err
		}
	} else {
		doctor.Render(os.Stdout, rep)
	}

	// A failing check still exits 1 in both shapes: the exit code is part of
	// the contract, and --json only changes where the report goes.
	if rep.Failures() > 0 {
		return exitCodeErr{code: 1}
	}
	return nil
}
