//go:build !unix

package doctor

import "os"

// statOwner reports no numeric owner off unix: there is no syscall.Stat_t, and
// the owner-root check tolerates it. relevo runs on Linux and macOS; this
// platform's build still has to compile.
func statOwner(os.FileInfo) (uid, gid uint32, ok bool) { return 0, 0, false }
