//go:build !unix

package delivery

// defaultClaimAlive: off Linux and macOS no claim is ever live, so the daemon
// always delivers to the pane.
func defaultClaimAlive(pid int) bool {
	return false
}
