package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// seedWaitJSONBinding seeds one active binding whose round 1 carries a report
// file on disk, so a wait without --peek has a payload to deliver. Store-only:
// no harness is launched and no network is touched.
func seedWaitJSONBinding(t *testing.T, name string) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{
		Name: name, CWD: filepath.Join(root, "work", name), Round: 1, State: store.StateActive,
	}); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	if err := os.WriteFile(s.ReportPath(name, 1), []byte("# report body\n"), 0o644); err != nil {
		t.Fatalf("write report %s: %v", name, err)
	}
	if err := s.AppendLog(name, store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Path: s.ReportPath(name, 1),
	}); err != nil {
		t.Fatalf("AppendLog %s: %v", name, err)
	}
}

// TestContractWaitJSON pins wait's machine document: one indented object
// carrying the resolved name, the round, the protocol code, the outcome line,
// and -- for a wait that delivers -- the payload. --peek reports the outcome
// only, and --any names the binding it stopped on.
func TestContractWaitJSON(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)

	seedWaitJSONBinding(t, "wait-json")
	seedWaitJSONBinding(t, "wait-json-any")
	seedWaitJSONBinding(t, "wait-json-peek")

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}

	for _, c := range []struct {
		golden      string
		args        []string
		wantName    string
		wantPayload bool
	}{
		{"wait-json", []string{"wait", "--name", "wait-json", "--round", "1", "--timeout", "1ns", "--json"}, "wait-json", true},
		{"wait-json-any", []string{"wait", "--any", "wait-json-any", "--timeout", "1ns", "--json"}, "wait-json-any", true},
		{"wait-json-peek", []string{"wait", "--name", "wait-json-peek", "--round", "1", "--timeout", "1ns", "--peek", "--json"}, "wait-json-peek", false},
	} {
		stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
		if runErr != nil {
			t.Fatalf("%v: %v (stderr: %s)", c.args, runErr, stderr)
		}
		assertGolden(t, c.golden, normalize(stdout, root))

		var doc WaitDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("%v: stdout is not one WaitDoc: %v", c.args, err)
		}
		if doc.Name != c.wantName {
			t.Errorf("%v: name = %q, want %q", c.args, doc.Name, c.wantName)
		}
		if doc.Round != 1 {
			t.Errorf("%v: round = %d, want 1", c.args, doc.Round)
		}
		if doc.Code != 0 {
			t.Errorf("%v: code = %d, want the closed round's 0", c.args, doc.Code)
		}
		if doc.Line == "" {
			t.Errorf("%v: line is empty, want the outcome line", c.args)
		}
		if got := doc.Payload != ""; got != c.wantPayload {
			t.Errorf("%v: payload present = %v, want %v (payload %q)", c.args, got, c.wantPayload, doc.Payload)
		}
	}
}

// seedReadVerbErrorStore seeds the two bindings the classification cases read:
// "errcodes" has one completed round and no diff or report file, and
// "errround" has a round in flight and nothing completed. Store-only.
func seedReadVerbErrorStore(t *testing.T) {
	t.Helper()

	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	for _, b := range []store.Binding{
		{Name: "errcodes", CWD: filepath.Join(root, "work", "errcodes"), Round: 2, State: store.StateActive},
		{Name: "errround", CWD: filepath.Join(root, "work", "errround"), Round: 1, State: store.StateActive},
	} {
		if err := s.Save(b); err != nil {
			t.Fatalf("Save %s: %v", b.Name, err)
		}
		if err := s.AppendLog(b.Name, store.LogEntry{
			Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true,
		}); err != nil {
			t.Fatalf("AppendLog %s: %v", b.Name, err)
		}
	}
}

// TestReadVerbErrorCodes is the forced-failure case for every converted site:
// each misuse and each classified read failure names its catalog code and the
// next command the frame prints. It is store-only apart from the two
// owner-routed cases, which read a seeded serve root and dial nothing.
func TestReadVerbErrorCodes(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	seedReadVerbErrorStore(t)

	for _, c := range []struct {
		name string
		args []string
		code errorCode
		next string
	}{
		// wait: the two value misuses and the --any spelling misuse are usage.
		{"wait timeout", []string{"wait", "--name", "errcodes", "--timeout", "0"}, codeUsage, "relevo help"},
		{"wait round", []string{"wait", "--name", "errcodes", "--round=-1"}, codeUsage, "relevo help"},
		{"wait any with name", []string{"wait", "--any", "--name", "errcodes"}, codeUsage, "relevo help"},
		// A name wait never loaded is the poll's own failure: internal.
		{"wait unknown name", []string{"wait", "--any", "nosuch", "--timeout", "1ns"}, codeInternal, ""},
		// status: --line takes no binding selector, and an unknown name is a
		// binding the store does not hold.
		{"status line with all", []string{"status", "--line", "--all"}, codeUsage, "relevo help"},
		{"status unknown name", []string{"status", "--name", "nosuch", "--json"}, codeBindingNotFound, "relevo status --all"},
		// history takes no positionals.
		{"history positional", []string{"history", "extra"}, codeUsage, "relevo help"},
		// show: every argument and flag misuse, then each classified read.
		{"show two names", []string{"show", "errcodes", "other"}, codeUsage, "relevo help"},
		{"show state without owner", []string{"show", "errcodes", "--state", "/tmp"}, codeUsage, "relevo help"},
		{"show two sections", []string{"show", "errcodes", "--prompt", "--report"}, codeUsage, "relevo help"},
		{"show owner two sections", []string{"show", "errcodes", "--owner", "alice", "--prompt", "--report"}, codeUsage, "relevo show --owner"},
		{"show stat without diff", []string{"show", "errcodes", "--stat"}, codeUsage, "relevo help"},
		{"show follow without log", []string{"show", "errcodes", "--follow"}, codeUsage, "relevo help"},
		{"show negative after", []string{"show", "errcodes", "--log", "--after=-1"}, codeUsage, "relevo help"},
		{"show unknown binding", []string{"show", "nosuch", "--report"}, codeBindingNotFound, "relevo status --all"},
		{"show log unknown binding", []string{"show", "nosuch", "--log"}, codeBindingNotFound, "relevo status --all"},
		{"show round with nothing completed", []string{"show", "errround", "--report"}, codeRoundNotFound, "relevo history"},
		{"show diff with nothing completed", []string{"show", "errround", "--diff"}, codeRoundNotFound, "relevo history"},
		{"show diff not recorded", []string{"show", "errcodes", "--diff"}, codeArtifactNotFound, "relevo history"},
		{"show unknown round", []string{"show", "errcodes", "--round", "5", "--report"}, codeInternal, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			requireCLIError(t, runErr, c.code, c.next)

			var ec exitCodeErr
			if !errors.As(runErr, &ec) || ec.code != catalogExit(c.code) {
				t.Errorf("exit = %v, want %d (code %s)", runErr, catalogExit(c.code), c.code)
			}
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty before report", stderr)
			}
		})
	}
}

// TestReadVerbErrorCodesOwnerRoute is the two owner-routed conversions: the
// same classifier serves `show --owner`, so an unknown binding read from a
// serve root is binding_not_found on both the section and the log route. It
// reads local crypto fixtures -- no listener, no network.
func TestReadVerbErrorCodesOwnerRoute(t *testing.T) {
	seedServeOwnerState(t, "alice")

	for _, c := range []struct {
		name string
		args []string
	}{
		{"owner show unknown binding", []string{"show", "nosuch", "--owner", "alice", "--report"}},
		{"owner log unknown binding", []string{"show", "nosuch", "--owner", "alice", "--log"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			requireCLIError(t, runErr, codeBindingNotFound, "relevo status --all")
			if len(stdout) != 0 {
				t.Errorf("stdout = %q, want empty before report", stdout)
			}
			if len(stderr) != 0 {
				t.Errorf("stderr = %q, want empty before report", stderr)
			}
		})
	}
}

// TestReadVerbErrorFrame pins one failure trace per converted verb: the code
// and its exit, and the next command, in both the human line and the JSON
// envelope the frame renders.
func TestReadVerbErrorFrame(t *testing.T) {
	stateHome := filepath.Join(t.TempDir(), "state")
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	for _, c := range []struct {
		verb string
		args []string
		code errorCode
		next string
	}{
		{"wait", []string{"wait", "--name", "errcodes", "--timeout", "0"}, codeUsage, "relevo help"},
		{"status", []string{"status", "--line", "--all"}, codeUsage, "relevo help"},
		{"history", []string{"history", "extra"}, codeUsage, "relevo help"},
		{"show", []string{"show", "errcodes", "--stat"}, codeUsage, "relevo help"},
	} {
		t.Run(c.verb, func(t *testing.T) {
			var human bytes.Buffer
			if code := report(&human, run(c.args), false); code != catalogExit(c.code) {
				t.Errorf("human exit = %d, want %d", code, catalogExit(c.code))
			}
			wantPrefix := fmt.Sprintf("relevo: %s: ", c.code)
			if !strings.HasPrefix(human.String(), wantPrefix) {
				t.Errorf("human line = %q, want the %q prefix", human.String(), wantPrefix)
			}
			if c.next != "" && !strings.Contains(human.String(), "\n  next: "+c.next+"\n") {
				t.Errorf("human line = %q, want the next line %q", human.String(), c.next)
			}

			var jsonBuf bytes.Buffer
			if code := report(&jsonBuf, run(c.args), true); code != catalogExit(c.code) {
				t.Errorf("json exit = %d, want %d", code, catalogExit(c.code))
			}
			var env errorEnvelope
			if err := json.Unmarshal(jsonBuf.Bytes(), &env); err != nil {
				t.Fatalf("json envelope %q: %v", jsonBuf.String(), err)
			}
			if env.Error.Code != c.code {
				t.Errorf("json code = %q, want %q", env.Error.Code, c.code)
			}
			if env.Error.Next != c.next {
				t.Errorf("json next = %q, want %q", env.Error.Next, c.next)
			}
			if env.Error.Message == "" {
				t.Error("json message is empty")
			}
		})
	}
}
