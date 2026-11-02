package db

// OwnerStatus is what a dialled owner reports about itself: the socket it
// answers on, its pid, the protocol version it speaks, how many connections
// it holds and how many of them hold a slot. The type lives in a
// platform-neutral file so a caller off unix can still name it; only
// ProbeOwner is unix-only.
type OwnerStatus struct {
	Socket  string
	PID     int
	Version int
	Conns   int
	// Pinned counts the connections holding a slot: the work the cap is
	// bounding now, beside the live total.
	Pinned int
}
