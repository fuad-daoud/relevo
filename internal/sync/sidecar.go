package sync

import (
	"bytes"
	"fmt"
	"os"
)

// The changes sidecar: the file the sync engine keeps beside the database, and
// the one thing a pull reads that the query path never opens.
//
// The driver writes two files next to a synced database -- `<path>-changes`,
// holding the engine's own bookkeeping (which change id this client has applied,
// the cdc bookkeeping tables) and a mirror of the schema, and `<path>-info`,
// holding the client's identity and the revision it is at. Neither is a row
// store: every row a person cares about is in `<path>` itself, which is why a
// machine whose sidecar is unusable is not a machine that lost data.
//
// They are invisible to everything in this repository. The query path opens
// `<path>` and never names them, so a sidecar in any condition at all leaves
// every read, every write and every count correct. Only a push or a pull, which
// is the one thing that hands the path to the sync engine, meets them -- which
// is why the two can disagree about the database for as long as it takes for
// someone to sync.
//
// ## The disagreement
//
// The engine parses the sidecar's schema the way it parses any database, and it
// holds the same rule the parser holds everywhere else: an automatic index
// (`sqlite_autoindex_<table>_1`) is created by the table it belongs to, so a
// schema that names one without naming that table is not a schema a database
// can have. Such a file is refused with
//
//	Corrupt database: sqlite_schema contains automatic index for missing table '<table>'
//
// and the refusal names the table. On a machine whose live file is healthy that
// message is not about the live file, and no amount of querying it will ever
// reproduce it: the file being parsed is the sidecar.
//
// A sidecar gets into that state the only way a file gets torn, by a write that
// stopped partway. The sidecar is sparse by design -- its header declares the
// page count of the database beside it, so page numbers line up one for one and
// the engine writes only the pages it needs -- which means a short write leaves
// a hole where a schema leaf should be, and the schema the engine then reads
// stops wherever the written bytes stop. What survives is whatever fitted
// before the hole, and an automatic index whose table row was still to come
// reads as exactly the one contradiction the parser refuses.
//
// ## What this does about it
//
// ConvergeSidecars removes a sidecar in that state, and only in that state, so
// the next open has the engine rebuild one from the live file. It is not a
// silencer: it applies the engine's own rule to the file the engine is about to
// refuse, and it is narrow -- a sidecar that parses has nothing removed, so the
// steady state costs two reads and no writes.
//
// The `-info` file is left alone on purpose. It carries the client's identity
// and the fact that this database was uploaded as a fresh remote, and removing
// it would make the next open a first open -- a bootstrap against a remote,
// which applies the remote's state over a live file. Dropping the derived half
// of the pair costs a re-fetch the remote has nothing to answer; dropping the
// identity half costs the local file.

const (
	// sidecarSuffix is what the engine appends to a database's path for the
	// file it keeps its bookkeeping and schema mirror in.
	sidecarSuffix = "-changes"
	// autoindexPrefix is what an automatic index's stored name begins with. Its
	// ordinal is read rather than matched, so the second automatic index on a
	// table reads the same way as the first.
	autoindexPrefix = "sqlite_autoindex_"
	// createTablePrefix is what a stored table's SQL begins with.
	createTablePrefix = "CREATE TABLE"
)

// SidecarRepair is what one convergence did to one sidecar.
type SidecarRepair struct {
	// Path is the sidecar that was removed.
	Path string
	// Bytes is how large it was, so a report names a number rather than only
	// that something happened.
	Bytes int64
	// OrphanTable is the table whose automatic index the sidecar named without
	// naming the table. It is a schema name: the rule that found it is about
	// schema, so nothing about any row is in it.
	OrphanTable string
}

// ConvergeSidecars removes a sidecar the sync engine cannot parse, so the next
// open of path has it rebuild one from the file itself.
//
// It reports the zero SidecarRepair and no error on every ordinary path: no
// sidecar, an unreadable one (which is somebody else's permission problem and
// not this call's to decide), a sidecar that parses, and a sidecar this call
// already removed. Every removal is therefore visible in the returned value,
// and a caller that wants the convergence announced can announce exactly the
// ones that happened.
//
// The path is named rather than derived from a handle because the handle is not
// what the sidecar belongs to: a dialled handle names no file at all, and the
// engine is handed the path.
func ConvergeSidecars(path string) (SidecarRepair, error) {
	if path == "" {
		return SidecarRepair{}, nil
	}
	sidecar := path + sidecarSuffix
	data, err := os.ReadFile(sidecar)
	switch {
	case os.IsNotExist(err):
		return SidecarRepair{}, nil
	case err != nil:
		return SidecarRepair{}, fmt.Errorf("sync: read the changes sidecar: %w", err)
	}
	table, torn := orphanAutoindexTable(data)
	if !torn {
		return SidecarRepair{}, nil
	}
	repair := SidecarRepair{Path: sidecar, Bytes: int64(len(data)), OrphanTable: table}
	if err := os.Remove(sidecar); err != nil {
		return SidecarRepair{}, fmt.Errorf("sync: remove the changes sidecar: %w", err)
	}
	return repair, nil
}

// orphanAutoindexTable is the engine's own rule over a sidecar's bytes: it
// reports the first table whose automatic index the file names without naming
// the table itself.
//
// It reads the file as bytes rather than as a database, and deliberately so.
// The sidecar's header does not start where a database header starts, so nothing
// in this repository can open one as a database at all; what the engine needs
// from it is the schema, and the schema is in there as the SQL text it stored,
// verbatim. Every table's `CREATE TABLE` and every automatic index's name are
// resident in any sidecar the engine can parse -- the schema is what it parses
// before it can do anything -- so a sidecar missing one is a sidecar whose
// written bytes stopped, which is the only way this rule fires on a file no
// process ever half-wrote on purpose.
//
// The name is read from both ends because a table name may itself end in the
// sequence number. The stored name has no terminator -- the table name follows
// it in the same record -- so the read is the shortest run that ends in `_<one
// or more digits>`: those digits are the index's own ordinal, which the engine
// writes as a single digit for every automatic index it creates, and everything
// before them is the table.
func orphanAutoindexTable(data []byte) (string, bool) {
	defined := definedTables(data)
	for i := 0; i+len(autoindexPrefix) < len(data); i++ {
		if !bytes.HasPrefix(data[i:], []byte(autoindexPrefix)) {
			continue
		}
		name := autoindexTableName(data[i+len(autoindexPrefix):])
		if name == "" || defined[name] {
			continue
		}
		return name, true
	}
	return "", false
}

// definedTables is every table name the sidecar's stored SQL creates.
func definedTables(data []byte) map[string]bool {
	defined := make(map[string]bool)
	for i := 0; i+len(createTablePrefix) < len(data); i++ {
		if !bytes.HasPrefix(data[i:], []byte(createTablePrefix)) {
			continue
		}
		rest := skipSpace(data[i+len(createTablePrefix):])
		// The migrations spell half their tables CREATE TABLE IF NOT EXISTS, and
		// the qualifier sits between the two words and the name.
		for _, qualifier := range []string{"IF NOT EXISTS ", "if not exists "} {
			if bytes.HasPrefix(rest, []byte(qualifier)) {
				rest = skipSpace(rest[len(qualifier):])
			}
		}
		if len(rest) == 0 {
			continue
		}
		// The name is quoted when it needed quoting, which is the only case
		// where it can hold a character this scan would stop at. The quotes are
		// the CREATE TABLE's own; an automatic index names the table without
		// them, so what is stored here is the name as the index spells it.
		if q := rest[0]; q == '"' || q == '`' || q == '[' {
			closing := q
			if q == '[' {
				closing = ']'
			}
			end := bytes.IndexByte(rest[1:], closing)
			if end < 0 {
				continue
			}
			defined[string(rest[1:1+end])] = true
			continue
		}
		end := 0
		for end < len(rest) && isNameByte(rest[end]) {
			end++
		}
		if end > 0 {
			defined[string(rest[:end])] = true
		}
	}
	return defined
}

func skipSpace(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	return b
}

// autoindexTableName is the table inside `sqlite_autoindex_<table>_<n>`, given
// the bytes from just after the prefix. It is empty when what follows carries no
// underscore followed by digits, which is what no automatic index name does.
func autoindexTableName(rest []byte) string {
	for end := 0; end < len(rest); end++ {
		if rest[end] != '_' {
			continue
		}
		digits := end + 1
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits == end+1 {
			continue
		}
		return string(rest[:end])
	}
	return ""
}

func isNameByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		return true
	}
	return false
}
