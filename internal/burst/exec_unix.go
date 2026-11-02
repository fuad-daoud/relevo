//go:build unix

package burst

import (
	"errors"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the provider in its own session, hence its own
// process group: the internal/proc pattern for a command the head must be
// able to stop as a unit. It keeps the kill out of the head's group and
// lets -pid reach every helper the provider spawned.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// killProcessGroup kills the provider's whole group outright: the head is
// already past the deadline it gave the answer, so a provider ignoring the
// timeout is not owed a graceful wind-down. ESRCH means the group already
// exited -- the tolerated case internal/proc's Kill carries, because a
// dead provider is exactly what the kill came for.
func killProcessGroup(cmd *exec.Cmd) error {
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
