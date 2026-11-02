//go:build unix

package burst

import (
	"errors"
	"io/fs"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The provider tests drive internal/burst/testdata/fake-provider.sh: no
// network and no harness, only a command the way `burst report` is one.

const fakeProvider = "testdata/fake-provider.sh"

func providerRequest(version int) ReportRequest {
	return ReportRequest{
		Version: version,
		At:      time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC),
		Reason:  ReportReasonInterval,
		Head:    ReportHead{Name: "contabo", CPUs: 8, MemGB: 32},
		Local:   ReportLocal{Running: 2, Queued: 3, Cap: 2},
		Waiting: []ReportWaiting{},
		Nodes:   []ReportNode{},
	}
}

func TestProviderRunsArgvAndValidatesResponse(t *testing.T) {
	p := &CommandProvider{Argv: []string{fakeProvider, "ok"}}
	// The script's ok mode answers only a request that arrives on stdin
	// carrying "version":2, so a clean reply also proves the request left.
	resp, notes, err := p.Report(providerRequest(ReportVersion))
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	want := ReportResponse{
		Version: ReportVersion,
		Nodes: []ReportNodeOut{{
			ID:          "n7",
			Name:        "b7 cx32@hel1",
			URL:         "https://10.0.0.7:9443",
			Fingerprint: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Cap:         2,
			State:       "ready",
		}},
		ReportEvery: "45s",
	}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
}

func TestProviderRefusesARequestThatIsNotV2(t *testing.T) {
	// ok mode exits 2 on a request whose version is not 2, the way the real
	// burst does: this is what catches a Report that never writes the
	// request to stdin at all.
	p := &CommandProvider{Argv: []string{fakeProvider, "ok"}}
	_, _, err := p.Report(providerRequest(1))
	var exit *ErrProviderExit
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want *ErrProviderExit", err)
	}
	if exit.Code != 2 {
		t.Errorf("exit code = %d, want 2", exit.Code)
	}
}

func TestProviderExitCarriesCodeAndStderrTail(t *testing.T) {
	p := &CommandProvider{Argv: []string{fakeProvider, "exit1"}}
	_, _, err := p.Report(providerRequest(ReportVersion))
	var exit *ErrProviderExit
	if !errors.As(err, &exit) {
		t.Fatalf("err = %v, want *ErrProviderExit", err)
	}
	if exit.Code != 1 {
		t.Errorf("exit code = %d, want 1", exit.Code)
	}
	if !strings.Contains(exit.Stderr, "no burst credentials configured") {
		t.Errorf("stderr tail = %q, want the provider's refusal", exit.Stderr)
	}
}

// TestProviderTimeoutKillsTheProcessGroup is the slow one: the fake sleeps
// 30 s and the exchange may take 10, so a pass at ~10 s means the kill
// landed. The script's background sleep inherits the stdout/stderr pipes;
// killing only the leader would leave it holding them and cmd.Wait open
// until it exits on its own, which the elapsed bound below refuses.
func TestProviderTimeoutKillsTheProcessGroup(t *testing.T) {
	p := &CommandProvider{Argv: []string{fakeProvider, "sleep"}}
	start := time.Now()
	_, _, err := p.Report(providerRequest(ReportVersion))
	elapsed := time.Since(start)
	var timeout *ErrProviderTimeout
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v, want *ErrProviderTimeout", err)
	}
	if timeout.After != providerTimeout {
		t.Errorf("After = %v, want %v", timeout.After, providerTimeout)
	}
	if elapsed < providerTimeout {
		t.Errorf("elapsed %v came back before the timeout it reports", elapsed)
	}
	if elapsed > 15*time.Second {
		t.Errorf("elapsed %v: the kill did not sweep the group (the fake sleeps 30s)", elapsed)
	}
}

func TestProviderBadJSONIsProviderOutput(t *testing.T) {
	p := &CommandProvider{Argv: []string{fakeProvider, "badjson"}}
	_, _, err := p.Report(providerRequest(ReportVersion))
	var out *ErrProviderOutput
	if !errors.As(err, &out) {
		t.Fatalf("err = %v, want *ErrProviderOutput", err)
	}
	if !errors.Is(err, ErrBadInput) {
		t.Errorf("err = %v, want it to reach ErrBadInput through Unwrap", err)
	}
}

func TestProviderVersionOneIsProviderOutput(t *testing.T) {
	p := &CommandProvider{Argv: []string{fakeProvider, "v1"}}
	_, _, err := p.Report(providerRequest(ReportVersion))
	var out *ErrProviderOutput
	if !errors.As(err, &out) {
		t.Fatalf("err = %v, want *ErrProviderOutput", err)
	}
	if !errors.Is(err, ErrBadInput) {
		t.Errorf("err = %v, want it to reach ErrBadInput through Unwrap", err)
	}
}

func TestProviderWithoutArgvIsRefused(t *testing.T) {
	p := &CommandProvider{}
	_, _, err := p.Report(providerRequest(ReportVersion))
	if err == nil {
		t.Fatal("an empty argv must be refused, not spawned")
	}
	if !strings.Contains(err.Error(), "no command configured") {
		t.Errorf("err = %v, want the missing-configuration refusal", err)
	}
}

func TestProviderUnspawnableArgvReportsAStartFailure(t *testing.T) {
	p := &CommandProvider{Argv: []string{"testdata/no-such-provider"}}
	_, _, err := p.Report(providerRequest(ReportVersion))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want it to reach the missing-file error", err)
	}
	// A provider that never ran has no exit status to blame: this must not
	// arrive dressed as one of the exchange's typed failures.
	for _, typed := range []any{new(*ErrProviderExit), new(*ErrProviderTimeout), new(*ErrProviderOutput)} {
		if errors.As(err, typed) {
			t.Errorf("err = %v, want a plain start failure, got %T", err, typed)
		}
	}
}

func TestProviderKillToleratesAGoneGroup(t *testing.T) {
	// The timeout kill races the provider's own death every time it fires
	// on a process that just exited; that path is ESRCH, and a dead
	// provider must not turn into a kill error.
	cmd := exec.Command(fakeProvider, "exit1")
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("exit1 mode must exit non-zero")
	}
	if err := killProcessGroup(cmd); err != nil {
		t.Errorf("killProcessGroup on a reaped provider = %v, want nil", err)
	}
}

func TestProviderExitCodeWithoutStatusIsMinusOne(t *testing.T) {
	if got := exitCode(errors.New("a wait failure with no status")); got != -1 {
		t.Errorf("exitCode = %d, want -1", got)
	}
}

func TestProviderStderrTailKeepsOnlyTheTail(t *testing.T) {
	w := &tailWriter{limit: 10}
	n, err := w.Write([]byte("0123456789AB"))
	if n != 12 || err != nil {
		t.Fatalf("Write = (%d, %v), want (12, nil)", n, err)
	}
	if got := w.String(); got != "23456789AB" {
		t.Errorf("tail = %q, want %q", got, "23456789AB")
	}
	if _, err := w.Write([]byte("cd")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := w.String(); got != "456789ABcd" {
		t.Errorf("tail = %q, want %q", got, "456789ABcd")
	}
}

func TestProviderErrorMessagesCarryTheirFacts(t *testing.T) {
	if got := (&ErrProviderExit{Code: 2}).Error(); got != "burst: provider exited with code 2" {
		t.Errorf("Error() = %q", got)
	}
	got := (&ErrProviderExit{Code: 1, Stderr: "no burst credentials configured"}).Error()
	if want := "burst: provider exited with code 1: no burst credentials configured"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if got := (&ErrProviderTimeout{After: providerTimeout}).Error(); !strings.Contains(got, "timed out after 10s") {
		t.Errorf("Error() = %q, want it to name the deadline", got)
	}
	out := &ErrProviderOutput{Err: errors.New("nodes[0].id: required")}
	if got := out.Error(); got != "burst: provider output: nodes[0].id: required" {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(out, out.Err) {
		t.Error("Unwrap must hand back the contract error")
	}
}
