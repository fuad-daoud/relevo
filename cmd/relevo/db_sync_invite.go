package main

// `relevo db sync invite`: the one line a machine that already syncs prints so a
// new machine can point itself at the same remote.
//
// stdout is exactly one line -- the code -- so it pipes into
// `relevo db sync enable --join-stdin` unchanged. Everything a person needs to know
// about that line goes to stderr instead, which is where a reader looking at a
// pipe does not expect output and a person looking at a terminal does.
//
// The code is a credential: it carries the bearer token that reaches the remote.
// It is printed on request to the terminal and never logged, never written to a
// file and never included in anything else this program emits.

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/relevo"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncInviteDoc is what `db sync invite --json` prints. Code is the whole
// deliverable and RemoteURL repeats where it points so a reader can see the
// destination without decoding it; Locked says what the machine that printed it
// could not offer.
type dbSyncInviteDoc struct {
	Code      string `json:"code"`
	RemoteURL string `json:"remote_url,omitempty"`
	// Locked says this machine holds no sync key. The code is still usable -- the
	// remote holds a key and a joiner unlocks with the passphrase -- but this
	// machine's own secrets have not been shared, so it is stated rather than
	// left for the reader to assume either way.
	Locked bool `json:"locked"`
	// Stale says this machine holds an older epoch's key than the remote's: the
	// passphrase changed, and this machine has not unlocked with the new one
	// yet. The code still works; the state is stated so the reader knows whose
	// view of the fleet's secrets this machine can vouch for.
	Stale bool `json:"stale"`
}

// dbSyncInviteLine is the stderr note that says what the line on stdout is. It
// exists because stdout stays exactly one line so it can be piped, and a pipe
// that silently carried a bearer credential would be the surprise.
const dbSyncInviteLine = "relevo db sync invite: that line carries the " + relevosync.SecretToken +
	"; it is printed once and never logged"

// dbSyncInviteLockedLine is the extra note a locked machine adds. It is the same
// rule the code's own documentation carries, said where the person reading the
// code is looking.
const dbSyncInviteLockedLine = "relevo db sync invite: this machine is locked, so its own secrets have not been shared; " +
	"a joiner still unlocks from the log"

// dbSyncInviteStaleLine is the extra note a stale machine adds: the remote's
// key moved and this machine holds the old one, so its own view of the fleet's
// secrets is one unlock behind what the code joins a new machine to.
const dbSyncInviteStaleLine = "relevo db sync invite: this machine holds an old key; " +
	"run relevo db sync unlock with the new passphrase"

func dbSyncInviteFlagSet(fs *flag.FlagSet) *dbSyncInviteFlagValues {
	v := &dbSyncInviteFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the invite produced")
	return v
}

// dbSyncInviteFlagValues holds the pointers `db sync invite` parses into. It is
// a named struct like every other verb's so the registry installer has a type
// to name rather than an anonymous one.
type dbSyncInviteFlagValues struct {
	asJSON *bool
}

// cmdDBSyncInvite prints this machine's join code.
//
// It reads the local rows itself rather than asking the daemon for a verb, the
// same way status does and for the same reason: an invite is a description of
// this machine, so it writes nothing and opens no worker.
func cmdDBSyncInvite(args []string) error {
	fs := flag.NewFlagSet("db sync invite", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := dbSyncInviteFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fail(codeUsage, "relevo db sync invite takes no arguments, got %d", fs.NArg())
	}

	shared, local, err := openDBSyncStatus()
	if err != nil {
		return err
	}
	defer func() { _ = shared.Close() }()

	invite, err := relevo.InviteCode(local)
	if err != nil {
		return dbSyncInviteClassify(err)
	}
	parsed, err := relevosync.ParseJoinCode(invite.Code)
	if err != nil {
		// The code was built by NewJoinCode from rows that were just read, so this
		// cannot happen. It is refused rather than printed anyway: printing a code
		// this process cannot read would hand the reader something it would fail
		// on the machine it was meant for.
		return fail(codeInternal, "relevo db sync invite: the code this machine built does not parse: %v", err)
	}
	if *v.asJSON {
		return printDoc(dbSyncInviteDoc{
			Code:      invite.Code,
			RemoteURL: parsed.URL,
			Locked:    invite.Locked,
			Stale:     invite.Stale,
		})
	}
	fmt.Fprintln(os.Stderr, dbSyncInviteLine)
	for _, note := range dbSyncInviteNotes(invite) {
		fmt.Fprintln(os.Stderr, note)
	}
	// The only thing on stdout, and the only thing a pipe should carry.
	fmt.Println(invite.Code)
	return nil
}

// dbSyncInviteNotes is everything the human output says beside the code, as a
// function of what the machine that printed it holds. It is pulled out of the
// verb so the rule can be pinned without an owner: a locked machine says so, a
// stale one says its key is old and names the unlock that fixes it, and an
// unlocked, current one adds nothing. Leaving the ordinary case out is
// deliberate -- an unconditional warning trains a reader to skip the lines that
// matter on the machines they are true for.
func dbSyncInviteNotes(invite relevo.Invite) []string {
	var notes []string
	if invite.Locked {
		notes = append(notes, dbSyncInviteLockedLine)
	}
	if invite.Stale {
		notes = append(notes, dbSyncInviteStaleLine)
	}
	return notes
}

// dbSyncInviteClassify maps an invite refusal onto this CLI's exit codes.
//
// The two refusals a reader can act on name the verb that answers them, the way
// every other refusal on this surface does: this machine is not syncing, or the
// remote holds no key yet and a joiner could not unlock what it was handed.
func dbSyncInviteClassify(err error) error {
	switch {
	case errors.Is(err, relevosync.ErrInviteSyncOff):
		return failNext(codeRefused, "relevo db sync enable", "%v", err)
	case errors.Is(err, relevosync.ErrNoKDF):
		return failNext(codeRefused, "relevo db sync unlock --new", "%v", err)
	case errors.Is(err, relevosync.ErrNoRemote), errors.Is(err, relevosync.ErrNoToken),
		errors.Is(err, relevosync.ErrJoinCode):
		return fail(codeUsage, "%v", err)
	}
	return dbSyncClassify(err)
}
