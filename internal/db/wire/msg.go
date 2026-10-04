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
