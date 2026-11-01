package verb

// Joining reports whether an enable's join holds a transport, from log open
// until settle.
func (v *Runner) Joining() bool {
	return v != nil && v.joinTransport() != nil
}
