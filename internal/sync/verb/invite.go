package verb

// `relevo db sync invite`: the code a machine that already syncs prints so a
// new machine can point itself at the same remote.
//
// It reads machine-local rows and nothing else. No worker is opened, no log is
// read and no network is touched, so an invite answers with the network
// blackholed -- which is the same rule status follows, and for the same reason: a
// description of this machine is a read of this machine.
//
// Two refusals matter more than the rest. A machine that is not syncing has no
// remote to name, and a remote that holds no key would hand a joiner a code it
// can follow and then not be able to unlock, so it would join holding nothing at
// all. Both are refused by name, and the second names the verb that fixes it.

import (
	"bytes"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// Invite is what one invite produced.
//
// Locked is about this machine, not about the code: a locked machine still
// prints a usable one, because the remote holds a key and a joiner can unlock
// with the passphrase. What it cannot offer is its own secrets, since it cannot
// read the ones it holds. The caller says so rather than letting the reader
// assume a code carries the inviter's whole configuration.
//
// Stale is about this machine's key, not about the code either: the remote has
// rekeyed and this machine holds the old key. The code still works -- the
// remote's newest key unlocks a joiner -- but this machine cannot read the
// values the rekeyed fleet seals, so it says so, and the one act that unblocks
// it is its own unlock with the new passphrase.
type Invite struct {
	// Code is the one line stdout carries.
	Code string
	// Locked says this machine holds no sync key, so the code lets a joiner
	// unlock while this machine's own secrets have not been shared.
	Locked bool
	// Stale says this machine holds an older epoch's key than the remote's:
	// the passphrase changed and this machine has not unlocked with the new
	// one yet.
	Stale bool
}

// InviteCode is the join code this machine hands a new one, read out of the rows
// sync already keeps.
//
// The order is the contract and it is the order a reader needs it in: whether
// this machine syncs at all, then whether it has somewhere to point a joiner,
// then whether it can reach that remote, and only then whether the remote holds
// a key a joiner could unlock. Each refusal names the verb that answers it, so
// the person holding the code is never left with a line that fails on the machine
// it was meant for.
func InviteCode(l relevosync.Local) (Invite, error) {
	on, err := relevosync.Enabled(l)
	if err != nil {
		return Invite{}, err
	}
	if !on {
		return Invite{}, relevosync.ErrInviteSyncOff
	}
	settings, err := relevosync.ReadSettings(l)
	if err != nil {
		return Invite{}, err
	}
	if settings.RemoteURL == "" {
		return Invite{}, relevosync.ErrNoRemote
	}
	token, ok, err := relevosync.ReadToken(l)
	if err != nil {
		return Invite{}, err
	}
	if !ok || len(bytes.TrimSpace(token)) == 0 {
		return Invite{}, relevosync.ErrNoToken
	}
	// Whether the remote holds a key is a fact this machine already recorded when
	// it last read it, so the answer is a local read rather than a round trip. A
	// machine that has never recorded it has never talked to a remote holding
	// one, and is refused the same way.
	fact, ok, err := l.KVGet(relevosync.KeyRemoteKDF)
	if err != nil {
		return Invite{}, err
	}
	if !ok || string(bytes.TrimSpace(fact)) != "1" {
		return Invite{}, relevosync.ErrNoKDF
	}
	code, err := relevosync.NewJoinCode(settings.RemoteURL, token)
	if err != nil {
		return Invite{}, err
	}
	_, keyed, err := relevosync.ReadKey(l)
	if err != nil {
		return Invite{}, err
	}
	// The stale state is the key state this machine already recorded, read the
	// same way status reads it: a machine whose held newest epoch sits below
	// the remote's is one unlock behind the fleet, and it says so beside the
	// code rather than letting a joiner guess why its secrets look unreadable.
	state, err := relevosync.KeyState(l)
	if err != nil {
		return Invite{}, err
	}
	return Invite{Code: code, Locked: !keyed, Stale: state == "stale"}, nil
}
