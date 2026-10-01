//go:build !unix

package serve

import "os"

// statOwner reports no numeric owner off unix: there is no syscall.Stat_t, and
// the split-layout check tolerates it. relevo runs on Linux and macOS; this
// platform's build still has to compile.
func statOwner(os.FileInfo) (uid, gid uint32, ok bool) { return 0, 0, false }
