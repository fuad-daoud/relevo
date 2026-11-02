//go:build unix

package proc

import (
	"context"
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// From the first systemd whose systemd-run takes the switch, every scope a
// Runner launches turns systemd-run's own command-line expansion off; before
// it, or when the version cannot be read, the switch is never passed.
func TestResolveScopeTurnsArgvExpansionOffWhereSystemdRunTakesTheSwitch(t *testing.T) {
	cases := map[string]struct {
		version int
		err     error
		want    bool
	}{
		"older than the switch": {253, nil, false},
		"first with the switch": {254, nil, true},
		"expanding scopes":      {262, nil, true},
		"unreadable version":    {0, errors.New("no systemd-run"), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ok := false
			r, _, _ := fakeProbeRunner(&ok)
			reads := 0
			r.systemdRunVersion = func(context.Context) (int, error) {
				reads++
				return c.version, c.err
			}
			tmpl := &spawn.ScopeSpec{Unit: "relevo-round-abc12345-foo-1", CPUWeight: 100}
			for i := 0; i < 2; i++ {
				got := r.resolveScope(context.Background(), spawn.ProcSpec{Scope: tmpl})
				if got.Scope == nil || got.Scope.NoExpand != c.want {
					t.Fatalf("start %d: scope = %+v, want NoExpand %v", i+1, got.Scope, c.want)
				}
			}
			if tmpl.NoExpand {
				t.Error("resolveScope wrote the switch into the caller's template")
			}
			if reads != 1 {
				t.Errorf("version reads over two starts = %d, want 1", reads)
			}
		})
	}
}
