// Package mcp implements relevo mcp: a hand-rolled JSON-RPC 2.0 server over
// stdio, small enough (five methods, one notification) to not need a dependency.
package mcp

import "encoding/json"

// Request is one JSON-RPC 2.0 request or notification; ID is absent on a
// notification, one per line on the stdio transport.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is one JSON-RPC 2.0 response, written for every Request with an ID.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a server-to-client message with no ID and no reply.
type Notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

// JSON-RPC 2.0 reserved error codes, plus CodeInvalidParams for this
// server's own tool argument decode and validation failures.
const (
	CodeParse         = -32700
	CodeInvalidReq    = -32600
	CodeMethodMissing = -32601
	CodeInvalidParams = -32602
	CodeInternal      = -32603
)
