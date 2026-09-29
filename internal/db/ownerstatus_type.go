package db

// OwnerStatus is what a dialled owner reports about itself: the socket it
// answers on, its pid, the protocol version it speaks and how many connections
// it holds. The type lives in a platform-neutral file so a caller off unix can
// still name it; only ProbeOwner is unix-only.
type OwnerStatus struct {
	Socket  string
	PID     int
	Version int
	Conns   int
}
