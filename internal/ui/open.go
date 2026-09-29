package ui

// This file holds the Actions methods that return a command for the cockpit
// to run in another program: the user's editor, and the pager, browser or
// editor that opens a round artifact (round 5b).

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// AgentEditor is the user's editor on path: $VISUAL, else $EDITOR, else
// vi, split with strings.Fields, path last. It mirrors runEditor
// (cmd/relevo/config.go:445) but returns the *exec.Cmd without running it, so
// the cockpit can hand it to tea.ExecProcess and suspend while it runs.
func (a *mastermindActions) AgentEditor(path string) (*exec.Cmd, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	argv := append(strings.Fields(editor), path)
	return exec.Command(argv[0], argv[1:]...), nil
}

// OpenArtifact builds the command that opens an artifact file (round 5b):
// kind "pager" is $PAGER, else less -R; kind "browser" is xdg-open on Linux
// and open on macOS; anything else is the user's editor, through AgentEditor.
// Like AgentEditor it never runs anything.
func (a *mastermindActions) OpenArtifact(path, kind string) (*exec.Cmd, error) {
	switch kind {
	case "pager":
		pager := os.Getenv("PAGER")
		if pager == "" {
			pager = "less -R"
		}
		argv := append(strings.Fields(pager), path)
		return exec.Command(argv[0], argv[1:]...), nil
	case "browser":
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		return exec.Command(opener, path), nil
	default:
		return a.AgentEditor(path)
	}
}
