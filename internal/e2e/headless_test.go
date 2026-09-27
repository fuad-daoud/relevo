package e2e

// TestHeadlessE2E is #303 step 5: the headless end-to-end round CI runs
// (docs/specs/2026-09-22-drop-herdr-design.md §6.4). No build tag, so plain
// `go test ./...` runs it. No network, no real harness, no herdr: a fake
// `claude` on PATH, this process as the Claude Code session, and `relevo mcp`
// served in-process over a pipe pair.
//
// The scenario, in the plan's order:
//
//	1  a fake `claude` on PATH that writes the report, commits, and creates
//	   the done marker;
//	2  isolated state under t.TempDir(), a throwaway repo, and a
//	   candidates.json and policy.json naming the fake harness;
//	3  the plugin's SessionStart hook registers the planner and exports
//	   RELEVO_PLANNER into $CLAUDE_ENV_FILE;
//	4  a channel-mode relevo mcp claims that planner over a pipe pair;
//	5  add, send, the daemon closes the round, and the report arrives as a
//	   kind="report" channel notification;
//	6  pull and done;
//	7  the hook fires again for /clear: same planner id, moved session;
//	8  a tools-mode relevo mcp sends round two, and its result carries the
//	   background wait the planner runs for its report.
//
// Every wait is bounded and fails with the thing it was waiting for named, so
// a broken step fails the test instead of hanging it.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/mcp"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	// fakeReportText is the sentence the fake harness writes into its report.
	// A channel push expands the report file (delivery.PushText), so this exact
	// sentence is what arrives as the notification's body.
	fakeReportText = "fake-harness-report: this round was handled by the fake claude on PATH"

	// The three harness sessions the scenario registers: the startup session,
	// the session /clear moves the same planner to, and the tools-mode
	// planner's own session.
	sessionStartup = "e2e-session-startup"
	sessionClear   = "e2e-session-after-clear"
	sessionTools   = "e2e-session-tools"

	// The bounded waits. A round that never closes, a notification that never
	// arrives, or an MCP call that is never answered fails with a message
	// naming what was missing, never hangs.
	roundDeadline  = 60 * time.Second
	notifyDeadline = 15 * time.Second
	mcpDeadline    = 10 * time.Second

	// The two bindings the scenario creates: one delivered by the channel,
	// one by the tools-mode planner's background wait.
	channelBinding = "e2e-round"
	toolsBinding   = "e2e-tools"
)

// TestHeadlessE2E is the one test the Makefile's `e2e` target runs:
// `go test ./internal/e2e/ -run TestHeadlessE2E -count=1`.
func TestHeadlessE2E(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// -- 2. Isolated state --------------------------------------------------
	// HOME, XDG_CONFIG_HOME and XDG_STATE_HOME all point under a t.TempDir(),
	// so nothing here reads or writes the user's real config or state.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	// The hook, relevo mcp and every planner verb detect a Claude Code session
	// from the environment (§1.1); this process plays that session.
	t.Setenv("CLAUDECODE", "1")

	// -- 1. A fake `claude` first on PATH -----------------------------------
	t.Setenv("PATH", writeFakeHarness(t)+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := filepath.Join(home, ".config", "relevo")
	writeCandidatesAndPolicy(t, configDir)
	root := filepath.Join(home, ".local", "state", "relevo")
	rt, reg := newHeadlessRuntime(t, root, configDir)
	repo := newRepo(t)

	// -- 3. Planner registration, the way the SessionStart hook does it -----
	// $CLAUDE_ENV_FILE is the temp file the hook appends its export to.
	envFile := filepath.Join(home, "claude-env.sh")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	first := runPlannerHook(t, reg, rt.Now, hookPayload(t, planner.SourceStartup, sessionStartup, repo))
	if got := readFile(t, envFile); !strings.Contains(got, "export RELEVO_PLANNER="+first.ID) {
		t.Fatalf("$CLAUDE_ENV_FILE after the startup hook = %q, want it to carry %q", got, "export RELEVO_PLANNER="+first.ID)
	}
	// §3.4: that export is how every later verb in the session finds its
	// planner, so the rest of the scenario runs with it set.
	t.Setenv("RELEVO_PLANNER", first.ID)

	// -- 4. The channel -----------------------------------------------------
	channel := startChannel(t, ctx, rt, first.ID, 50*time.Millisecond)
	if !claimExists(t, rt, first.ID) {
		t.Fatalf("relevo mcp wrote no live channel claim for planner %s", first.ID)
	}

	// The channel's poll loop and any builder a failing step left behind are
	// this test's children; both go away with it.
	t.Cleanup(func() {
		stopRecordedBuilders(t, rt, channelBinding, toolsBinding)
		cancel()
	})

	// -- 5. Round one -------------------------------------------------------
	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{Name: channelBinding, Repo: repo}); err != nil {
		t.Fatalf("relevo.Add(%s): %v", channelBinding, err)
	}
	channelPlan := writePlan(t, "channel.md", "# Round\n\nOne line of work.\n")
	if _, err := relevo.Send(ctx, rt, channelBinding, channelPlan, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send(%s): %v", channelBinding, err)
	}

	// 5.3: the daemon tick, bounded. A round that never closes fails here,
	// naming the binding, its state and its builder.
	daemon := relevo.NewDaemon(rt, 200*time.Millisecond)
	tickUntilRoundCloses(t, ctx, daemon, rt, channelBinding)

	// The round closed on the fake harness's own report and marker: a close
	// that fell back to a scrape or an unmarked exit carries a note.
	if e, _ := reportEntry(t, rt, channelBinding, 1); e.Note != "" {
		t.Fatalf("binding %s round 1 closed with note %q, want \"\": the fake harness's own marker must be what closed it; builder log tail:\n%s",
			channelBinding, e.Note, builderLogTail(rt, channelBinding, 1))
	}

	// 5.4: the report arrives on the channel, and its body carries the
	// report's own text (PushText expands the file a report entry points at).
	note := channel.waitNote(t, channelBinding, "report", notifyDeadline)
	if !strings.Contains(note.content, fakeReportText) {
		t.Fatalf("the kind=\"report\" channel notification for %s does not carry the fake report's text:\n%s",
			channelBinding, note.content)
	}

	// -- 6. pull / done -----------------------------------------------------
	// relevo mcp's own drain confirms the entry it pushed (route=channel), so
	// pull has nothing left to take: the documented "nothing pending" answer
	// (internal/relevo/pull.go). Wait for that confirm first, so this is not a
	// race with the poll loop, then assert pull reports nothing pending and
	// that the report itself is on disk where the notification named it.
	waitFor(t, notifyDeadline, func() bool {
		e, ok := reportEntry(t, rt, channelBinding, 1)
		return ok && e.Route == "channel"
	}, "the channel's confirm of %s round 1 (route=channel)", channelBinding)

	channelWait := runWait(t, ctx, rt, channelBinding, time.Minute)
	if channelWait.err != nil {
		t.Fatalf("relevo wait --name %s: %v", channelBinding, channelWait.err)
	}
	if channelWait.res.Payload != "" {
		t.Fatalf("relevo wait --name %s printed a payload %q; the channel already took the report (route=channel), so nothing must be pending", channelBinding, channelWait.res.Payload)
	}
	channelReport := rt.Store.ReportPath(channelBinding, 1)
	if got := readFile(t, channelReport); !strings.Contains(got, fakeReportText) {
		t.Fatalf("report %s = %q, want it to carry the fake report text", channelReport, got)
	}

	if _, err := relevo.Done(ctx, rt, channelBinding); err != nil {
		t.Fatalf("relevo.Done(%s): %v", channelBinding, err)
	}
	if b := loadBinding(t, rt, channelBinding); b.State != store.StateDone {
		t.Fatalf("binding %s state after done = %q, want %q", channelBinding, b.State, store.StateDone)
	}

	// -- 7. /clear: same planner, new session -------------------------------
	afterClear := runPlannerHook(t, reg, rt.Now, hookPayload(t, planner.SourceClear, sessionClear, repo))
	if afterClear.ID != first.ID {
		t.Fatalf("planner id after /clear = %s, want the same record %s", afterClear.ID, first.ID)
	}
	moved := recordByID(t, reg, first.ID)
	if moved.SessionID != sessionClear {
		t.Fatalf("planner %s current session = %q, want %q", first.ID, moved.SessionID, sessionClear)
	}
	if !hasSession(moved, sessionStartup) {
		t.Fatalf("planner %s sessions = %+v, want the startup session %q kept", first.ID, moved.Sessions, sessionStartup)
	}

	// -- 8. Tools mode (revised D6) -----------------------------------------
	// A second relevo mcp, in tools mode, for a second planner session -- what
	// a second `relevo mcp` in a second session is. The channel-mode server for
	// the first planner stays live, which is what makes 8.4's "nothing was
	// written to a channel mailbox for that binding" a real check: a channel
	// drains only its own planner's bindings (delivery.Drain), so the tools-mode
	// binding's report can only reach the planner through pull.
	second, _, err := planner.Init(reg, planner.InitInput{
		Kind: "claude", SessionID: sessionTools, CWD: repo, Now: rt.Now(),
	})
	if err != nil {
		t.Fatalf("register the tools-mode planner: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("the tools-mode registration reused planner %s", first.ID)
	}
	tools := startMCP(t, ctx, rt, mcp.ModeTools, second.ID)

	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{Name: toolsBinding, Repo: repo, PlannerID: second.ID}); err != nil {
		t.Fatalf("relevo.Add(%s): %v", toolsBinding, err)
	}
	toolsPlan := writePlan(t, "tools.md", "# Round two\n\nOne line of work.\n")

	// 8.1/8.2: the send tool's result ends with the background wait for this
	// binding's name and budget.
	text := tools.callSend(t, toolsBinding, toolsPlan)
	budget := (time.Duration(loadBinding(t, rt, toolsBinding).RoundTimeoutMS) * time.Millisecond).String()
	wantCommand := "relevo wait --name " + toolsBinding + " --timeout " + budget
	if !strings.HasSuffix(text, wantCommand) {
		t.Fatalf("the tools-mode send result does not end with the background-wait command for %s (budget %s):\n%s",
			toolsBinding, budget, text)
	}

	// 8.3: the daemon closes round two, then the test runs the command the
	// result named -- through the in-process equivalent of relevo wait, with
	// the name and budget parsed out of the result itself. The wait delivers
	// the report itself (§4.1), so there is no second command.
	tickUntilRoundCloses(t, ctx, daemon, rt, toolsBinding)

	waitName, waitBudget := parseWaitCommand(t, text)
	timeout, err := time.ParseDuration(waitBudget)
	if err != nil {
		t.Fatalf("the send result's budget %q is not a duration relevo wait accepts: %v", waitBudget, err)
	}
	waited := runWait(t, ctx, rt, waitName, timeout)
	if waited.err != nil {
		t.Fatalf("relevo wait --name %s --timeout %s: %v", waitName, waitBudget, waited.err)
	}
	if waited.res.Code == relevo.WaitTimeout || !waited.res.Done {
		t.Fatalf("relevo wait --name %s --timeout %s = %+v, want the closed round (a timeout means round 1 never closed)", waitName, waitBudget, waited.res)
	}
	if want := rt.Store.ReportPath(toolsBinding, 1); waited.res.Line != want {
		t.Fatalf("relevo wait printed %q, want the round's report path %s", waited.res.Line, want)
	}
	if !strings.Contains(waited.res.Payload, fakeReportText) {
		t.Fatalf("the wait output does not carry the report's text; want %q in:\n%s", fakeReportText, waited.res.Payload)
	}
	output := waited.res.Line + "\n" + waited.res.Payload

	// 8.4: the output names the fake report -- relevo wait prints the entry's
	// payload and the report's text (PushText), so the planner needs no
	// second read; the payload names the `relevo show` command that prints
	// the round's report (§4.2) -- and the report's own text is on disk at
	// the path that command reads.
	reportPath := rt.Store.ReportPath(toolsBinding, 1)
	if want := "relevo show " + toolsBinding + " --round 1 --report"; !strings.Contains(output, want) {
		t.Fatalf("the wait output does not name %s:\n%s", want, output)
	}
	if got := readFile(t, reportPath); !strings.Contains(got, fakeReportText) {
		t.Fatalf("report %s = %q, want the fake report text", reportPath, got)
	}

	// 8.4: the log entry is marked delivered with route=wait, nothing was
	// written to a channel mailbox for that binding -- the live channel-mode
	// server received no notification naming it -- and the round closed on the
	// fake harness's own marker, not on a fallback.
	entry, ok := reportEntry(t, rt, toolsBinding, 1)
	if !ok {
		t.Fatalf("binding %s round 1 has no report entry", toolsBinding)
	}
	if entry.Note != "" {
		t.Fatalf("binding %s round 1 closed with note %q, want \"\": the fake harness's own marker must be what closed it; builder log tail:\n%s",
			toolsBinding, entry.Note, builderLogTail(rt, toolsBinding, 1))
	}
	if !entry.Confirmed || entry.Route != "wait" {
		t.Fatalf("binding %s round 1 report entry = confirmed:%v route:%q, want confirmed with route=wait", toolsBinding, entry.Confirmed, entry.Route)
	}
	if names := channel.noteBindings(); contains(names, toolsBinding) {
		t.Fatalf("a channel notification was written for %s (notifications: %v)", toolsBinding, names)
	}
}

// --- the fake harness -------------------------------------------------------

// writeFakeHarness writes an executable named `claude` -- a kind the candidate
// table knows -- into a temp dir and returns that dir, so the caller can put it
// first on PATH. relevo resolves the binary with exec.LookPath when it starts a
// round (internal/proc), so PATH is the whole wiring.
func writeFakeHarness(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := strings.ReplaceAll(fakeHarnessScript, "__READER_LINE__", fakeReaderStreamLine(t))
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return dir
}

// fakeReportBody is the body the fake harness writes to the report path: the
// sentence the channel assertion looks for, plus a well-formed relevo tail so
// the round closes on a marked report (reporttail.Parse).
const fakeReportBody = "# Round Report\n\n" +
	fakeReportText + "\n\n" +
	"```relevo\n" +
	"status: done\n" +
	"halted_at: \"\"\n" +
	"changed_paths: [fake-round.txt]\n" +
	"commands_run: [\"git commit -m \\\"fake harness: one round\\\"\"]\n" +
	"not_done: []\n" +
	"```\n"

// fakeHarnessScript is the fake `claude` relevo launches for a round. Its argv
// is the real claude Print form -- `claude -p <prompt> --model M --agent
// plan-executor --output-format stream-json --verbose` (internal/harness) -- so
// it finds relevo's handoff prompt in its arguments and reads the plan, report
// and marker paths out of that prompt, the way the launch form passes them.
//
// It does the four things a fake harness must: writes the report, makes one
// commit in its worktree, creates the done marker, and prints one valid
// stream-json line.
var fakeHarnessScript = `#!/bin/sh
set -eu

prompt=""
for arg in "$@"; do
	case "$arg" in
	*"Your working tree is: "*)
		prompt="$arg"
		;;
	esac
done
if [ -z "$prompt" ]; then
	echo "fake-claude: relevo's handoff prompt is not in argv: $*" >&2
	exit 2
fi

worktree=$(printf '%s\n' "$prompt" | awk '/^Your working tree is: /{ sub(/^Your working tree is: /, ""); print; exit }')
plan=$(printf '%s\n' "$prompt" | awk '/^Read: /{ sub(/^Read: /, ""); print; exit }')
report=$(printf '%s\n' "$prompt" | awk '/^When you are done, write your report to: /{ sub(/^When you are done, write your report to: /, ""); print; exit }')
artifact=$(printf '%s\n' "$prompt" | awk '/^Write every file you produce into this directory/{ sub(/^[^:]*: /, ""); print; exit }')
marker=$(printf '%s\n' "$prompt" | awk '/create this empty file: /{ sub(/.*create this empty file: /, ""); print; exit }')

if [ -z "$worktree" ] || [ -z "$plan" ] || [ -z "$marker" ]; then
	echo "fake-claude: the prompt is missing a path" >&2
	echo "tree=$worktree plan=$plan report=$report artifact=$artifact marker=$marker" >&2
	exit 2
fi
if [ -z "$report" ] && [ -z "$artifact" ]; then
	echo "fake-claude: the prompt names neither a report nor an artifact directory" >&2
	exit 2
fi
if [ ! -s "$plan" ]; then
	echo "fake-claude: the staged plan $plan is missing or empty" >&2
	exit 2
fi

# A reader round: fill the artifact directory, edit the throwaway tree, create
# the marker, then -- after the marker, as a real runner does -- print the final
# message the summary is taken from and exit.
if [ -n "$artifact" ]; then
	mkdir -p "$artifact"
	printf '<!doctype html><title>reader</title>\n' > "$artifact/index.html"
	printf 'body { color: #000; }\n' > "$artifact/style.css"
	printf 'the reader edited this\n' > "$worktree/reader-edit.txt"
	: > "$marker"
	sleep 0.2
	printf '%s\n' '__READER_LINE__'
	exit 0
fi

{
	printf 'plan read by the fake harness: %s\n' "$plan"
	printf 'working tree: %s\n\n' "$worktree"
	cat <<'RELEVO_FAKE_REPORT'
` + fakeReportBody + `RELEVO_FAKE_REPORT
} > "$report"

printf 'one round of fake work\n' > "$worktree/fake-round.txt"

# One commit in the round's own worktree. The daemon reads that tree with git
# while the round is open (truthful diff capture, the escape check), but every
# read it makes runs with GIT_OPTIONAL_LOCKS=0, so it never takes index.lock
# and this commit cannot be blocked by one.
git -C "$worktree" add -A
git -C "$worktree" commit -q -m "fake harness: one round"

# The completion marker is the last thing the handoff asks for.
: > "$marker"

# One valid claude stream-json line, so the headless reader has a line to
# render into the builder log.
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"fake round done"}'
`

// --- runtime, config and hook helpers ---------------------------------------

// writeCandidatesAndPolicy writes the two config files a planner uses, naming
// the fake harness: one claude candidate that serves the builder role, and a
// policy that orders it.
func writeCandidatesAndPolicy(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	candidates := `[{"harness":"claude","provider":"anthropic","model":"fake-e2e","roles":["builder","reviewer"]}]`
	pol := `{"order":{"builder":["claude/anthropic/fake-e2e"],"reviewer":["claude/anthropic/fake-e2e"]}}`
	for name, body := range map[string]string{"candidates.json": candidates, "policy.json": pol} {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// newHeadlessRuntime assembles the production runtime's wiring for a test:
// the same store root, config resolution and claim store cmd/relevo's
// newRuntime builds, with a real process Runner (the fake harness is a real
// executable on PATH) and no remote client, database or release fetcher.
func newHeadlessRuntime(t *testing.T, root, configDir string) (relevo.Runtime, *planner.DBRegistry) {
	t.Helper()

	candidates, err := candidate.Load(filepath.Join(configDir, "candidates.json"))
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}
	pol, err := policy.Load(filepath.Join(configDir, "policy.json"))
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}

	st := store.New(root)
	gitClient := git.NewClient("git", 10*time.Second, 0)

	// Gates live in the store root's database, as buildRuntime wires them
	// (P3b plan §4.5); the planner records and the channel claims live in the
	// same database (P3b round 2 §4.1, §4.2).
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("open store db: %v", err)
	}
	reg := &planner.DBRegistry{KV: db.TxKV{DB: mdb}, Now: time.Now, Root: st.PlannersDir()}

	rt := relevo.Runtime{
		Git:        gitClient,
		Runner:     proc.New(),
		Store:      st,
		Candidates: candidates,
		Gates:      mdb,
		GatesDir:   root,
		Latency:    mdb,
		Policy:     pol,
		Now:        time.Now,
		Channels:   &delivery.KVClaims{KV: db.TxKV{DB: mdb}, Root: st.ChannelsDir()},
		Planners:   reg,
		ProcStart:  procStartUnix,
	}
	return rt, reg
}

// procStartUnix reads a process's start time in Unix seconds, the pid-reuse
// defence planner.Resolve's host step needs -- cmd/relevo's own wiring.
func procStartUnix(pid int) (int64, error) {
	started, err := proc.StartTime(context.Background(), pid)
	if err != nil {
		return 0, err
	}
	return started.Unix(), nil
}

// hookPayload is one canned Claude Code SessionStart payload (§3.4).
func hookPayload(t *testing.T, source, session, cwd string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"hook_event_name": "SessionStart",
		"source":          source,
		"session_id":      session,
		"transcript_path": filepath.Join(t.TempDir(), session+".jsonl"),
		"cwd":             cwd,
	})
	if err != nil {
		t.Fatalf("encode hook payload: %v", err)
	}
	return string(raw)
}

// runPlannerHook runs the hook's work in-process: it parses the SessionStart
// payload, calls planner.Init with this process as the host (the hook's parent
// IS the Claude Code process, §1.1), and appends the export line to
// $CLAUDE_ENV_FILE exactly as cmd/relevo's plannerInitHook does. cmd/relevo's
// command function itself is package main and cannot be called from here; this
// is the same sequence through the same planner package.
func runPlannerHook(t *testing.T, reg *planner.DBRegistry, now func() time.Time, raw string) planner.Record {
	t.Helper()

	in, err := planner.ParseHookInput(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseHookInput(%s): %v", raw, err)
	}
	rec, _, err := planner.Init(reg, planner.InitInput{
		Kind:           "claude",
		SessionID:      in.SessionID,
		TranscriptPath: in.TranscriptPath,
		CWD:            in.CWD,
		HostPID:        os.Getpid(),
		Now:            now(),
	})
	if err != nil {
		t.Fatalf("planner.Init(%s): %v", in.SessionID, err)
	}

	envFile := os.Getenv("CLAUDE_ENV_FILE")
	if envFile == "" {
		t.Fatal("$CLAUDE_ENV_FILE is not set: the hook has nowhere to export RELEVO_PLANNER")
	}
	f, err := os.OpenFile(envFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open $CLAUDE_ENV_FILE: %v", err)
	}
	defer f.Close()
	if _, err := io.WriteString(f, planner.EnvLine(rec.ID)); err != nil {
		t.Fatalf("append $CLAUDE_ENV_FILE: %v", err)
	}
	return rec
}

// --- the in-process MCP client ----------------------------------------------

// channelNote is one notifications/claude/channel push the client received.
type channelNote struct {
	content string
	meta    map[string]string
}

// mcpClient is the test's end of a `relevo mcp` stdio session: it writes
// JSON-RPC requests to the server and reads every line the server writes,
// keeping responses by id and every channel push.
type mcpClient struct {
	t   *testing.T
	srv *mcp.Server
	in  *io.PipeWriter

	mu      sync.Mutex
	nextID  int
	waiters map[int]chan map[string]any
	notes   []channelNote
}

// startMCP starts one relevo mcp server in-process over a pipe pair and runs the
// stdio handshake with it: initialize, then notifications/initialized.
func startMCP(t *testing.T, ctx context.Context, rt relevo.Runtime, mode mcp.Mode, plannerID string) *mcpClient {
	t.Helper()

	srvIn, clientWrite := io.Pipe() // the test writes; the server reads
	clientRead, srvOut := io.Pipe() // the server writes; the test reads
	srv := &mcp.Server{
		Verbs:   &mcp.RelevoVerbs{RT: rt, Planner: plannerID},
		Version: "e2e-test",
		Mode:    mode,
		Log:     io.Discard,
	}
	c := &mcpClient{t: t, srv: srv, in: clientWrite, waiters: map[int]chan map[string]any{}}

	go func() { _ = srv.Serve(ctx, srvIn, srvOut) }()
	go c.read(clientRead)

	t.Cleanup(func() {
		// Closing the request side turns Serve's read loop into an EOF; closing
		// the response side unblocks the reader goroutine.
		_ = clientWrite.Close()
		_ = clientRead.Close()
	})

	if resp := c.call(ctx, "initialize", map[string]any{"protocolVersion": mcp.ProtocolVersion}); resp["error"] != nil {
		t.Fatalf("relevo mcp initialize: %v", resp["error"])
	}
	c.notify("notifications/initialized")
	return c
}

// startChannel starts a channel-mode relevo mcp and, in cmd/relevo's order, makes
// the two moves the command wires from the server's OnInitialized callback: it
// writes the claim, then polls -- re-reading, refreshing the claim and draining
// the planner's mailbox every interval (cmd/relevo's pollMCPChannel).
func startChannel(t *testing.T, ctx context.Context, rt relevo.Runtime, plannerID string, interval time.Duration) *mcpClient {
	t.Helper()

	c := startMCP(t, ctx, rt, mcp.ModeChannel, plannerID)

	now := rt.Now()
	claim := delivery.Claim{
		Planner:   plannerID,
		PID:       os.Getpid(),
		HostPID:   os.Getpid(),
		StartedAt: now,
		SeenAt:    now,
		Version:   "e2e-test",
	}
	if err := rt.Channels.Write(claim, now); err != nil {
		t.Fatalf("write channel claim for planner %s: %v", plannerID, err)
	}

	st := &delivery.DrainState{Planner: plannerID}
	pollCtx, stopPoll := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				claim.SeenAt = rt.Now()
				if err := rt.Channels.Write(claim, claim.SeenAt); err != nil {
					return
				}
				if _, err := delivery.Drain(pollCtx, delivery.Deps{Store: rt.Store, Now: rt.Now, Channels: rt.Channels, Deliverers: rt.Deliverers, Planners: rt.Planners}, st, c.srv); err != nil {
					return
				}
			}
		}
	}()
	// The poll loop writes under the state root (its claim, and any entry it
	// delivers). Wait for it to stop before the test's temp dirs are removed,
	// or the removal races its next write and fails the test.
	t.Cleanup(func() {
		stopPoll()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Errorf("the channel poll loop for planner %s did not stop within 5s", plannerID)
		}
	})
	return c
}

// read consumes the server's stdout: responses by id, channel pushes appended
// to notes.
func (c *mcpClient) read(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for scanner.Scan() {
		var msg map[string]any
		if json.Unmarshal(scanner.Bytes(), &msg) != nil {
			continue
		}
		if id, ok := msg["id"].(float64); ok {
			c.mu.Lock()
			ch := c.waiters[int(id)]
			delete(c.waiters, int(id))
			c.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg["method"] != "notifications/claude/channel" {
			continue
		}
		params, _ := msg["params"].(map[string]any)
		content, _ := params["content"].(string)
		meta := map[string]string{}
		if raw, ok := params["meta"].(map[string]any); ok {
			for k, v := range raw {
				if s, ok := v.(string); ok {
					meta[k] = s
				}
			}
		}
		c.mu.Lock()
		c.notes = append(c.notes, channelNote{content: content, meta: meta})
		c.mu.Unlock()
	}
}

// call writes one request and waits, bounded, for its response.
func (c *mcpClient) call(ctx context.Context, method string, params any) map[string]any {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan map[string]any, 1)
	c.waiters[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	raw, err := json.Marshal(req)
	if err != nil {
		c.t.Fatalf("encode %s request: %v", method, err)
	}
	if _, err := c.in.Write(append(raw, '\n')); err != nil {
		c.t.Fatalf("relevo mcp %s: write: %v", method, err)
	}

	select {
	case resp := <-ch:
		return resp
	case <-time.After(mcpDeadline):
		c.t.Fatalf("relevo mcp answered no %s within %s", method, mcpDeadline)
	case <-ctx.Done():
		c.t.Fatalf("relevo mcp %s: test context ended: %v", method, ctx.Err())
	}
	return nil
}

// notify writes one notification (no id, no reply).
func (c *mcpClient) notify(method string) {
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if err != nil {
		c.t.Fatalf("encode %s notification: %v", method, err)
	}
	if _, err := c.in.Write(append(raw, '\n')); err != nil {
		c.t.Fatalf("relevo mcp %s: write: %v", method, err)
	}
}

// callSend drives the send tool and returns the text of its result.
func (c *mcpClient) callSend(t *testing.T, name, file string) string {
	t.Helper()
	resp := c.call(context.Background(), "tools/call", map[string]any{
		"name":      "send",
		"arguments": map[string]any{"name": name, "file": file},
	})
	if resp["error"] != nil {
		t.Fatalf("tools/call send: %v", resp["error"])
	}
	raw, err := json.Marshal(resp["result"])
	if err != nil {
		t.Fatalf("marshal send result: %v", err)
	}
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode send result %s: %v", raw, err)
	}
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("the send tool returned an error result: %s", raw)
	}
	return res.Content[0].Text
}

// waitNote waits, bounded, for a channel push with that binding and kind.
func (c *mcpClient) waitNote(t *testing.T, binding, kind string, timeout time.Duration) channelNote {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		var found *channelNote
		for i := range c.notes {
			if c.notes[i].meta["binding"] == binding && c.notes[i].meta["kind"] == kind {
				found = &c.notes[i]
			}
		}
		c.mu.Unlock()
		if found != nil {
			return *found
		}
		if time.Now().After(deadline) {
			t.Fatalf("the MCP client received no kind=%q channel notification for binding %q within %s (received: %s)",
				kind, binding, timeout, c.noteSummary())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// noteBindings is every binding a channel push has named so far.
func (c *mcpClient) noteBindings() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.notes))
	for _, n := range c.notes {
		out = append(out, n.meta["binding"])
	}
	return out
}

// noteSummary names what the client did receive, for a failure message.
func (c *mcpClient) noteSummary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.notes) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(c.notes))
	for _, n := range c.notes {
		parts = append(parts, n.meta["kind"]+" for "+n.meta["binding"])
	}
	return strings.Join(parts, ", ")
}

// --- small assertions -------------------------------------------------------

// waitCommandRE pulls the background wait out of a send result:
// relevo wait --name <n> --timeout <budget>. The wait prints the report
// itself, so there is no second half (§4.1).
var waitCommandRE = regexp.MustCompile(`relevo wait --name (\S+) --timeout (\S+)`)

// parseWaitCommand reads the wait command's name and budget out of a send
// result, so the test runs exactly what the result told the model to run.
func parseWaitCommand(t *testing.T, text string) (name, budget string) {
	t.Helper()
	m := waitCommandRE.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no background-wait command in the send result:\n%s", text)
	}
	return m[1], m[2]
}

// waitOutcome is one relevo.Wait call's result, carried out of its goroutine.
type waitOutcome struct {
	name string
	res  relevo.WaitResult
	err  error
}

// runWait runs relevo.Wait -- what `relevo wait --name n --timeout t` calls --
// under a test-side deadline, so a round that never closes fails the test
// rather than blocking the run.
func runWait(t *testing.T, ctx context.Context, rt relevo.Runtime, name string, timeout time.Duration) waitOutcome {
	t.Helper()
	waitCtx, stop := context.WithCancel(ctx)
	defer stop()

	done := make(chan waitOutcome, 1)
	go func() {
		n, res, err := relevo.Wait(waitCtx, rt, relevo.WaitOptions{
			Names: []string{name}, Timeout: timeout, Interval: 50 * time.Millisecond,
		})
		done <- waitOutcome{name: n, res: res, err: err}
	}()

	select {
	case out := <-done:
		return out
	case <-time.After(roundDeadline):
		t.Fatalf("relevo wait --name %s --timeout %s did not return within %s (the round never closed)", name, timeout, roundDeadline)
	}
	return waitOutcome{}
}

// tickUntilRoundCloses drives the daemon tick until the binding's round 1 has
// closed, bounded by roundDeadline. A round that never closes fails with the
// binding's own state in the message, so a fake harness that never wrote its
// report and marker is visible rather than a bare timeout.
func tickUntilRoundCloses(t *testing.T, ctx context.Context, daemon *relevo.Daemon, rt relevo.Runtime, name string) {
	t.Helper()
	deadline := time.Now().Add(roundDeadline)
	for {
		if err := daemon.Tick(ctx); err != nil {
			t.Fatalf("daemon.Tick: %v", err)
		}
		if hasReportEntry(t, rt, name, 1) {
			return
		}
		if time.Now().After(deadline) {
			b, err := rt.Store.Load(name)
			if err != nil {
				t.Fatalf("%s round 1 did not close within %s (binding unreadable: %v)", name, roundDeadline, err)
			}
			t.Fatalf("%s round 1 did not close within %s: state %s, round %d, builder pid %d (the fake harness writes its report and marker before exiting)",
				name, roundDeadline, b.State, b.Round, b.Builder.PID)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// stopRecordedBuilders is the plan's "clean up child processes with
// t.Cleanup": the fake harness exits on its own, but a round that broke may
// have left a process behind, and relevo's handle for one is its pid plus the
// start time that defends against pid reuse.
func stopRecordedBuilders(t *testing.T, rt relevo.Runtime, names ...string) {
	t.Helper()
	for _, name := range names {
		b, err := rt.Store.Load(name)
		if err != nil || b.Builder.PID == 0 {
			continue
		}
		h := spawn.ProcHandle{PID: b.Builder.PID, StartedAt: time.Unix(b.Builder.StartedAt, 0)}
		if err := rt.Runner.Kill(context.Background(), h, rt.Store.StreamPath(name, b.Round)); err != nil {
			t.Logf("cleanup: killing %s's builder pid %d: %v", name, b.Builder.PID, err)
		}
	}
}

// builderLogTail is the last lines of one round's builder log, for a failure
// message that needs to show what the fake harness actually did.
func builderLogTail(rt relevo.Runtime, name string, round int) string {
	raw, err := os.ReadFile(rt.Store.BuilderLogPath(name, round))
	if err != nil {
		return "(no builder log: " + err.Error() + ")"
	}
	text := strings.TrimRight(string(raw), "\n")
	if lines := strings.Split(text, "\n"); len(lines) > 20 {
		text = strings.Join(lines[len(lines)-20:], "\n")
	}
	return text
}

// waitFor polls pred, bounded, with a failure message naming what it waited for.
func waitFor(t *testing.T, timeout time.Duration, pred func() bool, format string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if pred() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, fmt.Sprintf(format, args...))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// reportEntry returns the newest to_planner report entry for one binding round.
func reportEntry(t *testing.T, rt relevo.Runtime, name string, round int) (store.LogEntry, bool) {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("read log for %s: %v", name, err)
	}
	var found store.LogEntry
	ok := false
	for _, e := range entries {
		if e.Round == round && e.Direction == store.DirToPlanner && e.Kind == store.KindReport {
			found, ok = e, true
		}
	}
	return found, ok
}

// hasReportEntry reports whether one binding round has closed.
func hasReportEntry(t *testing.T, rt relevo.Runtime, name string, round int) bool {
	t.Helper()
	_, ok := reportEntry(t, rt, name, round)
	return ok
}

// loadBinding loads one binding, failing the test when it cannot.
func loadBinding(t *testing.T, rt relevo.Runtime, name string) store.Binding {
	t.Helper()
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatalf("load binding %s: %v", name, err)
	}
	return b
}

// recordByID loads one planner record, failing the test when it cannot.
func recordByID(t *testing.T, reg *planner.DBRegistry, id string) planner.Record {
	t.Helper()
	rec, err := reg.Get(id)
	if err != nil {
		t.Fatalf("load planner %s: %v", id, err)
	}
	return rec
}

// hasSession reports whether a record's history names that session.
func hasSession(rec planner.Record, session string) bool {
	for _, s := range rec.Sessions {
		if s.SessionID == session {
			return true
		}
	}
	return false
}

// claimExists reports whether relevo mcp's claim for one planner is live in
// the runtime's claim store.
func claimExists(t *testing.T, rt relevo.Runtime, plannerID string) bool {
	t.Helper()
	c, err := rt.Channels.Live(plannerID, rt.Now())
	if err != nil {
		t.Fatalf("Live(%s): %v", plannerID, err)
	}
	return c != nil
}

// writePlan writes a plan file into a temp dir and returns its path.
func writePlan(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write plan %s: %v", path, err)
	}
	return path
}

// readFile reads a file, failing the test when it cannot.
func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// contains reports whether list holds want.
func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
