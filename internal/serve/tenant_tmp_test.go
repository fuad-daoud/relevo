package serve

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// TestTmpDirRunnerIsTransparentToScopes pins that the decorator keeps every
// optional half of a Runner, so wrapping it cannot hide the base's scope
// support from a caller that type-asserts for it.
func TestTmpDirRunnerIsTransparentToScopes(t *testing.T) {
	base := newScriptRunner()
	wrapped := withTmpDir(base, t.TempDir())
	if _, ok := wrapped.(spawn.ScopeProber); !ok {
		t.Error("wrapper is not a spawn.ScopeProber")
	}
	if _, ok := wrapped.(spawn.ScopeStopper); !ok {
		t.Error("wrapper is not a spawn.ScopeStopper")
	}
	if _, ok := wrapped.(spawn.ScopeResultProber); !ok {
		t.Error("wrapper is not a spawn.ScopeResultProber")
	}
}
