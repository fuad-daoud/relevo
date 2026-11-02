package burst

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Provider is the head's one exchange with the burst provider: a validated
// report request goes in, a validated response comes back with the field
// paths that were not recognised and were normalised to "" (the caller logs
// those; they are never an error). The request must already pass
// ValidateRequest -- the provider sends it as built, and the real `burst
// report` refuses one that does not with a non-zero exit. Every provider-side
// failure of the exchange surfaces as ErrProviderExit, ErrProviderTimeout or
// ErrProviderOutput, so a report loop can react to the kind of failure --
// keep the last good node list, wait a full interval, stop -- without
// matching on messages.
type Provider interface {
	Report(req ReportRequest) (ReportResponse, []string, error)
}

// providerTimeout is the ceiling on one exchange. The provider is a local
// command that answers in milliseconds, and a stalled one must not hold the
// head's report loop past the shortest cadence a reply is ever allowed to
// ask for (MinReportEvery): the exchange dies at ten seconds, its process
// group with it.
const providerTimeout = 10 * time.Second

// providerStderrTail bounds the stderr an exit error carries: a provider's
// reason for refusing sits at the end of what it wrote, and the whole stream
// of a babbling one is worth neither the memory nor the log line.
const providerStderrTail = 4096

// ErrProviderExit is a provider command that ran and exited non-zero: it
// answered the report with a status instead of a document.
type ErrProviderExit struct {
	// Code is the exit status; -1 when the process reported none, as one
	// killed by a signal does not.
	Code int
	// Stderr is the tail of the provider's last stderr, where its stated
	// reason sits.
	Stderr string
}

func (e *ErrProviderExit) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("burst: provider exited with code %d", e.Code)
	}
	return fmt.Sprintf("burst: provider exited with code %d: %s", e.Code, e.Stderr)
}

// ErrProviderTimeout is a provider that did not answer within
// providerTimeout. Its process group was killed on the way to this error;
// whatever it would have printed is discarded, because the exchange lost its
// deadline and the head keeps its last good answer instead.
type ErrProviderTimeout struct {
	After time.Duration
}

func (e *ErrProviderTimeout) Error() string {
	return fmt.Sprintf("burst: provider timed out after %s", e.After)
}

// ErrProviderOutput is a provider that exited zero without handing back a
// valid v2 response: stdout was not JSON, or it did not fit the contract.
// Err is the decode or validation failure naming what was wrong, so
// errors.Is(err, ErrBadInput) reaches the contract's verdict through it.
type ErrProviderOutput struct {
	Err error
}

func (e *ErrProviderOutput) Error() string {
	return "burst: provider output: " + e.Err.Error()
}

func (e *ErrProviderOutput) Unwrap() error { return e.Err }

// CommandProvider is a Provider that runs the policy's serve.burst.command
// argv, writes the report JSON to its stdin and reads the response from its
// stdout. It is the only I/O in this package.
type CommandProvider struct {
	// Argv is the provider command as configured: on a real head
	// ["burst", "report"], a script path and mode in tests.
	Argv []string
}

var _ Provider = (*CommandProvider)(nil)

// Report runs one exchange and validates the reply. A failed validation or
// an unreadable document is ErrProviderOutput, never a silent ""-normalised
// acceptance: a half-understood reply is worse than none, because the head
// would act on the seats it could read.
func (p *CommandProvider) Report(req ReportRequest) (ReportResponse, []string, error) {
	// json.Marshal fails only on values no encoding exists for, and a
	// request is strings, counts and times; the blank keeps that failure
	// from hiding: the empty document that follows is then refused by the
	// provider over the wire and surfaces as an exit error.
	data, _ := json.Marshal(req)
	out, err := p.exchange(data)
	if err != nil {
		return ReportResponse{}, nil, err
	}
	resp, notes, verr := ValidateResponse(out)
	if verr != nil {
		return ReportResponse{}, nil, &ErrProviderOutput{Err: verr}
	}
	return resp, notes, nil
}

// exchange starts the command once, waits at most providerTimeout for it,
// and returns its whole stdout. The child runs in its own process group and
// that group is killed on timeout, so a provider that spawned helpers cannot
// outlive the exchange that timed out on it.
func (p *CommandProvider) exchange(stdin []byte) ([]byte, error) {
	if len(p.Argv) == 0 {
		return nil, errors.New("burst: provider: no command configured")
	}
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout bytes.Buffer
	stderr := &tailWriter{limit: providerStderrTail}
	cmd.Stdout = &stdout
	cmd.Stderr = stderr
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("burst: provider start: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case werr := <-waited:
		if werr != nil {
			return nil, &ErrProviderExit{Code: exitCode(werr), Stderr: stderr.String()}
		}
		return stdout.Bytes(), nil
	case <-time.After(providerTimeout):
		// The kill is the cleanup of an exchange already lost to its
		// deadline, so what it reports cannot change the verdict the
		// caller needs: the timeout stands.
		_ = killProcessGroup(cmd)
		<-waited // the kill makes Wait return; a reaped child leaves no zombie
		return nil, &ErrProviderTimeout{After: providerTimeout}
	}
}

// exitCode is the status a wait failure reports. os/exec answers a completed
// command only with *exec.ExitError, but a failure that carries no status is
// reported as -1 rather than left out of the typed error.
func exitCode(werr error) int {
	var exit *exec.ExitError
	if errors.As(werr, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// tailWriter keeps the last limit bytes written to it, so a provider's
// stderr is retained where its reason sits -- at the end -- no matter how
// much a stalling one printed before it.
type tailWriter struct {
	limit int
	buf   []byte
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.limit; over > 0 {
		n := copy(w.buf, w.buf[over:])
		w.buf = w.buf[:n]
	}
	return len(p), nil
}

func (w *tailWriter) String() string { return string(w.buf) }
