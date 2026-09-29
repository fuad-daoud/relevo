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

// Hello opens the handshake; SchemaKnow is this client's highest embedded
// migration, carried as information for the owner.
type Hello struct {
	Header
	Proto      string `json:"proto"`
	Version    int    `json:"version"`
	ExeID      string `json:"exe_id"`
	SchemaKnow int    `json:"schema_know"`
}

// Welcome answers Hello. SchemaHave is the served database's version and
// SchemaKnow the owner's embedded maximum; Origin is the installation id every
// scoped query needs, which a client must not read from the file.
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
