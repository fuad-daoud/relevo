//go:build !unix

package remote

// noFollow is 0 where the platform has no O_NOFOLLOW. relevo runs on Linux and
// macOS; this file exists so the windows portability build keeps compiling.
const noFollow = 0
