package main

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/doctor"
)

// TestRestartNotice pins the pure rule: the notice appears only on the
// restart row's Warn, and its count comes from Check.Unsafe.
func TestRestartNotice(t *testing.T) {
	cases := []struct {
		name string
		c    doctor.Check
		want string
	}{
		{"ok", doctor.Check{Name: "restart", Severity: doctor.SevOK, Detail: "no rounds running"}, ""},
		{"fail", doctor.Check{Name: "restart", Severity: doctor.SevFail}, ""},
		{"info", doctor.Check{Name: "restart", Severity: doctor.SevInfo}, ""},
		{"one", doctor.Check{Name: "restart", Severity: doctor.SevWarn, Unsafe: 1}, "restart unsafe: 1 running outside their own scope -- relevo doctor"},
		{"three", doctor.Check{Name: "restart", Severity: doctor.SevWarn, Unsafe: 3}, "restart unsafe: 3 running outside their own scope -- relevo doctor"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restartNotice(c.c); got != c.want {
				t.Errorf("restartNotice(%+v) = %q, want %q", c.c, got, c.want)
			}
		})
	}
}

// TestInsertRestartRow pins the row position: directly after the daemon
// row, before the first per-kind row.
func TestInsertRestartRow(t *testing.T) {
	checks := []doctor.Check{
		{Group: "", Name: "release"},
		{Group: "", Name: "daemon"},
		{Group: "claude", Name: "binary"},
	}
	restart := doctor.Check{Group: "", Name: "restart"}
	got := insertRestartRow(checks, restart)
	if len(got) != 4 {
		t.Fatalf("len(checks) = %d, want 4", len(got))
	}
	if got[2].Name != "restart" {
		t.Errorf("row 2 = %q, want the restart row right after daemon", got[2].Name)
	}

	// With no daemon row, the row still lands among the globals.
	noDaemon := []doctor.Check{
		{Group: "", Name: "release"},
		{Group: "claude", Name: "binary"},
	}
	got = insertRestartRow(noDaemon, restart)
	if got[1].Name != "restart" {
		t.Errorf("with no daemon row, row 1 = %q, want restart after the globals", got[1].Name)
	}
}
