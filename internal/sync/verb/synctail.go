package verb

// Joining reports whether an enable's join holds a transport: from when the join
// opens its log until the enable settles.
func (v *Runner) Joining() bool {
	return v != nil && v.joinTransport() != nil
}
