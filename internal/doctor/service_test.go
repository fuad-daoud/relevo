package doctor

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/serviceunit"
	"github.com/fuad-daoud/relevo/internal/store"
)

func unitEnvOf(installed, shipped []byte, mtime, started time.Time) *fakeEnv {
	return &fakeEnv{
		daemonInfo:   store.DaemonInfo{StartedAt: started},
		daemonInfoOK: true,
		unit: ServiceUnit{
			Doc: serviceunit.ServiceName, Installed: installed, Shipped: shipped,
			MTime: mtime, Present: true,
		},
	}
}

func TestServiceRowDetails(t *testing.T) {
	now := time.Now()
	cur := serviceunit.ShippedService()
	cases := []struct {
		name   string
		env    *fakeEnv
		detail string
		fix    string
	}{
		{"restart due", unitEnvOf(cur, cur, now, now.Add(-time.Hour)), "unit refreshed, restart due", "systemctl --user restart relevo.service"},
		{"edited", unitEnvOf([]byte("hand edited"), cur, now, now), "unit edited, not refreshed", "relevo service install --force"},
		// The installed bytes are a blob the index knows; the shipped bytes moved on.
		{"older than shipped", unitEnvOf(cur, []byte("newer"), now, now), "unit older than shipped", "relevo service install"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row, ok := serviceRow(c.env)
			if !ok || row.Name != "service" || row.Severity != SevWarn || row.Detail != c.detail || row.Fix != c.fix {
				t.Fatalf("serviceRow = %+v ok=%v, want warn %q fix %q", row, ok, c.detail, c.fix)
			}
		})
	}
}

func TestServiceRowQuietWhenCurrentOrAbsent(t *testing.T) {
	now := time.Now()
	cur := serviceunit.ShippedService()
	row, ok := serviceRow(unitEnvOf(cur, cur, now.Add(-time.Hour), now))
	if !ok || row.Severity != SevOK {
		t.Fatalf("current unit row = %+v ok=%v, want OK", row, ok)
	}
	if _, ok := serviceRow(&fakeEnv{}); ok {
		t.Fatal("absent unit produced a row")
	}
}
