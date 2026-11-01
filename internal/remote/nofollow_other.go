//go:build !unix

package remote

// relevo runs on Linux and macOS; this file exists so the windows portability
// build keeps compiling.
const noFollow = 0
