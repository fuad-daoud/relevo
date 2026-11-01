package verb

// `relevo db sync invite`: the code a machine that already syncs prints so a new
// machine can point at the same remote.
//
// It reads machine-local rows and nothing else -- no worker, no log, no network
// -- so an invite answers with the network blackholed, the same rule status
// follows, because a description of this machine is a read of this machine.
//
// Two refusals matter most: a machine that is not syncing has no remote to
// name, and a remote holding no key would hand a joiner a code it can follow
// but not unlock with. Both are refused by name.

import (
	"bytes"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// Invite is what one invite produced.
//
// Locked is about this machine, not the code: a locked machine still prints a
// usable one, because a joiner unlocks against the remote's key. What it cannot
// offer is its own secrets, since it cannot read the ones it holds.
//
// Stale is about this machine's key: the remote has rekeyed and this machine
// holds the old key. The code still works, but this machine cannot read what
// the rekeyed fleet seals until its own unlock with the new passphrase.
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
// The order is the contract: whether this machine syncs at all, then whether it
// has somewhere to point a joiner, then the token, then whether the remote
// holds a key a joiner could unlock. Each refusal names the verb that answers
// it.
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
	// The stale state is the recorded key state, read the way status reads it: a
	// machine one unlock behind the fleet says so beside the code rather than
	// letting a joiner guess why its secrets look unreadable.
	state, err := relevosync.KeyState(l)
	if err != nil {
		return Invite{}, err
	}
	return Invite{Code: code, Locked: !keyed, Stale: state == "stale"}, nil
}
