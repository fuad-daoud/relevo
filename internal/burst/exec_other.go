//go:build !unix

package burst

import "os/exec"

// setProcessGroup is a no-op off unix: there is no session to detach into,
// so the provider starts in the head's own group and the timeout kill
// reaches the command alone.
func setProcessGroup(*exec.Cmd) {}

// killProcessGroup kills only the provider process. Children it spawned
// outlive it and keep the output pipes open until they exit on their own,
// so the wait after the kill can run past the deadline: unix process groups
// are the whole of what the timeout sweeps.
func killProcessGroup(cmd *exec.Cmd) error {
	return cmd.Process.Kill()
}
