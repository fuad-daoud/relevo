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
