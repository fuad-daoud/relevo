package main

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// These cases stop at flag parsing or at a refusal the rules in internal/relevo
// give before any git or network call: no test here runs a push, a worker or a
// harness.
func TestHandoverAndTakeoverUsageAndRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code errorCode
		text string
	}{
		{"handover names nothing", []string{"handover"}, codeUsage, "usage: relevo handover"},
		{"handover without --to", []string{"handover", "alpha"}, codeUsage, "--to"},
		{"handover with sync off", []string{"handover", "alpha", "--to", "zen"}, codeRefused, "sync is off"},
		{"takeover names nothing", []string{"takeover"}, codeUsage, "usage: relevo takeover"},
		{"takeover with sync off", []string{"takeover", "alpha", "--force"}, codeRefused, "sync is off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docsEnv(t)
			seedWriteBinding(t, "alpha", store.Binding{})
			_, _, err := captureOutput(t, func() error { return run(c.args) })
			ce := requireCLIError(t, err, c.code, "")
			if !strings.Contains(ce.message, c.text) {
				t.Errorf("message = %q, want it to contain %q", ce.message, c.text)
			}
		})
	}
}

// TestTakeoverRefusedOnHistoryNoneMachine pins that takeover on a machine with
// sync history set to none refuses and names the re-enable fix.
func TestTakeoverRefusedOnHistoryNoneMachine(t *testing.T) {
	docsEnv(t)
	seedWriteBinding(t, "alpha", store.Binding{})
	d, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	if err := relevosync.PutSettings(d.Local(), relevosync.Settings{Enabled: true, History: relevosync.HistoryNone}, time.Now()); err != nil {
		_ = d.Close()
		t.Fatalf("PutSettings: %v", err)
	}
	_ = d.Close()
	_, _, err = captureOutput(t, func() error { return run([]string{"takeover", "alpha", "--force"}) })
	ce := requireCLIError(t, err, codeRefused, "")
	if !strings.Contains(ce.message, "disable, then enable --history full") {
		t.Errorf("message = %q, want it to contain %q", ce.message, "disable, then enable --history full")
	}
}
