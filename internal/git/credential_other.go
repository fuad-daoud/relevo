//go:build !unix

package git

import "os/exec"

// applyCredential is a no-op off unix: there is no SysProcAttr.Credential to
// set, and the tree still has to compile for the windows build.
func applyCredential(*exec.Cmd, uint32, uint32, bool) {}
