package ui

import (
	"errors"
	"os"
	"os/exec"
)

// Shell is a shell in the binding's own tree: its worktree when relevo made
// one, else its recorded CWD. A remote binding has no local tree to run in.
func (a *mastermindActions) Shell(key string) (*exec.Cmd, error) {
	rt, name, ok := a.resolve(key)
	if !ok {
		return nil, errors.New("unknown binding")
	}
	b, err := rt.Store.Load(name)
	if err != nil {
		return nil, err
	}
	if b.Builder.Remote() {
		return nil, errors.New("a remote binding has no local tree")
	}
	dir := b.Worktree
	if dir == "" {
		dir = b.CWD
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	cmd := exec.Command(shell)
	cmd.Dir = dir
	return cmd, nil
}
