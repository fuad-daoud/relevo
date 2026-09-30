package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// encodeDoc renders one document exactly as printDoc would: one indented JSON
// object, encoder-style escaping, one trailing newline. The shape goldens are
// built from literal fixtures through the pure …DocOf builders, so no case
// needs a state directory, a harness or a network (seed discrepancy 6).
func encodeDoc(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return buf.Bytes()
}

// writeDocFixtures is the literal input for every write golden: one row per
// document the write verbs emit, plus the dry-run document, whose type carries
// its own JSON tags.
func writeDocFixtures() []struct {
	golden string
	doc    any
} {
	until := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	return []struct {
		golden string
		doc    any
	}{
		// bind: dir is the add route's worktree, deliberately different from
		// the binding's own CWD (the mutation that makes the builder fall back
		// to CWD must break this golden).
		{"bind-json", bindDocOf(store.Binding{
			Name:             "alpha",
			CWD:              "/c/alpha",
			BuilderCandidate: "claude/p/m",
			Tier:             "edit",
			State:            store.StateActive,
			Round:            2,
		}, "/w/alpha", "builder", "builder-1", false)},
		{"send-json", sendDocOf("alpha", 3, "builder-1", "edit", false)},
		{"stop-json", stopDocOf("alpha", relevo.StopResult{Round: 3, Action: "killed"})},
		{"done-json", doneDocOf("alpha", relevo.DoneResult{
			WorktreeRemoved: "/w/alpha",
			Branch:          "relevo/alpha",
		})},
		{"unbind-json", unbindDocOf("alpha", relevo.UnbindResult{
			Archived:        true,
			WorktreeRemoved: "/w/alpha",
			ProcessStopped:  4242,
		})},
		{"unbind-done-json", gcDocOf(true, []relevo.GCResult{{
			Name:         "alpha",
			CWD:          "/c/alpha",
			Rounds:       3,
			Archived:     true,
			MasterMindID: "pl_alpha",
		}})},
		{"unbind-sweep-json", sweepDocOf(false, relevo.SweepResult{
			Dir: "/repo",
			Refs: []relevo.RefOutcome{{
				Ref:     "refs/heads/relevo/alpha",
				Deleted: true,
			}},
		})},
		// gate set: a real expiry, so the RFC3339 field is pinned.
		{"gate-set-json", gateSetDocOf("anthropic", until, 3)},
		{"gate-clear-json", gateClearDocOf("anthropic", 3, 2)},
		// gate --serve: the same two shapes on the server ledger's own values,
		// one open-ended and one clear that removed nothing (which the removed
		// pointer still prints).
		{"gate-serve-set-json", gateSetDocOf("openai", time.Time{}, 1)},
		{"gate-serve-clear-json", gateClearDocOf("openai", 1, 0)},
		// send --dry-run: relevo.DryRun printed as-is, the one source its
		// RenderDryRun text also reads.
		{"send-dryrun-json", relevo.DryRun{
			Name:          "alpha",
			Round:         3,
			Mode:          "headless",
			Candidate:     "claude/p/m",
			CandidateName: "builder-1",
			Where:         "/usr/local/bin/claude --print",
			PromptPath:    "/s/alpha/003-plan.md",
			PromptFrom:    "/tmp/plan.md",
			PromptBytes:   12,
			ReportPath:    "/s/alpha/003-report.md",
			DonePath:      "/s/alpha/003-done",
			Tier:          "edit",
			PromptHead:    []string{"# plan", "do the thing"},
		}},
	}
}

// TestContractWriteShape pins every write result document from literal
// fixtures through its pure builder. It is the shape half of the write
// contract: the CLI half (one document, empty stderr) is pinned by the
// state-only verb tests below.
func TestContractWriteShape(t *testing.T) {
	for _, c := range writeDocFixtures() {
		t.Run(c.golden, func(t *testing.T) {
			assertGolden(t, c.golden, encodeDoc(t, c.doc))
		})
	}
}

// seedWriteBinding saves one store-only binding: no worktree, no branch, no
// process. The state-only verbs therefore never touch git, a harness or a
// network (the round's CI rule).
func seedWriteBinding(t *testing.T, name string, b store.Binding) {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	b.Name = name
	if b.CWD == "" {
		b.CWD = filepath.Join(root, "work", name)
	}
	if b.State == "" {
		b.State = store.StateActive
	}
	if err := s.Save(b); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
}

// assertWriteJSON runs one write verb in --json mode and returns its stdout
// after asserting the CLI half of the contract: no error, stderr empty, and
// stdout exactly one JSON object.
func assertWriteJSON(t *testing.T, args ...string) []byte {
	t.Helper()
	stdout, stderr, err := captureOutput(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
	}
	if len(stderr) != 0 {
		t.Errorf("%v: stderr = %q, want empty", args, stderr)
	}
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("%v: stdout = %q, want exactly one JSON object", args, stdout)
	}
	return stdout
}

// assertHumanUnchanged runs one write verb's human default and asserts it
// still prints its human line: non-empty stdout, not a document, carrying the
// text the plan's field list derives the document from. The byte-identical
// guarantee is carried by the untouched human branches; the approved golden
// list has no write human goldens, so this pins the shape instead.
func assertHumanUnchanged(t *testing.T, wantText string, args ...string) {
	t.Helper()
	stdout, stderr, err := captureOutput(t, func() error { return run(args) })
	if err != nil {
		t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
	}
	if len(stdout) == 0 {
		t.Fatalf("%v: stdout is empty, want the human line", args)
	}
	if json.Valid(stdout) {
		t.Errorf("%v: stdout = %q, want the human line, not a document", args, stdout)
	}
	if !strings.Contains(string(stdout), wantText) {
		t.Errorf("%v: stdout = %q, want it to contain %q", args, stdout, wantText)
	}
}

// TestContractWriteJSONStateVerbs pins the CLI half of the write contract on
// the four verbs a store-only fixture can reach end to end: one document on
// stdout with empty stderr under --json, and the unchanged human line on the
// same fixture without it. bind and send cannot be reached end to end here
// (they spawn a harness), so their shape is pinned by the builders above and
// their CLI plumbing by the forced-failure table below (seed discrepancy 6).
func TestContractWriteJSONStateVerbs(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		docsEnv(t)
		seedWriteBinding(t, "alpha", store.Binding{Round: 2})

		stdout := assertWriteJSON(t, "done", "alpha", "--json")
		var doc DoneDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("done --json: %v", err)
		}
		if doc.Name != "alpha" {
			t.Errorf("name = %q, want alpha", doc.Name)
		}
		if doc.Released {
			t.Errorf("released = true, want false for a binding with no worktree")
		}
		if doc.Worktree != "" || doc.KeptReason != "" || doc.Branch != "" {
			t.Errorf("worktree fields = %+v, want them empty", doc)
		}

		docsEnv(t)
		seedWriteBinding(t, "beta", store.Binding{Round: 1})
		assertHumanUnchanged(t, "beta", "done", "beta")
	})

	t.Run("stop", func(t *testing.T) {
		docsEnv(t)
		// No RoundStartedAt, so there is no open round: stop answers
		// "nothing" without touching a process.
		seedWriteBinding(t, "gamma", store.Binding{Round: 2})

		stdout := assertWriteJSON(t, "stop", "gamma", "--json")
		var doc StopDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("stop --json: %v", err)
		}
		if doc.Name != "gamma" || doc.Round != 2 {
			t.Errorf("doc = %+v, want gamma at round 2", doc)
		}
		if doc.Action != "nothing" || doc.Killed {
			t.Errorf("action = %q killed = %v, want nothing/false", doc.Action, doc.Killed)
		}

		docsEnv(t)
		seedWriteBinding(t, "delta", store.Binding{Round: 2})
		assertHumanUnchanged(t, "delta", "stop", "delta")
	})

	t.Run("unbind", func(t *testing.T) {
		docsEnv(t)
		seedWriteBinding(t, "epsilon", store.Binding{Round: 2})

		stdout := assertWriteJSON(t, "unbind", "epsilon", "--json")
		var doc UnbindDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("unbind --json: %v", err)
		}
		if doc.Name != "epsilon" || doc.Archived || doc.Released || doc.ProcessStopped != 0 {
			t.Errorf("doc = %+v, want a plain unbound binding", doc)
		}

		docsEnv(t)
		seedWriteBinding(t, "zeta", store.Binding{Round: 2})
		assertHumanUnchanged(t, "zeta", "unbind", "zeta")
	})

	t.Run("gate set", func(t *testing.T) {
		docsEnv(t)
		setDocConfig(t)

		stdout := assertWriteJSON(t, "gate", "claude/p/m", "--for", "1h", "--json")
		var doc GateDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("gate --json: %v", err)
		}
		if doc.Subject != "p" || doc.Candidates != 1 || doc.Mode != "gated" {
			t.Errorf("doc = %+v, want provider p with one candidate", doc)
		}
		if doc.Until == "" {
			t.Error("until is empty, want the 1h expiry")
		}
		if doc.Removed != nil {
			t.Errorf("removed = %v, want unset on a set", *doc.Removed)
		}

		docsEnv(t)
		setDocConfig(t)
		assertHumanUnchanged(t, "gated p", "gate", "claude/p/m", "--for", "1h")
	})

	t.Run("gate clear", func(t *testing.T) {
		docsEnv(t)
		setDocConfig(t)
		if _, _, err := captureOutput(t, func() error { return run([]string{"gate", "claude/p/m"}) }); err != nil {
			t.Fatalf("gate set: %v", err)
		}

		stdout := assertWriteJSON(t, "gate", "--clear", "claude/p/m", "--json")
		var doc GateDoc
		if err := json.Unmarshal(stdout, &doc); err != nil {
			t.Fatalf("gate --clear --json: %v", err)
		}
		if doc.Subject != "p" || doc.Mode != "gated" {
			t.Errorf("doc = %+v, want provider p", doc)
		}
		if doc.Removed == nil || *doc.Removed != 1 {
			t.Errorf("removed = %v, want one entry cleared", doc.Removed)
		}

		docsEnv(t)
		setDocConfig(t)
		assertHumanUnchanged(t, "nothing was gating p", "gate", "--clear", "claude/p/m")
	})
}

// TestContractWriteNoticeRouting pins the stream rule at the one place it is
// decided: a supplementary line goes to stdout for the human default and to
// stderr under --json, where stdout carries the document alone. It also pins
// noteRegateNoGate's own conditions, since bind --json cannot be reached end to
// end here (it spawns a harness).
func TestContractWriteNoticeRouting(t *testing.T) {
	if noticeWriter(false) != os.Stdout {
		t.Error("noticeWriter(false) is not stdout")
	}
	if noticeWriter(true) != os.Stderr {
		t.Error("noticeWriter(true) is not stderr")
	}

	var buf bytes.Buffer
	noteRegateNoGate(&buf, store.Binding{Regate: 2})
	if !strings.Contains(buf.String(), "regate 2") {
		t.Errorf("noteRegateNoGate wrote %q, want the regate line", buf.String())
	}

	// A binding with a gate, and one with no budget, are both silent.
	buf.Reset()
	noteRegateNoGate(&buf, store.Binding{Regate: 2, Gate: "make check"})
	noteRegateNoGate(&buf, store.Binding{})
	if buf.Len() != 0 {
		t.Errorf("noteRegateNoGate wrote %q, want nothing", buf.String())
	}
}

// TestContractWriteRegistryExits is the machine half of the exit-code audit
// (spec 2.6): for every converted write verb, every code its registry row
// declares must be a catalog code whose exit that row's own exit list carries.
// It fails when a row names a code whose exit the row cannot produce, and on
// any added code without a matching exit.
func TestContractWriteRegistryExits(t *testing.T) {
	for _, name := range []string{
		"bind", "send", "stop", "done", "unbind", "gate",
		"config agents", "config import", "config init", "config secret rm",
		"config secret set", "config server add", "config server rm", "config set",
		"config unset", "mastermind disable", "mastermind enable", "mastermind forget",
		"mastermind init", "mastermind rename", "mastermind reset",
		"serve enroll", "serve gc", "serve init", "serve revoke", "serve unbind",
		"update",
	} {
		e, ok := registryEntry(name)
		if !ok {
			t.Errorf("no registry entry for %q", name)
			continue
		}
		if len(e.Errors) == 0 {
			t.Errorf("%q names no error codes", name)
		}
		for _, code := range e.Errors {
			entry, ok := catalog[errorCode(code)]
			if !ok {
				t.Errorf("%q: code %q has no catalog row", name, code)
				continue
			}
			if !slices.Contains(e.Exit, entry.exit) {
				t.Errorf("%q: code %q exits %d, which its exit list %v does not carry", name, code, entry.exit, e.Exit)
			}
		}
	}
}

// TestContractWriteUnclassifiedIsInternal pins the other half of the audit: an
// error the write classifier does not recognise becomes internal, never an
// invented code, so no converted site can emit a code the catalog does not
// list (spec 4).
func TestContractWriteUnclassifiedIsInternal(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want errorCode
	}{
		{"missing binding", fmt.Errorf("load: %w", store.ErrNotFound), codeBindingNotFound},
		{"working tree taken", fmt.Errorf("bind: %w", store.ErrCWDTaken), codeConflict},
		{"tier above max", fmt.Errorf("bind: %w", relevo.ErrTierAboveMax), codeTierCap},
		{"everything gated", fmt.Errorf("bind: %w", relevo.ErrAllGated), codeGateActive},
		{"no candidate serves the actor", fmt.Errorf("bind: %w", relevo.ErrNoCandidates), codePolicyRefused},
		{"unclassified", errors.New("something nobody classified"), codeInternal},
	} {
		t.Run(c.name, func(t *testing.T) {
			var ce *cliError
			if !errors.As(writeError(c.err), &ce) {
				t.Fatalf("writeError(%v) is not a coded error", c.err)
			}
			if ce.code != c.want {
				t.Errorf("code = %q, want %q", ce.code, c.want)
			}
			if _, ok := catalog[ce.code]; !ok {
				t.Errorf("code %q is not in the catalog", ce.code)
			}
			if ce.message == "" {
				t.Error("message is empty")
			}
		})
	}
}

// TestContractWriteInternalExit is the frame-level companion: the internal code
// an unclassified write failure earns renders with exit 1 and the bundle
// command as its next hint.
func TestContractWriteInternalExit(t *testing.T) {
	var buf bytes.Buffer
	code := report(&buf, writeError(errors.New("boom")), false)
	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	want := "relevo: internal: boom\n  next: relevo bugreport\n"
	if got := buf.String(); got != want {
		t.Errorf("report wrote %q, want %q", got, want)
	}
}

// converted write site that a fixture can reach prints nothing on stdout and
// nothing on stderr before report, and its coded error carries the catalog's
// exit and next hint. One row per verb, plus one converted non-state site.
func TestContractWriteForcedFailures(t *testing.T) {
	docsEnv(t)

	cases := []struct {
		name string
		args []string
		code errorCode
		next string
	}{
		// The binding-missing family: a write verb pointed at a name the store
		// does not hold.
		{"done unknown binding", []string{"done", "nosuch", "--json"}, codeBindingNotFound, "relevo status --all"},
		{"stop unknown binding", []string{"stop", "nosuch", "--json"}, codeBindingNotFound, "relevo status --all"},
		{"unbind unknown binding", []string{"unbind", "nosuch", "--json"}, codeBindingNotFound, "relevo status --all"},
		// The usage family: a missing binding, a missing --file and a gate
		// misuse.
		{"done no binding", []string{"done", "--json"}, codeUsage, "relevo help"},
		{"stop no binding", []string{"stop", "--json"}, codeUsage, "relevo help"},
		{"unbind no binding", []string{"unbind", "--json"}, codeUsage, "relevo help"},
		{"gate misuse", []string{"gate", "one", "two", "--json"}, codeUsage, "relevo help"},
		{"send no file", []string{"send", "--json"}, codeUsage, "relevo help"},
		// The refusal family: the flag contradictions, which exit 2 with no
		// next hint.
		{"send verify conflict", []string{"send", "--verify", "--no-verify", "--file", "p.md", "--json"}, codeRefused, ""},
		{"bind no feature choice", []string{"bind", "--name", "x", "--json"}, codeRefused, ""},
		{"unbind sweep takes no binding", []string{"unbind", "--sweep", "x", "--json"}, codeRefused, ""},
		{"done pick names one", []string{"done", "--pick", "x", "--json"}, codeRefused, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, runErr := captureOutput(t, func() error { return run(c.args) })
			ce := requireCLIError(t, runErr, c.code, c.next)

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
			if ce.message == "" {
				t.Error("message is empty")
			}
		})
	}
}

// TestSendRefusedErrorIsAConflict pins the sentinel mapping: a manual send
// refused because the binding belongs to a running chain is a conflict, so a
// script can tell "the chain owns it" from an internal failure.
func TestSendRefusedErrorIsAConflict(t *testing.T) {
	wrapped := fmt.Errorf("binding %q belongs to running chain %s; relevo stop %s first: %w",
		"shop", "shop", "shop", relevo.ErrRunningChainMember)

	var ce *cliError
	if !errors.As(writeError(wrapped), &ce) {
		t.Fatalf("writeError(%v) is not a coded error", wrapped)
	}
	if ce.code != codeConflict {
		t.Errorf("code = %q, want %q", ce.code, codeConflict)
	}
	if _, ok := catalog[ce.code]; !ok {
		t.Errorf("code %q is not in the catalog", ce.code)
	}
}
