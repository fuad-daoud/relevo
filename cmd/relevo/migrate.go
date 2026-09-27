package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/fuad-daoud/relevo/dist"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/migrate"
)

// cmdMigrate is `relevo migrate` (#292 §3): one ordered cutover that moves the
// relay-era state and config to their relevo-era names, rewrites the paths // name-guard: legacy
// stored inside them, switches the client service unit, and removes the old
// binary. Every step is reported, and a re-run resumes from whatever a crash
// left behind.
//
// --state-from/--state-to runs the core alone on an explicit pair, which is
// what contabo's server uses; units and binaries are not touched then.
func cmdMigrate(args []string) error {
	const usage = `usage: relevo migrate [--dry-run] [--keep-old-binary]
       relevo migrate --state-from DIR --state-to DIR [--dry-run]`

	fs := flag.NewFlagSet("relevo migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "report every step without touching anything")
	keepOld := fs.Bool("keep-old-binary", false, "do not remove the old relay binary beside relevo") // name-guard: legacy
	stateFrom := fs.String("state-from", "", "migrate only this state root")
	stateTo := fs.String("state-to", "", "to this state root")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if (*stateFrom == "") != (*stateTo == "") {
		fmt.Fprintln(os.Stderr, "relevo migrate: --state-from and --state-to must be given together")
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	ctx := context.Background()

	// The repair timeout is longer than newRuntime's: a cutover repairs every
	// worktree a state-root rename relocated, one git process each.
	gitClient := git.NewClient("git", 60*time.Second, git.DefaultMaxPatchBytes)
	repair := func(ctx context.Context, repo, wt string) error {
		return gitClient.WorktreeRepair(ctx, repo, wt)
	}
	rewrite := func(ctx context.Context, path string, pairs []migrate.Prefix) (int64, bool, error) {
		dbPairs := make([]db.Prefix, len(pairs))
		for i, p := range pairs {
			dbPairs[i] = db.Prefix{Old: p.Old, New: p.New}
		}
		return db.RewritePathPrefix(ctx, path, dbPairs)
	}

	base := migrate.Options{
		Alive:     pidAlive,
		Repair:    repair,
		RewriteDB: rewrite,
		Out:       os.Stdout,
	}

	if *stateFrom != "" {
		o := base
		o.StateFrom, o.StateTo = *stateFrom, *stateTo
		o.DryRun = *dryRun
		res, err := migrate.Run(ctx, o)
		if err != nil {
			return migrateFailure(err)
		}
		if res.Nothing {
			fmt.Println("nothing to migrate")
		}
		return nil
	}

	roots, err := renameRoots()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("relevo migrate: resolve home directory: %w", err)
	}
	configHome, err := userConfigRoot()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("relevo migrate: resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	units := migrate.DefaultClientUnits(runtime.GOOS, configHome, home)
	uo := migrate.UnitOptions{
		Units:          units,
		Services:       migrate.OSServices(runtime.GOOS),
		ClientUnitText: dist.ClientUnit,
		PlistTemplate:  dist.LaunchdPlist,
		Exe:            exe,
		Home:           home,
		DryRun:         *dryRun,
		Out:            os.Stdout,
	}

	o := base
	o.StateFrom, o.StateTo = roots.OldState, roots.NewState
	o.ConfigFrom, o.ConfigTo = roots.OldConfig, roots.NewConfig
	o.DefaultServeRoot = filepath.Join(roots.OldState, "serve")

	// 1. Dry-run pre-check, before anything is stopped. The daemon check is
	// skipped here -- the old daemon is still up -- and runs on the real pass.
	fmt.Println("pre-check: dry run of the core migration")
	pre := o
	pre.DryRun, pre.SkipDaemonCheck = true, true
	preRes, err := migrate.Run(ctx, pre)
	if err != nil {
		return migrateFailure(err)
	}

	oldUnitPath := units.Old.Path
	oldBinaryPath := filepath.Join(filepath.Dir(exe), legacy.Binary)
	if preRes.Nothing && !migratePathExists(oldUnitPath) && !migratePathExists(oldBinaryPath) {
		fmt.Println("nothing to migrate")
		return nil
	}

	if *dryRun {
		_, old, err := migrate.StopOld(ctx, uo)
		if err != nil {
			return migrateFailure(err)
		}
		if _, err := migrate.SwapClient(ctx, uo, old); err != nil {
			return migrateFailure(err)
		}
		step, err := migrate.RemoveOldBinary(exe, *keepOld, true)
		if err != nil {
			return migrateFailure(err)
		}
		printMigrateStep(step, true)
		// Nothing has moved yet in a dry run, so the new config root holds no
		// policy.json to report on. When the old root holds one, report
		// against that, so the step says what the real run would do (#292 §6).
		sliceConfig := roots.NewConfig
		if !migratePathExists(filepath.Join(sliceConfig, "policy.json")) &&
			migratePathExists(filepath.Join(roots.OldConfig, "policy.json")) {
			sliceConfig = roots.OldConfig
		}
		step, err = migrate.RenameSliceValue(sliceConfig, true)
		if err != nil {
			return migrateFailure(err)
		}
		printMigrateStep(step, true)
		printNextSteps()
		return nil
	}

	// 2. Stop the old client service, if it is installed and running.
	_, old, err := migrate.StopOld(ctx, uo)
	if err != nil {
		return migrateFailure(err)
	}

	// 3. The real core run, daemon check on.
	res, err := migrate.Run(ctx, o)
	if err != nil {
		if old.WasActive {
			if serr := uo.Services.Start(ctx, uo.Units.Old); serr != nil {
				fmt.Fprintf(os.Stderr, "relevo migrate: restart %s: %v\n", uo.Units.Old.Name, serr)
			} else {
				fmt.Fprintf(os.Stderr, "relevo migrate: restarted %s\n", uo.Units.Old.Name)
			}
		}
		return migrateFailure(err)
	}

	// 3b. The slice value in the moved policy.json.
	step, err := migrate.RenameSliceValue(roots.NewConfig, false)
	if err != nil {
		return postCoreFailure("the slice value was not renamed", err)
	}
	printMigrateStep(step, false)

	// 4/5. Install the new client unit and retire the old one.
	if _, err := migrate.SwapClient(ctx, uo, old); err != nil {
		printSwapRecovery(err, uo, old)
		return exitCodeErr{code: 1}
	}

	// 6. Remove the old binary.
	step, err = migrate.RemoveOldBinary(exe, *keepOld, false)
	if err != nil {
		return postCoreFailure("the old binary was not removed", err)
	}
	printMigrateStep(step, false)

	// 7. Warnings, then next steps.
	printWarnings(res)
	printNextSteps()
	return nil
}

// migrateFailure prints err and reports exit 1: a refusal or any other core
// failure leaves the state for a re-run to resume.
func migrateFailure(err error) error {
	fmt.Fprintln(os.Stderr, err)
	return exitCodeErr{code: 1}
}

// postCoreFailure reports a failure that happened after the data moved. The
// data is never rolled back -- that would be a second migration -- so it says
// so and leaves the rest to a re-run.
func postCoreFailure(what string, err error) error {
	fmt.Fprintf(os.Stderr, "relevo migrate: the data is migrated, but %s: %v\n", what, err)
	return exitCodeErr{code: 1}
}

// printMigrateStep renders one step of the unit report the way migrate.Run
// renders the core's: an optional dry-run prefix, then name and detail.
func printMigrateStep(step migrate.Step, dryRun bool) {
	prefix := ""
	if dryRun {
		prefix = "(dry run) "
	}
	fmt.Printf("%s%s: %s\n", prefix, step.Name, step.Detail)
}

// printWarnings repeats every step the core flagged, so a newer database or a
// failed worktree repair cannot scroll off the top of a long report.
func printWarnings(res migrate.Result) {
	var warnings []migrate.Step
	for _, s := range res.Steps {
		if s.Warn {
			warnings = append(warnings, s)
		}
	}
	if len(warnings) == 0 {
		return
	}
	fmt.Println("warnings:")
	for _, s := range warnings {
		fmt.Printf("  %s: %s\n", s.Name, s.Detail)
	}
}

// printNextSteps names what is left to the human after the cutover.
func printNextSteps() {
	fmt.Println("next steps:")
	fmt.Println("  relevo doctor")
	fmt.Println("  relevo config agents")
	fmt.Println("  reinstall the mastermind plugin as relevo (README: Upgrading from relay)") // name-guard: legacy
	fmt.Println("  restart mastermind sessions")
}

// printSwapRecovery says what remains after the data moved but the client
// service did not switch. The data is not rolled back -- that would be a
// second migration -- so it prints the literal commands that finish the job.
func printSwapRecovery(err error, o migrate.UnitOptions, old migrate.OldUnit) {
	fmt.Printf("relevo migrate: the data is migrated, but the client service was not switched: %v\n", err)
	fmt.Println("finish it by hand, or fix the cause and run `relevo migrate` again:")

	wanted := old.WasActive || old.WasEnabled
	switch o.Units.Platform {
	case "linux":
		fmt.Println("  systemctl --user daemon-reload")
		if wanted {
			fmt.Printf("  systemctl --user enable --now %s\n", o.Units.New.Name)
		}
		fmt.Printf("  systemctl --user disable %s\n", o.Units.Old.Name)
		fmt.Printf("  rm -f %s\n", o.Units.Old.Path)
		fmt.Println("  systemctl --user daemon-reload")
	case "darwin":
		if wanted {
			fmt.Printf("  launchctl load -w %s\n", o.Units.New.Path)
		}
		fmt.Printf("  launchctl unload -w %s\n", o.Units.Old.Path)
		fmt.Printf("  rm -f %s\n", o.Units.Old.Path)
	default:
		fmt.Println("  no client unit is managed on this platform")
	}
}

// migratePathExists reports whether a path is there at all, without caring
// what it is.
func migratePathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
