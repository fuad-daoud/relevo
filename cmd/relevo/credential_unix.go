//go:build unix

package main

import (
	"os/exec"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// applyCredential makes cmd run as cred's tenant when cred is set. It is the
// unix half of binExec's identity: syscall.Credential does not exist on
// windows, so the command is assembled cross-platform and this helper applies
// the credential.
func applyCredential(cmd *exec.Cmd, cred *spawn.Credential) {
	if cred == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: cred.UID, Gid: cred.GID},
	}
}
