//go:build unix

package proc

import (
	"os/exec"
	"strings"
	"syscall"
)

// psVerdict is what a failed ps says: the process is really gone, or the
// failure was relevo's own and must not be read as a dead process.
type psVerdict int

const (
	psOK psVerdict = iota
	psNoProcess
	psTransient
)

// classifyPS is psInfo's decision, pure so it can be tested without a process.
// Only "exit 1 with empty stdout" -- how procps and BSD ps report a gone pid --
// is psNoProcess; a cancelled or signalled ps is psTransient, which every Alive
// caller treats as alive this tick rather than dead.
func classifyPS(ctxErr error, exitErr *exec.ExitError, stdout []byte) psVerdict {
	if exitErr == nil {
		return psOK
	}
	if ctxErr != nil {
		return psTransient
	}
	ws, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		// An unfamiliar wait status cannot be read as a signal: report the
		// failure rather than guess it means a dead process.
		return psTransient
	}
	if ws.Signaled() {
		return psTransient
	}
	if exitErr.ExitCode() == 1 && strings.TrimSpace(string(stdout)) == "" {
		return psNoProcess
	}
	return psTransient
}
