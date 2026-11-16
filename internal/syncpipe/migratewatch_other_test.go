//go:build !unix

package syncpipe

// workerPidAlive has no probe off unix, so the reaping test skips there
// rather than passing vacuously.
func workerPidAlive(int) (bool, bool) {
	return false, false
}
