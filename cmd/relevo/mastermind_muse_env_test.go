package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// TestMuseHookXDGFromHost pins the scrubbed-environment copy: an unset root
// is taken from the host, a set one is kept, nothing happens outside a muse
// plugin, and nothing but the two roots ever moves.
func TestMuseHookXDGFromHost(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")

	envOf := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}

	t.Run("unset roots are taken from the host", func(t *testing.T) {
		env := envOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
		host := envOf(map[string]string{"XDG_STATE_HOME": "/host/state", "XDG_CONFIG_HOME": "/host/config"})
		want := map[string]string{"XDG_STATE_HOME": "/host/state", "XDG_CONFIG_HOME": "/host/config"}
		if got := museXDGFromHost(env, host); !mapsEqual(got, want) {
			t.Errorf("museXDGFromHost = %v, want %v", got, want)
		}
	})

	t.Run("a set root is kept", func(t *testing.T) {
		env := envOf(map[string]string{"MUSE_PLUGIN_ID": "p", "XDG_STATE_HOME": "/mine"})
		host := envOf(map[string]string{"XDG_STATE_HOME": "/host/state", "XDG_CONFIG_HOME": "/host/config"})
		want := map[string]string{"XDG_CONFIG_HOME": "/host/config"}
		if got := museXDGFromHost(env, host); !mapsEqual(got, want) {
			t.Errorf("museXDGFromHost = %v, want %v", got, want)
		}
	})

	t.Run("a root the host lacks is left alone", func(t *testing.T) {
		env := envOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
		host := envOf(map[string]string{"XDG_STATE_HOME": "/host/state"})
		want := map[string]string{"XDG_STATE_HOME": "/host/state"}
		if got := museXDGFromHost(env, host); !mapsEqual(got, want) {
			t.Errorf("museXDGFromHost = %v, want %v", got, want)
		}
	})

	t.Run("outside a muse plugin nothing moves", func(t *testing.T) {
		env := envOf(map[string]string{})
		host := envOf(map[string]string{"XDG_STATE_HOME": "/host/state", "XDG_CONFIG_HOME": "/host/config"})
		if got := museXDGFromHost(env, host); len(got) != 0 {
			t.Errorf("museXDGFromHost = %v, want nothing", got)
		}
	})

	t.Run("other variables never move", func(t *testing.T) {
		env := envOf(map[string]string{"MUSE_PLUGIN_ID": "p"})
		host := envOf(map[string]string{"HOME": "/host/home", "PATH": "/host/bin", "RELEVO_RUNNER": "api"})
		if got := museXDGFromHost(env, host); len(got) != 0 {
			t.Errorf("museXDGFromHost = %v, want nothing", got)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

// TestMasterMindInitHookMuseSilentWhenHostIsRunner pins the scrubbed-runner
// rule: a muse hook whose own env lost the marker still answers {} and
// registers nothing when the injected host environ carries it.
func TestMasterMindInitHookMuseSilentWhenHostIsRunner(t *testing.T) {
	oldHost, oldEnviron := museRunnerHost, museRunnerEnviron
	t.Cleanup(func() { museRunnerHost, museRunnerEnviron = oldHost, oldEnviron })
	museRunnerHost = func(int) (int, bool) { return 4242, true }
	museRunnerEnviron = func(int) (map[string]string, bool) {
		return map[string]string{"RELEVO_RUNNER": "api"}, true
	}

	cases := []struct {
		name  string
		state db.Consent
	}{{"unset repo", db.ConsentUnset}, {"yes repo", db.ConsentYes}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			t.Setenv("MUSE_SESSION_ID", "")
			t.Setenv("MUSE_PLUGIN_ID", "p")
			t.Setenv("RELEVO_RUNNER", "")
			t.Setenv("CLAUDE_ENV_FILE", "")
			repo := mastermindConsentRepo(t, state, tc.state)

			payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"77777777-7777-7777-7777-777777777777","cwd":%q}`, repo)
			stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse")
			if err != nil {
				t.Fatalf("init = %v, want exit 0", err)
			}
			if got := strings.TrimSpace(string(stdout)); got != "{}" {
				t.Errorf("init stdout = %q, want {}", got)
			}
			if recs := mastermindRecordsAt(t, state); len(recs) != 0 {
				t.Errorf("registry holds %d records, want none", len(recs))
			}
			if _, ok := mastermindSessionToldAt(t, state, "muse", "77777777-7777-7777-7777-777777777777"); ok {
				t.Error("a runner must get no told baseline")
			}
		})
	}
}

// TestMasterMindInitHookMuseHonoursSessionConsent pins that a session-only
// yes for kind muse registers init --hook muse in a repo with no answer, and
// that the answer belongs to kind muse: mastermindEffective with "claude"
// stays unset for the same session.
func TestMasterMindInitHookMuseHonoursSessionConsent(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")
	t.Setenv("CLAUDE_ENV_FILE", "")
	const session = "66666666-6666-6666-6666-666666666666"
	repo := mastermindConsentRepo(t, state, db.ConsentUnset)
	mastermindSessionConsent(t, state, "muse", session, db.ConsentYes)

	payload := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":%q,"cwd":%q}`, session, repo)
	stdout, _, err := runWithStdin(t, payload, "mastermind", "init", "--hook", "muse")
	if err != nil {
		t.Fatalf("init = %v, want exit 0", err)
	}
	var env hookEnvelope
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &env); err != nil {
		t.Fatalf("stdout is not the hook envelope: %v: %q", err, stdout)
	}
	if !strings.Contains(env.HookSpecificOutput.AdditionalContext, "You are relevo MasterMind") {
		t.Errorf("additionalContext = %q, want the identity sentence", env.HookSpecificOutput.AdditionalContext)
	}
	recs := mastermindRecordsAt(t, state)
	if len(recs) != 1 || recs[0].HarnessKind != "muse" || recs[0].SessionID != session {
		t.Fatalf("records = %+v, want the muse session's one record", recs)
	}

	d := openConsentMachineDB(t, state)
	rt := relevo.Runtime{Git: git.NewClient("git", 10*time.Second, git.DefaultMaxPatchBytes), Now: time.Now}
	if _, _, effective := mastermindEffective(d, rt, repo, "claude", session); effective != mastermind.ConsentUnset {
		t.Errorf("mastermindEffective(claude) = %q, want unset: the session yes belongs to kind muse", effective)
	}
	if _, _, effective := mastermindEffective(d, rt, repo, "muse", session); effective != mastermind.ConsentYes {
		t.Errorf("mastermindEffective(muse) = %q, want yes", effective)
	}
}

// TestMuseEnvironOfReadsProcSelf pins the production environ reader against
// the one pid every Linux test has: it parses this process's own environ.
func TestMuseEnvironOfReadsProcSelf(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "")
	t.Setenv("MUSE_PLUGIN_ID", "")
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc here")
	}

	// /proc/<pid>/environ is the exec-time block, so the probe is a variable
	// set before the test binary started, not one set here.
	env, ok := museEnvironOf(os.Getpid())
	if !ok {
		t.Fatal("museEnvironOf(self) = missing, want this process's environ")
	}
	if want := os.Getenv("PATH"); env["PATH"] != want {
		t.Errorf("own PATH = %q, want %q", env["PATH"], want)
	}
	if _, ok := museEnvironOf(1 << 30); ok {
		t.Error("museEnvironOf(absent pid) = found, want missing")
	}
}
