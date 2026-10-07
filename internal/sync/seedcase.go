package sync

// The seed matrix: which of the three situations an enable found, and the calls
// each one wants. It is a pure function over three facts so the decision can be
// read and tested with no file and no remote in reach.

// SeedCase names which of the three situations an enable found. The three want
// opposite things from the remote, so the case is decided before any handle is
// opened rather than discovered afterwards.
type SeedCase string

const (
	// SeedEmptyCloud is a database with history meeting a remote that holds
	// nothing: the first push is the seed and nothing is ever fetched.
	SeedEmptyCloud SeedCase = "empty_cloud"
	// SeedExistingDB is a database with history meeting a remote that already
	// holds data. History on both sides is the only case that cannot proceed on
	// its own, because last-push-wins drops one of them silently.
	SeedExistingDB SeedCase = "existing_db"
	// SeedNewMachine is a machine holding no history at all: the remote is the
	// only source, so the open bootstraps and the state is pulled in.
	SeedNewMachine SeedCase = "new_machine"
)

// SeedInput is everything the seed decision turns on. It is three facts rather
// than a database handle so the decision is a pure function a test can drive
// without a remote or a file.
type SeedInput struct {
	// LocalHasHistory is whether this machine's shared database holds rows a
	// push would carry.
	LocalHasHistory bool
	// CloudEmpty is whether the remote holds nothing yet.
	CloudEmpty bool
	// SeedUploaded reports that the documented Turso upload already ran against
	// the seed copy a previous enable wrote, so the history on both sides is
	// this machine's own rather than two histories that disagree.
	SeedUploaded bool
}

// SeedDecision is what the seed matrix decided: the open it wants and the calls
// that follow it. Every arm sets Pull when Bootstrap is false, which is the
// driver's own rule -- an open that skipped the bootstrap owes the caller a
// pull -- so no decision in this file can be the false-then-forgets-the-pull
// one. The one exception is the arm whose remote was filled by an upload rather
// than by this machine's own push, and its pull is the one call shape that has
// aborted the process.
type SeedDecision struct {
	// Case is which of the three situations this is.
	Case SeedCase
	// Bootstrap is what the open's BootstrapIfEmpty must carry. It is a plain
	// bool because the decision is total: no case here wants the pointer left
	// unset, so the driver's own default never decides for us.
	Bootstrap bool
	// Pull is whether an explicit pull follows the open.
	Pull bool
	// Push is whether a push follows the open.
	Push bool
	// NeedsUpload is whether the case cannot proceed until the seed copy has
	// been uploaded through the documented path.
	NeedsUpload bool
}

// DecideSeed is the whole seed matrix. A machine with no history bootstraps,
// because the remote is the only source of anything it will ever hold. A
// machine with history and an empty remote pushes, because that push is the
// seed and there is nothing to fetch. History on both sides is the one case
// that refuses, and it refuses until the upload has happened.
func DecideSeed(in SeedInput) SeedDecision {
	switch {
	case !in.LocalHasHistory:
		// The pull is made explicit even though the bootstrap already performs
		// one: a remote that changes between the open and the mark must not
		// leave this machine a round behind with no call that says so.
		return SeedDecision{Case: SeedNewMachine, Bootstrap: true, Pull: true}
	case in.CloudEmpty:
		// A pull against an empty remote costs one round trip and changes
		// nothing, and it is what an open that skipped the bootstrap owes.
		return SeedDecision{Case: SeedEmptyCloud, Bootstrap: false, Pull: true, Push: true}
	case in.SeedUploaded:
		// The upload put this database's history on the remote, so the push that
		// follows carries only what changed since.
		//
		// There is deliberately no pull here, and this is the one place the
		// driver's rule above is set aside. `turso db import` wrote the remote as
		// a fresh database from a file rather than as this machine's change
		// stream, so the remote's frames and this file's write-ahead log share no
		// ancestry. A pull in that state asks the driver to apply remote frames
		// against a local log whose checkpointed prefix has already moved past
		// them, and the WAL reader asserts rather than returning -- turso_assert!
		// in find_frame, which aborts the process across the C ABI where no Go
		// recover can reach it. The push is the whole of what this arm owes: the
		// upload already carried every row, so the pull has nothing to bring that
		// the push has not already sent, and its only effect is to reach the
		// aborting call.
		//
		// The remote may have moved since the upload, and the tick's own
		// push-then-pull picks that up on the next window -- through a WAL whose
		// ancestry this machine's push established.
		return SeedDecision{Case: SeedExistingDB, Bootstrap: false, Push: true}
	default:
		return SeedDecision{Case: SeedExistingDB, Bootstrap: false, Pull: true, NeedsUpload: true}
	}
}
