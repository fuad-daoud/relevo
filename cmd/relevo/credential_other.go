//go:build !unix

package main

import (
	"os/exec"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// applyCredential is a no-op off unix: there is no SysProcAttr.Credential to
// set, and the tree still has to compile for the windows build.
func applyCredential(*exec.Cmd, *spawn.Credential) {}
