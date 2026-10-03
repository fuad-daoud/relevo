package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"sync"
	"time"
)

// ProtocolVersion is what initialize always answers: Claude Code refuses a channel that negotiates a newer one.
const ProtocolVersion = "2025-06-18"

// ServerName is this server's name and the channel event's "source" attribute.
const ServerName = "relevo"

// Mode is whether this process claims the pane and pushes events, or only serves tools.
type Mode int

const (
	// ModeTools serves tools/list and tools/call only; it pushes nothing.
	ModeTools Mode = iota
	// ModeChannel additionally claims its pane and drains its mailbox.
	ModeChannel
)

// maxLineBytes bounds one JSON-RPC line: a report payload can be large.
const maxLineBytes = 16 << 20

// metaKeyPattern is what Claude Code accepts as a meta key: an identifier.
var metaKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Server is relevo mcp's JSON-RPC 2.0 loop over stdio; it implements delivery.Pusher via Push.
type Server struct {
	Verbs   Verbs
	Version string // serverInfo.version
	// Mode picks the instructions text when Instructions is empty.
	Mode Mode
	// Kind is the harness this server runs under: "" is Claude Code, and
	// "opencode" selects the opencode instructions and drops the wait command
	// a tools-mode send would otherwise carry.
	Kind string
	// Instructions overrides the mode's text when non-empty; tests use it.
	Instructions string
	Log          io.Writer // stderr; nil -> discard
	// Notice, when set and non-empty, appends one more text block to every
	// tools/call result: how a mastermind session hears the daemon moved on to
	// a newer relevo.
	Notice func() string
	// OnInitialized fires once, after notifications/initialized.
	OnInitialized func()

	// ProgressInterval overrides the 30s progress tick when non-zero; tests use it.
	ProgressInterval time.Duration

	cancelMu    sync.Mutex
	waitCancels map[string]context.CancelFunc

	logger     *log.Logger
	loggerOnce sync.Once

	// out is the transport's outbound side, set once at the top of Serve; the
	// request loop and the poll goroutine (via Push) both write to it through writeMu.
	out     io.Writer
	writeMu sync.Mutex
}

func (s *Server) log() *log.Logger {
	s.loggerOnce.Do(func() {
		w := s.Log
		if w == nil {
			w = io.Discard
		}
		s.logger = log.New(w, "relevo mcp: ", log.LstdFlags)
	})
	return s.logger
}

// Serve reads one JSON object per line from in until EOF or ctx is done, dispatching each per the method table.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out

	type lineResult struct {
		line []byte
		err  error
	}
	lines := make(chan lineResult)

	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
		for scanner.Scan() {
			line := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- lineResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case lines <- lineResult{err: err}:
			case <-ctx.Done():
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case lr, ok := <-lines:
			if !ok {
				return nil
			}
			if lr.err != nil {
				return lr.err
			}
			if len(bytesTrimSpace(lr.line)) == 0 {
				continue
			}
			s.handleLine(ctx, lr.line)
		}
	}
}

func bytesTrimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && isSpaceByte(b[start]) {
		start++
	}
	for end > start && isSpaceByte(b[end-1]) {
		end--
	}
	return b[start:end]
}

func isSpaceByte(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func (s *Server) handleLine(ctx context.Context, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: nil, Error: &RPCError{Code: CodeParse, Message: err.Error()}})
		return
	}

	if req.JSONRPC != "2.0" || req.Method == "" {
		if len(req.ID) > 0 {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidReq, Message: "invalid request"}})
		}
		return
	}

	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: s.initializeResult()})
	case "notifications/initialized":
		if s.OnInitialized != nil {
			s.OnInitialized()
		}
	case "ping":
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
		}
	case "tools/list":
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": ToolsFor(s.Mode, s.Kind)}})
		}
	case "tools/call":
		s.handleToolsCall(ctx, req)
	case "notifications/cancelled":
		var cp struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(req.Params, &cp); err == nil && len(cp.RequestID) > 0 {
			s.cancelWait(string(bytes.TrimSpace(cp.RequestID)))
		}
	default:
		if !isNotification {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeMethodMissing, Message: fmt.Sprintf("unknown method %q", req.Method)}})
		}
		// Any other notification: ignored.
	}
}

func (s *Server) initializeResult() map[string]any {
	instructions := s.Instructions
	if instructions == "" {
		instructions = InstructionsFor(s.Mode, s.Kind)
	}
	return map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities": map[string]any{
			"tools":        map[string]any{},
			"experimental": map[string]any{"claude/channel": map[string]any{}},
		},
		"serverInfo": map[string]any{
			"name":    ServerName,
			"version": s.Version,
		},
		"instructions": instructions,
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	// Meta carries the calling harness session. opencode sends it under the
	// namespaced key ai.opencode/sessionID on every call (docs.opencode
	// mcp-servers#context), which is how one server resolves several sessions;
	// a bare sessionID stays the fallback.
	Meta callMeta `json:"_meta"`
}

// callMeta is the request metadata relevo reads. Other keys are ignored.
type callMeta struct {
	// SessionID is the bare fallback key.
	SessionID string `json:"sessionID"`
	// OpenCodeSessionID is the namespaced key opencode 2.0.18 sends.
	OpenCodeSessionID string `json:"ai.opencode/sessionID"`
	// ProgressToken is the MCP progress token if the caller requests progress notifications.
	ProgressToken json.RawMessage `json:"progressToken,omitempty"`
}

// session picks the calling harness session: opencode's namespaced key when it
// is present, else the bare fallback.
func (m callMeta) session() string {
	if m.OpenCodeSessionID != "" {
		return m.OpenCodeSessionID
	}
	return m.SessionID
}

func (s *Server) handleToolsCall(ctx context.Context, req Request) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}

	if params.Name == "wait" {
		if s.Mode != ModeTools || s.Kind == "opencode" {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", params.Name)}})
			return
		}
		s.handleWaitCall(ctx, req, params)
		return
	}

	result, rpcErr := s.callTool(ctx, params.Name, params.Arguments, params.Meta.session())
	if rpcErr != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
		return
	}
	s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: s.withNotice(result)})
}

func (s *Server) handleWaitCall(ctx context.Context, req Request, params toolCallParams) {
	var a WaitArgs
	if err := decodeArgs(params.Arguments, &a); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}
	if err := validateWaitArgs(a); err != nil {
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: &RPCError{Code: CodeInvalidParams, Message: err.Error()}})
		return
	}

	rawProg := bytes.TrimSpace(params.Meta.ProgressToken)
	hasProgress := len(rawProg) > 0 && !bytes.Equal(rawProg, []byte("null"))
	a.HasProgressToken = hasProgress

	reqIDKey := string(bytes.TrimSpace(req.ID))
	callCtx, cancel := context.WithCancel(ctx)
	s.recordWaitCancel(reqIDKey, cancel)

	if hasProgress {
		var token any
		_ = json.Unmarshal(rawProg, &token)
		interval := s.ProgressInterval
		if interval <= 0 {
			interval = 30 * time.Second
		}
		s.emitProgress(token, 1, "waiting for runner")
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			beat := 1
			for {
				select {
				case <-callCtx.Done():
					return
				case <-ticker.C:
					beat++
					s.emitProgress(token, beat, "waiting for runner")
				}
			}
		}()
	}

	go func() {
		defer func() {
			cancel()
			s.clearWaitCancel(reqIDKey)
		}()

		res, err := s.Verbs.Wait(callCtx, params.Meta.session(), a)
		out, rpcErr := toolResultFrom(res, err)
		if rpcErr != nil {
			s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			return
		}
		s.writeResponse(Response{JSONRPC: "2.0", ID: req.ID, Result: s.withNotice(out)})
	}()
}

func (s *Server) emitProgress(token any, progress int, msg string) {
	note := Notification{
		JSONRPC: "2.0",
		Method:  "notifications/progress",
		Params: map[string]any{
			"progressToken": token,
			"progress":      progress,
			"message":       msg,
		},
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return
	}
	s.writeLine(raw)
}

func (s *Server) recordWaitCancel(id string, cancel context.CancelFunc) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if s.waitCancels == nil {
		s.waitCancels = make(map[string]context.CancelFunc)
	}
	s.waitCancels[id] = cancel
}

func (s *Server) clearWaitCancel(id string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	delete(s.waitCancels, id)
}

func (s *Server) cancelWait(id string) {
	s.cancelMu.Lock()
	defer s.cancelMu.Unlock()
	if cancel, ok := s.waitCancels[id]; ok {
		cancel()
	}
}

// withNotice appends the upgrade notice as one more content block; a nil Notice, or "", leaves the result unchanged.
func (s *Server) withNotice(r ToolResult) ToolResult {
	if s.Notice == nil {
		return r
	}
	notice := s.Notice()
	if notice == "" {
		return r
	}
	r.Content = append(r.Content, Content{Type: "text", Text: notice})
	return r
}

func (s *Server) callTool(ctx context.Context, name string, raw json.RawMessage, session string) (ToolResult, *RPCError) {
	switch name {
	case "status":
		var a StatusArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Status(ctx, session, a)
		return toolResultFrom(res, err)

	case "send":
		var a SendArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateSendArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Send(ctx, session, a)
		out, rpcErr := toolResultFrom(res, err)
		if rpcErr != nil || err != nil || a.DryRun {
			return out, rpcErr
		}
		// Claude's tools mode gets no push, so the result points at the wait
		// tool; channel mode already gets the event, and an opencode mastermind
		// gets the report as a new turn.
		if s.Mode == ModeTools && s.Kind != "opencode" {
			return appendWaitPointer(out, a.Name, budgetOf(res)), nil
		}
		return out, nil

	case "done":
		var a DoneArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateDoneArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Done(ctx, session, a)
		return toolResultFrom(res, err)

	case "show":
		var a ShowArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateShowArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Show(ctx, session, a)
		return toolResultFrom(res, err)

	case "gate":
		var a GateArgs
		if err := decodeArgs(raw, &a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if err := validateGateArgs(a); err != nil {
			return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		res, err := s.Verbs.Gate(ctx, session, a)
		return toolResultFrom(res, err)

	default:
		return ToolResult{}, &RPCError{Code: CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
	}
}

// toolResultFrom turns a verb's (result, error) into a ToolResult: a verb
// error becomes isError:true, never a JSON-RPC error, so the model can act on it.
func toolResultFrom(res any, err error) (ToolResult, *RPCError) {
	if err != nil {
		return textResult(err.Error(), true), nil
	}
	if s, ok := res.(string); ok {
		return textResult(s, false), nil
	}
	r, jerr := jsonResult(res)
	if jerr != nil {
		return ToolResult{}, &RPCError{Code: CodeInternal, Message: jerr.Error()}
	}
	return r, nil
}

func (s *Server) writeResponse(resp Response) {
	if resp.ID == nil {
		resp.ID = json.RawMessage("null")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		s.log().Printf("marshal response: %v", err)
		return
	}
	s.writeLine(raw)
}

func (s *Server) writeLine(raw []byte) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(raw, '\n')); err != nil {
		s.log().Printf("write: %v", err)
	}
}

// Push implements delivery.Pusher, writing one notifications/claude/channel
// line; a meta key that is not a legal identifier is dropped and logged.
func (s *Server) Push(ctx context.Context, content string, meta map[string]string) error {
	clean := make(map[string]string, len(meta))
	for k, v := range meta {
		if metaKeyPattern.MatchString(k) {
			clean[k] = v
		} else {
			s.log().Printf("dropping non-identifier meta key %q", k)
		}
	}

	note := Notification{
		JSONRPC: "2.0",
		Method:  "notifications/claude/channel",
		Params:  map[string]any{"content": content, "meta": clean},
	}
	raw, err := json.Marshal(note)
	if err != nil {
		return err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}
