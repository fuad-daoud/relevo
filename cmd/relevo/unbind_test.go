package main

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestUnbindRefusesFlagsItsPathDoesNotHonour pins that a flag the chosen
// unbind path does not honour is refused by name with codeRefused before any
// store or network access, so a seeded binding survives untouched.
func TestUnbindRefusesFlagsItsPathDoesNotHonour(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		message string
	}{
		{"plain dry-run", []string{"unbind", "alpha", "--dry-run"}, "--dry-run goes with --done or --sweep"},
		{"pick dry-run", []string{"unbind", "--pick", "--dry-run"}, "--dry-run goes with --done or --sweep"},
		{"plain delete", []string{"unbind", "alpha", "--delete"}, "--delete goes with --done"},
		{"pick delete", []string{"unbind", "--pick", "--delete"}, "--delete goes with --done"},
		{"done archive", []string{"unbind", "--done", "--archive"}, "--archive goes with a named binding or --pick, not --done"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			docsEnv(t)
			seedWriteBinding(t, "alpha", store.Binding{})

			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			ce := requireCLIError(t, runErr, codeRefused, "")

			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != 2 {
				t.Errorf("exit = %v, want 2 (code refused)", runErr)
			}
			if ce.message != c.message {
				t.Errorf("message = %q, want %q", ce.message, c.message)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty before report", stderr)
			}

			// The refusal precedes newRuntime: the seeded binding still loads
			// from the same root, so no store write happened.
			root, err := store.DefaultRoot()
			if err != nil {
				t.Fatalf("DefaultRoot: %v", err)
			}
			reloaded, err := store.New(root).Load("alpha")
			if err != nil {
				t.Fatalf("Load after refusal: %v", err)
			}
			if reloaded.Name != "alpha" || reloaded.State != store.StateActive {
				t.Errorf("binding = %+v, want the seeded active alpha", reloaded)
			}
		})
	}
}
