package db

// ownerHop is the transparency switch a test installs with SetOwnerHop: it
// maps an opened path to the socket every later open of that path dials.
var ownerHop func(path string, o Options, direct func() (*DB, error)) (string, error)

// SetOwnerHop installs the switch used by the test suites to route every
// database through an in-process owner; relevo never calls it. start is
// invoked on the first open of a path with a genuinely direct opener, and
// must return the socket that path is reached through. Passing nil clears it.
func SetOwnerHop(start func(path string, o Options, direct func() (*DB, error)) (sock string, err error)) {
	ownerHop = start
}

// ownerHopClosed pairs with ownerHop: open calls it with the socket once a
// handle it dialled through the hop has closed. dbtest's owner mode uses it to
// stop the owner and close its direct handle when the last client handle for a
// path is gone.
var ownerHopClosed func(sock string)

// SetOwnerHopClosed installs the close observer that pairs with SetOwnerHop:
// open calls f with the socket once a handle dialled through the hop has
// closed. Passing nil clears it.
func SetOwnerHopClosed(f func(sock string)) {
	ownerHopClosed = f
}
