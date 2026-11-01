package main

import (
	"errors"
	"fmt"
	"strings"
)

// errorCode names a class of failure with a stable, lowercase snake word. A code
// is never reused for a different meaning, so an agent can branch on the code
// instead of on prose.
type errorCode string

const (
	codeUsage              errorCode = "usage"
	codeRefused            errorCode = "refused"
	codeUnmigrated         errorCode = "unmigrated"
	codeBindingNotFound    errorCode = "binding_not_found"
	codeRoundNotFound      errorCode = "round_not_found"
	codeArtifactNotFound   errorCode = "artifact_not_found"
	codeConflict           errorCode = "conflict"
	codeConfigInvalid      errorCode = "config_invalid"
	codeConfigPathNotSet   errorCode = "config_path_not_set"
	codeRevisionNotFound   errorCode = "revision_not_found"
	codePolicyRefused      errorCode = "policy_refused"
	codeTierCap            errorCode = "tier_cap"
	codeNoDaemon           errorCode = "no_daemon"
	codeRemoteUnreachable  errorCode = "remote_unreachable"
	codeRemoteAuth         errorCode = "remote_auth"
	codeGateActive         errorCode = "gate_active"
	codeNotAvailable       errorCode = "not_available"
	codeMastermindNotFound errorCode = "mastermind_not_found"
	codeClientNotFound     errorCode = "client_not_found"
	codeServerNotFound     errorCode = "server_not_found"
	codeRoundCap           errorCode = "round_cap"
	codeMastermindMismatch errorCode = "mastermind_mismatch"
	codePushSeqNotFound    errorCode = "push_seq_not_found"
	codePushAlreadyConfirm errorCode = "push_already_confirmed"
	codePushNotAdmitted    errorCode = "push_not_admitted"
	codePushNoClaim        errorCode = "push_no_claim"
	codeOwnerSandboxDenied errorCode = "owner_sandbox_denied"
	codeInternal           errorCode = "internal"
)

// catalogEntry is what the frame needs to render one code: the process exit a
// failure of that class earns, and the command that usually comes next.
type catalogEntry struct {
	exit int
	next string
}

// catalog is the error catalog. It is the only place a code's exit or next hint
// is decided.
var catalog = map[errorCode]catalogEntry{
	codeUsage:              {exit: 2, next: "relevo help"},
	codeRefused:            {exit: 2},
	codeUnmigrated:         {exit: 1},
	codeBindingNotFound:    {exit: 1, next: "relevo status --all"},
	codeRoundNotFound:      {exit: 1, next: "relevo history"},
	codeArtifactNotFound:   {exit: 1, next: "relevo history"},
	codeConflict:           {exit: 1},
	codeConfigInvalid:      {exit: 1, next: "relevo config edit"},
	codeConfigPathNotSet:   {exit: 1, next: "relevo config export"},
	codeRevisionNotFound:   {exit: 1, next: "relevo config log"},
	codePolicyRefused:      {exit: 1},
	codeTierCap:            {exit: 1},
	codeNoDaemon:           {exit: 1, next: "relevo daemon"},
	codeRemoteUnreachable:  {exit: 1},
	codeRemoteAuth:         {exit: 1},
	codeGateActive:         {exit: 1, next: "relevo gate"},
	codeNotAvailable:       {exit: 1},
	codeMastermindNotFound: {exit: 1, next: "relevo mastermind list"},
	codeClientNotFound:     {exit: 1, next: "relevo serve clients"},
	codeServerNotFound:     {exit: 1, next: "relevo config server list"},
	codeRoundCap:           {exit: 1, next: "relevo bind"},
	codeMastermindMismatch: {exit: 1},
	codePushSeqNotFound:    {exit: 1},
	codePushAlreadyConfirm: {exit: 1},
	codePushNotAdmitted:    {exit: 1},
	codePushNoClaim:        {exit: 1},
	codeOwnerSandboxDenied: {exit: 1, next: "relevo verbs need an unsandboxed shell: run the verb under muse --yolo or muse --disable-sandbox; relevo's MCP tools still work inside the sandbox"},
	codeInternal:           {exit: 1, next: "relevo bugreport"},
}

// cliError is the frame's error value: a catalog code, a human message that is
// never part of the contract, and the classified-out cause, kept so errors.Is
// still finds a library sentinel under the code.
type cliError struct {
	code    errorCode
	message string
	next    string
	cause   error
}

// Error renders the code with its message, so the code is always the first thing
// on the line.
func (e *cliError) Error() string {
	return fmt.Sprintf("%s: %s", e.code, e.message)
}

// Unwrap exposes the classified cause.
func (e *cliError) Unwrap() error {
	return e.cause
}

// As fills a *exitCodeErr with the catalog's exit for this code.
func (e *cliError) As(target any) bool {
	ec, ok := target.(*exitCodeErr)
	if !ok {
		return false
	}
	*ec = exitCodeErr{code: catalogExit(e.code)}
	return true
}

// catalogExit is the exit a code earns; an unknown code is treated as internal
// rather than panicking.
func catalogExit(code errorCode) int {
	entry, ok := catalog[code]
	if !ok {
		return catalog[codeInternal].exit
	}
	return entry.exit
}

// sandboxCode is the one central reclassification rule: an internal failure whose
// arguments or cause carry the sandbox denial is built under its own code.
// Every other code comes back unchanged.
func sandboxCode(code errorCode, args []any, cause error) errorCode {
	if code != codeInternal {
		return code
	}
	if errors.Is(cause, errOwnerSandboxDenied) {
		return codeOwnerSandboxDenied
	}
	for _, a := range args {
		if err, ok := a.(error); ok && errors.Is(err, errOwnerSandboxDenied) {
			return codeOwnerSandboxDenied
		}
	}
	return code
}

func fail(code errorCode, format string, args ...any) error {
	return newCLIError(sandboxCode(code, args, nil), "", fmt.Sprintf(format, args...))
}

// failNext is fail with an explicit next hint, unless the failure is
// reclassified.
func failNext(code errorCode, next, format string, args ...any) error {
	code = sandboxCode(code, args, nil)
	if code == codeOwnerSandboxDenied {
		next = ""
	}
	return newCLIError(code, next, fmt.Sprintf(format, args...))
}

// failWrap is fail keeping cause, so the coded failure still unwraps to the
// library error it was classified out of.
func failWrap(code errorCode, cause error, format string, args ...any) error {
	code = sandboxCode(code, args, cause)
	message := fmt.Sprintf(format, args...)
	if cause != nil && !strings.Contains(message, cause.Error()) {
		message += ": " + cause.Error()
	}
	return newCLIErrorCause(code, "", message, cause)
}

func newCLIErrorCause(code errorCode, next, message string, cause error) error {
	entry, ok := catalog[code]
	if !ok {
		code = codeInternal
		entry = catalog[codeInternal]
	}
	if next == "" {
		next = entry.next
	}
	return &cliError{code: code, message: message, next: next, cause: cause}
}

// newCLIError resolves a code and hint against the catalog; an uncatalogued code
// becomes internal.
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

type errorEnvelope struct {
	Error errorDocument `json:"error"`
}

type errorDocument struct {
	Code    errorCode `json:"code"`
	Message string    `json:"message"`
	Next    string    `json:"next"`
}
