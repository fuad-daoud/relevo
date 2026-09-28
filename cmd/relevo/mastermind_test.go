package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// mastermindRegistryAt is a registry over the database the state root holds, for
// a test that inspects what a verb wrote without building a runtime.
func mastermindRegistryAt(t *testing.T, state string) *mastermind.DBRegistry {
	t.Helper()
	dir := filepath.Join(state, "relevo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	d, err := db.Open(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &mastermind.DBRegistry{KV: db.TxKV{DB: d}, Root: filepath.Join(dir, "masterminds")}
}

// mastermindConsentRepo makes a git repository, stores an answer for it in the
// state root when it is not unset, and returns the repository directory. A
// test then chdirs there and runs a verb the way a session would.
func mastermindConsentRepo(t *testing.T, state string, c db.Consent) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if c == db.ConsentUnset {
		return dir
	}

	common, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	root := filepath.Join(state, "relevo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", root, err)
	}
	d, err := db.Open(filepath.Join(root, "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.SetRepoConsent(db.Repo{CommonDir: &common}, c, time.Now()); err != nil {
		t.Fatalf("SetRepoConsent: %v", err)
	}
	return dir
}

// mastermindRepoConsent reads the answer stored for repoDir's repository.
func mastermindRepoConsent(t *testing.T, state, repoDir string) db.Consent {
	t.Helper()
	common, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	d, err := db.Open(filepath.Join(state, "relevo", "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	c, err := d.RepoConsent(db.Repo{CommonDir: &common})
	if err != nil {
		t.Fatalf("RepoConsent: %v", err)
	}
	return c
}

// mastermindSetRepoConsent writes an answer for repoDir's repository, the way a
// second terminal would.
func mastermindSetRepoConsent(t *testing.T, state, repoDir string, c db.Consent) {
	t.Helper()
	common, err := filepath.EvalSymlinks(filepath.Join(repoDir, ".git"))
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	d, err := db.Open(filepath.Join(state, "relevo", "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.SetRepoConsent(db.Repo{CommonDir: &common}, c, time.Now()); err != nil {
		t.Fatalf("SetRepoConsent: %v", err)
	}
}

// mastermindRecordsAt lists the registry's records, so a test can pin that a
// gated hook registered nothing.
func mastermindRecordsAt(t *testing.T, state string) []mastermind.Record {
	t.Helper()
	recs, err := mastermindRegistryAt(t, state).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return recs
}

// mastermindSessionConsent writes a session's own answer in the state root's
// database, creating the root the way a verb would.
func mastermindSessionConsent(t *testing.T, state, kind, session string, c db.Consent) {
	t.Helper()
	dir := filepath.Join(state, "relevo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", dir, err)
	}
	d, err := db.Open(filepath.Join(dir, "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.SetSessionConsent(kind, session, c, time.Now()); err != nil {
		t.Fatalf("SetSessionConsent(%s/%s, %q): %v", kind, session, c, err)
	}
}

// mastermindSessionConsentAt reads a session's own answer back.
func mastermindSessionConsentAt(t *testing.T, state, kind, session string) db.Consent {
	t.Helper()
	d, err := db.Open(filepath.Join(state, "relevo", "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	c, err := d.SessionConsent(kind, session)
	if err != nil {
		t.Fatalf("SessionConsent(%s/%s): %v", kind, session, err)
	}
	return c
}

// mastermindSessionToldAt reads the status token a session was last told.
func mastermindSessionToldAt(t *testing.T, state, kind, session string) (string, bool) {
	t.Helper()
	d, err := db.Open(filepath.Join(state, "relevo", "relevo.db"))
	if err != nil {
		t.Fatalf("open relevo.db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	told, ok, err := d.SessionTold(kind, session)
	if err != nil {
		t.Fatalf("SessionTold(%s/%s): %v", kind, session, err)
	}
	return told, ok
}

// stdinFile is a temp file holding payload, rewound and ready to be os.Stdin.
func stdinFile(t *testing.T, payload string) *os.File {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "hook-stdin-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	if _, err := f.WriteString(payload); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("rewind payload: %v", err)
	}
	return f
}

// runWithStdin runs one verb with payload on stdin, capturing both outputs.
func runWithStdin(t *testing.T, payload string, args ...string) (stdout, stderr []byte, err error) {
	t.Helper()

	orig := os.Stdin
	os.Stdin = stdinFile(t, payload)
	defer func() { os.Stdin = orig }()

	return captureOutput(t, func() error { return run(args) })
}

// hookEnvelope is the shape both `relevo mastermind init --hook` answers use.
type hookEnvelope struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// TestMasterMindInitHookAlwaysExitsZero is the hook's contract (§4.4, §6.1): a
// hook failure must never block a Claude Code session, so malformed or
// incomplete stdin still exits 0 and answers on stdout with the note envelope.
func TestMasterMindInitHookAlwaysExitsZero(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Never write to whatever the developer's own CLAUDE_ENV_FILE names.
	t.Setenv("CLAUDE_ENV_FILE", "")

	for _, payload := range []string{
		"not json at all",
		`{"hook_event_name":"SessionStart","session_id":"sess-1"}`, // no cwd
		`{"hook_event_name":"SessionStart","cwd":"/tmp/p"}`,        // no session
	} {
		stdout, stderr, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
		if err != nil {
			t.Fatalf("run(%s) = %v, want exit 0", payload, err)
		}

		var env hookEnvelope
		if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
			t.Fatalf("stdout for %s is not the hook envelope: %v: %q", payload, err, stdout)
		}
		if env.HookSpecificOutput.HookEventName != "SessionStart" {
			t.Errorf("hookEventName = %q, want SessionStart", env.HookSpecificOutput.HookEventName)
		}
		if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "relevo mastermind init failed") {
			t.Errorf("additionalContext for %s = %q, want the failure note", payload, env.HookSpecificOutput.AdditionalContext)
		}
		if len(stderr) == 0 {
			t.Errorf("stderr for %s is empty; the error belongs there too", payload)
		}
	}
}

// TestMasterMindInitHookWritesEnvFile is the hook's happy path: a good payload
// registers a mastermind, appends the export line to $CLAUDE_ENV_FILE, and tells
// the model which mastermind it is.
func TestMasterMindInitHookWritesEnvFile(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	envFile := filepath.Join(t.TempDir(), "claude-env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("CLAUDE_CODE_AGENT", "architect")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-abc","transcript_path":"/tmp/t.jsonl","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	raw, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatalf("read %s: %v", envFile, err)
	}
	line := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(line, "export RELEVO_MASTERMIND=mm_") {
		t.Fatalf("env file holds %q, want an export RELEVO_MASTERMIND=<id> line", line)
	}
	id := strings.TrimPrefix(line, "export RELEVO_MASTERMIND=")

	// The record is in the state root's database, , and the
	// record's name comes from CLAUDE_CODE_AGENT.
	rec, err := mastermindRegistryAt(t, state).Get(id)
	if err != nil {
		t.Fatalf("read record %s: %v", id, err)
	}
	if rec.ID != id || rec.SessionID != "sess-abc" || rec.HarnessKind != "claude" {
		t.Errorf("record = %+v", rec)
	}
	if rec.Name != "architect-1" {
		t.Errorf("Name = %q, want architect-1", rec.Name)
	}
	if rec.TranscriptLocator != "/tmp/t.jsonl" || rec.CWD != repo {
		t.Errorf("record = %+v, want the hook payload's transcript and cwd", rec)
	}
	if rec.HostPID != os.Getppid() {
		t.Errorf("HostPID = %d, want the hook's parent pid %d", rec.HostPID, os.Getppid())
	}

	// And the model is told.
	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "You are relevo MasterMind architect-1 ("+id+")") {
		t.Errorf("additionalContext = %q, want it to name architect-1 (%s)", env.HookSpecificOutput.AdditionalContext, id)
	}
}

// TestMasterMindInitHookWithoutEnvFileSaysSo pins §3.4's rule for an unset
// $CLAUDE_ENV_FILE: `init --hook` still registers and exits 0, and its
// additionalContext says RELEVO_MASTERMIND could not be exported and that relevo
// resolves the session through its host process instead.
func TestMasterMindInitHookWithoutEnvFileSaysSo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_CODE_AGENT", "architect")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","source":"startup","session_id":"sess-noenv","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "You are relevo MasterMind architect-1 (mm_") {
		t.Errorf("additionalContext = %q, want it to name the mastermind", ctx)
	}
	if !strings.Contains(ctx, "RELEVO_MASTERMIND could not be exported ($CLAUDE_ENV_FILE is unset); relevo resolves this session through its host process.") {
		t.Errorf("additionalContext = %q, want the unset-env-file note", ctx)
	}

	// Registration still happened: the state root's database holds one record.
	records, err := mastermindRegistryAt(t, state).List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("registry holds %d records, want exactly 1", len(records))
	}
}

// TestMasterMindInitHookAsksWhenRepoUnanswered pins the unset answer: the hook
// registers nothing and injects the ask-note, so the model puts the question
// to the human.
func TestMasterMindInitHookAsksWhenRepoUnanswered(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	repo := mastermindConsentRepo(t, state, db.ConsentUnset)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-ask","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	if got := env.HookSpecificOutput.AdditionalContext; got != mastermind.AskNote {
		t.Errorf("additionalContext = %q, want the ask-note", got)
	}
	if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
		t.Errorf("registry holds %d records, want none", len(recs))
	}
}

// TestMasterMindInitHookStaysSilentWhenRepoSaysNo pins the no answer: no
// context and no record.
func TestMasterMindInitHookStaysSilentWhenRepoSaysNo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	repo := mastermindConsentRepo(t, state, db.ConsentNo)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-no","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}
	if got := strings.TrimSpace(string(stdout)); got != "{}" {
		t.Errorf("stdout = %q, want {}", got)
	}
	if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
		t.Errorf("registry holds %d records, want none", len(recs))
	}
}

// TestMasterMindInitHookIgnoresANonRepo pins the no-repository rule: there is
// nothing to remember, so the session is left entirely alone.
func TestMasterMindInitHookIgnoresANonRepo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-nowhere","cwd":%q}`, t.TempDir())
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}
	if got := strings.TrimSpace(string(stdout)); got != "{}" {
		t.Errorf("stdout = %q, want {}", got)
	}
	if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
		t.Errorf("registry holds %d records, want none", len(recs))
	}
}

// TestMasterMindInitHookSessionNoHidesAYesRepo pins session precedence: a
// session that answered no stays silent even when the repository answered yes,
// and registers nothing.
func TestMasterMindInitHookSessionNoHidesAYesRepo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)
	mastermindSessionConsent(t, state, "claude", "sess-hides", db.ConsentNo)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-hides","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}
	if got := strings.TrimSpace(string(stdout)); got != "{}" {
		t.Errorf("stdout = %q, want {}", got)
	}
	if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
		t.Errorf("registry holds %d records, want none", len(recs))
	}
	if told, ok := mastermindSessionToldAt(t, state, "claude", "sess-hides"); !ok || told != mastermind.StatusNone {
		t.Errorf("told = (%q, %v), want the silent baseline", told, ok)
	}
}

// TestMasterMindInitHookSessionYesRegistersInAnUnsetRepo pins the other half of
// session precedence: a session that answered yes registers even when the
// repository has not answered.
func TestMasterMindInitHookSessionYesRegistersInAnUnsetRepo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	envFile := filepath.Join(t.TempDir(), "claude-env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	repo := mastermindConsentRepo(t, state, db.ConsentUnset)
	mastermindSessionConsent(t, state, "claude", "sess-own-yes", db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-own-yes","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "You are relevo MasterMind") {
		t.Errorf("additionalContext = %q, want the identity sentence", env.HookSpecificOutput.AdditionalContext)
	}
	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 || recs[0].SessionID != "sess-own-yes" {
		t.Fatalf("records = %+v, want the session's one record", recs)
	}
}

// TestMasterMindInitHookWritesToldBaseline pins that the hook records what it
// just told the session, so the next prompt's notice has something to compare.
func TestMasterMindInitHookWritesToldBaseline(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-baseline","cwd":%q}`, repo)
	if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude"); err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one", recs)
	}
	want := "mastermind:" + recs[0].ID + ":" + recs[0].Name
	if told, ok := mastermindSessionToldAt(t, state, "claude", "sess-baseline"); !ok || told != want {
		t.Errorf("told = (%q, %v), want %q", told, ok, want)
	}

	// An unset answer records the ask token instead.
	unset := mastermindConsentRepo(t, state, db.ConsentUnset)
	payload = fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-ask-baseline","cwd":%q}`, unset)
	if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude"); err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}
	if told, ok := mastermindSessionToldAt(t, state, "claude", "sess-ask-baseline"); !ok || told != mastermind.StatusAsk {
		t.Errorf("told = (%q, %v), want the ask baseline", told, ok)
	}
}

// TestMasterMindEnableWritesConsentAndRegisters pins the yes answer: the repo
// gains the answer and the calling session a record.
func TestMasterMindEnableWritesConsentAndRegisters(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_CODE_AGENT", "architect")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-enable")
	repo := mastermindConsentRepo(t, state, db.ConsentUnset)
	t.Chdir(repo)

	stdout, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "enable", "--repo"}) })
	if err != nil {
		t.Fatalf("enable --repo: %v", err)
	}
	if !strings.Contains(string(stdout), "MasterMind architect-1 (mm_") {
		t.Errorf("enable printed %q, want the registered MasterMind", stdout)
	}
	if !strings.Contains(string(stdout), "will register this repository's sessions") {
		t.Errorf("enable printed %q, want the repo confirmation", stdout)
	}

	if c := mastermindRepoConsent(t, state, repo); c != db.ConsentYes {
		t.Errorf("repo consent = %q, want yes", c)
	}
	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 || recs[0].SessionID != "sess-enable" {
		t.Errorf("records = %+v, want the calling session's one record", recs)
	}
}

// TestMasterMindDisableWritesNo pins the no answer.
func TestMasterMindDisableWritesNo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := mastermindConsentRepo(t, state, db.ConsentYes)
	t.Chdir(repo)

	stdout, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "disable", "--repo"}) })
	if err != nil {
		t.Fatalf("disable --repo: %v", err)
	}
	if !strings.Contains(string(stdout), "will not register") {
		t.Errorf("disable printed %q, want the confirmation line", stdout)
	}
	if c := mastermindRepoConsent(t, state, repo); c != db.ConsentNo {
		t.Errorf("repo consent = %q, want no", c)
	}
}

// TestMasterMindGuideStates pins the verb the opencode plugin reads: the state
// word, the text per answer, and the record an enabled opencode session gets.
func TestMasterMindGuideStates(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	type guideOut struct {
		State string `json:"state"`
		Text  string `json:"text"`
		Repo  string `json:"repo"`
		ID    string `json:"id"`
		Name  string `json:"name"`
	}
	runGuide := func(t *testing.T, cwd string, extra ...string) guideOut {
		t.Helper()
		args := append([]string{"mastermind", "guide", "--json", "--cwd", cwd}, extra...)
		stdout, _, err := captureOutput(t, func() error { return run(args) })
		if err != nil {
			t.Fatalf("guide %v: %v", args, err)
		}
		var out guideOut
		if err := json.Unmarshal(stdout, &out); err != nil {
			t.Fatalf("guide --json output %q: %v", stdout, err)
		}
		return out
	}

	unanswered := mastermindConsentRepo(t, state, db.ConsentUnset)
	if out := runGuide(t, unanswered); out.State != "ask" || out.Text != mastermind.AskNote || out.Repo == "" {
		t.Errorf("unset guide = %+v, want ask with the ask-note", out)
	}

	refused := mastermindConsentRepo(t, state, db.ConsentNo)
	if out := runGuide(t, refused); out.State != "disabled" || out.Text != "" {
		t.Errorf("no guide = %+v, want disabled with no text", out)
	}

	if out := runGuide(t, t.TempDir()); out.State != "disabled" || out.Text != "" {
		t.Errorf("non-repo guide = %+v, want disabled with no text", out)
	}

	enabled := mastermindConsentRepo(t, state, db.ConsentYes)
	out := runGuide(t, enabled, "--kind", "opencode", "--session", "ses_abc123")
	if out.State != "enabled" || !strings.Contains(out.Text, mastermind.Guide()) {
		t.Errorf("enabled guide = %+v, want enabled with the guide", out)
	}
	if out.ID == "" || out.Name != "opencode-1" {
		t.Errorf("enabled guide = %+v, want the created opencode record's id and name", out)
	}
	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 || recs[0].ID != out.ID {
		t.Errorf("records = %+v, want the one the guide created", recs)
	}
}

// TestMasterMindVerbsListRenameForget exercises the explicit registration and the
// three read/manage verbs, including forget's guard: a binding that is not DONE
// still names the record, so it must be refused.
func TestMasterMindVerbsListRenameForget(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"mastermind", "init", "--kind", "opencode", "--session", "ses_abc123"})
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	if len(lines) != 2 {
		t.Fatalf("init printed %d lines, want 2:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "MasterMind opencode-1 (mm_") || !strings.HasSuffix(lines[0], " created") {
		t.Errorf("first line = %q, want \"MasterMind opencode-1 (<id>) created\"", lines[0])
	}
	if !strings.HasPrefix(lines[1], "export RELEVO_MASTERMIND=mm_") {
		t.Fatalf("second line = %q, want the export line", lines[1])
	}
	id := strings.TrimPrefix(lines[1], "export RELEVO_MASTERMIND=")

	if stdout, _, err = captureOutput(t, func() error {
		return run([]string{"mastermind", "rename", id, "reviewer-2"})
	}); err != nil {
		t.Fatalf("rename: %v", err)
	} else if !strings.Contains(string(stdout), "reviewer-2") {
		t.Errorf("rename printed %q, want the new name", stdout)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"mastermind", "list", "--json"}) })
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var records []mastermind.Record
	if err := json.Unmarshal(stdout, &records); err != nil {
		t.Fatalf("list --json is not a records array: %v: %q", err, stdout)
	}
	if len(records) != 1 || records[0].Name != "reviewer-2" || records[0].ID != id {
		t.Fatalf("records = %+v, want one named reviewer-2", records)
	}

	// A tabular list names the mastermind and its session.
	stdout, _, err = captureOutput(t, func() error { return run([]string{"mastermind", "list"}) })
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(string(stdout), "reviewer-2") || !strings.Contains(string(stdout), "ses_abc123") {
		t.Errorf("list printed %q, want the name and the session", stdout)
	}

	// A live binding that names the mastermind refuses the forget.
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	b := store.Binding{Name: "webshop", CWD: t.TempDir(), Round: 1, State: store.StateActive, MasterMindID: id}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "forget", id}) }); !errors.Is(err, mastermind.ErrInUse) {
		t.Fatalf("forget with a live binding = %v, want ErrInUse", err)
	}

	// Once the binding is DONE, the record is free.
	b, err = s.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.State = store.StateDone
	if err := s.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"mastermind", "forget", id}) })
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(string(stdout), "forgot") {
		t.Errorf("forget printed %q, want a confirmation line", stdout)
	}
	if _, err := mastermindRegistryAt(t, state).Get(id); !errors.Is(err, mastermind.ErrNotFound) {
		t.Errorf("record row still there after forget: %v", err)
	}
}

// TestAnnotateMasterMindChat is #386's CLI surface: annotateMasterMindChat fills a
// row's chat label and link from the mastermind record it names, resolves a
// repeated id once, and leaves a row naming an unknown mastermind empty. It writes
// a synthetic claude transcript and names claude records only, so the test
// spawns nothing and reaches no network.
func TestAnnotateMasterMindChat(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	reg := mastermindRegistryAt(t, state)

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{\"type\":\"custom-title\",\"customTitle\":\"my chat\"}\n" +
		"{\"type\":\"bridge-session\",\"bridgeSessionId\":\"cse_01ABCDEF\"}\n"
	if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	const knownID = "pl_aaaaaaaaaaaa"
	if _, err := reg.Create(mastermind.Record{
		ID: knownID, Name: "alpha", HarnessKind: "claude",
		SessionID: "sess-alpha", CWD: t.TempDir(), TranscriptLocator: transcript,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	rt := relevo.Runtime{MasterMinds: reg}
	rep := view.Report{Bindings: []view.BindingStatus{
		{Name: "one", MasterMindID: knownID},
		{Name: "two", MasterMindID: knownID},
		{Name: "three", MasterMindID: "pl_zzzzzzzzzzzz"},
	}}

	annotateMasterMindChat(rt, &rep, chatlabel.Resolver{})

	const wantText = "my chat"
	const wantLink = "https://claude.ai/code/session_01ABCDEF"
	for i := 0; i < 2; i++ {
		if rep.Bindings[i].MasterMindChatLabel != wantText || rep.Bindings[i].MasterMindChatLink != wantLink {
			t.Errorf("row %d carries label %q / link %q, want %q / %q",
				i, rep.Bindings[i].MasterMindChatLabel, rep.Bindings[i].MasterMindChatLink, wantText, wantLink)
		}
	}
	if rep.Bindings[2].MasterMindChatLabel != "" || rep.Bindings[2].MasterMindChatLink != "" {
		t.Errorf("the unknown mastermind's row carries label %q / link %q, want neither",
			rep.Bindings[2].MasterMindChatLabel, rep.Bindings[2].MasterMindChatLink)
	}
}

// TestListAlignsLongValues is the list polish: the old fixed-width format
// shifted every later column when a 36-character session id or a long cwd
// overflowed its cell. Under tabwriter every row -- header included -- starts
// its `seen` column at the same byte offset.
func TestListAlignsLongValues(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	reg := mastermindRegistryAt(t, state)

	long := filepath.Join(t.TempDir(), strings.Repeat("long-cwd-segment-", 6))
	longer := filepath.Join(t.TempDir(), strings.Repeat("an-even-longer-cwd-segment-", 6))
	for _, rec := range []mastermind.Record{
		{
			ID: "pl_aaaaaaaaaaaa", Name: "alpha", HarnessKind: "claude",
			SessionID: "f26cad68-8a43-4de9-80c6-7b13d88aafd0", // 36 characters
			CWD:       long,
		},
		{
			ID: "pl_bbbbbbbbbbbb", Name: "beta", HarnessKind: "claude",
			SessionID: "f26cad68-8a43-4de9-80c6-7b13d88aafd1", // 36 characters
			CWD:       longer,
		},
	} {
		if _, err := reg.Create(rec); err != nil {
			t.Fatalf("Create(%s): %v", rec.ID, err)
		}
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "list"}) })
	if err != nil {
		t.Fatalf("mastermind list: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("mastermind list printed %d lines, want a header and two rows:\n%s", len(lines), stdout)
	}

	// seen is the last column, so its cell is the line's last space-separated
	// token: the byte after the final space. Every line must agree on it.
	want := -1
	for i, line := range lines {
		at := strings.LastIndex(line, " ") + 1
		if at <= 0 {
			t.Fatalf("line %d has no seen column:\n%s", i, line)
		}
		if want < 0 {
			want = at
			continue
		}
		if at != want {
			t.Errorf("line %d's seen column starts at byte %d, want %d:\n%s", i, at, want, line)
		}
	}
}

// TestListShowsChat is the #386 chat column: a claude mastermind's row names the
// chat the way its own harness does, a mastermind with nothing readable shows "-",
// and --json carries the same two fields. It writes a synthetic transcript and
// uses claude records only, so the test spawns nothing and reaches no network.
func TestListShowsChat(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	reg := mastermindRegistryAt(t, state)

	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	content := "{\"type\":\"custom-title\",\"customTitle\":\"my chat\"}\n" +
		"{\"type\":\"bridge-session\",\"bridgeSessionId\":\"cse_01ABCDEF\"}\n"
	if err := os.WriteFile(transcript, []byte(content), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	cwd := t.TempDir()
	for _, rec := range []mastermind.Record{
		{
			ID: "pl_aaaaaaaaaaaa", Name: "alpha", HarnessKind: "claude",
			SessionID: "sess-alpha", CWD: cwd, TranscriptLocator: transcript,
		},
		{
			ID: "pl_bbbbbbbbbbbb", Name: "beta", HarnessKind: "claude",
			SessionID: "sess-beta", CWD: cwd,
		},
	} {
		if _, err := reg.Create(rec); err != nil {
			t.Fatalf("Create(%s): %v", rec.Name, err)
		}
	}

	stdout, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "list"}) })
	if err != nil {
		t.Fatalf("mastermind list: %v", err)
	}

	rows := strings.Split(strings.TrimRight(string(stdout), "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("mastermind list printed %d lines, want a header and two rows:\n%s", len(rows), stdout)
	}
	if !strings.Contains(rows[0], "chat") {
		t.Errorf("header %q does not name the chat column", rows[0])
	}

	rowFor := func(name string) string {
		for _, row := range rows[1:] {
			if strings.HasPrefix(row, name) {
				return row
			}
		}
		t.Fatalf("no row for %s in:\n%s", name, stdout)
		return ""
	}

	wantChat := "my chat · https://claude.ai/code/session_01ABCDEF"
	if got := rowFor("alpha"); !strings.Contains(got, wantChat) {
		t.Errorf("alpha's row %q does not contain %q", got, wantChat)
	}
	beta := rowFor("beta")
	if fields := strings.Fields(beta); len(fields) < 2 || fields[1] != "-" {
		t.Errorf("beta's chat cell is not \"-\" in row %q", beta)
	}

	stdout, _, err = captureOutput(t, func() error { return run([]string{"mastermind", "list", "--json"}) })
	if err != nil {
		t.Fatalf("mastermind list --json: %v", err)
	}
	var views []map[string]any
	if err := json.Unmarshal(stdout, &views); err != nil {
		t.Fatalf("decode --json: %v\n%s", err, stdout)
	}
	if len(views) != 2 {
		t.Fatalf("--json printed %d records, want 2", len(views))
	}
	if got := views[0]["chat_label"]; got != "my chat" {
		t.Errorf("alpha chat_label = %v, want %q", got, "my chat")
	}
	if got := views[0]["chat_link"]; got != "https://claude.ai/code/session_01ABCDEF" {
		t.Errorf("alpha chat_link = %v, want the claude.ai url", got)
	}
	if _, ok := views[1]["chat_label"]; ok {
		t.Errorf("beta should carry no chat_label, got %v", views[1]["chat_label"])
	}
	if _, ok := views[1]["chat_link"]; ok {
		t.Errorf("beta should carry no chat_link, got %v", views[1]["chat_link"])
	}
}

// TestMasterMindPruneWasRemoved pins §4.4: pruning is automatic now, so the verb
// exits 2 naming the daemon's hourly prune.
func TestMasterMindPruneWasRemoved(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"mastermind", "prune"})
	})
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("run = %v, want exit code 2", runErr)
	}
	if !strings.Contains(string(stderr), "the daemon prunes dead MasterMinds hourly") {
		t.Errorf("stderr = %q, want it to name the daemon's hourly prune", stderr)
	}
}

// TestPlannerVerbIsRemoved pins D5: `relevo planner ...` names its replacement
// and exits 2 through removedVerbs, byte for byte like every other removed verb.
func TestPlannerVerbIsRemoved(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"planner", "list"})
	})
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("relevo planner list = %v, want exit code 2", runErr)
	}
	if !strings.Contains(string(stderr), `"planner" was removed; use relevo mastermind`) {
		t.Errorf("stderr = %q, want the removed-verb message", stderr)
	}
}

// TestRemovedPlannerFlagsAreUnknown pins D5's clean break: --planner and
// --all-planners are removed, not aliased, so parsing one fails with the flag
// package's own "flag provided but not defined" error, before any harness runs.
func TestRemovedPlannerFlagsAreUnknown(t *testing.T) {
	for _, args := range [][]string{
		{"bind", "--planner", "x"},
		{"unbind", "--done", "--all-planners"},
	} {
		err := run(args)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Errorf("%v: got %v, want an unknown-flag error", args, err)
		}
	}
}

// TestMasterMindResetClearsConsent pins the third answer: reset returns the
// repository to unset, so the next session asks again.
func TestMasterMindResetClearsConsent(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := mastermindConsentRepo(t, state, db.ConsentYes)
	t.Chdir(repo)

	stdout, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "reset"}) })
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if !strings.Contains(string(stdout), "ask about this repository again") {
		t.Errorf("reset printed %q, want the ask-again confirmation", stdout)
	}
	if c := mastermindRepoConsent(t, state, repo); c != db.ConsentUnset {
		t.Errorf("repo consent after reset = %q, want unset", c)
	}
}

// TestMasterMindGuideSeesThisSessionsRecord pins the session-only answer: with
// a record but no repository answer, guide reports ask plus the record, and
// the text briefs the session instead of asking again.
func TestMasterMindGuideSeesThisSessionsRecord(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	repo := mastermindConsentRepo(t, state, db.ConsentUnset)

	rec, err := mastermindRegistryAt(t, state).Create(mastermind.Record{
		ID: "mm_bbbbbbbbbbbb", Name: "opencode-9", HarnessKind: "opencode",
		SessionID: "ses_g1", CWD: repo,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"mastermind", "guide", "--json", "--cwd", repo, "--kind", "opencode", "--session", "ses_g1"})
	})
	if err != nil {
		t.Fatalf("guide: %v", err)
	}
	var out struct {
		State string `json:"state"`
		Text  string `json:"text"`
		ID    string `json:"id"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(stdout, &out); err != nil {
		t.Fatalf("guide --json output %q: %v", stdout, err)
	}
	if out.State != "ask" || out.ID != rec.ID || out.Name != rec.Name {
		t.Errorf("guide = %+v, want ask with the session record", out)
	}
	if !strings.HasPrefix(out.Text, "You are relevo MasterMind opencode-9") {
		t.Errorf("text = %q, want the identity sentence", out.Text)
	}
}

// TestMasterMindSessionPrecedence pins the four cases the session answer
// governs: a session no hides a yes repo, a session yes enables in a no repo,
// reset clears both answers, and --repo clears the caller's own answer.
func TestMasterMindSessionPrecedence(t *testing.T) {
	t.Run("a session no in a yes repo reports disabled and creates no record", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		mastermindSessionConsent(t, state, "opencode", "ses_no", db.ConsentNo)

		stdout, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "guide", "--json", "--cwd", repo, "--kind", "opencode", "--session", "ses_no"})
		})
		if err != nil {
			t.Fatalf("guide: %v", err)
		}
		var out struct {
			State string `json:"state"`
			Text  string `json:"text"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(stdout, &out); err != nil {
			t.Fatalf("guide --json output %q: %v", stdout, err)
		}
		if out.State != "disabled" || out.Text != "" || out.ID != "" {
			t.Errorf("guide = %+v, want disabled with no text and no record", out)
		}
		if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
			t.Errorf("records = %+v, want none created for a session no", recs)
		}
	})

	t.Run("a session yes in a no repo reports enabled", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		repo := mastermindConsentRepo(t, state, db.ConsentNo)
		mastermindSessionConsent(t, state, "opencode", "ses_yes", db.ConsentYes)

		stdout, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "guide", "--json", "--cwd", repo, "--kind", "opencode", "--session", "ses_yes"})
		})
		if err != nil {
			t.Fatalf("guide: %v", err)
		}
		var out struct {
			State string `json:"state"`
			Text  string `json:"text"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(stdout, &out); err != nil {
			t.Fatalf("guide --json output %q: %v", stdout, err)
		}
		if out.State != "enabled" || !strings.Contains(out.Text, mastermind.Guide()) || out.ID == "" {
			t.Errorf("guide = %+v, want enabled with the guide and a record", out)
		}
	})

	t.Run("reset clears the repository and the session answer", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		mastermindSessionConsent(t, state, "opencode", "ses_reset", db.ConsentNo)
		t.Chdir(repo)

		if _, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "reset", "--kind", "opencode", "--session", "ses_reset"})
		}); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if c := mastermindRepoConsent(t, state, repo); c != db.ConsentUnset {
			t.Errorf("repo consent after reset = %q, want unset", c)
		}
		if c := mastermindSessionConsentAt(t, state, "opencode", "ses_reset"); c != db.ConsentUnset {
			t.Errorf("session consent after reset = %q, want unset", c)
		}
	})

	t.Run("reset outside a repo clears only the session", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		nowhere := t.TempDir()
		mastermindSessionConsent(t, state, "opencode", "ses_outside", db.ConsentNo)
		t.Chdir(nowhere)

		stdout, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "reset", "--kind", "opencode", "--session", "ses_outside"})
		})
		if err != nil {
			t.Fatalf("reset outside a repo: %v", err)
		}
		if !strings.Contains(string(stdout), "ask this session") {
			t.Errorf("reset printed %q, want the session-only confirmation", stdout)
		}
		if c := mastermindSessionConsentAt(t, state, "opencode", "ses_outside"); c != db.ConsentUnset {
			t.Errorf("session consent after reset = %q, want unset", c)
		}

		// Neither a repo nor a session is the only error.
		if _, _, err := captureOutput(t, func() error { return run([]string{"mastermind", "reset"}) }); err == nil {
			t.Error("reset with neither a repo nor a session must error")
		}
	})

	t.Run("--repo clears the caller's own answer", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		t.Setenv("CLAUDE_CODE_AGENT", "architect")
		t.Setenv("CLAUDECODE", "1")
		t.Setenv("CLAUDE_CODE_SESSION_ID", "sess_repo")
		repo := mastermindConsentRepo(t, state, db.ConsentUnset)
		mastermindSessionConsent(t, state, "claude", "sess_repo", db.ConsentNo)
		t.Chdir(repo)

		if _, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "enable", "--repo"})
		}); err != nil {
			t.Fatalf("enable --repo: %v", err)
		}
		if c := mastermindRepoConsent(t, state, repo); c != db.ConsentYes {
			t.Errorf("repo consent = %q, want yes", c)
		}
		if c := mastermindSessionConsentAt(t, state, "claude", "sess_repo"); c != db.ConsentUnset {
			t.Errorf("caller's session answer = %q, want cleared", c)
		}
	})
}

// TestMasterMindNoticeHook pins the UserPromptSubmit notice: no change is the
// empty object, an enable from elsewhere carries the notice with its own event
// name and updates the baseline, a rename is the rename line, a NULL baseline
// is written silently, and a broken payload still exits 0 with `{}`.
func TestMasterMindNoticeHook(t *testing.T) {
	runNotice := func(t *testing.T, payload string) hookEnvelope {
		t.Helper()
		stdout, _, err := runWithStdin(t, payload, "mastermind", "notice", "--hook", "claude")
		if err != nil {
			t.Fatalf("notice = %v, want exit 0", err)
		}
		var env hookEnvelope
		if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
			t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
		}
		return env
	}

	t.Run("no change is the empty object", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-notice","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude"); err != nil {
			t.Fatalf("init hook = %v", err)
		}

		stdout, _, err := runWithStdin(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-notice","cwd":%q}`, repo), "mastermind", "notice", "--hook", "claude")
		if err != nil {
			t.Fatalf("notice = %v", err)
		}
		if got := strings.TrimSpace(string(stdout)); got != "{}" {
			t.Errorf("notice stdout = %q, want {}", got)
		}
	})

	t.Run("an enable from elsewhere carries the notice", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentUnset)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-grant","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude"); err != nil {
			t.Fatalf("init hook = %v", err)
		}
		if told, ok := mastermindSessionToldAt(t, state, "claude", "sess-grant"); !ok || told != mastermind.StatusAsk {
			t.Fatalf("baseline = (%q, %v), want the ask token", told, ok)
		}

		// Another terminal answers yes for the repository.
		if _, err := mastermindRegistryAt(t, state).List(); err != nil {
			t.Fatalf("registry: %v", err)
		}
		mastermindSetRepoConsent(t, state, repo, db.ConsentYes)

		env := runNotice(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-grant","cwd":%q}`, repo))
		if env.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
			t.Errorf("hookEventName = %q, want UserPromptSubmit", env.HookSpecificOutput.HookEventName)
		}
		if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "You are relevo MasterMind") {
			t.Errorf("additionalContext = %q, want the identity sentence", env.HookSpecificOutput.AdditionalContext)
		}
		told, ok := mastermindSessionToldAt(t, state, "claude", "sess-grant")
		if !ok || !strings.HasPrefix(told, "mastermind:") {
			t.Errorf("told = (%q, %v), want the mastermind token", told, ok)
		}
	})

	t.Run("a rename is the rename line", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess-rename","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "claude"); err != nil {
			t.Fatalf("init hook = %v", err)
		}
		recs := mastermindRecordsAt(t, state)
		if len(recs) != 1 {
			t.Fatalf("records = %+v, want one", recs)
		}
		if _, _, err := captureOutput(t, func() error {
			return run([]string{"mastermind", "rename", recs[0].ID, "reviewer-9"})
		}); err != nil {
			t.Fatalf("rename: %v", err)
		}

		env := runNotice(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-rename","cwd":%q}`, repo))
		ctx := env.HookSpecificOutput.AdditionalContext
		if !strings.Contains(ctx, "renamed") && !strings.Contains(ctx, "now named") || !strings.Contains(ctx, "reviewer-9") {
			t.Errorf("additionalContext = %q, want the one-line rename notice", ctx)
		}
		if strings.Contains(ctx, "\n") {
			t.Errorf("additionalContext = %q, want one line", ctx)
		}
	})

	t.Run("a NULL baseline is written and stays silent", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)

		// No SessionStart hook ran, so no baseline exists.
		payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-fresh","cwd":%q}`, repo)
		if env := runNotice(t, payload); env.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("additionalContext = %q, want nothing on a fresh baseline", env.HookSpecificOutput.AdditionalContext)
		}
		told, ok := mastermindSessionToldAt(t, state, "claude", "sess-fresh")
		if !ok || !strings.HasPrefix(told, "mastermind:") {
			t.Errorf("baseline = (%q, %v), want the mastermind token", told, ok)
		}
		// And the next notice is still silent.
		if env := runNotice(t, payload); env.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("second notice additionalContext = %q, want nothing", env.HookSpecificOutput.AdditionalContext)
		}
	})

	t.Run("a broken payload exits 0 with the empty object", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		for _, payload := range []string{"not json at all", `{"session_id":"s"}`, `{"hook_event_name":"UserPromptSubmit","cwd":"/tmp"}`} {
			stdout, stderr, err := runWithStdin(t, payload, "mastermind", "notice", "--hook", "claude")
			if err != nil {
				t.Fatalf("notice(%s) = %v, want exit 0", payload, err)
			}
			if got := strings.TrimSpace(string(stdout)); got != "{}" {
				t.Errorf("notice(%s) stdout = %q, want {}", payload, got)
			}
			if len(stderr) == 0 {
				t.Errorf("notice(%s) stderr is empty; the error belongs there too", payload)
			}
		}
	})

	t.Run("nothing outside a repo without a session answer", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("CLAUDE_ENV_FILE", "")
		payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-nowhere","cwd":%q}`, t.TempDir())
		if env := runNotice(t, payload); env.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("additionalContext = %q, want nothing outside a repo", env.HookSpecificOutput.AdditionalContext)
		}
		if _, ok := mastermindSessionToldAt(t, state, "claude", "sess-nowhere"); ok {
			t.Error("a session outside a repo must not get a baseline row")
		}
	})
}

// TestMasterMindNoticeRequiresClaude pins that the verb refuses any other hook.
func TestMasterMindNoticeRequiresClaude(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	_, stderr, runErr := captureOutput(t, func() error {
		return run([]string{"mastermind", "notice", "--hook", "other"})
	})
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("notice --hook other = %v, want exit code 2", runErr)
	}
	if !strings.Contains(string(stderr), `supports only "claude"`) {
		t.Errorf("stderr = %q, want the claude-only message", stderr)
	}
}

// TestMasterMindDisableBySession pins the explicit pair a plugin passes:
// (kind, session) names the session to answer no for. A record is forgotten
// when one exists; a pair naming no record still records the answer, because a
// session that never registered can still say never.
func TestMasterMindDisableBySession(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	reg := mastermindRegistryAt(t, state)
	rec, err := reg.Create(mastermind.Record{
		ID: "mm_cccccccccccc", Name: "opencode-3", HarnessKind: "opencode",
		SessionID: "ses_d1", CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stdout, _, err := captureOutput(t, func() error {
		return run([]string{"mastermind", "disable", "--kind", "opencode", "--session", "ses_d1"})
	})
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if !strings.Contains(string(stdout), "forgot") {
		t.Errorf("disable printed %q, want a confirmation line", stdout)
	}
	if _, err := reg.Get(rec.ID); !errors.Is(err, mastermind.ErrNotFound) {
		t.Errorf("record still there after disable: %v", err)
	}
	if c := mastermindSessionConsentAt(t, state, "opencode", "ses_d1"); c != db.ConsentNo {
		t.Errorf("session answer = %q, want no", c)
	}

	// A pair naming no record is not an error: the answer is still recorded.
	stdout, _, err = captureOutput(t, func() error {
		return run([]string{"mastermind", "disable", "--kind", "opencode", "--session", "ses_missing"})
	})
	if err != nil {
		t.Fatalf("disable with an unknown session = %v, want a recorded answer", err)
	}
	if !strings.Contains(string(stdout), "no record to forget") {
		t.Errorf("disable printed %q, want the recorded-answer line", stdout)
	}
	if c := mastermindSessionConsentAt(t, state, "opencode", "ses_missing"); c != db.ConsentNo {
		t.Errorf("session answer = %q, want no", c)
	}
}
