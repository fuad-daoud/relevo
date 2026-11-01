package sync

// The two checks a join runs that a plain enable does not: a join arrives without
// bucket credentials -- the code carries none, the secrets document owes them -- so
// "have them yet" and "fetchable" answer only around that document. A plain enable
// holds them from its flags, preflight-refused before storing, so neither check
// touches it.

import (
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// ErrNoR2AfterJoin reports a join whose document import brought no bucket
// credentials: owed in the secrets document, so absence is the fleet's fact, not
// this command's. Refused here rather than at first body fetch, since a joiner
// syncing on without them reads working while holding every unfetchable origin.
// Both fixes are named and either clears: the inviter never configured a bucket, or
// this import never reached it -- unseen from here.
var ErrNoR2AfterJoin = errors.New("sync: enable --join: the fleet's secrets document carried no " +
	SecretR2Endpoint + ", " + SecretR2Bucket + ", " + SecretR2KeyID + " and " + SecretR2Secret +
	": either run `relevo db sync enable --r2-endpoint ... --r2-bucket ... --r2-key-id ... --r2-secret-stdin` " +
	"on this machine, or fix the machine that ran `relevo db sync invite`, whose own secrets have not been shared")

// prepassSecrets applies the fleet's secrets document ahead of the join's rows; a
// plain enable already stores its credentials by import, so only a join walks.
func (e *Enabler) prepassSecrets(transport synclog.LogTransport) error {
	if !e.Request.Join {
		return nil
	}
	return PrepassSecrets(e.Request.Shared, e.Request.Local, transport)
}

// PrepassSecrets applies the fleet's secrets document ahead of the join's rows, for
// a join whose R2 credentials are not stored; importer and passphrase-check worker
// both reach it, so it applies once behind whichever transport first could. Complete
// stored set or keyless machine is a no-op: a locked machine parks the unverifiable
// on the existing path. A failure is the caller's: the pre-pass reads the caller's
// log, so a silent log fails the caller rather than deferring one step. No import
// mark moves: later entries are redeliveries applied as no-ops.
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

// checkJoinR2 is the completeness check a join owes its operator after the document
// import: arrived or not, since turning on without them reads as working. An
// already-configured machine is left alone: a code carries the credentials but is
// not the only way to have them.
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
