//go:build !unix

package syncworker

// OwnerOnlyFiles has nothing to narrow where files carry no unix mode.
func OwnerOnlyFiles() {}
