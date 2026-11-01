package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/upgrade"
)

type updateFlagValues struct {
	check        *bool
	to           *string
	forceRelease *bool
	asJSON       *bool
}

func updateFlagSet(fs *flag.FlagSet) *updateFlagValues {
	v := &updateFlagValues{}
	v.check = fs.Bool("check", false, "print what update would do; change nothing")
	v.to = fs.String("to", "", "install this release tag instead of the latest; allows a downgrade")
	v.forceRelease = fs.Bool("release", false, "replace a local build with the release binary")
	v.asJSON = fs.Bool("json", false, "print the document the run produced")
	return v
}

// resolveUpdateDecision maps the flags to the update target: the running
// binary's identity, its kind, and what DecideUpdate says to do. The latest
// tag is fetched only when the decision can need it.
func resolveUpdateDecision(to string, forceRelease bool) (exe, running string, kind release.Kind, dec release.UpdateDecision, err error) {
	in := releaseInputs()
	kind = release.Detect(in)
	running = in.Version

	exe, err = upgrade.ResolveExe()
	if err != nil {
		return "", "", "", release.UpdateDecision{}, fail(codeInternal, "cannot resolve the running executable: %v", err)
	}

	// The fetch is skipped whenever the decision cannot need the latest tag:
	// an explicit --to names the target, a go install only prints, and a
	// local or unknown build without --release refuses before any target is
	// read.
	needLatest := to == "" && kind != release.KindGoInstall &&
		((kind != release.KindLocalBuild && kind != release.KindUnknown) || forceRelease)
	latest := ""
	if needLatest {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		latest, err = release.NewHTTPFetcher("", 0).Latest(ctx)
		cancel()
		if err != nil {
			return "", "", "", release.UpdateDecision{}, fail(codeRemoteUnreachable, "cannot learn the latest release: %v", err)
		}
	}

	dec = release.DecideUpdate(release.UpdateRequest{
		Kind:         kind,
		Running:      running,
		Latest:       latest,
		To:           to,
		ForceRelease: forceRelease,
	})
	return exe, running, kind, dec, nil
}

// installUpdateTarget downloads the target beside exe, runs the candidate's
// own checks, and swaps it over the running binary. A failure leaves exe
// unchanged.
func installUpdateTarget(running, exe string, dec release.UpdateDecision, w io.Writer) error {
	archive, _ := release.AssetURLs(dec.Target, runtime.GOOS, runtime.GOARCH)
	_, _ = fmt.Fprintf(w, "relevo %s -> %s: downloading %s\n", running, dec.Target, path.Base(archive))

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
			_ = os.Remove(tmp)
		}
	}()

	if err := preflightCandidate(tmp, dec.Target, exe); err != nil {
		return fail(codeInternal, "%v", err)
	}

	if err := os.Rename(tmp, exe); err != nil {
		return fail(codeInternal, "cannot replace %s: %v", exe, err)
	}
	swapped = true
	return nil
}

// cmdUpdate replaces a release binary with a checksum-verified release binary
// of the target tag. It never restarts anything: a running daemon follows a
// replaced binary on its own.
func cmdUpdate(args []string) error {
	return outcomeError(cmdUpdateRun(args))
}

func cmdUpdateRun(args []string) error {
	const usage = "usage: relevo update [--check] [--to vX.Y.Z] [--release]"

	fs := flag.NewFlagSet("relevo update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprintln(fs.Output(), usage)
	}
	v := updateFlagSet(fs)
	check, to, forceRelease, asJSON := v.check, v.to, v.forceRelease, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "update wants no arguments, got %v", fs.Args())
	}

	exe, running, kind, dec, err := resolveUpdateDecision(*to, *forceRelease)
	if err != nil {
		return err
	}

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

	// The progress line moves to stderr under --json, where stdout carries the
	// document alone.
	if err := installUpdateTarget(running, exe, dec, noticeWriter(*asJSON)); err != nil {
		return err
	}

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
// renamed over the running one; a failure leaves exe unchanged.
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
// output.
func runCandidate(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = nil
	return cmd.CombinedOutput()
}

// preflightReason is the first line of the child's output, or the error when
// the child printed nothing.
func preflightReason(out []byte, err error) string {
	if line := text.FirstLine(string(out)); line != "" {
		return line
	}
	return err.Error()
}
