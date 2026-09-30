package consult

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// VerifyRole is the consult Role a verify round's reviewer is recorded under.
// It is deliberately not "reviewer": the harness role that runs the consult is
// `reviewer`, while this name marks the record as the round-close check so
// finishConsult can pick the verdict out of its findings. A `relevo ask --actor
// reviewer` consult is a different thing.
const VerifyRole = "verify"

// Verdict values parseVerdict returns and store.Verdict.Verdict carries. The
// third is not a judgement: the findings carried no readable block, and the
// mastermind reads the prose.
const (
	verdictAccepted     = "accepted"
	verdictRejected     = "rejected"
	verdictUnstructured = "unstructured"
)

// verifyPrompt is the question relevo composes for a verify consult. It names
// every artefact the reviewer needs -- the plan, the builder's report, the
// round's diff and the gate log -- and asks for the verdict block it parses.
const verifyPrompt = `Verify round %d of binding %q independently. You are in a throwaway worktree at the builder's HEAD; you may run tests and read anything; do not edit files.

Plan:   %s      Report: %s
Diff:   %s      Gate:   %s

The Diff line is a git command: run it in this worktree to see the round's change, including edits the builder did not commit.

Acceptance: does the tree do what the plan asked, with evidence you checked yourself?
Code: is the change correct, safe, and maintainable? Do not reject for style.
Answer as your final message: your findings in markdown, ending with exactly this block:

` + "```relevo" + `
verdict: accepted | rejected
reasons: ["..."]
` + "```"

// VerifyDiffCommand renders the git diff command handed to a verify reviewer.
// Both tree ids come from the round's snapshots; the objects live in the
// repository's shared object store, so the command runs in the verify worktree
// and shows uncommitted edits the builder left.
func VerifyDiffCommand(baselineTree, closedTree string) string {
	if baselineTree == "" || closedTree == "" {
		return "none"
	}
	return "git diff " + baselineTree + " " + closedTree
}

// verifyQuestion renders the reviewer's question. gateLog "" reads "none": a
// round with no gate has no log to name, and an empty field would read as a
// path the reviewer should have been given.
func verifyQuestion(name string, round int, planPath, reportPath, diff, gateLog string) string {
	if gateLog == "" {
		gateLog = "none"
	}
	return fmt.Sprintf(verifyPrompt, round, name, planPath, reportPath, diff, gateLog)
}

// parseVerdict decodes the verdict and reasons from a verify consult's
// findings: the LAST relevo block, whose `verdict: accepted|rejected` and
// `reasons:` the reviewer was asked for. Anything else -- no block, an
// unreadable one, a verdict that is neither word -- is "unstructured", with the
// reasons parsed from a block that had any.
//
// Pure; no I/O; every failure returns the unstructured verdict rather than an
// error, so a consult that answered in prose is delivered as prose.
func parseVerdict(findings []byte) (verdict string, reasons []string) {
	if len(findings) == 0 {
		return verdictUnstructured, nil
	}

	lines := reporttail.SplitFenceLines(findings)
	openIdx, closeIdx, _ := reporttail.FindRelevoBlock(lines)
	if openIdx == -1 || closeIdx == -1 {
		return verdictUnstructured, nil
	}

	var (
		verdictRaw string
		openList   bool
	)
	for i := openIdx + 1; i < closeIdx; i++ {
		line := strings.TrimSpace(reporttail.StripComment(lines[i]))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "-") && (len(line) == 1 || line[1] == ' ' || line[1] == '\t') {
			if openList {
				if item := strings.TrimSpace(reporttail.UnquoteScalar(strings.TrimSpace(line[1:]))); item != "" {
					reasons = append(reasons, item)
				}
			}
			continue
		}
		colon := strings.Index(line, ":")
		if colon == -1 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		openList = false
		switch key {
		case "verdict":
			verdictRaw = val
		case "reasons":
			if val == "" {
				// A YAML-ish list: the "- ..." lines that follow.
				openList = true
			} else {
				reasons = reporttail.ParseListValue(val)
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(reporttail.UnquoteScalar(verdictRaw))) {
	case verdictAccepted:
		return verdictAccepted, reasons
	case verdictRejected:
		return verdictRejected, reasons
	default:
		return verdictUnstructured, reasons
	}
}

// removeVerifyWorktree takes the throwaway tree away. Failure is a warning,
// never fatal: the round has already closed, and a leftover tree under
// .worktrees/.verify/ is the human's to `git worktree remove`.
func removeVerifyWorktree(ctx context.Context, d Deps, b store.Binding, round int) {
	if d.Git == nil {
		return
	}
	if err := d.Git.RemoveWorktree(ctx, b.CWD, d.Store.VerifyWorktreePath(b.Name, round), true); err != nil {
		slog.Warn("verify worktree not removed", "binding", b.Name, "round", round, "err", err)
		return
	}
	d.Store.PruneWorktreeDirs()
}

// verifyStart carries the state every step of StartVerify shares, so its
// helpers can skip or fail without a closure chain.
type verifyStart struct {
	ctx   context.Context
	d     Deps
	tx    *store.Tx
	b     store.Binding
	round int
}

// skip records why the reviewer did not run and keeps the round closed.
func (v verifyStart) skip(reason string) (store.Binding, error) {
	entry := store.LogEntry{
		TS: v.d.Now().UTC(), Round: v.round,
		Direction: store.DirToConsult, Kind: store.KindAsk,
		Note: "verify skipped: " + reason,
	}
	if err := v.tx.AppendLog(v.b.Name, entry); err != nil {
		return v.b, err
	}
	return v.b, nil
}

// fail is skip plus the worktree cleanup every failure after creation owes.
func (v verifyStart) fail(reason string) (store.Binding, error) {
	removeVerifyWorktree(v.ctx, v.d, v.b, v.round)
	return v.skip(reason)
}

// StartVerify starts this round's read-only reviewer at round close.
//
// Preconditions:  the round just closed (b.Round == round+1); the caller holds
// the state lock and passes its tx; gateLog is the closed round's gate log
// path, "" when no gate ran.
//
// Postconditions: on success one headless consult with Role VerifyRole is
// reserved and running in a detached worktree at the builder's HEAD, its
// question recorded at the binding's ask path, and a to_consult/ask entry in
// the log. On every failure -- no git client, no reviewer candidate, a worktree
// that could not be created, no runner, a launch that failed -- the worktree is
// removed when it exists, one to_consult/ask note "verify skipped: <why>" is
// appended, and (b, nil) is returned: a reviewer relevo could not start is not
// a round failure. Only a store error can fail the call.
func StartVerify(ctx context.Context, d Deps, tx *store.Tx, b store.Binding, round int, diff, gateLog string) (store.Binding, error) {
	v := verifyStart{ctx: ctx, d: d, tx: tx, b: b, round: round}

	if d.Git == nil {
		return v.skip("no git client")
	}

	// The tree as the builder left it. CWD is the builder's worktree for an add
	// binding, and the tree the round's diff was taken from either way.
	head, err := d.Git.HeadCommit(ctx, b.CWD)
	if err != nil {
		return v.skip("head: " + err.Error())
	}

	wt := d.Store.VerifyWorktreePath(b.Name, round)
	if err := d.Git.AddDetachedWorktree(ctx, b.CWD, wt, head); err != nil {
		return v.skip("worktree: " + err.Error())
	}

	return v.launch(wt, diff, gateLog)
}

// launch resolves the reviewer, stages the question and reserves the record.
// Every failure past the worktree's creation goes through fail.
func (v verifyStart) launch(wt, diff, gateLog string) (store.Binding, error) {
	c, role, tier, err := v.d.ResolveReviewer()
	if err != nil {
		return v.fail(err.Error())
	}
	if v.d.Runner == nil {
		return v.fail("no runner")
	}
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return v.fail(fmt.Sprintf("unknown harness kind %q", c.Harness))
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return v.fail(err.Error())
	}

	id := newID(v.d)
	askPath := v.d.Store.AskPath(v.b.Name, v.round, id)
	question := verifyQuestion(v.b.Name, v.round,
		v.d.Store.PromptPath(v.b.Name, v.round), v.d.Store.ReportPath(v.b.Name, v.round), diff, gateLog)
	render := func(ref string) string { return fmt.Sprintf(headlessPrompt, ref) }
	prompt, inline := inlinePrompt(render, []byte(question))
	if !inline {
		// The prompt is one argv element and relevo cannot hand an oversized
		// question to the reviewer: skip the verify rather than spawn a
		// reviewer it cannot give the question to.
		return v.fail("question over the inline prompt limit")
	}
	if err := stageVerifyQuestion(v.tx, v.b.Name, v.round, askPath, question); err != nil {
		return v.fail(err.Error())
	}

	consult := store.Consult{
		ID:           id,
		Role:         VerifyRole,
		Round:        v.round,
		AskPath:      askPath,
		FindingsPath: v.d.Store.FindingsPath(v.b.Name, v.round, id),
		Endpoint:     store.Endpoint{AgentName: v.b.Name + "-" + VerifyRole + "-" + id, Kind: l.Kind},
		State:        store.ConsultSpawning,
		SpawnedAt:    v.d.Now().UTC(),
	}

	// Reserve: the spawning record exists before the process does, so a crash
	// between the two leaves something `relevo reap` can see.
	b := v.b
	b.Consults = append(b.Consults, consult)
	if err := v.tx.Save(b); err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		return v.fail("stage consult: " + brief(err))
	}

	return v.start(b, consult, askPath, prompt, wt, c, role, tier)
}

// start launches the reserved reviewer and records it running, rolling the
// reservation back before every reported failure.
func (v verifyStart) start(b store.Binding, consult store.Consult, askPath, prompt, wt string, c candidate.Candidate, role harness.RoleSpec, tier harness.Tier) (store.Binding, error) {
	streamPath := v.d.Store.ConsultStreamPath(v.b.Name, v.round, consult.ID)
	argv, err := spawn.HeadlessLaunch(c, role, tier, Timeout, prompt, wt, v.d.Store.Dir(v.b.Name))
	if err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		if saveErr := v.tx.Save(b); saveErr != nil {
			return b, saveErr
		}
		return v.fail("spawn failed: " + brief(err))
	}

	// The consult's Dir is the throwaway worktree, not b.CWD: the reviewer
	// reads the builder's tree without writing in it.
	handle, err := v.d.Runner.Start(v.ctx, spawn.ProcSpec{
		Dir:        wt,
		Argv:       argv,
		LogPath:    streamPath,
		StreamPath: streamPath,
		Env:        []string{mastermind.RunnerEnvEntry(v.b.Name)},
		Scope:      v.d.Scope("verify", v.b.Owner, v.b.Name, v.round, consult.ID),
	})
	if err != nil {
		b.Consults = b.Consults[:len(b.Consults)-1]
		if saveErr := v.tx.Save(b); saveErr != nil {
			return b, saveErr
		}
		return v.fail("spawn failed: " + brief(err))
	}
	// The daemon has now seen the reviewer alive: a later tick never judges it
	// lost to its own restart.
	v.d.Seen(handle.PID, handle.StartedAt.Unix())

	consult.Endpoint = store.Endpoint{
		AgentName: consult.Endpoint.AgentName,
		Kind:      consult.Endpoint.Kind,
		Mode:      store.ModeHeadless,
		PID:       handle.PID,
		StartedAt: handle.StartedAt.Unix(),
		LogPath:   streamPath,
	}
	consult.State = store.ConsultRunning

	// Record: the consult is running (Spawn's phase 3), and the ask entry logs
	// it as `verify <id>`.
	b.Consults[len(b.Consults)-1] = consult
	if err := v.tx.AppendLog(v.b.Name, store.LogEntry{
		TS: v.d.Now().UTC(), Round: v.round,
		Direction: store.DirToConsult, Kind: store.KindAsk,
		Path:      askPath,
		Note:      VerifyRole + " " + consult.ID,
		Confirmed: true,
	}); err != nil {
		return b, err
	}
	if err := v.tx.Save(b); err != nil {
		return b, err
	}

	return b, nil
}

// stageVerifyQuestion records the reviewer's question as a round_file row, the
// same way Spawn records an ask: a verify question is row-only, so the ask name
// is reserved and relevo never writes it to disk, whatever its size.
func stageVerifyQuestion(tx *store.Tx, name string, round int, askPath, question string) error {
	if err := tx.PutRoundFile(name, round, askPath, []byte(question)); err != nil {
		return fmt.Errorf("record question at %s: %s", askPath, err.Error())
	}
	return nil
}
