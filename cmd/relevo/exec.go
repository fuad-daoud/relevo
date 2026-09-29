package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// binExec is usage.Exec over the real PATH. Stderr rides on the error so
// the reader can quote its first line.
type binExec struct{}

func (binExec) Run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
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
