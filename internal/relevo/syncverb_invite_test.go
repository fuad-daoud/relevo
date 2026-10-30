package relevo

// These tests drive invite as the pure function it is: machine-local rows in, a
// code and a locked fact out. Nothing here dials, opens a worker or reads a log,
// which is the property the rule is about -- an invite describes this machine, so
// it must answer with the network blackholed.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
)

const inviteRemote = "libsql://fleet.invalid"

// inviteMachine is one machine-local file with the rows an invite reads. The
// caller sets up the state it wants and the function under test decides whether
// that machine may hand out a code.
func inviteMachine(t *testing.T) relevosync.Local {
	t.Helper()
	shared, err := db.OpenSplit(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: "m1"})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := relevosync.LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	return local
}

// readyInvite brings a machine to the state an invite is meant for: syncing,
// pointed at a remote, holding the token and recording that the remote holds a
// key a joiner could unlock.
func readyInvite(t *testing.T, l relevosync.Local, keyed bool) {
	t.Helper()
	now := time.Unix(0, 0).UTC()
	if err := relevosync.MarkEnabled(l, true, now); err != nil {
		t.Fatalf("MarkEnabled: %v", err)
	}
	if err := relevosync.PutSettings(l, relevosync.Settings{RemoteURL: inviteRemote}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	if err := relevosync.SetToken(l, []byte(cfgToken), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	if err := l.KVPut(relevosync.KeyRemoteKDF, []byte("1")); err != nil {
		t.Fatalf("KVPut %s: %v", relevosync.KeyRemoteKDF, err)
	}
	if keyed {
		if err := relevosync.SetKey(l, cfgSyncKey, now); err != nil {
			t.Fatalf("SetKey: %v", err)
		}
	}
}

// The code is the whole deliverable: a machine that syncs, points somewhere and
// can reach that remote prints one, and it carries the two things a joiner
// cannot get any other way.
func TestInvitePrintsACodeCarryingTheRemoteAndTheToken(t *testing.T) {
	t.Parallel()
	l := inviteMachine(t)
	readyInvite(t, l, true)

	got, err := verb.InviteCode(l)
	if err != nil {
		t.Fatalf("InviteCode: %v", err)
	}
	parsed, err := relevosync.ParseJoinCode(got.Code)
	if err != nil {
		t.Fatalf("the code this machine printed does not parse: %v", err)
	}
	if parsed.URL != inviteRemote {
		t.Errorf("the code names remote %q, want %q", parsed.URL, inviteRemote)
	}
	if parsed.Token != cfgToken {
		t.Errorf("the code's token is not the one this machine stores")
	}
	if got.Locked {
		t.Error("an unlocked machine reported itself locked")
	}
}

// A locked machine still prints a code: the remote holds a key, so a joiner can
// unlock and get everything the log carries. What it cannot offer is its own
// secrets, so the caller is told rather than left to assume a code brought them.
func TestInviteOnALockedMachineStillPrintsACodeAndSaysItIsLocked(t *testing.T) {
	t.Parallel()
	l := inviteMachine(t)
	readyInvite(t, l, false)

	got, err := verb.InviteCode(l)
	if err != nil {
		t.Fatalf("a locked machine refused to print a code: %v", err)
	}
	if !got.Locked {
		t.Error("a machine holding no key reported itself unlocked")
	}
	if _, err := relevosync.ParseJoinCode(got.Code); err != nil {
		t.Errorf("the code a locked machine printed does not parse: %v", err)
	}
}

// TestInviteSaysWhenThisMachineHoldsAnOldKey pins the stale fact: a machine
// whose held newest epoch sits below the recorded remote epoch still prints the
// code -- the remote's newest key unlocks a joiner -- and says its key is old,
// which is the state the human note and the --json field both carry.
func TestInviteSaysWhenThisMachineHoldsAnOldKey(t *testing.T) {
	t.Parallel()
	l := inviteMachine(t)
	readyInvite(t, l, true)
	// The remote rekeyed to epoch 2 and the last attempt recorded it, while
	// this machine still holds the epoch-1 key readyInvite set.
	if err := l.KVPut(relevosync.SyncRemoteEpoch, []byte("2")); err != nil {
		t.Fatalf("KVPut %s: %v", relevosync.SyncRemoteEpoch, err)
	}

	got, err := verb.InviteCode(l)
	if err != nil {
		t.Fatalf("a stale machine refused to print a code: %v", err)
	}
	if got.Stale != true || got.Locked != false {
		t.Errorf("invite = (stale %v, locked %v), want stale and not locked", got.Stale, got.Locked)
	}
	if _, err := relevosync.ParseJoinCode(got.Code); err != nil {
		t.Errorf("the code a stale machine printed does not parse: %v", err)
	}

	// A machine whose key names the remote's newest epoch says nothing.
	if err := l.KVPut(relevosync.SyncRemoteEpoch, []byte("1")); err != nil {
		t.Fatalf("KVPut %s: %v", relevosync.SyncRemoteEpoch, err)
	}
	if got, err := verb.InviteCode(l); err != nil || got.Stale {
		t.Fatalf("a current machine's invite = (stale %v, %v), want stale false", got.Stale, err)
	}
}

// The three refusals. Each names the verb that answers it, because the person
// holding the machine is the one who has to run it.
func TestInviteRefusesAMachineThatCannotHandOutACode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		// arrange brings the machine to the state the refusal is about.
		arrange func(t *testing.T, l relevosync.Local)
		want    error
	}{
		{
			name:    "sync off",
			arrange: func(*testing.T, relevosync.Local) {},
			want:    relevosync.ErrInviteSyncOff,
		},
		{
			name: "no remote",
			arrange: func(t *testing.T, l relevosync.Local) {
				readyInvite(t, l, true)
				now := time.Unix(0, 0).UTC()
				if err := relevosync.PutSettings(l, relevosync.Settings{}, now); err != nil {
					t.Fatalf("PutSettings: %v", err)
				}
			},
			want: relevosync.ErrNoRemote,
		},
		{
			name: "no token",
			arrange: func(t *testing.T, l relevosync.Local) {
				readyInvite(t, l, true)
				if err := relevosync.DeleteToken(l); err != nil {
					t.Fatalf("DeleteToken: %v", err)
				}
			},
			want: relevosync.ErrNoToken,
		},
		{
			name: "the remote holds no key",
			arrange: func(t *testing.T, l relevosync.Local) {
				readyInvite(t, l, true)
				if err := l.KVPut(relevosync.KeyRemoteKDF, []byte("0")); err != nil {
					t.Fatalf("KVPut: %v", err)
				}
			},
			want: relevosync.ErrNoKDF,
		},
		{
			name: "the remote's key was never learned",
			arrange: func(t *testing.T, l relevosync.Local) {
				readyInvite(t, l, true)
				if err := l.KVDelete(relevosync.KeyRemoteKDF); err != nil {
					t.Fatalf("KVDelete: %v", err)
				}
			},
			want: relevosync.ErrNoKDF,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l := inviteMachine(t)
			tc.arrange(t, l)
			got, err := verb.InviteCode(l)
			if !errors.Is(err, tc.want) {
				t.Fatalf("InviteCode = (%+v, %v), want %v", got, err, tc.want)
			}
			if got.Code != "" {
				t.Errorf("a refused invite produced the code %q", got.Code)
			}
		})
	}
}

// The no-key refusal is the one whose message has to carry the fix, because
// "the remote holds no key yet" is not something a reader can act on by
// themselves. It names the verb that creates one.
func TestInvitesNoKeyRefusalNamesUnlockNew(t *testing.T) {
	t.Parallel()
	l := inviteMachine(t)
	readyInvite(t, l, true)
	if err := l.KVDelete(relevosync.KeyRemoteKDF); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	_, err := verb.InviteCode(l)
	if !errors.Is(err, relevosync.ErrNoKDF) {
		t.Fatalf("InviteCode = %v, want ErrNoKDF", err)
	}
	if got := err.Error(); !strings.Contains(got, "unlock --new") {
		t.Errorf("the refusal %q does not name the verb that creates the key", got)
	}
}
