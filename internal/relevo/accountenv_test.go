package relevo

import (
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/store"
)

// lastEnvValue is the value of the last entry named name in env: the value
// os/exec hands the child when a variable appears more than once.
func lastEnvValue(env []string, name string) (string, bool) {
	value, found := "", false
	for _, e := range env {
		if n, v, ok := strings.Cut(e, "="); ok && n == name {
			value, found = v, true
		}
	}
	return value, found
}

// TestRoundEnvPinsTheAccountHome pins the per-account env composition: a claude
// or codex account appends its own home entry after the runner marker, so it
// lands after proc.ChildEnv's deny filter and a stale parent value cannot win; a
// nil account and an opencode account (no per-process home) add nothing, so an
// empty pool spawns exactly today's environment.
func TestRoundEnvPinsTheAccountHome(t *testing.T) {
	t.Parallel()

	b := store.Binding{Name: "api"}
	base := roundEnv(b)
	if got := roundEnvFor(b, nil); !reflect.DeepEqual(got, base) {
		t.Errorf("roundEnvFor(nil) = %v, want roundEnv %v", got, base)
	}

	cases := []struct {
		name      string
		a         account.Account
		wantName  string
		wantValue string
	}{
		{"claude", account.Account{Name: "work", Harness: account.Claude, Groups: []string{"test"}, ConfigDir: "/homes/work"}, "CLAUDE_CONFIG_DIR", "/homes/work"},
		{"codex", account.Account{Name: "work", Harness: account.Codex, Groups: []string{"test"}, Home: "/homes/codex"}, "CODEX_HOME", "/homes/codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.a
			want := append(append([]string(nil), base...), tc.wantName+"="+tc.wantValue)
			if got := roundEnvFor(b, &a); !reflect.DeepEqual(got, want) {
				t.Fatalf("roundEnvFor = %v, want %v", got, want)
			}
			child := proc.ChildEnv([]string{"PATH=/bin", tc.wantName + "=/parent"}, nil, roundEnvFor(b, &a))
			if v, ok := lastEnvValue(child, tc.wantName); !ok || v != tc.wantValue {
				t.Errorf("child %s = %q, %v; want %q from the account home", tc.wantName, v, ok, tc.wantValue)
			}
		})
	}

	oc := account.Account{Name: "cp", Harness: account.OpenCode, Groups: []string{"test"}, Integration: "cline-pass", Label: "ClinePass"}
	if got := roundEnvFor(b, &oc); !reflect.DeepEqual(got, base) {
		t.Errorf("roundEnvFor(opencode) = %v, want roundEnv %v", got, base)
	}
}
