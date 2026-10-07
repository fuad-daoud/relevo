package db

// SharedTable names one table whose rows another installation may read, and
// the columns that identify a row in it.
type SharedTable struct {
	// Name is the table name.
	Name string
	// PrimaryKey lists the table's primary-key columns in key order. It is the
	// key the outbox records, so a reader parses one shape for a one-column key
	// and a several-column one alike.
	PrimaryKey []string
}

// SharedTables is every table whose rows carry shared history, parents before
// children: a walk in this order meets a parent before anything that needs it,
// so a consumer never has to insert a row whose parent it has not applied yet.
//
// The list is the one place that says which tables are shared and how each row
// is keyed. The outbox triggers, the drift test and the reconcile that walks
// these tables all read it, so a migration that adds or re-keys a shared table
// shows up as a disagreement rather than as a silent divergence.
var SharedTables = []SharedTable{
	{Name: "repo", PrimaryKey: []string{"id"}},
	{Name: "mastermind", PrimaryKey: []string{"id"}},
	{Name: "binding_record", PrimaryKey: []string{"id"}},
	{Name: "installation", PrimaryKey: []string{"id"}},
	{Name: "binding", PrimaryKey: []string{"id"}},
	{Name: "chains", PrimaryKey: []string{"id"}},
	{Name: "binding_event", PrimaryKey: []string{"record_id", "seq"}},
	{Name: "round_file", PrimaryKey: []string{"record_id", "name"}},
	{Name: "chain_event", PrimaryKey: []string{"chain_id", "seq"}},
	{Name: "chain_member", PrimaryKey: []string{"chain_id", "binding"}},
	{Name: "chain_check", PrimaryKey: []string{"chain_id", "run"}},
	{Name: "round", PrimaryKey: []string{"id"}},
	{Name: "event", PrimaryKey: []string{"id"}},
	{Name: "artifact", PrimaryKey: []string{"id"}},
	{Name: "transcript", PrimaryKey: []string{"id"}},
}

// outboxOp names one of the three write operations a shared table's triggers
// record, and is the suffix a trigger's name carries for it.
type outboxOp struct {
	// Name is the value written to the outbox's op column.
	Name string
	// Suffix is the trigger-name suffix for this operation.
	Suffix string
}

// outboxOps is every operation the triggers record, in the order a reader
// expects to meet them on a row's history.
var outboxOps = []outboxOp{
	{Name: "insert", Suffix: "_ins"},
	{Name: "update", Suffix: "_upd"},
	{Name: "delete", Suffix: "_del"},
}
