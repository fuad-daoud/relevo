//go:build !unix

package db

// ProbeOwner refuses off unix: the owner protocol is a unix socket, and relevo
// serves the database over one only on Linux and macOS.
func ProbeOwner(sock string) (OwnerStatus, error) {
	return OwnerStatus{Socket: sock}, notHere()
}
