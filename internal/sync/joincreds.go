package sync

// The two checks a join runs that a plain enable does not, because a join is the
// one enable that arrives without its bucket credentials: the code has nowhere
// to carry them, so they are owed in the secrets document, and both questions --
// "do we have them yet" and "can the rows be fetched" -- are only answerable
// around that document rather than before it.
//
// A plain enable has them in hand from its flags, so neither check touches it:
// its own preflight already refused for their absence, before anything was
// stored, and saying so again here would only repeat a decision it has made.

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrNoR2AfterJoin reports a join whose document import brought no bucket
// credentials. The enable did not refuse for it: a code has nowhere to carry
// them, so they were owed in the secrets document and their absence is a fact
// about the fleet rather than about this command. It is refused here rather
// than left to the first body fetch, because a joiner that turns sync on
// without them reads as a working machine and then holds every origin it could
// not fetch.
//
// The two fixes are named because either one clears it, and which one applies is
// a fact this machine cannot see: the inviter may never have configured a bucket
// (so its own machines cannot move bodies either), or it may have and this
// machine's document import simply did not reach them.
var ErrNoR2AfterJoin = errors.New("sync: enable --join: the fleet's secrets document carried no " +
	SecretR2Endpoint + ", " + SecretR2Bucket + ", " + SecretR2KeyID + " and " + SecretR2Secret +
	": either run `relevo db sync enable --r2-endpoint ... --r2-bucket ... --r2-key-id ... --r2-secret-stdin` " +
	"on this machine, or fix the machine that ran `relevo db sync invite`, whose own secrets have not been shared")

// prepassSecrets applies the fleet's secrets document ahead of the join's rows.
// A plain enable has nothing to unblock -- its credentials are stored by the
// time it imports -- so only a join runs the walk.
func (e *Enabler) prepassSecrets(transport synclog.LogTransport) error {
	if !e.Request.Join {
		return nil
	}
	return PrepassSecrets(e.Request.Shared, e.Request.Local, transport)
}

// PrepassSecrets applies the fleet's secrets document ahead of the join's rows,
// for a join whose R2 credentials are not stored. Both an enable's importer and
// the worker the passphrase check ran on reach it, so the document is applied
// once behind whichever transport first could -- the check's worker exists
// before an importer does.
//
// It is a no-op when the stored set is complete, and a no-op on a machine with
// no key either: a locked machine parks what it cannot verify, which is the
// existing path and needs no help from here.
//
// A failure is the caller's failure. The pre-pass reads the log the caller is
// about, so a log that will not answer fails the caller rather than letting it
// continue into the same refusal one step later.
//
// Nothing here moves an import mark: the entries the import reaches later are
// redeliveries it applies as no-ops.
func PrepassSecrets(shared *db.DB, l Local, transport synclog.LogTransport) error {
	stored, err := ReadR2(l)
	if err != nil {
		return err
	}
	if stored.Complete() {
		return nil
	}
	imp := synclog.NewImporter(shared, transport)
	if err := BindVerifier(imp, l); err != nil {
		return err
	}
	BindDocs(imp, shared)
	if _, err := imp.PrepassDocs(SecretsDocName); err != nil {
		return fmt.Errorf("sync: enable: apply the %s document: %w", SecretsDocName, err)
	}
	return nil
}

// checkJoinR2 is the completeness check a join owes its operator: the document
// import has run, so either the credentials arrived or they did not, and a join
// that turned the machine on without them would read as a working machine.
//
// A machine that was already configured is left alone. The join's contract is
// that a code carries the credentials, not that a code is the only way to have
// them, and a machine that set them up by hand has nothing to be told.
func (e *Enabler) checkJoinR2() error {
	if !e.Request.Join {
		return nil
	}
	stored, err := ReadR2(e.Request.Local)
	if err != nil {
		return err
	}
	if stored.Complete() {
		return nil
	}
	return ErrNoR2AfterJoin
}
