//go:build !unix

package delivery

// defaultClaimAlive: relevo targets Linux and macOS; on other platforms no
// claim is ever live, so the daemon always delivers to the pane.
func defaultClaimAlive(pid int) bool {
	return false
}
