package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/proc"
	"github.com/fuad-daoud/relevo/internal/spawn"
)

// binExec is usage.Exec over the real PATH, with stderr folded into the
// error. deny and extra come from isolate.UserSpec's rule. It optionally runs
// the child as a tenant, which is how a user-mode server's session reaper
// deletes a session as the tenant.
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
		applyCredential(cmd, e.credential)
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

// binEnvExec is binExec with an environment, appended to the parent's so agy
// still finds its PATH and HOME; there is no shell, so no value is re-interpreted.
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
