package relevo

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// TestResolveVerbMasterMindUnknownEnvNamesVariable pins the consumer side of
// the env wrapper: how bind, add, fork and ask see a stale $RELEVO_MASTERMIND
// or $RELEVO_PLANNER. The error still unwraps to ErrUnknownMasterMind with
// the ref, and names the variable that held it, with no harness or network
// involved.
func TestResolveVerbMasterMindUnknownEnvNamesVariable(t *testing.T) {
	for _, variable := range []string{"RELEVO_MASTERMIND", "RELEVO_PLANNER"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("RELEVO_MASTERMIND", "")
			t.Setenv("RELEVO_PLANNER", "")
			t.Setenv(variable, "nope")
			rt := newRuntime(t)

			_, _, err := resolveVerbMasterMind(rt, "")
			if err == nil {
				t.Fatal("resolveVerbMasterMind = nil, want an unknown-mastermind error")
			}
			var wrapped mastermind.ErrUnknownMasterMindEnv
			if !errors.As(err, &wrapped) {
				t.Fatalf("resolveVerbMasterMind = %v (%T), want it to carry the variable", err, err)
			}
			if wrapped.Var != variable || wrapped.Ref != "nope" {
				t.Errorf("wrapped = %+v, want Var %q Ref %q", wrapped, variable, "nope")
			}
			var unknown mastermind.ErrUnknownMasterMind
			if !errors.As(err, &unknown) || unknown.Ref != "nope" {
				t.Errorf("errors.As(ErrUnknownMasterMind) = %+v, want Ref nope", unknown)
			}
			if got := err.Error(); !strings.Contains(got, "$"+variable) || !strings.Contains(got, `"nope"`) {
				t.Errorf("message = %q, want it to name $%s and \"nope\"", got, variable)
			}
		})
	}
}
