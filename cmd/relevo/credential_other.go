//go:build !unix

package main

import (
	"os/exec"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// applyCredential is a no-op off unix; the tree still has to compile for
// windows.
func applyCredential(*exec.Cmd, *spawn.Credential) {}
