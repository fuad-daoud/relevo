package setup

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestProbeMarksLoggedOutHarnessWithLoginCommand pins the logout signal every
// wizard harness carries: on PATH, not logged in, and the login command the
// notes must name -- including agy's run-once text for its absence of a
// login verb.
func TestProbeMarksLoggedOutHarnessWithLoginCommand(t *testing.T) {
	env := probeEnv{
		pathEnv: pathEnv{onPath: map[string]bool{"claude": true, "codex": true, "agy": true}},
		run: func(name string, args ...string) ([]byte, int, error) {
			if name == "claude" {
				return []byte(`{"loggedIn": false}`), 0, nil
			}
			return []byte("not logged in"), 1, nil
		},
	}
	inv := Probe(env)
	if want := []string{"agy", "claude", "codex"}; !reflect.DeepEqual(harnessKinds(inv), want) {
		t.Fatalf("probed kinds = %v, want %v", harnessKinds(inv), want)
	}
	for _, tt := range []struct {
		kind, login string
	}{
		{"claude", "claude auth login"},
		{"codex", "codex login"},
		{"agy", "run agy once to sign in"},
	} {
		st, ok := statusOf(inv, tt.kind)
		if !ok {
			t.Fatalf("no status for %s: %v", tt.kind, inv.Harnesses)
		}
		if st.LoggedIn {
			t.Errorf("%s = logged in, want logged out", tt.kind)
		}
		if st.Login != tt.login {
			t.Errorf("%s login text = %q, want %q", tt.kind, st.Login, tt.login)
		}
		if len(st.Models) != 0 {
			t.Errorf("%s models = %v, want none while logged out", tt.kind, st.Models)
		}
	}
}

// TestProbeParsesCodexModelsByPriority pins the codex offer: only "list"
// entries, ordered by ascending priority.
func TestProbeParsesCodexModelsByPriority(t *testing.T) {
	env := probeEnv{
		pathEnv: pathEnv{onPath: map[string]bool{"codex": true}},
		run: func(name string, args ...string) ([]byte, int, error) {
			switch callKey(name, args...) {
			case "codex login status":
				return nil, 0, nil
			case "codex debug models":
				return []byte(`[
					{"slug": "gpt-5.6-terra", "visibility": "list", "priority": 2},
					{"slug": "o9-secret", "visibility": "hidden", "priority": 1},
					{"slug": "gpt-6-luna", "visibility": "list", "priority": 5}
				]`), 0, nil
			}
			return nil, -1, errProbeTimeout
		},
	}
	st, ok := statusOf(Probe(env), "codex")
	if !ok || !st.LoggedIn {
		t.Fatalf("codex status = %+v (ok %v), want logged in", st, ok)
	}
	if want := []string{"gpt-5.6-terra", "gpt-6-luna"}; !reflect.DeepEqual(st.Models, want) {
		t.Errorf("codex models = %v, want %v (visible, ascending priority)", st.Models, want)
	}
}

// TestProbeParsesAgyModelsAfterFetchingLine pins the agy offer: the first
// tab-separated field per line, after any Fetching progress line and no
// others.
func TestProbeParsesAgyModelsAfterFetchingLine(t *testing.T) {
	env := probeEnv{
		pathEnv: pathEnv{onPath: map[string]bool{"agy": true}},
		run: func(string, ...string) ([]byte, int, error) {
			return []byte("Fetching models...\t\ngemini-3.8-flash-high\tcheapest usage\ngemini-3.7-pro\tpremium\n"), 0, nil
		},
	}
	st, ok := statusOf(Probe(env), "agy")
	if !ok || !st.LoggedIn {
		t.Fatalf("agy status = %+v (ok %v), want logged in through the models command", st, ok)
	}
	if want := []string{"gemini-3.8-flash-high", "gemini-3.7-pro"}; !reflect.DeepEqual(st.Models, want) {
		t.Errorf("agy models = %v, want %v", st.Models, want)
	}
}

// TestProbeParsesClaudeLoggedInJSON pins claude's login signal on its status
// JSON alone: the parsed flag, a false flag, and output that is not the JSON
// at all.
func TestProbeParsesClaudeLoggedInJSON(t *testing.T) {
	for _, tt := range []struct {
		name, stdout string
		want         bool
	}{
		{"true", `{"loggedIn": true}`, true},
		{"false", `{"loggedIn": false}`, false},
		{"malformed", `usage: claude auth status`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := probeEnv{
				pathEnv: pathEnv{onPath: map[string]bool{"claude": true}},
				run: func(string, ...string) ([]byte, int, error) {
					return []byte(tt.stdout), 0, nil
				},
			}
			st, ok := statusOf(Probe(env), "claude")
			if !ok {
				t.Fatal("no claude status")
			}
			if st.LoggedIn != tt.want {
				t.Errorf("claude loggedIn = %v, want %v", st.LoggedIn, tt.want)
			}
			if st.LoggedIn {
				if want := []string{"sonnet", "opus", "haiku"}; !reflect.DeepEqual(st.Models, want) {
					t.Errorf("claude models = %v, want the fixed aliases %v", st.Models, want)
				}
			}
		})
	}
}

// TestProbeHungCommandTimesOut pins the probe deadline: a runner that blocks
// must find Probe logged-out and fast, on the test's own clock.
func TestProbeHungCommandTimesOut(t *testing.T) {
	start := time.Now()
	env := probeEnv{
		pathEnv: pathEnv{onPath: map[string]bool{"claude": true}},
		probeT:  50 * time.Millisecond,
		run: func(name string, args ...string) ([]byte, int, error) {
			<-time.After(10 * time.Second)
			return []byte(`{"loggedIn": true}`), 0, nil
		},
	}
	st, ok := statusOf(Probe(env), "claude")
	if !ok {
		t.Fatal("no claude status after the timeout")
	}
	if st.LoggedIn {
		t.Errorf("claude = logged in, want logged out after the probe timed out")
	}
	if el := time.Since(start); el > probeDeadline {
		t.Errorf("Probe took %s, want to return well before the default %s deadline", el, probeDeadline)
	}
}

// TestProbeNoHarnessKeepsExistingMessage pins the empty probe's shape: no
// status at all, and Build refusing with the same nothing-on-PATH text the
// other paths print.
func TestProbeNoHarnessKeepsExistingMessage(t *testing.T) {
	env := probeEnv{pathEnv: pathEnv{onPath: map[string]bool{}}}
	inv := Probe(env)
	if len(inv.Harnesses) != 0 || len(inv.Notes) != 0 {
		t.Fatalf("empty probe = %+v, want nothing", inv)
	}
	_, err := Build(inv, RecommendedAnswers(inv, nil))
	if err == nil || err.Error() != "no harness binaries on PATH (agy, claude, codex, opencode); install one first" {
		t.Fatalf("Build error = %v, want the existing no-binaries text", err)
	}
}

// statusOf fetches one kind's status from an inventory.
func statusOf(inv Inventory, kind string) (HarnessStatus, bool) {
	for _, s := range inv.Harnesses {
		if s.Harness == kind {
			return s, true
		}
	}
	return HarnessStatus{}, false
}

// harnessKinds lists an inventory's kinds in probe order.
func harnessKinds(inv Inventory) []string {
	kinds := make([]string, 0, len(inv.Harnesses))
	for _, s := range inv.Harnesses {
		kinds = append(kinds, s.Harness)
	}
	sort.Strings(kinds)
	return kinds
}

// TestBuildOpencodeOnlyNamesRelevoConfig pins that a machine with only
// opencode is not told it has no harness at all.
func TestBuildOpencodeOnlyNamesRelevoConfig(t *testing.T) {
	env := probeEnv{pathEnv: pathEnv{onPath: map[string]bool{"opencode": true}}}
	inv := Probe(env)
	_, err := Build(inv, RecommendedAnswers(inv, nil))
	if err == nil || !strings.Contains(err.Error(), "configure opencode with relevo config") {
		t.Fatalf("Build error = %v, want the opencode-only refusal", err)
	}
}
