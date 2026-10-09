package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type interactiveServer struct {
	srv   *Server
	in    *bufio.Scanner
	write func(string)
	done  chan error
}

func newInteractiveServer(t *testing.T, srv *Server) *interactiveServer {
	t.Helper()
	reqR, reqW := io.Pipe()
	respR, respW := io.Pipe()

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		done <- srv.Serve(ctx, reqR, respW)
	}()

	scanner := bufio.NewScanner(respR)
	t.Cleanup(func() {
		cancel()
		_ = reqW.Close()
		_ = respR.Close()
	})

	writeFn := func(line string) {
		if _, err := reqW.Write([]byte(line + "\n")); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}

	return &interactiveServer{srv: srv, in: scanner, write: writeFn, done: done}
}

func (s *interactiveServer) readLine(t *testing.T, timeout time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		if s.in.Scan() {
			ch <- s.in.Text()
		}
	}()

	select {
	case line := <-ch:
		return line
	case <-time.After(timeout):
		t.Fatalf("timed out after %s waiting for response line", timeout)
		return ""
	}
}

func TestServerListingGating(t *testing.T) {
	t.Parallel()

	hasTool := func(tools []ToolSpec, name string) bool {
		for _, tool := range tools {
			if tool.Name == name {
				return true
			}
		}
		return false
	}

	t.Run("tools mode lists wait", func(t *testing.T) {
		srv := &Server{Verbs: &fakeVerbs{}, Version: "test", Mode: ModeTools}
		tools := ToolsFor(srv.Mode, srv.Kind)
		if len(tools) != 6 {
			t.Fatalf("len(tools) = %d, want 6", len(tools))
		}
		if !hasTool(tools, "wait") {
			t.Fatal("tools mode must list wait")
		}
	})

	t.Run("opencode omits wait and refuses calls", func(t *testing.T) {
		srv := &Server{Verbs: &fakeVerbs{}, Version: "test", Mode: ModeTools, Kind: "opencode"}
		tools := ToolsFor(srv.Mode, srv.Kind)
		if len(tools) != 5 || hasTool(tools, "wait") {
			t.Fatalf("opencode tools = %+v, want 5 tools without wait", tools)
		}

		out := serveOnce(t, srv, []string{
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop"}}}`,
		})
		lines := splitLines(out)
		if len(lines) != 1 {
			t.Fatalf("want 1 line, got %d", len(lines))
		}
		resp := decodeResponse(t, lines[0])
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("opencode wait call must return CodeInvalidParams, got %+v", resp.Error)
		}
	})
}

func TestServerAsyncPingDuringWait(t *testing.T) {
	t.Parallel()

	waitStarted := make(chan struct{})
	waitRelease := make(chan struct{})

	verbs := &fakeVerbs{
		waitFn: func(ctx context.Context, session string, a WaitArgs) (any, error) {
			close(waitStarted)
			<-waitRelease
			return "done", nil
		},
	}
	srv := &Server{Verbs: verbs, Version: "test", Mode: ModeTools}
	is := newInteractiveServer(t, srv)

	// Send wait call.
	is.write(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop"}}}`)

	select {
	case <-waitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not start within 2s")
	}

	// Send ping while wait is still blocked.
	is.write(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)

	// Ping response must arrive before wait finishes.
	pingLine := is.readLine(t, 2*time.Second)
	var pingResp Response
	if err := json.Unmarshal([]byte(pingLine), &pingResp); err != nil {
		t.Fatalf("unmarshal ping response: %v", err)
	}
	if string(pingResp.ID) != "2" {
		t.Fatalf("expected ping response ID 2, got %s", string(pingResp.ID))
	}

	// Now unblock wait and verify its response arrives.
	close(waitRelease)
	waitLine := is.readLine(t, 2*time.Second)
	var waitResp Response
	if err := json.Unmarshal([]byte(waitLine), &waitResp); err != nil {
		t.Fatalf("unmarshal wait response: %v", err)
	}
	if string(waitResp.ID) != "1" {
		t.Fatalf("expected wait response ID 1, got %s", string(waitResp.ID))
	}
}

func TestServerProgressWithAndWithoutToken(t *testing.T) {
	t.Parallel()

	t.Run("with progress token emits progress notification", func(t *testing.T) {
		waitUnblock := make(chan struct{})
		verbs := &fakeVerbs{
			waitFn: func(ctx context.Context, session string, a WaitArgs) (any, error) {
				<-waitUnblock
				return "done", nil
			},
		}
		srv := &Server{Verbs: verbs, Version: "test", Mode: ModeTools, ProgressInterval: 10 * time.Millisecond}
		is := newInteractiveServer(t, srv)

		is.write(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop"},"_meta":{"progressToken":"prog-token-1"}}}`)

		line := is.readLine(t, 2*time.Second)
		var note Notification
		if err := json.Unmarshal([]byte(line), &note); err != nil {
			t.Fatalf("unmarshal notification: %v", err)
		}
		if note.Method != "notifications/progress" {
			t.Fatalf("expected notifications/progress, got %q", note.Method)
		}
		params, ok := note.Params.(map[string]any)
		if !ok || params["progressToken"] != "prog-token-1" {
			t.Fatalf("progress params = %+v, want progressToken 'prog-token-1'", note.Params)
		}

		close(waitUnblock)
		// Drain wait response.
		for {
			l := is.readLine(t, 2*time.Second)
			var resp Response
			if err := json.Unmarshal([]byte(l), &resp); err == nil && string(resp.ID) == "10" {
				break
			}
		}
	})

	t.Run("without progress token emits no progress notification", func(t *testing.T) {
		verbs := &fakeVerbs{
			waitFn: func(ctx context.Context, session string, a WaitArgs) (any, error) {
				return "done", nil
			},
		}
		srv := &Server{Verbs: verbs, Version: "test", Mode: ModeTools, ProgressInterval: 10 * time.Millisecond}
		is := newInteractiveServer(t, srv)

		is.write(`{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop"}}}`)

		// The wait answers quickly, so the response must be the very first
		// line: a progress beat ahead of it would mean the token-free call
		// emitted one anyway.
		line := is.readLine(t, 2*time.Second)
		var resp Response
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(resp.ID) != "20" {
			t.Fatalf("first line is not the wait response: %s", line)
		}
	})
}

func TestServerCancellationStopsPoll(t *testing.T) {
	t.Parallel()

	waitStarted := make(chan struct{})
	pollStopped := make(chan struct{})

	verbs := &fakeVerbs{
		waitFn: func(ctx context.Context, session string, a WaitArgs) (any, error) {
			close(waitStarted)
			select {
			case <-ctx.Done():
				close(pollStopped)
				return "webshop round 1 cancelled", nil
			case <-time.After(10 * time.Second):
				t.Error("wait was not cancelled within 10s")
				return "timed out", nil
			}
		},
	}
	srv := &Server{Verbs: verbs, Version: "test", Mode: ModeTools}
	is := newInteractiveServer(t, srv)

	is.write(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"wait","arguments":{"name":"webshop"}}}`)

	select {
	case <-waitStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not start within 2s")
	}

	// Send cancellation notification naming request ID 42.
	is.write(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":42}}`)

	select {
	case <-pollStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not stop on cancellation within 2s")
	}

	// Wait response arrives with cancelled result.
	line := is.readLine(t, 2*time.Second)
	var resp Response
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(resp.ID) != "42" {
		t.Fatalf("expected response ID 42, got %s", string(resp.ID))
	}
	rawResult, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(rawResult), "cancelled") {
		t.Fatalf("result = %s, want it to contain 'cancelled'", rawResult)
	}
}
