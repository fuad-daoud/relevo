package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeVerbs is a Verbs whose methods are swappable per test; a nil field returns (nil, nil).
type fakeVerbs struct {
	statusFn func(ctx context.Context, session string, a StatusArgs) (any, error)
	sendFn   func(ctx context.Context, session string, a SendArgs) (any, error)
	doneFn   func(ctx context.Context, session string, a DoneArgs) (any, error)
	showFn   func(ctx context.Context, session string, a ShowArgs) (any, error)
	gateFn   func(ctx context.Context, session string, a GateArgs) (any, error)
	waitFn   func(ctx context.Context, session string, a WaitArgs) (any, error)
}

func (f *fakeVerbs) Status(ctx context.Context, session string, a StatusArgs) (any, error) {
	if f.statusFn == nil {
		return nil, nil
	}
	return f.statusFn(ctx, session, a)
}

func (f *fakeVerbs) Send(ctx context.Context, session string, a SendArgs) (any, error) {
	if f.sendFn == nil {
		return nil, nil
	}
	return f.sendFn(ctx, session, a)
}

func (f *fakeVerbs) Done(ctx context.Context, session string, a DoneArgs) (any, error) {
	if f.doneFn == nil {
		return nil, nil
	}
	return f.doneFn(ctx, session, a)
}

func (f *fakeVerbs) Show(ctx context.Context, session string, a ShowArgs) (any, error) {
	if f.showFn == nil {
		return nil, nil
	}
	return f.showFn(ctx, session, a)
}

func (f *fakeVerbs) Gate(ctx context.Context, session string, a GateArgs) (any, error) {
	if f.gateFn == nil {
		return nil, nil
	}
	return f.gateFn(ctx, session, a)
}

func (f *fakeVerbs) Wait(ctx context.Context, session string, a WaitArgs) (any, error) {
	if f.waitFn == nil {
		return nil, nil
	}
	return f.waitFn(ctx, session, a)
}

// runServer drives Serve over an in-memory pipe, one line per request, and returns everything it wrote.
func runServer(t *testing.T, verbs Verbs, requests []string) []byte {
	t.Helper()
	return runServerWith(t, &Server{Verbs: verbs, Version: "0.6.0-test"}, requests)
}

// runServerWith is runServer over a caller-built Server, for tests that need a Notice or other field runServer leaves zero.
func runServerWith(t *testing.T, srv *Server, requests []string) []byte {
	t.Helper()
	pr, pw := io.Pipe()
	var out bytes.Buffer

	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, &out) }()

	for _, line := range requests {
		if _, err := pw.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out.Bytes()
}

func splitLines(b []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func decodeResponse(t *testing.T, line []byte) Response {
	t.Helper()
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response %s: %v", line, err)
	}
	return resp
}

func TestServerInitializePinsProtocolVersionAndAdvertisesChannel(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28"}}`,
	})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1: %s", len(lines), out)
	}
	resp := decodeResponse(t, lines[0])
	if resp.Error != nil {
		t.Fatalf("initialize error = %+v", resp.Error)
	}

	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", resp.Result)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v (the client offered 2026-07-28)", result["protocolVersion"], ProtocolVersion)
	}

	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", result["capabilities"])
	}
	exp, ok := caps["experimental"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities.experimental = %#v", caps["experimental"])
	}
	if _, ok := exp["claude/channel"]; !ok {
		t.Errorf("capabilities.experimental must contain claude/channel, got %#v", exp)
	}
}

func TestServerInitializeSameVersionAnswersSame(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	result := resp.Result.(map[string]any)
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v", result["protocolVersion"], ProtocolVersion)
	}
}

func TestServerToolsListOrderAndStrictSchemasPerMode(t *testing.T) {
	tests := []struct {
		name      string
		mode      Mode
		kind      string
		wantOrder []string
	}{
		{"tools mode adds wait after send", ModeTools, "", []string{"status", "send", "wait", "done", "show", "gate"}},
		{"channel mode lists the five base verbs", ModeChannel, "", []string{"status", "send", "done", "show", "gate"}},
		{"opencode lists the five base verbs", ModeTools, "opencode", []string{"status", "send", "done", "show", "gate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runServerWith(t, &Server{Verbs: &fakeVerbs{}, Version: "0.6.0-test", Mode: tt.mode, Kind: tt.kind}, []string{
				`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			})
			resp := decodeResponse(t, splitLines(out)[0])
			result := resp.Result.(map[string]any)
			tools, ok := result["tools"].([]any)
			if !ok || len(tools) != len(tt.wantOrder) {
				t.Fatalf("tools = %#v, want %d entries", result["tools"], len(tt.wantOrder))
			}

			for i, raw := range tools {
				tool, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("tools[%d] = %#v, want an object", i, raw)
				}
				if tool["name"] != tt.wantOrder[i] {
					t.Errorf("tools[%d].name = %v, want %v", i, tool["name"], tt.wantOrder[i])
				}
				schema, ok := tool["inputSchema"].(map[string]any)
				if !ok {
					t.Fatalf("tools[%d].inputSchema = %#v", i, tool["inputSchema"])
				}
				if ap, ok := schema["additionalProperties"].(bool); !ok || ap {
					t.Errorf("tools[%d].inputSchema.additionalProperties = %#v, want false", i, schema["additionalProperties"])
				}
			}
		})
	}
}

func TestServerToolsCallDoneErrorBecomesIsError(t *testing.T) {
	verbs := &fakeVerbs{
		doneFn: func(ctx context.Context, _ string, a DoneArgs) (any, error) {
			return nil, errors.New("boom")
		},
	}
	out := runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"done","arguments":{"name":"judge"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("want no JSON-RPC error for a verb failure, got %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("result = %#v, want isError true", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one block", result["content"])
	}
	block := content[0].(map[string]any)
	if block["text"] != "boom" {
		t.Errorf("text = %v, want %q (the error verbatim)", block["text"], "boom")
	}
}

func TestServerToolsCallDoneSuccess(t *testing.T) {
	verbs := &fakeVerbs{
		doneFn: func(ctx context.Context, _ string, a DoneArgs) (any, error) {
			return map[string]any{"ok": true, "name": a.Name}, nil
		},
	}
	out := runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"done","arguments":{"name":"judge"}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("error = %+v", resp.Error)
	}
	result := resp.Result.(map[string]any)
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("result = %#v, want isError false/absent", result)
	}
}

// TestServerToolsCallShowAndGateValidateArgs pins the two new tools' required
// argument: a missing name or token is an invalid-params error before any verb
// is called.
func TestServerToolsCallShowAndGateValidateArgs(t *testing.T) {
	cases := []struct {
		name    string
		request string
	}{
		{"show without a name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"show","arguments":{}}}`},
		{"gate without a token", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"gate","arguments":{}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := runServer(t, &fakeVerbs{}, []string{tc.request})
			resp := decodeResponse(t, splitLines(out)[0])
			if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
				t.Fatalf("error = %+v, want code %d", resp.Error, CodeInvalidParams)
			}
		})
	}
}

func TestServerToolsCallUnknownTool(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bogus","arguments":{}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeInvalidParams)
	}
}

func TestServerUnknownMethodWithIDIsMethodMissing(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","id":1,"method":"bogus"}`})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error == nil || resp.Error.Code != CodeMethodMissing {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeMethodMissing)
	}
}

func TestServerUnknownNotificationIsIgnored(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","method":"bogus"}`})
	if lines := splitLines(out); len(lines) != 0 {
		t.Fatalf("an unknown notification must produce no response, got %q", lines)
	}
}

func TestServerUnparseableLineIsParseError(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`not json`})
	lines := splitLines(out)
	if len(lines) != 1 {
		t.Fatalf("responses = %d, want 1", len(lines))
	}
	resp := decodeResponse(t, lines[0])
	if resp.Error == nil || resp.Error.Code != CodeParse {
		t.Fatalf("error = %+v, want code %d", resp.Error, CodeParse)
	}
	if string(resp.ID) != "null" {
		t.Errorf("id = %s, want null", resp.ID)
	}
}

func TestServerPingRepliesEmptyObject(t *testing.T) {
	out := runServer(t, &fakeVerbs{}, []string{`{"jsonrpc":"2.0","id":1,"method":"ping"}`})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("ping error = %+v", resp.Error)
	}
}

func TestServerOnInitializedFiresAfterNotification(t *testing.T) {
	fired := false
	srv := &Server{Verbs: &fakeVerbs{}, OnInitialized: func() { fired = true }}

	pr, pw := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, &out) }()

	if _, err := pw.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := pw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}

	if !fired {
		t.Error("OnInitialized must fire after notifications/initialized")
	}
	if len(splitLines(out.Bytes())) != 0 {
		t.Error("notifications/initialized must produce no reply")
	}
}

func TestServerPushEmitsNotificationAndDropsBadKey(t *testing.T) {
	var out bytes.Buffer
	srv := &Server{Verbs: &fakeVerbs{}}
	srv.out = &out

	if err := srv.Push(context.Background(), "hello", map[string]string{"binding": "judge", "bad-key": "x"}); err != nil {
		t.Fatalf("Push: %v", err)
	}

	var note struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  struct {
			Content string            `json:"content"`
			Meta    map[string]string `json:"meta"`
		} `json:"params"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &note); err != nil {
		t.Fatalf("unmarshal push: %v", err)
	}
	if note.Method != "notifications/claude/channel" {
		t.Errorf("method = %q, want notifications/claude/channel", note.Method)
	}
	if note.Params.Content != "hello" {
		t.Errorf("content = %q, want %q", note.Params.Content, "hello")
	}
	if _, ok := note.Params.Meta["bad-key"]; ok {
		t.Errorf("meta must drop the non-identifier key, got %+v", note.Params.Meta)
	}
	if note.Params.Meta["binding"] != "judge" {
		t.Errorf("meta must keep the identifier key, got %+v", note.Params.Meta)
	}
}

// TestServerAppendsNoticeToToolResults: a verb error's result gets the notice block exactly as a success does.
func TestServerAppendsNoticeToToolResults(t *testing.T) {
	const notice = "note: relevo was upgraded to v0.8.0; this session's relevo MCP server is still v0.7.0. Reconnect it (/mcp) or restart the session to load the new version."

	srv := &Server{
		Verbs: &fakeVerbs{
			statusFn: func(context.Context, string, StatusArgs) (any, error) {
				return map[string]string{"state": "running"}, nil
			},
			doneFn: func(context.Context, string, DoneArgs) (any, error) {
				return nil, errors.New("no binding")
			},
		},
		Version: "0.7.0",
		Notice:  func() string { return notice },
	}

	out := runServerWith(t, srv, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"done","arguments":{"name":"webshop"}}}`,
	})
	lines := splitLines(out)
	if len(lines) != 2 {
		t.Fatalf("responses = %d, want 2: %s", len(lines), out)
	}

	for i, wantIsError := range []bool{false, true} {
		resp := decodeResponse(t, lines[i])
		if resp.Error != nil {
			t.Fatalf("response %d carried a JSON-RPC error: %+v", i+1, resp.Error)
		}
		result, ok := resp.Result.(map[string]any)
		if !ok {
			t.Fatalf("result %d = %#v, want an object", i+1, resp.Result)
		}
		content, ok := result["content"].([]any)
		if !ok {
			t.Fatalf("content %d = %#v, want an array", i+1, result["content"])
		}
		if len(content) != 2 {
			t.Fatalf("content blocks = %d, want 2 (the result plus the notice): %#v", len(content), content)
		}
		block, ok := content[1].(map[string]any)
		if !ok {
			t.Fatalf("notice block = %#v, want an object", content[1])
		}
		if block["type"] != "text" || block["text"] != notice {
			t.Errorf("notice block = %#v, want {type: text, text: %q}", block, notice)
		}
		if gotErr, _ := result["isError"].(bool); gotErr != wantIsError {
			t.Errorf("result %d isError = %v, want %v", i+1, gotErr, wantIsError)
		}
	}
}

func TestServerNoticeNilOrEmptyIsUnchanged(t *testing.T) {
	verbs := func() Verbs {
		return &fakeVerbs{statusFn: func(context.Context, string, StatusArgs) (any, error) {
			return map[string]string{"state": "running"}, nil
		}}
	}
	req := []string{`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{}}}`}

	noNotice := runServer(t, verbs(), req)
	emptyNotice := runServerWith(t, &Server{
		Verbs:   verbs(),
		Version: "0.6.0-test",
		Notice:  func() string { return "" },
	}, req)

	if !bytes.Equal(noNotice, emptyNotice) {
		t.Errorf("a Notice that returns \"\" changed the output:\n%s\nvs\n%s", noNotice, emptyNotice)
	}

	resp := decodeResponse(t, splitLines(noNotice)[0])
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", resp.Result)
	}
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Errorf("content blocks = %d, want 1: %#v", len(content), content)
	}
}

// TestServerPassesCallSessionFromMeta pins the opencode contract: the harness
// session in a tools/call's _meta reaches the verbs under opencode's
// namespaced key, a bare sessionID is the fallback, the namespaced key wins
// when both are present, and a call without _meta passes "".
func TestServerPassesCallSessionFromMeta(t *testing.T) {
	cases := []struct {
		name string
		meta string
		want string
	}{
		{"namespaced key", `,"_meta":{"ai.opencode/sessionID":"ses_ns"}`, "ses_ns"},
		{"bare key fallback", `,"_meta":{"sessionID":"ses_bare"}`, "ses_bare"},
		{"namespaced wins with both keys", `,"_meta":{"sessionID":"ses_bare","ai.opencode/sessionID":"ses_ns"}`, "ses_ns"},
		{"no meta", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got string
			verbs := &fakeVerbs{statusFn: func(_ context.Context, session string, _ StatusArgs) (any, error) {
				got = session
				return map[string]string{"state": "running"}, nil
			}}

			params := `{"name":"status","arguments":{}` + tc.meta + `}`
			runServer(t, verbs, []string{
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}`,
			})
			if got != tc.want {
				t.Errorf("verb session = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServerStatusWithoutSessionIsAToolError pins the opencode server's
// sessionless status: it is a tool error naming the key it looks for, never a
// silently empty binding list, and the resolver is never called; all:true still
// lists every binding without any identity.
func TestServerStatusWithoutSessionIsAToolError(t *testing.T) {
	s := store.New(t.TempDir())
	rt := relevo.Runtime{Store: s, Now: func() time.Time { return time.Unix(0, 0) }}
	saveVerbBinding(t, s, store.Binding{Name: "mine", CWD: "/repo/mine", MasterMindID: mcpTestMasterMindA, Round: 1, State: store.StateActive})

	calls := 0
	verbs := &RelevoVerbs{RT: rt, ResolveSession: func(string) (string, error) {
		calls++
		return mcpTestMasterMindA, nil
	}}

	out := runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{}}}`,
	})
	resp := decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("want a tool error, not a JSON-RPC error, got %+v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", resp.Result)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("result = %#v, want isError true", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want one block", result["content"])
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "ai.opencode/sessionID") {
		t.Errorf("tool error = %q, want it to name ai.opencode/sessionID", text)
	}
	if strings.Contains(text, "bindings") {
		t.Errorf("a sessionless status must not carry a binding list, got %q", text)
	}
	if calls != 0 {
		t.Errorf("resolver called %d times for a sessionless call, want 0", calls)
	}

	out = runServer(t, verbs, []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":{"all":true}}}`,
	})
	resp = decodeResponse(t, splitLines(out)[0])
	if resp.Error != nil {
		t.Fatalf("all:true carried a JSON-RPC error: %+v", resp.Error)
	}
	result, ok = resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("all:true result = %#v, want an object", resp.Result)
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("all:true result = %#v, want isError false", result)
	}
	content, ok = result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("all:true content = %#v, want one block", result["content"])
	}
	body, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(body, `"bindings"`) || !strings.Contains(body, "mine") {
		t.Errorf("all:true body = %q, want the seeded binding", body)
	}
}
