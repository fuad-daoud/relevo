package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// Actions is the cockpit's write seam (§1, §4.2): one method per key that
// changes something, each one a thin call into the same internal/relevo
// function the CLI verb calls. A nil Actions (serve ui) hides every action
// key and makes one do nothing.
type Actions interface {
	Stop(ctx context.Context, key string) Result
	Done(ctx context.Context, key string) Result
	Unbind(ctx context.Context, key string) Result // always archives
	Gate(ctx context.Context, subject string, forDur time.Duration, reason string) Result
	Ungate(ctx context.Context, subject string) Result
	Shell(key string) (*exec.Cmd, error) // a shell in the binding's tree

	// Round 2: the acts that need a file (send, retry), a new binding
	// (bind), or the human mastermind's pending report (pull, via the fleet
	// row's report-ready state, §4.5).
	Send(ctx context.Context, key, planFile string) Result
	Bind(ctx context.Context, in BindInput) Result
	Retry(ctx context.Context, key, candidate string) Result
	Pull(ctx context.Context, key string) (text string, ok bool, err error)
	Candidates(role string) []string // names, in the role's order

	// The config views (round 2): the stored config, one validated edit
	// applied and reloaded, and one candidate probed.
	ConfigDoc() (relevo.ConfigDoc, error)                        // the stored config, freshly read
	ApplyConfig(ctx context.Context, e relevo.ConfigEdit) Result // write one edit, then reload this adapter's runtime
	// The audit view (round 6): every revision, one revision's changes in
	// human words, the same for a roll back's preview, and the roll back
	// itself.
	ConfigLog() ([]db.RevisionRow, error)                 // every revision, newest first, no snapshots
	ConfigChanges(rev int64) ([]relevo.ChangeLine, error) // one revision's changes, in human words
	RollbackPreview(rev int64) ([]relevo.ChangeLine, error)
	Rollback(ctx context.Context, rev int64) Result
	Probe(ctx context.Context, name string) Result // probe one candidate (spawns its harness)

	// The agents view (round 5): one agent's definition files across the
	// harnesses that carry it, one file reset to the copy relevo ships,
	// and the user's editor for one file.
	AgentFiles(agent string) ([]harness.AgentFile, error)          // the dry-run install state of one agent
	ResetAgentFile(ctx context.Context, kind, agent string) Result // overwrite one definition file
	AgentEditor(path string) (*exec.Cmd, error)                    // the user's editor on one file

	// The round view's artifacts tab (round 5b): the pager, browser or
	// editor for one artifact. Like Shell and AgentEditor it returns the
	// command without running it, so the cockpit can run the pager and the
	// editor under tea.ExecProcess and start the browser detached.
	OpenArtifact(path, kind string) (*exec.Cmd, error)
}

// BindInput is one b key's answers (§3): the new binding's name, the
// candidate it should run ("" means resolve the policy pick), and its feature
// label.
type BindInput struct{ Name, Candidate, Feature string }

// Result is what one action did. Text is what the CLI would print on success
// (StopText, DoneText, ...), multi-line allowed; Err is the failure the CLI
// would have returned; Refresh asks the shell to refetch status now.
type Result struct {
	Text    string
	Err     error
	Refresh bool
}

// mastermindActions is the real Actions: a thin adapter over internal/relevo.
// repo is os.Getwd() at start, or "" when not inside a git repo (round 2's
// bind refuses then); you is the human mastermind's id, from ensureYou.
type mastermindActions struct {
	live  *liveRuntime
	repo  string
	you   string
	probe availability.LineExec
}

// runtime is the shared holder's current snapshot, so a write and a render
// never disagree about the configured candidates, actors or policy.
func (a *mastermindActions) runtime() relevo.Runtime {
	return a.live.Get()
}

// resolve maps a row key to the runtime that owns it and the bare binding
// name inside that runtime's store. `relevo ui` is welded to one mastermind
// runtime, so every key resolves to it under its own name (§4.2).
func (a *mastermindActions) resolve(key string) (relevo.Runtime, string, bool) {
	return a.runtime(), key, true
}

// Stop ends the binding's open round (§4.2). Nothing to stop is an answer,
// not a failure: the text says so and Err stays nil, exactly as
// `relevo stop` prints it (cmd/relevo/main.go cmdStop).
func (a *mastermindActions) Stop(ctx context.Context, key string) Result {
	rt, name, ok := a.resolve(key)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}
	res, err := relevo.Stop(ctx, rt, name, relevo.StopOptions{})
	if errors.Is(err, relevo.ErrNothingToStop) {
		return Result{Text: fmt.Sprintf("nothing to stop: %s has no open round", name), Refresh: true}
	}
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	return Result{Text: relevo.StopText(name, res), Refresh: true}
}

// Done marks the binding done. On ErrStopFailed the CLI prints the text and
// still returns the error, so both come back (§4.2).
func (a *mastermindActions) Done(ctx context.Context, key string) Result {
	rt, name, ok := a.resolve(key)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}
	res, err := relevo.Done(ctx, rt, name)
	if err != nil && !errors.Is(err, relevo.ErrStopFailed) {
		return Result{Err: err, Refresh: true}
	}
	out := Result{Text: relevo.DoneText(name, res), Refresh: true}
	if err != nil {
		out.Err = err
	}
	return out
}

// Unbind archives the binding: the cockpit's unbind always passes archive
// true, so the round log survives (§4.2).
func (a *mastermindActions) Unbind(ctx context.Context, key string) Result {
	rt, name, ok := a.resolve(key)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}
	res, err := relevo.Unbind(ctx, rt, name, true)
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	return Result{Text: relevo.UnbindText(name, res), Refresh: true}
}

// Gate records a rate limit on subject's provider and forwards it, the same
// text `relevo gate <token>` prints (cmd/relevo/main.go gateUnavailable).
// subject is a candidate name or canonical token; forDur 0 means "until
// cleared" (§4.2, §4.3).
func (a *mastermindActions) Gate(ctx context.Context, subject string, forDur time.Duration, reason string) Result {
	rt, _, ok := a.resolve(subject)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}

	var until time.Time
	if forDur > 0 {
		until = rt.Now().Add(forDur)
	}

	provider, err := availability.Unavailable(relevo.AvailabilityDeps(rt), subject, until, reason)
	if err != nil {
		return Result{Err: err, Refresh: true}
	}

	count := 0
	if rt.Candidates != nil {
		for _, ref := range rt.Candidates.Refs() {
			parsed, perr := candidate.ParseRef(ref)
			if perr == nil && parsed.Provider == provider {
				count++
			}
		}
	}

	lines := []string{fmt.Sprintf("gated %s (%d candidates) %s", provider, count, availability.GateUntilText(until))}
	if bs, lerr := rt.Store.List(); lerr == nil {
		if names := availability.BindingsOnProvider(bs, provider); len(names) > 0 {
			lines = append(lines, "the daemon will switch: "+strings.Join(names, ", "))
		}
	}
	lines = append(lines, relevo.ForwardUnavailable(ctx, rt, subject, reason)...)

	return Result{Text: strings.Join(lines, "\n"), Refresh: true}
}

// Ungate clears every rate-limit gate on subject's provider and forwards the
// clear, the same text `relevo gate --clear` prints (cmd/relevo/main.go
// gateClear). An ungate is never destructive, so it has no confirm (§4.3).
func (a *mastermindActions) Ungate(ctx context.Context, subject string) Result {
	rt, _, ok := a.resolve(subject)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}

	provider, removed, err := availability.Available(relevo.AvailabilityDeps(rt), subject, availability.ClearedByMasterMind)
	if err != nil {
		return Result{Err: err, Refresh: true}
	}

	var lines []string
	if removed == 0 {
		lines = append(lines, fmt.Sprintf("nothing was gating %s", provider))
	} else {
		lines = append(lines, fmt.Sprintf("cleared %s (%d entries)", provider, removed))
	}
	lines = append(lines, relevo.ForwardAvailable(ctx, rt, subject)...)

	return Result{Text: strings.Join(lines, "\n"), Refresh: true}
}

// Shell is a shell in the binding's own tree: its worktree when relevo made
// one, else its recorded CWD. A remote binding has no local tree (§4.2).
func (a *mastermindActions) Shell(key string) (*exec.Cmd, error) {
	rt, name, ok := a.resolve(key)
	if !ok {
		return nil, errors.New("unknown binding")
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return nil, err
	}
	if b.Builder.Remote() {
		return nil, errors.New("a remote binding has no local tree")
	}
	dir := b.Worktree
	if dir == "" {
		dir = b.CWD
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	return cmd, nil
}

// ensureYou returns the human mastermind's id, creating the record on first use
// (§4.1, spec §6.3). It is idempotent -- mastermind.Init looks the record up by
// (kind, session) before it creates anything -- and is only called by an
// action that needs an owner, so a read-only session never creates it. The
// record is never pruned, because HostPID is 0 (internal/mastermind/prune.go).
func ensureYou(rt relevo.Runtime) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	rec, _, err := mastermind.Init(rt.MasterMinds, mastermind.InitInput{
		Kind:      "human",
		SessionID: "tui",
		CWD:       home,
		Name:      "you",
		Now:       rt.Now(),
	})
	if err != nil {
		return "", err
	}
	return rec.ID, nil
}

// Send files planFile as the binding's next round, the same call `relevo send`
// makes (§4.5). The text is the pick line, the drift line when there is one,
// then the round that was filed.
func (a *mastermindActions) Send(ctx context.Context, key, planFile string) Result {
	rt, name, ok := a.resolve(key)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}
	res, err := relevo.Send(ctx, rt, name, planFile, relevo.SendOptions{})
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	return Result{Text: sendText(name, res), Refresh: true}
}

// sendText is what a send prints: the pick line, the drift line when there is
// one, then `sent round <N> to <name>` (§4.5).
func sendText(name string, res relevo.SendResult) string {
	var lines []string
	if res.Pick != "" {
		lines = append(lines, res.Pick)
	}
	if res.Drift != "" {
		lines = append(lines, res.Drift)
	}
	lines = append(lines, fmt.Sprintf("sent round %d to %s", res.Round, name))
	return strings.Join(lines, "\n")
}

// Bind adds a binding of this repo for the human mastermind (§4.5): the same
// relevo.Add the `relevo bind` verb calls, with `you` as the owner. The text
// is `added <name>: builder <candidate> on <tree>`, then the gated note and
// the pick note, as runAdd prints them (cmd/relevo/main.go:1509-1604).
func (a *mastermindActions) Bind(ctx context.Context, in BindInput) Result {
	if a.repo == "" {
		return Result{Err: errors.New("start relevo ui inside a git repository to bind")}
	}
	you, err := ensureYou(a.runtime())
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	res, err := relevo.Add(ctx, a.runtime(), relevo.AddOptions{
		Name:         in.Name,
		Candidate:    in.Candidate,
		MasterMindID: you,
		Repo:         a.repo,
		Feature:      in.Feature,
	})
	if err != nil {
		return Result{Err: err, Refresh: true}
	}

	tree := res.Worktree
	if tree == "" {
		tree = res.Binding.CWD
	}
	lines := []string{fmt.Sprintf("added %s: builder %s on %s",
		res.Binding.Name, a.runtime().Candidates.NameOf(res.Binding.BuilderCandidate), tree)}
	if n := availability.GatedNote(relevo.AvailabilityDeps(a.runtime()), res.Binding.BuilderCandidate); n != "" {
		lines = append(lines, n)
	}
	if n := bindPickNote(a.runtime(), res.Resolution); n != "" {
		lines = append(lines, n)
	}
	return Result{Text: strings.Join(lines, "\n"), Refresh: true}
}

// bindPickNote is notePick's line (cmd/relevo/main.go:842-847): the pick, in
// the candidates' short names, and only when relevo actually chose (neither an
// explicit token nor an adoption has anything to explain).
func bindPickNote(rt relevo.Runtime, res relevo.Resolution) string {
	if res.How == "" || res.How == relevo.HowExplicit {
		return ""
	}
	return relevo.PickText("builder", res, rt.Candidates)
}

// Retry resends a round's plan on another candidate (§4.5): stop the open
// round if there is one (and resend that round), else resend the newest round
// with a recorded plan; then Send the plan bytes with the candidate, which
// persists as the binding's builder from that round on.
func (a *mastermindActions) Retry(ctx context.Context, key, candidate string) Result {
	rt, name, ok := a.resolve(key)
	if !ok {
		return Result{Err: errors.New("unknown binding")}
	}

	stopped, err := relevo.Stop(ctx, rt, name, relevo.StopOptions{})
	round := 0
	switch {
	case err == nil:
		round = stopped.Round
	case errors.Is(err, relevo.ErrNothingToStop):
		round = lastPlannedRound(rt, name)
		if round == 0 {
			// Nothing to stop and nothing plan-shaped on disk: fall back to
			// the newest round that ought to have a plan, so RetryPlan's own
			// "no plan recorded for <name> round <N>" is the notice (§6).
			b, lerr := rt.Store.Load(name)
			if lerr != nil {
				return Result{Err: lerr, Refresh: true}
			}
			round = b.Round - 1
		}
	default:
		return Result{Err: err, Refresh: true}
	}

	plan, err := relevo.RetryPlan(rt, name, round)
	if err != nil {
		return Result{Err: err, Refresh: true}
	}

	tmp, err := os.CreateTemp("", "relevo-retry-"+name+"-*.md")
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(plan); err != nil {
		tmp.Close()
		return Result{Err: err, Refresh: true}
	}
	if err := tmp.Close(); err != nil {
		return Result{Err: err, Refresh: true}
	}

	res, err := relevo.Send(ctx, rt, name, tmp.Name(), relevo.SendOptions{Builder: candidate})
	if err != nil {
		return Result{Err: err, Refresh: true}
	}
	return Result{Text: sendText(name, res), Refresh: true}
}

// lastPlannedRound is the highest round with a plan recorded, 0 when none is:
// the round a retry resends when the binding has no open round (§4.5).
func lastPlannedRound(rt relevo.Runtime, name string) int {
	files, err := rt.Store.RoundFiles(name)
	if err != nil {
		return 0
	}
	best := 0
	for _, base := range files {
		if r, ok := plannedRound(base); ok && r > best {
			best = r
		}
	}
	return best
}

// plannedRound parses a round file's basename, e.g. "004-prompt.md"; the
// legacy "004-plan.md" spelling still answers.
func plannedRound(base string) (int, bool) {
	stem := strings.TrimSuffix(base, "-prompt.md")
	if stem == base {
		stem = strings.TrimSuffix(base, "-plan.md")
	}
	n, err := strconv.Atoi(stem)
	if err != nil || stem == base || n < 1 {
		return 0, false
	}
	return n, true
}

// Pull takes the human mastermind's pending payload for key, delivered with the
// TUI's own route: the cockpit shows it in the round view's report tab.
func (a *mastermindActions) Pull(ctx context.Context, key string) (string, bool, error) {
	rt, name, ok := a.resolve(key)
	if !ok {
		return "", false, errors.New("unknown binding")
	}
	return delivery.Pull(ctx, rt.Store, name, "tui")
}

// Candidates is a role's candidate names in the role's order (§4.5): what the
// bind and retry prompts cycle through. A role the registry does not know has
// none.
func (a *mastermindActions) Candidates(role string) []string {
	r, ok := a.runtime().RoleRegistry().Role(role)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(r.Ranked))
	for _, ranked := range r.Ranked {
		out = append(out, a.runtime().Candidates.NameOf(ranked.Token))
	}
	return out
}

// ConfigDoc is the stored config, freshly read through the store (§4.2). A
// runtime with no config store cannot answer.
func (a *mastermindActions) ConfigDoc() (relevo.ConfigDoc, error) {
	if a.runtime().Config == nil {
		return relevo.ConfigDoc{}, errors.New("no config store")
	}
	return relevo.LoadConfigDoc(a.runtime().Config)
}

// ApplyConfig writes one validated edit and reloads this adapter's runtime from
// the store (§4.2): the sections ConfigWatcher.Refresh would replace. An error
// from the write is a failure; a failed reload after a successful write is not,
// because the write already happened, so the text says so.
func (a *mastermindActions) ApplyConfig(ctx context.Context, e relevo.ConfigEdit) Result {
	if a.runtime().Config == nil {
		return Result{Err: errors.New("no config store")}
	}
	if err := relevo.WriteConfigEdit(a.runtime().Config, e); err != nil {
		return Result{Err: err, Refresh: true}
	}
	if err := a.live.Refresh(); err != nil {
		return Result{Text: "saved; reload failed: " + err.Error(), Refresh: true}
	}
	return Result{Text: e.Message, Refresh: true}
}

// Probe spawns one candidate's harness headless and measures it (§4.2), the
// same relevo.Probe `relevo probe <token>` runs. The result is one formatted
// line; a sample carrying an error is that error.
func (a *mastermindActions) Probe(ctx context.Context, name string) Result {
	if a.probe == nil {
		return Result{Err: errors.New("probing needs relevo ui")}
	}
	host, _ := os.Hostname()
	rs, err := availability.Probe(ctx, relevo.AvailabilityDeps(a.runtime()), a.probe, []string{name}, host, nil)
	if err != nil {
		return Result{Err: err}
	}
	if len(rs) == 0 {
		return Result{}
	}
	r := rs[0]
	if r.Err != "" {
		return Result{Err: errors.New(r.Err)}
	}
	return Result{Text: availability.FormatProbe(r, availability.ProbeNameWidth([]string{name}))}
}

// AgentFiles is one agent's definition state on every harness kind whose
// binary is installed, in harness.All() order: the same dry run `relevo config
// agents --agent <agent> --dry-run` prints. A source custom agent is rendered
// from the live config; a native or unknown name has none.
func (a *mastermindActions) AgentFiles(agent string) ([]harness.AgentFile, error) {
	env, err := relevo.AgentInstallEnv()
	if err != nil {
		return nil, err
	}
	fs, err := harness.AgentFiles(env, agent)
	if err != nil || fs != nil {
		return fs, err
	}
	return relevo.CustomAgentFiles(a.runtime().Config, env, agent)
}

// ResetAgentFile overwrites agent's definition file on kind with the copy
// relevo renders or ships: a source custom agent resets through
// relevo.ResetCustomAgentFile, everything else through harness.ResetAgentFile.
// Its text is `reset <kind>'s <agent>`; Refresh re-reads the view's rows.
func (a *mastermindActions) ResetAgentFile(ctx context.Context, kind, agent string) Result {
	env, err := relevo.AgentInstallEnv()
	if err != nil {
		return Result{Err: err}
	}
	cfg := a.runtime().Config
	if cfg != nil {
		if loaded, lerr := cfg.Load(); lerr == nil && relevo.IsSourceAgent(loaded.Agents, agent) {
			if _, err := relevo.ResetCustomAgentFile(cfg, env, kind, agent); err != nil {
				return Result{Err: err}
			}
			return Result{Text: "reset " + kind + "'s " + agent, Refresh: true}
		}
	}
	if _, err := harness.ResetAgentFile(env, kind, agent); err != nil {
		return Result{Err: err}
	}
	return Result{Text: "reset " + kind + "'s " + agent, Refresh: true}
}

// actionMsg is what an action's tea.Cmd returns (§3).
type actionMsg struct {
	verb, key string
	res       Result
}

// stderrMsg carries one captured stderr/slog line (§3, §4.4).
type stderrMsg struct{ line string }

// workingMsg marks one action in flight, so the footer can show it until its
// actionMsg arrives (§4.3).
type workingMsg struct{ verb, key string }

// pullMsg is what a Pull in flight returns: the report the human mastermind was
// waiting for, or why there is none (§4.5).
type pullMsg struct {
	key  string
	text string
	ok   bool
	err  error
}

// pullCmd takes the human mastermind's pending report off the update loop.
func pullCmd(ctx context.Context, a Actions, key string) tea.Cmd {
	return func() tea.Msg {
		text, ok, err := a.Pull(ctx, key)
		return pullMsg{key: key, text: text, ok: ok, err: err}
	}
}

// openOverlayMsg asks the shell to show an overlay (§3).
type openOverlayMsg struct{ ov overlay }

// openOverlay returns the command a key returns to raise ov.
func openOverlay(ov overlay) tea.Cmd {
	return func() tea.Msg { return openOverlayMsg{ov: ov} }
}

// runAction is what a confirm's yes or a prompt's enter returns: mark the
// action in flight, then run it off the update loop (§5).
func runAction(ctx context.Context, verb, key string, f func(context.Context) Result) tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return workingMsg{verb: verb, key: key} },
		func() tea.Msg { return actionMsg{verb: verb, key: key, res: f(ctx)} },
	)
}
