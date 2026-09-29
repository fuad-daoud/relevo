package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

// updateFlagValues holds the pointers update parses into.
type updateFlagValues struct {
	check        *bool
	to           *string
	forceRelease *bool
	asJSON       *bool
}

// updateFlagSet defines those flags on fs and returns what they parse into.
func updateFlagSet(fs *flag.FlagSet) *updateFlagValues {
	v := &updateFlagValues{}
	v.check = fs.Bool("check", false, "print what update would do; change nothing")
	v.to = fs.String("to", "", "install this release tag instead of the latest; allows a downgrade")
	v.forceRelease = fs.Bool("release", false, "replace a local build with the release binary")
	v.asJSON = fs.Bool("json", false, "print the document the run produced")
	return v
}

// cmdUpdate is `relevo update` (#293): it replaces a release binary with a
// checksum-verified release binary of the target tag, or prints the command a
// `go install` needs, or refuses a local build. It never restarts anything:
// a running daemon follows a replaced binary on its own (#371).
func cmdUpdate(args []string) error {
	return outcomeError(cmdUpdateRun(args))
}

func cmdUpdateRun(args []string) error {
	const usage = "usage: relevo update [--check] [--to vX.Y.Z] [--release]"

	fs := flag.NewFlagSet("relevo update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usage)
	}
	v := updateFlagSet(fs)
	check, to, forceRelease, asJSON := v.check, v.to, v.forceRelease, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "update wants no arguments, got %v", fs.Args())
	}

	in := releaseInputs()
	kind := release.Detect(in)
	running := in.Version

	exe, err := upgrade.ResolveExe()
	if err != nil {
		return fail(codeInternal, "cannot resolve the running executable: %v", err)
	}

	// The fetch is skipped whenever the decision cannot need the latest tag:
	// an explicit --to names the target, a go install only prints, and a
	// local or unknown build without --release refuses before any target is
	// read.
	needLatest := *to == "" && kind != release.KindGoInstall &&
		!((kind == release.KindLocalBuild || kind == release.KindUnknown) && !*forceRelease)
	latest := ""
	if needLatest {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		latest, err = release.NewHTTPFetcher("", 0).Latest(ctx)
		cancel()
		if err != nil {
			return fail(codeRemoteUnreachable, "cannot learn the latest release: %v", err)
		}
	}

	dec := release.DecideUpdate(release.UpdateRequest{
		Kind:         kind,
		Running:      running,
		Latest:       latest,
		To:           *to,
		ForceRelease: *forceRelease,
	})

	if *check {
		doc := updateDocOf(running, exe, kind, dec, false)
		if *asJSON {
			return printDoc(doc)
		}
		renderUpdateCheck(doc)
		return nil
	}

	switch dec.Action {
	case release.UpdateInvalid:
		return fail(codeUsage, "%s", dec.Message)
	case release.UpdateRefuse:
		return fail(codeRefused, "%s", dec.Message)
	case release.UpdateCurrent:
		if *asJSON {
			return printDoc(updateDocOf(running, exe, kind, dec, false))
		}
		fmt.Println(dec.Message)
		return nil
	case release.UpdatePrintGoInstall:
		if *asJSON {
			return printDoc(updateDocOf(running, exe, kind, dec, false))
		}
		fmt.Println("relevo was installed with go install; run:")
		fmt.Printf("  %s\n", dec.Message)
		return nil
	}

	// UpdateReplace: download, verify, preflight, then swap in place. The
	// progress line keeps stdout in the default mode and moves to stderr under
	// --json, where stdout carries the document alone.
	w := noticeWriter(*asJSON)
	archive, _ := release.AssetURLs(dec.Target, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(w, "relevo %s -> %s: downloading %s\n", running, dec.Target, path.Base(archive))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tmp, err := (&release.Downloader{Base: release.DownloadBase}).
		FetchBinary(ctx, dec.Target, runtime.GOOS, runtime.GOARCH, filepath.Dir(exe))
	if err != nil {
		msg := fmt.Sprintf("update failed: %v", err)
		if errors.Is(err, os.ErrPermission) {
			msg += fmt.Sprintf(" (relevo update replaces the binary in place and needs write access to %s)", filepath.Dir(exe))
			return fail(codeInternal, "%s", msg)
		}
		return fail(codeRemoteUnreachable, "%s", msg)
	}
	// The temp file is removed on every path that does not swap it in.
	swapped := false
	defer func() {
		if !swapped {
			os.Remove(tmp)
		}
	}()

	if err := preflightCandidate(tmp, dec.Target, exe); err != nil {
		return fail(codeInternal, "%v", err)
	}

	if err := os.Rename(tmp, exe); err != nil {
		return fail(codeInternal, "cannot replace %s: %v", exe, err)
	}
	swapped = true

	if *asJSON {
		return printDoc(updateDocOf(running, exe, kind, dec, true))
	}
	fmt.Printf("relevo updated to %s (%s)\n", dec.Target, exe)
	if kind != release.KindRelease {
		fmt.Println("this install is now a release binary; relevo update keeps it current")
	}
	fmt.Println("A running daemon moves onto it by itself within a few seconds; rounds in flight keep running. relevo doctor shows what the daemon runs.")
	return nil
}

// preflightCandidate runs the downloaded binary's own checks before it is
// renamed over the running one: `version` must name the target tag and
// `daemon --preflight` must exit 0. A failure leaves exe unchanged.
func preflightCandidate(tmp, target, exe string) error {
	ctx, cancel := context.WithTimeout(context.Background(), upgrade.PreflightTimeout)
	defer cancel()

	verOut, verErr := runCandidate(ctx, tmp, "version")
	if verErr == nil && strings.TrimSpace(string(verOut)) != "relevo "+target {
		verErr = fmt.Errorf("version printed %q, want %q", strings.TrimSpace(string(verOut)), "relevo "+target)
	}
	if verErr != nil {
		return fmt.Errorf("the downloaded %s failed its preflight: %s; %s is unchanged", target, preflightReason(verOut, verErr), exe)
	}

	pfOut, pfErr := runCandidate(ctx, tmp, "daemon", "--preflight")
	if pfErr != nil {
		return fmt.Errorf("the downloaded %s failed its preflight: %s; %s is unchanged", target, preflightReason(pfOut, pfErr), exe)
	}
	return nil
}

// runCandidate starts the candidate binary with no stdin and captures its
// output. It runs only in the UpdateReplace path.
func runCandidate(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	return cmd.CombinedOutput()
}

// preflightReason is the first line of the child's output, or the error when
// the child printed nothing.
func preflightReason(out []byte, err error) string {
	if line := firstLine(string(out)); line != "" {
		return line
	}
	return err.Error()
}
