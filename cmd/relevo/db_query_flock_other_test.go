//go:build !unix

package main

// flockIsFree reports that relevo's open lock is free. Off unix there is no
// such lock and no database open that holds one, so a test observing the lock
// has nothing to wait for.
func flockIsFree(string) bool { return true }
