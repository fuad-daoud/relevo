package setup

// smoke.go owns the smoke round: a throwaway git repository, one real builder
// round bound to it, and proof from HELLO.md. A failure keeps everything in
// place for the transcript.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

// SmokeName is the binding and the repo name the smoke round uses.
const SmokeName = "setup-smoke"

// smokeBudget bounds the wait: ten minutes means a builder that is stuck, not slow.
const smokeBudget = 10 * time.Minute

// smokePlanText is the plan the smoke round sends; HELLO.md holding the line
// proves the builder ran and did the plan.
func smokePlanText() string {
	return "Create HELLO.md in this repository's root containing exactly one line:\n\n" +
		"relevo works\n\n" +
		"Commit it with git (message: relevo setup smoke) and finish with a report\n" +
		"that names the commit.\n"
}

// runSetupSmoke drives one round on a throwaway tree. Errors before the
// round was sent name the doctor, errors after it name the transcript, and
// everything stays on disk.
func runSetupSmoke(ctx context.Context, deps Deps, builderToken string) (SetupStepDoc, error) {
	started := time.Now()
	rt, err := deps.Refresh()
	if err != nil {
		return SetupStepDoc{}, &StepError{Message: err.Error(), Next: "relevo doctor"}
	}
	smokeDir, err := setupSmokePrepare(ctx, rt)
	if err != nil {
		return SetupStepDoc{}, smokeStepDoc(err)
	}
	round, b, err := setupSmokeRound(ctx, deps, rt, smokeDir, builderToken)
	if err != nil {
		return SetupStepDoc{}, smokeStepDoc(err)
	}
	return setupSmokeFinish(ctx, deps, rt, b, round, started, smokeDir)
}

// smokeStepDoc renders a smoke failure as its step row; a failure the caller
// already shaped passes through, keeping its own message and exit.
func smokeStepDoc(err error) error {
	var se *StepError
	if errors.As(err, &se) {
		return err
	}
	var ce *CodedStepError
	if errors.As(err, &ce) {
		return err
	}
	return &StepError{Message: text.FirstLine(err.Error()), Next: "relevo doctor"}
}

// classifyTransport shapes a smoke-transport failure through the Deps hook;
// a nil hook keeps the raw error.
func classifyTransport(deps Deps, err error) error {
	if deps.ClassifyTransport != nil {
		return deps.ClassifyTransport(err)
	}
	return err
}

// setupSmokePrepare frees a leftover smoke binding, then rebuilds the
// throwaway repository from scratch.
func setupSmokePrepare(ctx context.Context, rt relevo.Runtime) (string, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return "", err
	}
	smokeDir := filepath.Join(root, SmokeName)
	if _, err := rt.Store.Load(SmokeName); err == nil {
		if _, uerr := relevo.Unbind(ctx, rt, SmokeName, false); uerr != nil {
			return "", uerr
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if err := os.RemoveAll(smokeDir); err != nil {
		return "", err
	}
	if err := smokeRepo(filepath.Join(smokeDir, "repo")); err != nil {
		return "", err
	}
	return smokeDir, nil
}

// smokeRepo gives the round's tree one committed README; the commit's identity
// comes from flags so the machine's own name never leaks into it.
func smokeRepo(repo string) error {
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# relevo smoke\n"), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=relevo setup", "-c", "user.email=setup@relevo", "commit", "-q", "-m", "relevo setup smoke"},
	} {
		if err := smokeGit(repo, args...); err != nil {
			return err
		}
	}
	return nil
}

func smokeGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), text.FirstLine(string(out)))
	}
	return nil
}

// setupSmokeRound binds the throwaway tree, hands it the plan, and returns
// the round it opened.
func setupSmokeRound(ctx context.Context, deps Deps, rt relevo.Runtime, smokeDir, builderToken string) (int, store.Binding, error) {
	repo := filepath.Join(smokeDir, "repo")
	b, err := setupSmokeBind(ctx, deps, rt, repo, builderToken)
	if err != nil {
		return 0, store.Binding{}, err
	}
	plan := filepath.Join(smokeDir, "plan.md")
	if err := os.WriteFile(plan, []byte(smokePlanText()), 0o644); err != nil {
		return 0, store.Binding{}, err
	}
	res, err := relevo.Send(ctx, rt, SmokeName, plan, relevo.SendOptions{})
	if err != nil {
		return 0, store.Binding{}, classifyTransport(deps, err)
	}
	return res.Round, b, nil
}

// setupSmokeBind is one bind without a feature flag; when no MasterMind
// resolves, setup registers its own named setup first.
func setupSmokeBind(ctx context.Context, deps Deps, rt relevo.Runtime, repo, builderToken string) (store.Binding, error) {
	opts := relevo.BindOptions{
		Name:      SmokeName,
		Candidate: builderToken,
		CWD:       repo,
		NoFeature: true,
	}
	b, _, err := relevo.BindResolved(ctx, rt, opts)
	if errors.Is(err, relevo.ErrNoMasterMindSession) {
		rec, rerr := setupRegisterMasterMind(deps, rt)
		if rerr != nil {
			return store.Binding{}, classifyTransport(deps, rerr)
		}
		opts.MasterMindID = rec.ID
		b, _, err = relevo.BindResolved(ctx, rt, opts)
	}
	if err != nil {
		return store.Binding{}, classifyTransport(deps, err)
	}
	return b, nil
}

// setupRegisterMasterMind is the MasterMind setup registers as itself when no
// record resolves: the wizard runs from a shell, so it names an explicit
// kind and session.
func setupRegisterMasterMind(deps Deps, rt relevo.Runtime) (mastermind.Record, error) {
	reg, err := deps.MastermindRegistry(rt)
	if err != nil {
		return mastermind.Record{}, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return mastermind.Record{}, err
	}
	rec, _, err := mastermind.Init(reg, mastermind.InitInput{Kind: "setup", SessionID: "setup", Name: "setup", CWD: cwd, Now: rt.Now()})
	return rec, err
}

// setupSmokeFinish waits with the feed running and proves the plan's
// artifact. A pass cleans the whole throwaway state up; a failure keeps it
// for the transcript command the row names.
func setupSmokeFinish(ctx context.Context, deps Deps, rt relevo.Runtime, b store.Binding, round int, started time.Time, smokeDir string) (SetupStepDoc, error) {
	stopFeed := startSmokeFeed(rt, SmokeName, smokeDir)
	name, res, err := relevo.Wait(ctx, rt, relevo.WaitOptions{
		Names:    []string{SmokeName},
		Round:    round,
		Timeout:  smokeBudget,
		Interval: time.Second,
	})
	stopFeed()
	next := fmt.Sprintf("relevo show %s --round %d --transcript", SmokeName, round)
	if err != nil {
		return SetupStepDoc{}, &StepError{Message: fmt.Sprintf("%v", err), Next: next}
	}
	if res.Code != 0 {
		if lerr := SmokeLimitFailure(ctx, rt, smokeDir); lerr != nil {
			return SetupStepDoc{}, lerr
		}
		return SetupStepDoc{}, &StepError{Message: text.FirstLine(res.Line), Next: next}
	}
	if herr := smokeHelloOkay(b.CWD); herr != nil {
		return SetupStepDoc{}, &StepError{Message: fmt.Sprintf("%s", herr), Next: next}
	}
	detail := smokePassDetail(rt, b, time.Since(started)) + smokeProof(b.CWD)

	if _, uerr := relevo.Unbind(ctx, rt, name, false); uerr != nil {
		return SetupStepDoc{}, &StepError{Message: fmt.Sprintf("unbind the smoke binding: %v", uerr), Next: "relevo doctor"}
	}
	if rerr := os.RemoveAll(smokeDir); rerr != nil {
		return SetupStepDoc{}, &StepError{Message: fmt.Sprintf("remove the smoke repository: %v", rerr), Next: "relevo doctor"}
	}
	return SetupStepDoc{Step: "smoke", OK: true, Detail: detail}, nil
}

// SmokeLimitFailure is the failure a round gets when its last builder hit a
// usage limit with nobody left to take over; the row says when it resets and
// how to add a fallback. nil when the round failed some other way.
func SmokeLimitFailure(ctx context.Context, rt relevo.Runtime, smokeDir string) error {
	b, err := rt.Store.Load(SmokeName)
	if err != nil || b.BuilderCandidate == "" {
		return nil
	}
	for _, g := range availability.LedgerGates(relevo.AvailabilityDeps(rt), []string{b.BuilderCandidate}) {
		if g.Token != b.BuilderCandidate || g.Kind != availability.RateLimited {
			continue
		}
		until := ""
		if !g.Until.IsZero() {
			until = " (resets " + availability.GateTimeText(g.Until) + ")"
		}
		_, _ = relevo.Unbind(ctx, rt, SmokeName, false)
		_ = os.RemoveAll(smokeDir)
		return &LimitError{
			Message: fmt.Sprintf("%s hit its usage limit%s and no other builder could take over; run relevo setup again and pick a second builder as a fallback, or wait for the reset",
				rt.Candidates.NameOf(b.BuilderCandidate), until),
			Next: "relevo setup",
		}
	}
	return nil
}

func smokeHelloOkay(repo string) error {
	body, err := os.ReadFile(filepath.Join(repo, "HELLO.md"))
	if err != nil {
		return fmt.Errorf("the round closed, but HELLO.md is missing: %v", err)
	}
	if !strings.Contains(string(body), "relevo works") {
		return errors.New("the round closed, but HELLO.md does not hold the line")
	}
	return nil
}

// smokeProof is what the builder left, for the row to show before the
// throwaway repository goes.
func smokeProof(repo string) string {
	proof := ""
	if body, err := os.ReadFile(filepath.Join(repo, "HELLO.md")); err == nil {
		proof += "\n  HELLO.md: " + text.FirstLine(strings.TrimSpace(string(body)))
	}
	cmd := exec.Command("git", "log", "-2", "--format=%h %s")
	cmd.Dir = repo
	out, err := cmd.Output()
	if lines := strings.Split(strings.TrimSpace(string(out)), "\n"); err == nil && len(lines) == 2 {
		proof += "\n  commit " + lines[0]
	}
	return proof
}

// smokePassDetail names the builder that closed the round, which differs from
// the bound one when a usage limit switched it mid-round.
func smokePassDetail(rt relevo.Runtime, bound store.Binding, took time.Duration) string {
	first := rt.Candidates.NameOf(bound.BuilderCandidate)
	last := first
	if now, err := rt.Store.Load(bound.Name); err == nil {
		last = rt.Candidates.NameOf(now.BuilderCandidate)
	}
	took = took.Truncate(time.Second)
	if last == first {
		return fmt.Sprintf("builder %s: closed in %s", last, took)
	}
	return fmt.Sprintf("builder %s (switched from %s): closed in %s", last, first, took)
}
