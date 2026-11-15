package sync

import "github.com/fuad-daoud/relevo/internal/synclog"

// MirrorDocNames are the documents a reader must have before it has history:
// the config, and the secrets that carry the R2 credentials. A machine reading
// none of the log's history mirrors these alone, in this fixed order.
func MirrorDocNames() []string {
	return []string{ConfigDocName, SecretsDocName}
}

// MirrorTables returns the log tables the mirrored documents travel in, in
// MirrorDocNames's order: the tables a reader of none of the history still
// follows.
func MirrorTables() []string {
	names := MirrorDocNames()
	tables := make([]string, 0, len(names))
	for _, name := range names {
		tables = append(tables, synclog.DocPrefix+name)
	}
	return tables
}
