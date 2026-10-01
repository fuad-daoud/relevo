package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// binExec is usage.Exec over the real PATH. Stderr rides on the error so
// the reader can quote its first line.
//
// It optionally runs the child as a tenant (credential) with the tenant's
// environment (deny/extra, from isolate.UserSpec's rule), which is how a
// user-mode server's session reaper deletes a session as the tenant.
type binExec struct {
	credential *spawn.Credential
	deny       []string
	extra      []string
}

func (e binExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if len(e.deny) > 0 || len(e.extra) > 0 {
		cmd.Env = proc.ChildEnv(os.Environ(), e.deny, e.extra)
	}
	if e.credential != nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: e.credential.UID, Gid: e.credential.GID}}
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, errors.New(s)
		}
		return nil, err
	}
	return out, nil
}

// binEnvExec is binExec with an environment: delivery.EnvExec over the real PATH,
// with stderr folded into the error the same way. extraEnv is appended to the
// parent environment, never replacing it, so agy still finds its own PATH and
// HOME; there is no shell, so no value here can be re-interpreted.
type binEnvExec struct{}

func (binEnvExec) Run(ctx context.Context, extraEnv []string, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if s := strings.TrimSpace(stderr.String()); s != "" {
			return nil, errors.New(s)
		}
		return nil, err
	}
	return out, nil
}
