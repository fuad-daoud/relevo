//go:build unix

package main

import (
	"os/exec"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// applyCredential makes cmd run as cred's tenant when cred is set;
// syscall.Credential does not exist on windows.
func applyCredential(cmd *exec.Cmd, cred *spawn.Credential) {
	if cred == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: cred.UID, Gid: cred.GID},
	}
}
