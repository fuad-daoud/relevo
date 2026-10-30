package main

// cmd/relevo/setup_smoke.go owns the smoke round: a throwaway git repository
// under the state root, one real builder round bound to it with the current
// MasterMind, and proof from HELLO.md before the pass cleans up. A failure
// keeps everything in place for the transcript.

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

// smokeName is the binding and the repo name the smoke round uses: one
// run's state, reusable by name for the next run.
const smokeName = "setup-smoke"

// smokeBudget bounds the wait: a healthy machine closes the round in about a
// minute, so ten of them means a builder that is stuck, not slow.
const smokeBudget = 10 * time.Minute

// smokePlanText is the plan the smoke round sends. Its artifact means
// "the builder ran and closed" and "the builder did the plan" are one check:
// HELLO.md holding the line.
func smokePlanText() string {
	return "Create HELLO.md in this repository's root containing exactly one line:\n\n" +
		"relevo works\n\n" +
		"Commit it with git (message: relevo setup smoke) and finish with a report\n" +
		"that names the commit.\n"
}

// runSetupSmoke drives one round on a throwaway tree. Errors before the
// round was sent name the doctor; errors after it was sent name the
// transcript, and everything stays on disk.
func runSetupSmoke(ctx context.Context, refresh func() (relevo.Runtime, error), builderToken string) (SetupStepDoc, error) {
	started := time.Now()
	rt, err := refresh()
	if err != nil {
		return SetupStepDoc{}, failNext(codeInternal, "relevo doctor", "%v", err)
	}
	smokeDir, err := setupSmokePrepare(ctx, rt)
	if err != nil {
		return SetupStepDoc{}, smokeStepDocOf(err)
	}
	round, b, err := setupSmokeRound(ctx, rt, smokeDir, builderToken)
	if err != nil {
		return SetupStepDoc{}, smokeStepDocOf(err)
	}
	return setupSmokeFinish(ctx, rt, b, round, started, smokeDir)
}

// smokeStepDocOf renders a smoke failure that happened before or during the
// round: a roundless tree is what `relevo doctor` inspects, so that is the
// next the row names. The failure's own code survives when it was coded.
func smokeStepDocOf(err error) error {
	var ce *cliError
	if errors.As(err, &ce) {
		return failNext(ce.code, "relevo doctor", "%s", ce.message)
	}
	return failNext(codeInternal, "relevo doctor", "%s", text.FirstLine(err.Error()))
}

// setupSmokePrepare frees a leftover smoke binding, then rebuilds the
// throwaway repository with one committed README from scratch.
func setupSmokePrepare(ctx context.Context, rt relevo.Runtime) (string, error) {
	root, err := tmpdir.DefaultRoot()
	if err != nil {
		return "", err
	}
	smokeDir := filepath.Join(root, smokeName)
	if _, err := rt.Store.Load(smokeName); err == nil {
		if _, uerr := relevo.Unbind(ctx, rt, smokeName, false); uerr != nil {
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

// smokeRepo gives the round's tree the one committed README a fresh clone
// would have; the commit's identity comes from flags because the repo is
// throwaway and the machine's own name must not leak into it.
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

// setupSmokeRound binds the throwaway tree to the first builder candidate,
// hands it the plan, and returns the round it opened.
func setupSmokeRound(ctx context.Context, rt relevo.Runtime, smokeDir, builderToken string) (int, store.Binding, error) {
	repo := filepath.Join(smokeDir, "repo")
	b, err := setupSmokeBind(ctx, rt, repo, builderToken)
	if err != nil {
		return 0, store.Binding{}, err
	}
	plan := filepath.Join(smokeDir, "plan.md")
	if err := os.WriteFile(plan, []byte(smokePlanText()), 0o644); err != nil {
		return 0, store.Binding{}, err
	}
	res, err := relevo.Send(ctx, rt, smokeName, plan, relevo.SendOptions{})
	if err != nil {
		return 0, store.Binding{}, writeError(err)
	}
	return res.Round, b, nil
}

// setupSmokeBind is one bind without a feature flag; when no MasterMind
// resolves for this process, setup registers its own named setup first.
func setupSmokeBind(ctx context.Context, rt relevo.Runtime, repo, builderToken string) (store.Binding, error) {
	opts := relevo.BindOptions{
		Name:      smokeName,
		Candidate: builderToken,
		CWD:       repo,
		NoFeature: true,
	}
	b, _, err := relevo.BindResolved(ctx, rt, opts)
	if errors.Is(err, relevo.ErrNoMasterMindSession) {
		rec, rerr := setupRegisterMasterMind(rt)
		if rerr != nil {
			return store.Binding{}, writeError(rerr)
		}
		opts.MasterMindID = rec.ID
		b, _, err = relevo.BindResolved(ctx, rt, opts)
	}
	if err != nil {
		return store.Binding{}, writeError(err)
	}
	return b, nil
}

// setupRegisterMasterMind is the MasterMind setup registers as itself when
// no record resolves: the wizard runs from a shell, not from a session to
// detect, so it names an explicit kind and session the way `mastermind init
// --kind --session` does.
func setupRegisterMasterMind(rt relevo.Runtime) (mastermind.Record, error) {
	reg, err := mastermindRegistry(rt)
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
// artifact: HELLO.md holding the line. A pass shows the file and the commit
// the builder made, then cleans the whole throwaway state up; a failure
// keeps it for the transcript command the row names.
func setupSmokeFinish(ctx context.Context, rt relevo.Runtime, b store.Binding, round int, started time.Time, smokeDir string) (SetupStepDoc, error) {
	stopFeed := startSmokeFeed(rt, smokeName, smokeDir)
	name, res, err := relevo.Wait(ctx, rt, relevo.WaitOptions{
		Names:    []string{smokeName},
		Round:    round,
		Timeout:  smokeBudget,
		Interval: time.Second,
	})
	stopFeed()
	next := fmt.Sprintf("relevo show %s --round %d --transcript", smokeName, round)
	if err != nil {
		return SetupStepDoc{}, failNext(codeInternal, next, "%v", err)
	}
	if res.Code != 0 {
		if lerr := smokeLimitFailure(ctx, rt, smokeDir); lerr != nil {
			return SetupStepDoc{}, lerr
		}
		return SetupStepDoc{}, failNext(codeInternal, next, "%s", text.FirstLine(res.Line))
	}
	if herr := smokeHelloOkay(b.CWD); herr != nil {
		return SetupStepDoc{}, failNext(codeInternal, next, "%s", herr)
	}
	detail := smokePassDetail(rt, b, time.Since(started)) + smokeProof(b.CWD)

	if _, uerr := relevo.Unbind(ctx, rt, name, false); uerr != nil {
		return SetupStepDoc{}, failNext(codeInternal, "relevo doctor", "unbind the smoke binding: %v", uerr)
	}
	if rerr := os.RemoveAll(smokeDir); rerr != nil {
		return SetupStepDoc{}, failNext(codeInternal, "relevo doctor", "remove the smoke repository: %v", rerr)
	}
	return SetupStepDoc{Step: "smoke", OK: true, Detail: detail}, nil
}

// smokeLimitFailure is the failure a round gets when its last builder hit a
// usage limit with nobody left to take over: the account's problem, not
// relevo's, so the row says when it resets and how to add a fallback. The
// throwaway state goes, because its transcript only repeats the limit.
// nil when the round failed some other way.
func smokeLimitFailure(ctx context.Context, rt relevo.Runtime, smokeDir string) error {
	b, err := rt.Store.Load(smokeName)
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
		_, _ = relevo.Unbind(ctx, rt, smokeName, false)
		_ = os.RemoveAll(smokeDir)
		return failNext(codeGateActive, "relevo setup",
			"%s hit its usage limit%s and no other builder could take over; run relevo setup again and pick a second builder as a fallback, or wait for the reset",
			rt.Candidates.NameOf(b.BuilderCandidate), until)
	}
	return nil
}

// smokeHelloOkay proves the artifact: HELLO.md holding the line is what the
// round's whole point buys.
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
// throwaway repository goes: the file it wrote and the commit it made on top
// of the repository's first one.
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

// smokePassDetail names the builder that closed the round, which differs
// from the bound one when a usage limit switched it mid-round; saying so
// shows the newcomer that the fallback order works.
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
