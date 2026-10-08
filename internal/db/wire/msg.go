package wire

// The message type names, repeated in every JSON header so a captured stream
// reads on its own.
const (
	TypeHello   = "hello"
	TypeWelcome = "welcome"
	TypeRefuse  = "refuse"
	TypeExec    = "exec"
	TypeQuery   = "query"
	TypeRows    = "rows"
	TypeNext    = "next"
	TypeDone    = "done"
	TypeClose   = "close"
	TypeCancel  = "cancel"
	TypeError   = "error"

	TypeSyncVerb   = "sync_verb"
	TypeSyncResult = "sync_result"
)

// Header is the part every control message shares: its type and the request id
// that ties a response to its request.
type Header struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
}

// The two files one owner serves, named by the scope a connection asks for.
// A connection that names none is served the shared file, which is what a
// client predating the field expects.
const (
	// ScopeShared is the file that leaves this machine.
	ScopeShared = "shared"
	// ScopeLocal is the machine-local file beside it, the one every row that
	// must not sync lives in.
	ScopeLocal = "local"
)

// Hello opens the handshake; SchemaKnow is this client's highest embedded
// migration, carried as information for the owner. AdHoc marks a connection on
// the ad-hoc read path (`db query`), which the owner may refuse while it is
// reaping an abandoned statement; it is additive, so an owner that predates the
// bit ignores it and a client that predates it never sends one.
//
// Scope names which of the owner's files this connection speaks to, and is
// additive for the same reason: a client that predates it sends none and is
// served the shared file, and an owner that predates it serves the shared file
// to everyone. A client therefore never mistakes the shared file for the local
// one across an upgrade window -- it attaches no local handle at all.
type Hello struct {
	Header
	Proto      string `json:"proto"`
	Version    int    `json:"version"`
	ExeID      string `json:"exe_id"`
	SchemaKnow int    `json:"schema_know"`
	AdHoc      bool   `json:"ad_hoc"`
	Scope      string `json:"scope,omitempty"`
}

// Welcome answers Hello. SchemaHave is the served database's version and
// SchemaKnow the owner's embedded maximum; Origin is the installation id every
// scoped query needs, which a client must not read from the file.
//
// HasLocal says the owner serves a machine-local file beside the shared one, so
// a client may open a second connection scoped to it. It is false on an owner
// opened without one, and on an owner that predates the field, which is what
// keeps an old owner from being asked for a file it does not have.
type Welcome struct {
	Header
	Proto      string   `json:"proto"`
	MinClient  int      `json:"min_client"`
	Version    int      `json:"version"`
	SchemaHave int      `json:"schema_have"`
	SchemaKnow int      `json:"schema_know"`
	Origin     string   `json:"origin"`
	PID        int      `json:"pid"`
	Conns      int      `json:"conns"`
	Features   []string `json:"features"`
	HasLocal   bool     `json:"has_local"`
}

// Refusal answers Hello when the owner will not serve: a protocol mismatch or
// a shutdown. Its Code is a string, so a caller can never confuse it with a
// SQLite result code.
type Refusal struct {
	Header
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (r *Refusal) Error() string { return r.Message }

// Exec runs one statement that returns a result.
type Exec struct {
	Header
	Query string `json:"query"`
}

// Query starts one statement whose rows stream back in batches.
type Query struct {
	Header
	Query string `json:"query"`
}

// Rows is a value frame: its header names the columns (first batch only), the
// batch's row count, and whether another batch follows. When none does, a done
// frame follows immediately, so a client that stops after the last row can
// still leave the stream in a known state.
type Rows struct {
	Header
	Columns []string `json:"columns,omitempty"`
	Count   int      `json:"count"`
	More    bool     `json:"more"`
}

// Next asks for the batch after the one just delivered.
type Next struct {
	Header
}

// Done ends a request. For an exec it carries the result callers read back.
type Done struct {
	Header
	RowsAffected int64 `json:"rows_affected"`
	LastInsertID int64 `json:"last_insert_id"`
}

// Close abandons a stream; the owner rolls the pinned connection back and
// discards it.
type Close struct {
	Header
}

// Cancel interrupts the request with this id.
type Cancel struct {
	Header
}

// The sync verbs a client asks the owner to run. They are named rather than
// numbered so a captured stream reads on its own, and they are a closed set:
// an owner refuses a name it does not know rather than guessing.
const (
	SyncVerbEnable  = "enable"
	SyncVerbDisable = "disable"
	SyncVerbPush    = "push"
	SyncVerbPull    = "pull"
	SyncVerbRetry   = "retry"
	// SyncVerbProbe asks the owner's log whether it answers without moving a
	// change set. It is a verb so the cockpit reaches a test connection over the
	// owner socket like every other action, rather than opening a worker of its
	// own.
	SyncVerbProbe = "probe"
)

// The refusal codes a SyncResult carries. They are a closed set and every one is
// fixed text written in this repository: the owner names the class and the
// caller maps it onto its own exit code, so a failure is never classified by
// reading prose, and no remote-chosen body and no credential can reach a
// message. A new class is added here rather than invented at a call site.
const (
	// SyncCodeInvalid is a refusal the caller cannot fix by reading a message:
	// a malformed section, a missing opener, a build with no sync driver.
	SyncCodeInvalid = "invalid"
	// SyncCodeNoToken is an enable with no token on either route.
	SyncCodeNoToken = "no_token"
	// SyncCodeNoRemote is an enable that named no remote on either route.
	SyncCodeNoRemote = "no_remote"
	// SyncCodeAlreadyEnabled is a machine already syncing.
	SyncCodeAlreadyEnabled = "already_enabled"
	// SyncCodeRemoteConflict is a --url that contradicts the stored remote.
	SyncCodeRemoteConflict = "remote_conflict"
	// SyncCodePreflightRefused is the enable preflight's own answer: the checks
	// that decide whether this database may leave the machine.
	SyncCodePreflightRefused = "preflight_refused"
	// SyncCodeAuthRefused is the one failure a retry cannot fix: the remote
	// rejected this installation's token.
	SyncCodeAuthRefused = "auth_refused"
	// SyncCodeRemoteUnreachable is a remote that did not answer in the bound.
	SyncCodeRemoteUnreachable = "remote_unreachable"
	// SyncCodeContended is the local database refusing a bounded step because it
	// was busy: the write slot was held, or a connection was not free. What held
	// it is a writer that ends on its own, so a retry answers this and a defect
	// report does not.
	SyncCodeContended = "contended"
	// SyncCodeRemoteRefused is a remote that refused a statement of this
	// machine's change set -- a constraint it enforces that the rows reached it
	// in an order it cannot apply. The remote is answering rather than failing,
	// so the caller's way out is a change set this machine can push and not a
	// defect report.
	SyncCodeRemoteRefused = "remote_refused"
	// SyncCodeRemoteSchemaMissing is a remote with no table for the rows this
	// machine is pushing: the engine never taught it this database's schema. It
	// is its own class because the fix is a different one from a constraint
	// refusal -- DDL over the sync connection rather than a re-recorded row --
	// and a reader sent to `relevo bugreport` for it would be reporting a driver
	// boundary as a defect.
	SyncCodeRemoteSchemaMissing = "remote_schema_missing"
	// SyncCodeInternal is a failure with no class of its own -- the daemon's
	// own bug, or a hook that panicked or reported nothing.
	SyncCodeInternal = "internal"
)

// SyncVerb asks the owner to run one sync verb against the handles the owner
// already has open. The client ships the settings body and the token bytes
// because those are what the caller's route produced; the owner performs the
// verb against its own shared and machine-local handles rather than opening a
// second pair.
//
// The token travels in the frame's raw tail and never in this header. That is
// the whole of the redaction rule on this surface: the header is the part a
// reader of a captured stream, a log or an error sees, and a field that never
// holds the value is a rule that cannot be forgotten at a call site. No error
// on either end formats the tail, and no response carries the value back --
// TokenPresent is the only thing a response says about it.
type SyncVerb struct {
	Header
	// Verb is one of the SyncVerb names above.
	Verb string `json:"verb"`
	// RemoteURL is the remote `--url` named, empty when the flag was not passed.
	// The remote already stored is not carried: it is a row in the
	// machine-local file the owner already has open, so it reads it there
	// rather than trusting a body a client sent.
	RemoteURL string `json:"remote_url,omitempty"`
	// TimeoutMS bounds the verb's network work in milliseconds. Zero selects
	// the package default on the owner side.
	TimeoutMS int64 `json:"timeout_ms,omitempty"`
}

// SyncResult is the owner's answer to one verb: what the run did, in the shape
// the caller reports. It carries no credential on any field -- TokenPresent is a
// bool precisely so a caller can say whether a token exists without a way to
// say what it was.
type SyncResult struct {
	Header
	// OK is whether the verb completed. A refusal sets it false and carries a
	// Code the caller maps, so the classification happens where the codes live
	// rather than in prose a reader has to decode.
	OK bool `json:"ok"`
	// Code is the refusal class, empty on success. It is one of the sync
	// sentinels' names rather than free text, so the owner never chooses how a
	// caller reads a failure.
	Code string `json:"code,omitempty"`
	// Message is the refusal's own text. Every one of these is fixed text
	// written in this repository; none of them is the token and none of them is
	// a body the remote chose.
	Message string `json:"message,omitempty"`
	// TokenPresent says whether a token is stored, never what it holds.
	TokenPresent bool `json:"token_present,omitempty"`
	// Applied is whether a pull rebased anything.
	Applied bool `json:"applied,omitempty"`
	// RemoteURL is the remote the enable stored.
	RemoteURL string `json:"remote_url,omitempty"`
	// Steps is the order a disable took, which is its contract.
	Steps []string `json:"steps,omitempty"`
	// FinalPush is whether the best-effort final export landed.
	FinalPush bool `json:"final_push"`
	// Warning is what a disable's final export hit, kept as a warning rather
	// than a failure.
	Warning string `json:"warning,omitempty"`
}
