//go:build unix

package git

import (
	"os/exec"
	"syscall"
)

// applyCredential makes cmd run as uid/gid when set is true. It is the unix
// half of the client's tenant identity: syscall.Credential and
// SysProcAttr.Credential exist only here, so the cross-platform client stores
// the numbers and this helper applies them.
func applyCredential(cmd *exec.Cmd, uid, gid uint32, set bool) {
	if !set {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: uid, Gid: gid},
	}
}
