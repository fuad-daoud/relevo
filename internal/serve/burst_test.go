package serve

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// burstTestPolicy is the policy block a head configures to reach the
// provider; the argv is never executed by these tests.
func burstTestPolicy() *policy.BurstPolicy {
	return &policy.BurstPolicy{Command: []string{"burst", "report"}}
}

// countBurstKeyLines counts the log records naming the enrollment command,
// the one the startup gate must write exactly once per process.
func countBurstKeyLines(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "relevo config server key")
}

// TestServeBurstOffWithoutClientKey pins the startup gate: a configured
// burst command with no client key does not fail New -- the head must keep
// serving local rounds -- but it says so once at error level, names the
// command that enrolls a key, resolves no provider, and keeps that reason
// for the status block. A head with no command at all logs nothing.
func TestServeBurstOffWithoutClientKey(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	s, err := New(Config{
		DB:    testServeDB(t),
		Root:  t.TempDir(),
		Now:   time.Now,
		Burst: burstTestPolicy(),
	})
	if err != nil {
		t.Fatalf("New with a burst command and no client key = %v, want the head to start anyway", err)
	}
	if s.burstProvider != nil {
		t.Error("burst provider resolved, want bursting off with no client key")
	}
	if s.burstTable != nil {
		t.Error("node table loaded, want no table state while bursting is off")
	}
	if !strings.Contains(s.burstOffErr, "relevo config server key") {
		t.Errorf("burstOffErr = %q, want it to name relevo config server key for the status block", s.burstOffErr)
	}
	if n := countBurstKeyLines(&buf); n != 1 {
		t.Errorf("log lines naming the enrollment command = %d, want exactly 1:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("startup log = %q, want an error-level record", buf.String())
	}

	buf.Reset()
	off, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now})
	if err != nil {
		t.Fatalf("New with no burst command: %v", err)
	}
	if buf.String() != "" {
		t.Errorf("log for a head with no burst command = %q, want nothing", buf.String())
	}
	if off.burstProvider != nil || off.burstOffErr != "" {
		t.Errorf("burst state = %v %q, want off with no error when no command is configured", off.burstProvider, off.burstOffErr)
	}
}

// TestServeBurstProviderResolved pins what New does with a usable burst
// config: an injected provider is kept as given, and with none the command
// builds the default one -- nil Provider means built from the command.
func TestServeBurstProviderResolved(t *testing.T) {
	injected := fakeBurstProvider{}

	built, err := New(Config{
		DB:        testServeDB(t),
		Root:      t.TempDir(),
		Now:       time.Now,
		ClientKey: []byte("pem-client-key"),
		Burst:     burstTestPolicy(),
	})
	if err != nil {
		t.Fatalf("New with command and key: %v", err)
	}
	cp, ok := built.burstProvider.(*burst.CommandProvider)
	if !ok {
		t.Fatalf("burstProvider = %T, want *burst.CommandProvider built from the command", built.burstProvider)
	}
	if strings.Join(cp.Argv, " ") != "burst report" {
		t.Errorf("provider Argv = %v, want the configured command", cp.Argv)
	}
	if built.burstOffErr != "" {
		t.Errorf("burstOffErr = %q, want empty with bursting on", built.burstOffErr)
	}
	if built.burstTable == nil || len(built.burstTable.Nodes) != 0 {
		t.Errorf("burstTable = %+v, want an empty table on a head that never reported", built.burstTable)
	}

	given, err := New(Config{
		DB:        testServeDB(t),
		Root:      t.TempDir(),
		Now:       time.Now,
		ClientKey: []byte("pem-client-key"),
		Burst:     burstTestPolicy(),
		Provider:  injected,
	})
	if err != nil {
		t.Fatalf("New with an injected provider: %v", err)
	}
	if given.burstProvider != burst.Provider(injected) {
		t.Errorf("burstProvider = %T, want the injected provider kept as given", given.burstProvider)
	}
}

// TestServeBurstOffAdmitsLocalRounds pins the gate's other half: a head
// whose bursting was refused at startup keeps its normal job -- queued
// rounds are admitted and run locally on the next tick.
func TestServeBurstOffAdmitsLocalRounds(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.Burst = burstTestPolicy() })
	if env.srv.burstProvider != nil {
		t.Fatal("burst provider resolved with no client key, want off")
	}

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan 1")
	requireCreated(t, resp, body, "api")
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	requireRoundState(t, testRuntime(t, env.srv, env.id), "api", remote.RoundRunning)
}

// fakeBurstProvider is a burst.Provider that answers nothing; tests check
// identity, never a call.
type fakeBurstProvider struct{}

func (fakeBurstProvider) Report(burst.ReportRequest) (burst.ReportResponse, []string, error) {
	return burst.ReportResponse{}, nil, nil
}
