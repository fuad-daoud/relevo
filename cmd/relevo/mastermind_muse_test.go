package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// TestMasterMindInitHookMuseUsesMuseWording pins the muse init path: a good
// payload registers kind muse, answers with the muse no-env wording, and
// never appends to $CLAUDE_ENV_FILE even when it is set.
func TestMasterMindInitHookMuseUsesMuseWording(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("MUSE_SESSION_ID", "")
	envFile := filepath.Join(t.TempDir(), "claude-env")
	t.Setenv("CLAUDE_ENV_FILE", envFile)
	t.Setenv("CLAUDE_CODE_AGENT", "")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","source":"startup","session_id":"11111111-1111-1111-1111-111111111111","cwd":%q}`, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}

	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	ctx := env.HookSpecificOutput.AdditionalContext
	if !strings.Contains(ctx, "You are relevo MasterMind muse-1 (mm_") {
		t.Errorf("additionalContext = %q, want it to name the muse mastermind", ctx)
	}
	if !strings.Contains(ctx, "MUSE_SESSION_ID") {
		t.Errorf("additionalContext = %q, want the muse session note", ctx)
	}
	if strings.Contains(ctx, "host process") {
		t.Errorf("additionalContext = %q, must not resolve through the host process", ctx)
	}
	if !strings.HasSuffix(ctx, mastermind.Guide()) {
		t.Error("additionalContext does not end with the guide")
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Errorf("muse init touched $CLAUDE_ENV_FILE: stat err = %v", err)
	}

	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 {
		t.Fatalf("registry holds %d records, want exactly 1", len(recs))
	}
	rec := recs[0]
	if rec.HarnessKind != "muse" || rec.SessionID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("record = %+v, want kind muse and the payload session", rec)
	}
	if rec.Name != "muse-1" {
		t.Errorf("Name = %q, want muse-1", rec.Name)
	}
	if rec.HostPID != os.Getppid() {
		t.Errorf("HostPID = %d, want the hook's parent pid %d", rec.HostPID, os.Getppid())
	}
	if told, ok := mastermindSessionToldAt(t, state, "muse", "11111111-1111-1111-1111-111111111111"); !ok || !strings.HasPrefix(told, "mastermind:") {
		t.Errorf("told = (%q, %v), want the mastermind baseline for kind muse", told, ok)
	}
}

// TestMasterMindInitHookMuseSilentInRunner pins that a runner stays silent
// for --hook muse too: the empty object, no record, no told baseline and
// nothing to $CLAUDE_ENV_FILE.
func TestMasterMindInitHookMuseSilentInRunner(t *testing.T) {
	t.Setenv("RELEVO_RUNNER", "api")

	cases := []struct {
		name  string
		state db.Consent
	}{{"unset repo", db.ConsentUnset}, {"yes repo", db.ConsentYes}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("MUSE_SESSION_ID", "")
			envFile := filepath.Join(t.TempDir(), "claude-env")
			t.Setenv("CLAUDE_ENV_FILE", envFile)
			repo := mastermindConsentRepo(t, state, tc.state)

			payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"sess_runner","cwd":%q}`, repo)
			for _, in := range []string{payload, "not json at all"} {
				stdout, _, err := runWithStdin(t, in, "mastermind", "init", "--hook", "muse")
				if err != nil {
					t.Fatalf("init(%q) = %v, want exit 0", in, err)
				}
				if got := strings.TrimSpace(string(stdout)); got != "{}" {
					t.Errorf("init(%q) stdout = %q, want {}", in, got)
				}
			}
			if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
				t.Errorf("registry holds %d records, want none", len(recs))
			}
			if _, ok := mastermindSessionToldAt(t, state, "muse", "sess_runner"); ok {
				t.Error("a runner must get no told baseline")
			}
			if _, err := os.Stat(envFile); !os.IsNotExist(err) {
				t.Errorf("$CLAUDE_ENV_FILE was written for a runner: stat err = %v", err)
			}
		})
	}
}

// TestMasterMindNoticeHookMuseSilentInRunner pins that the UserPromptSubmit
// hook stays silent for a runner under --hook muse: the empty object, no
// record, no told baseline and no $CLAUDE_ENV_FILE write.
func TestMasterMindNoticeHookMuseSilentInRunner(t *testing.T) {
	t.Setenv("RELEVO_RUNNER", "api")

	cases := []struct {
		name  string
		state db.Consent
	}{{"unset repo", db.ConsentUnset}, {"yes repo", db.ConsentYes}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("MUSE_SESSION_ID", "")
			envFile := filepath.Join(t.TempDir(), "claude-env")
			t.Setenv("CLAUDE_ENV_FILE", envFile)
			repo := mastermindConsentRepo(t, state, tc.state)

			payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"sess_runner","cwd":%q}`, repo)
			stdout, _, err := runWithStdin(t, payload, "mastermind", "notice", "--hook", "muse")
			if err != nil {
				t.Fatalf("notice = %v, want exit 0", err)
			}
			if got := strings.TrimSpace(string(stdout)); got != "{}" {
				t.Errorf("notice stdout = %q, want {}", got)
			}
			if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
				t.Errorf("registry holds %d records, want none", len(recs))
			}
			if _, ok := mastermindSessionToldAt(t, state, "muse", "sess_runner"); ok {
				t.Error("a runner must get no told baseline")
			}
			if _, err := os.Stat(envFile); !os.IsNotExist(err) {
				t.Errorf("$CLAUDE_ENV_FILE was written for a runner: stat err = %v", err)
			}
		})
	}
}

// TestMasterMindNoticeHookMuse pins the muse UserPromptSubmit notice: no
// change is the empty object, a grant from elsewhere carries the muse wording,
// a rename is the rename line, and a fresh baseline is written silently.
func TestMasterMindNoticeHookMuse(t *testing.T) {
	runNotice := func(t *testing.T, payload string) hookEnvelope {
		t.Helper()
		stdout, _, err := runWithStdin(t, payload, "mastermind", "notice", "--hook", "muse")
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
		t.Setenv("MUSE_SESSION_ID", "")
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"22222222-2222-2222-2222-222222222222","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse"); err != nil {
			t.Fatalf("init hook = %v", err)
		}

		stdout, _, err := runWithStdin(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"22222222-2222-2222-2222-222222222222","cwd":%q}`, repo), "mastermind", "notice", "--hook", "muse")
		if err != nil {
			t.Fatalf("notice = %v", err)
		}
		if got := strings.TrimSpace(string(stdout)); got != "{}" {
			t.Errorf("notice stdout = %q, want {}", got)
		}
	})

	t.Run("a grant from elsewhere carries the muse wording", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("MUSE_SESSION_ID", "")
		envFile := filepath.Join(t.TempDir(), "claude-env")
		t.Setenv("CLAUDE_ENV_FILE", envFile)
		repo := mastermindConsentRepo(t, state, db.ConsentUnset)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"33333333-3333-3333-3333-333333333333","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse"); err != nil {
			t.Fatalf("init hook = %v", err)
		}
		if told, ok := mastermindSessionToldAt(t, state, "muse", "33333333-3333-3333-3333-333333333333"); !ok || told != mastermind.StatusAsk {
			t.Fatalf("baseline = (%q, %v), want the ask token", told, ok)
		}

		// Another terminal answers yes for the repository.
		mastermindSetRepoConsent(t, state, repo, db.ConsentYes)

		env := runNotice(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"33333333-3333-3333-3333-333333333333","cwd":%q}`, repo))
		if env.HookSpecificOutput.HookEventName != "UserPromptSubmit" {
			t.Errorf("hookEventName = %q, want UserPromptSubmit", env.HookSpecificOutput.HookEventName)
		}
		ctx := env.HookSpecificOutput.AdditionalContext
		if !strings.Contains(ctx, "You are relevo MasterMind") {
			t.Errorf("additionalContext = %q, want the identity sentence", ctx)
		}
		if !strings.Contains(ctx, "MUSE_SESSION_ID") {
			t.Errorf("additionalContext = %q, want the muse session note even with $CLAUDE_ENV_FILE set", ctx)
		}
		told, ok := mastermindSessionToldAt(t, state, "muse", "33333333-3333-3333-3333-333333333333")
		if !ok || !strings.HasPrefix(told, "mastermind:") {
			t.Errorf("told = (%q, %v), want the mastermind token", told, ok)
		}
	})

	t.Run("a rename is the rename line", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		t.Setenv("MUSE_SESSION_ID", "")
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)
		payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"44444444-4444-4444-4444-444444444444","cwd":%q}`, repo)
		if _, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse"); err != nil {
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

		env := runNotice(t, fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"44444444-4444-4444-4444-444444444444","cwd":%q}`, repo))
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
		t.Setenv("MUSE_SESSION_ID", "")
		t.Setenv("CLAUDE_ENV_FILE", "")
		repo := mastermindConsentRepo(t, state, db.ConsentYes)

		// No SessionStart hook ran, so no baseline exists.
		payload := fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"55555555-5555-5555-5555-555555555555","cwd":%q}`, repo)
		if env := runNotice(t, payload); env.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("additionalContext = %q, want nothing on a fresh baseline", env.HookSpecificOutput.AdditionalContext)
		}
		told, ok := mastermindSessionToldAt(t, state, "muse", "55555555-5555-5555-5555-555555555555")
		if !ok || !strings.HasPrefix(told, "mastermind:") {
			t.Errorf("baseline = (%q, %v), want the mastermind token", told, ok)
		}
		// And the next notice is still silent.
		if env := runNotice(t, payload); env.HookSpecificOutput.AdditionalContext != "" {
			t.Errorf("second notice additionalContext = %q, want nothing", env.HookSpecificOutput.AdditionalContext)
		}
	})
}

// TestMasterMindInitRejectsUnknownHook pins that init refuses any hook but
// the two it knows, naming both values.
func TestMasterMindInitRejectsUnknownHook(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("MUSE_SESSION_ID", "")
	_, _, runErr := captureOutput(t, func() error {
		return run([]string{"mastermind", "init", "--hook", "other"})
	})
	var ec exitCodeErr
	if !errors.As(runErr, &ec) || ec.code != 2 {
		t.Fatalf("init --hook other = %v, want exit code 2", runErr)
	}
	if !strings.Contains(runErr.Error(), `supports only "claude" and "muse"`) {
		t.Errorf("error = %q, want both hook values named", runErr.Error())
	}
}

// TestMasterMindInitHookMuseRegistersFromAnEmptyStdin pins the fallback a
// shell that read the hook's stdin needs: with no payload, --hook muse inside
// a muse plugin registers the session MUSE_SESSION_ID names in the cwd, while
// --hook claude with the same empty stdin still reports the read failure.
func TestMasterMindInitHookMuseRegistersFromAnEmptyStdin(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CLAUDE_ENV_FILE", "")
	t.Setenv("CLAUDE_CODE_AGENT", "")
	repo := mastermindConsentRepo(t, state, db.ConsentYes)
	t.Chdir(repo)
	const id = "22222222-2222-2222-2222-222222222222"
	t.Setenv("MUSE_SESSION_ID", id)
	t.Setenv("MUSE_PLUGIN_ID", "relevo")
	// The host's environ decides runner silence once MUSE_PLUGIN_ID is set; the
	// test process's parent may itself be a round, so it reads as a plain host.
	origEnviron := museRunnerEnviron
	museRunnerEnviron = func(int) (map[string]string, bool) { return map[string]string{}, true }
	t.Cleanup(func() { museRunnerEnviron = origEnviron })

	stdout, _, err := runWithStdin(t, "", "mastermind", "init", "--hook", "muse")
	if err != nil {
		t.Fatalf("run = %v, want exit 0", err)
	}
	if !strings.Contains(string(stdout), "You are relevo MasterMind muse-1 (mm_") {
		t.Errorf("stdout = %q, want the muse mastermind named", stdout)
	}
	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 || recs[0].HarnessKind != "muse" || recs[0].SessionID != id {
		t.Fatalf("records = %+v, want one muse record for %s", recs, id)
	}

	stdout, _, err = runWithStdin(t, "", "mastermind", "init", "--hook", "claude")
	if err != nil {
		t.Fatalf("claude run = %v, want exit 0", err)
	}
	if !strings.Contains(string(stdout), "read hook payload") {
		t.Errorf("claude stdout = %q, want the read failure note", stdout)
	}
}
