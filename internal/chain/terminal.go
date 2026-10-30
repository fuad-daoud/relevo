package chain

// StatusTerminal reports whether a chain status word is one of the three
// the state machine stops on.
func StatusTerminal(status string) bool {
	switch Status(status) {
	case StatusDone, StatusHalted, StatusStopped:
		return true
	}
	return false
}
