package relevo

// The mapping from a sync failure onto the closed set of codes the wire carries.
//
// It is its own file because the table it is has grown past what belongs beside
// the executor: the executor runs verbs, and this decides what a caller is told
// about one. Keeping them apart also keeps the file each of them fits in.

import (
	"errors"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// classify maps a sync package error onto the closed set of refusal codes. The
// mapping is by sentinel rather than by message, so a wording change cannot
// change which code a caller maps and no remote-chosen body can name a class.
func verbClassify(err error) string {
	switch {
	case errors.Is(err, relevosync.ErrNoToken):
		return wire.SyncCodeNoToken
	case errors.Is(err, relevosync.ErrNoRemote):
		return wire.SyncCodeNoRemote
	case errors.Is(err, relevosync.ErrAlreadyEnabled):
		return wire.SyncCodeAlreadyEnabled
	case errors.Is(err, relevosync.ErrRemoteConflict):
		return wire.SyncCodeRemoteConflict
	case errors.Is(err, relevosync.ErrSeedUploadRequired):
		return wire.SyncCodeSeedUploadRequired
	case errors.Is(err, relevosync.ErrAuthRefused):
		return wire.SyncCodeAuthRefused
	case errors.Is(err, relevosync.ErrRemoteSchema):
		// The remote has no table for the rows this machine is pushing, which is
		// not a fault in the call and not something re-running it fixes: the DDL
		// never reached the remote. It is its own code rather than a share of the
		// ordinary refusal because the way out is a different one, and a reader
		// sent to `relevo bugreport` for a remote that never learned the schema
		// has been sent to report a driver boundary as a defect.
		return wire.SyncCodeRemoteSchemaMissing
	case errors.Is(err, relevosync.ErrRemoteRefused):
		// A remote refusing a statement is a refusal, not an internal failure:
		// the message names the constraint and the call, and the reader's answer
		// is a change set this machine can push, not a defect report.
		return wire.SyncCodeRemoteRefused
	case errors.Is(err, relevosync.ErrNotSynced):
		// The open's role gate refused, so this file is not a member of a sync.
		// It is a refusal rather than an internal failure because a reader can
		// act on it: the fix is to turn sync on, which is a command. Classifying
		// it as internal would point the reader at `relevo bugreport` for
		// something they did right.
		//
		// The same sentinel answers an open that named no role at all, which is
		// a defect in this tree rather than on the reader's machine. It is still
		// not internal: an unset role reaches the reader with a path and a
		// sentence saying membership was not created, and no exit code makes that
		// sentence actionable. The refusal class is the honest one either way,
		// and the message is what says which of the two happened.
		return wire.SyncCodeInvalid
	case errors.Is(err, db.ErrPreflightRefused):
		return wire.SyncCodePreflightRefused
	case errors.Is(err, db.ErrContended):
		// A busy database is a refusal, not a defect: what held the step was
		// another writer that has since ended, and the reader's answer is to run
		// the verb again. Classifying it as internal would send that reader to
		// `relevo bugreport` for a machine that is working.
		return wire.SyncCodeContended
	case errors.Is(err, db.ErrLocked):
		return wire.SyncCodeRemoteUnreachable
	case errors.Is(err, db.ErrInvalid):
		return wire.SyncCodeInvalid
	}
	return wire.SyncCodeInternal
}
