package main

import (
	"fmt"
)

// errorCode names a class of failure with a stable, lowercase snake word. The
// catalog below is the one table of them; a code is never reused for a
// different meaning, so an agent can branch on the code instead of on prose.
type errorCode string

// The initial catalog: one name per code the catalog carries.
const (
	codeUsage             errorCode = "usage"
	codeRefused           errorCode = "refused"
	codeUnmigrated        errorCode = "unmigrated"
	codeBindingNotFound   errorCode = "binding_not_found"
	codeRoundNotFound     errorCode = "round_not_found"
	codeArtifactNotFound  errorCode = "artifact_not_found"
	codeConflict          errorCode = "conflict"
	codeConfigInvalid     errorCode = "config_invalid"
	codePolicyRefused     errorCode = "policy_refused"
	codeTierCap           errorCode = "tier_cap"
	codeNoDaemon          errorCode = "no_daemon"
	codeRemoteUnreachable errorCode = "remote_unreachable"
	codeRemoteAuth        errorCode = "remote_auth"
	codeGateActive        errorCode = "gate_active"
	codeNotAvailable      errorCode = "not_available"
	codeInternal          errorCode = "internal"
)

// catalogEntry is what the frame needs to render one code: the process exit a
// failure of that class earns, and the command that usually comes next. next
// is empty where no single command exists.
type catalogEntry struct {
	exit int
	next string
}

// catalog is the error catalog: one row per code above. It is the only place a
// code's exit or next hint is decided.
var catalog = map[errorCode]catalogEntry{
	codeUsage:             {exit: 2, next: "relevo help"},
	codeRefused:           {exit: 2},
	codeUnmigrated:        {exit: 1},
	codeBindingNotFound:   {exit: 1, next: "relevo status --all"},
	codeRoundNotFound:     {exit: 1, next: "relevo history"},
	codeArtifactNotFound:  {exit: 1, next: "relevo history"},
	codeConflict:          {exit: 1},
	codeConfigInvalid:     {exit: 1},
	codePolicyRefused:     {exit: 1},
	codeTierCap:           {exit: 1},
	codeNoDaemon:          {exit: 1, next: "relevo daemon"},
	codeRemoteUnreachable: {exit: 1},
	codeRemoteAuth:        {exit: 1},
	codeGateActive:        {exit: 1, next: "relevo gate"},
	codeNotAvailable:      {exit: 1},
	codeInternal:          {exit: 1},
}

// cliError is the frame's error value: a catalog code, a human message that is
// never part of the contract, and the next command when one exists.
type cliError struct {
	code    errorCode
	message string
	next    string
}

// Error renders the code with its message, without the "relevo: " prefix the
// renderer adds, so the code is always the first thing on the line.
func (e *cliError) Error() string {
	return fmt.Sprintf("%s: %s", e.code, e.message)
}

// As fills a *exitCodeErr with the catalog's exit for this code, so every
// errors.As(err, &exitCodeErr) assertion in the package -- and the 0/2/3/4/5/
// 124 protocol codes wait documents -- keeps working unchanged.
func (e *cliError) As(target any) bool {
	ec, ok := target.(*exitCodeErr)
	if !ok {
		return false
	}
	*ec = exitCodeErr{code: catalogExit(e.code)}
	return true
}

// catalogExit is the exit a code earns; an unknown code is treated as internal
// rather than panicking, so the renderer can never be the thing that fails.
func catalogExit(code errorCode) int {
	entry, ok := catalog[code]
	if !ok {
		return catalog[codeInternal].exit
	}
	return entry.exit
}

// fail builds a coded error whose next hint is the catalog's own.
func fail(code errorCode, format string, args ...any) error {
	return newCLIError(code, "", fmt.Sprintf(format, args...))
}

// failNext is fail with an explicit next hint, overriding the catalog's.
func failNext(code errorCode, next, format string, args ...any) error {
	return newCLIError(code, next, fmt.Sprintf(format, args...))
}

// newCLIError resolves a code and hint against the catalog. An uncatalogued
// code becomes internal: a typo in a call site must never panic the CLI or
// emit a code the catalog does not list.
func newCLIError(code errorCode, next, message string) error {
	entry, ok := catalog[code]
	if !ok {
		code = codeInternal
		entry = catalog[codeInternal]
	}
	if next == "" {
		next = entry.next
	}
	return &cliError{code: code, message: message, next: next}
}

// errorEnvelope is the JSON shape of a failure on stderr: one object, one
// line, the same three fields the human form carries.
type errorEnvelope struct {
	Error errorDocument `json:"error"`
}

// errorDocument is the error object inside errorEnvelope.
type errorDocument struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
	Next    string    `json:"next"`
}
