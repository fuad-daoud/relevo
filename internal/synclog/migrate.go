package synclog

// The migration's own numbers: what one Migrate call moved and built. The pipe
// carries the worker's shape; the packages on both sides of it share this one,
// so neither the daemon nor a later transport imports the worker to read an
// answer. The per-origin entries travel as a slice in origin order rather than
// a map because a report over a map would shuffle its lines on every run.

// MigrateOrigin is one origin's share of a migration: the rows read, the
// objects those rows now live in, the head the migration recorded, and the
// floor and label the heads row carries.
type MigrateOrigin struct {
	Rows       int    `json:"rows,omitempty"`
	Objects    int    `json:"objects,omitempty"`
	Seq        int    `json:"seq,omitempty"`
	Floor      int    `json:"floor,omitempty"`
	LegacyUpto int    `json:"legacy_upto,omitempty"`
	Label      string `json:"label,omitempty"`
}

// MigrateEntry is one origin's share with its origin named. Entries travel
// sorted by origin, so a reader renders them as held.
type MigrateEntry struct {
	Origin string `json:"origin"`
	MigrateOrigin
}

// MigrateResult is what one Migrate call did. The byte totals are this call's
// deltas, from the difference of the backend's counters before and after, so
// a long-lived worker's earlier totals never leak in.
type MigrateResult struct {
	AlreadyMigrated bool           `json:"already_migrated,omitempty"`
	Origins         []MigrateEntry `json:"origins,omitempty"`
	Rows            int            `json:"rows,omitempty"`
	ObjectsCreated  int            `json:"objects_created,omitempty"`
	ObjectsVerified int            `json:"objects_verified,omitempty"`
	RowsSwept       int            `json:"rows_swept,omitempty"`
	SharedRows      int            `json:"shared_rows,omitempty"`
	R2Put           int64          `json:"r2_put,omitempty"`
	R2Get           int64          `json:"r2_get,omitempty"`
	TursoSent       int64          `json:"turso_sent,omitempty"`
	TursoReceived   int64          `json:"turso_received,omitempty"`
}

// Migrator runs the format migration and releases the remote. It stays
// optional rather than a LogTransport method so one addition does not churn
// every fake; a caller asks for it with a type assertion and reads a missing
// migrator as a transport that cannot migrate rather than a failure.
type Migrator interface {
	// Migrate converts the remote to this build's format, answering with
	// what one call moved and built.
	Migrate() (MigrateResult, error)
	// Close releases the remote and the replica.
	Close() error
}
