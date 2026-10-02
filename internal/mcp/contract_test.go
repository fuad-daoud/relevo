package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// update rewrites every testdata/contract/*.golden file this test binary touches.
var update = flag.Bool("update", false, "rewrite testdata/contract/*.golden")

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)

	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./internal/mcp -run Contract -update' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./internal/mcp -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s",
			path, got, want)
	}
}

// TestContractToolsList pins the tools/list result via one golden file.
func TestContractToolsList(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1: %s", len(lines), out)
	}
	resp := decodeResponse(t, lines[0])
	raw, err := json.MarshalIndent(resp.Result, "", "  ")
	if err != nil {
		t.Fatalf("marshal tools/list result: %v", err)
	}
	assertGolden(t, "tools-list", raw)
}

func TestContractInstructions(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1: %s", len(lines), out)
	}
	resp := decodeResponse(t, lines[0])
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal initialize result: %v", err)
	}
	var envelope struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	assertGolden(t, "instructions", []byte(envelope.Instructions))
}

// TestContractToolResults pins the result text of status, send (dry_run:
// true) and done over a fakeVerbs: what is under test is the wrapping
// (JSON indentation, the background-wait line), not the verbs' own computation.
func TestContractToolResults(t *testing.T) {
	cases := []struct {
		golden  string
		request string
		verbs   *fakeVerbs
	}{
		{
			"tool-status",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
			&fakeVerbs{statusFn: func(context.Context, string, StatusArgs) (any, error) {
				return map[string]any{
					"bindings": []map[string]any{
						{"name": "webshop", "cwd": "/repo/webshop", "round": 2, "state": "active"},
					},
				}, nil
			}},
		},
		{
			"tool-send",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"send","arguments":{"name":"webshop","file":"/tmp/plan.md","dry_run":true}}}`,
			&fakeVerbs{sendFn: func(context.Context, string, SendArgs) (any, error) {
				return map[string]any{"dry_run": true, "would_send": true, "round": 2}, nil
			}},
		},
		{
			"tool-done",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"done","arguments":{"name":"webshop"}}}`,
			&fakeVerbs{doneFn: func(context.Context, string, DoneArgs) (any, error) {
				return map[string]any{"branch": "feature/webshop", "worktree_removed": "/tmp/webshop"}, nil
			}},
		},
	}

	for _, c := range cases {
		out := runServer(t, c.verbs, []string{c.request})
		lines := splitLines(out)
		if len(lines) != 1 {
			t.Fatalf("%s: responses = %d, want 1: %s", c.golden, len(lines), out)
		}
		resp := decodeResponse(t, lines[0])
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("%s: marshal tool result: %v", c.golden, err)
		}
		var result ToolResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("%s: decode tool result: %v", c.golden, err)
		}
		if len(result.Content) == 0 {
			t.Fatalf("%s: tool result has no content", c.golden)
		}
		assertGolden(t, c.golden, []byte(result.Content[0].Text))
	}
}

// TestContractShowAndGateToolResults pins the two new tools' result text over
// real RelevoVerbs: a seeded store, candidate set and gates database are the
// only inputs -- no harness, no network.
func TestContractShowAndGateToolResults(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	showVerbs := &RelevoVerbs{RT: relevo.Runtime{Store: newShowVerbStore(t), Now: func() time.Time { return now }}}
	gateVerbs := &RelevoVerbs{
		RT: relevo.Runtime{
			Store:      store.New(t.TempDir()),
			Candidates: writeCandidates(t, `[{"harness":"agy","provider":"test","model":"m","roles":["builder"]}]`),
			Gates:      testGateKV(t),
			Now:        func() time.Time { return now },
		},
		MasterMind: mcpTestMasterMindA,
	}

	cases := []struct {
		golden  string
		request string
		verbs   *RelevoVerbs
	}{
		{
			"tool-show",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"show","arguments":{"name":"webshop"}}}`,
			showVerbs,
		},
		{
			"tool-gate",
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gate","arguments":{"token":"agy/test/m","for":"2h","reason":"429 from the provider"}}}`,
			gateVerbs,
		},
	}

	for _, c := range cases {
		out := runServer(t, c.verbs, []string{c.request})
		lines := splitLines(out)
		if len(lines) != 1 {
			t.Fatalf("%s: responses = %d, want 1: %s", c.golden, len(lines), out)
		}
		resp := decodeResponse(t, lines[0])
		raw, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("%s: marshal tool result: %v", c.golden, err)
		}
		var result ToolResult
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("%s: decode tool result: %v", c.golden, err)
		}
		if len(result.Content) == 0 {
			t.Fatalf("%s: tool result has no content", c.golden)
		}
		assertGolden(t, c.golden, []byte(result.Content[0].Text))
	}
}

// TestContractWaitToolResult pins the wait tool's result text. The call runs
// in its own goroutine, so the server is driven over a pipe that stays open
// until the response lands rather than one closed at once.
func TestContractWaitToolResult(t *testing.T) {
	verbs := &fakeVerbs{
		waitFn: func(_ context.Context, _ string, a WaitArgs) (any, error) {
			if a.Name != "webshop" {
				t.Errorf("wait name = %q, want webshop", a.Name)
			}
			return "webshop round 2 closed\nrunner report body", nil
		},
	}
	srv := &Server{Verbs: verbs, Version: "0.6.0-test", Mode: ModeTools}
	is := newInteractiveServer(t, srv)

	is.write(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop","timeout":"10s"}}}`)

	line := is.readLine(t, 5*time.Second)
	resp := decodeResponse(t, []byte(line))
	if resp.Error != nil {
		t.Fatalf("wait call error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal wait tool result: %v", err)
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode wait tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("wait tool result has no content")
	}
	assertGolden(t, "tool-wait", []byte(result.Content[0].Text))
}
