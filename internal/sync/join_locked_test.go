package sync

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// A machine whose own origin already signs, joining with no key: the enable has
// to finish and turn the machine on, because unlock refuses on a machine that is
// off, and the history the join could not send has to go out once unlock has a
// key.

// lockedJoin is a machine "m1" with one binding of its own to send, on a log
// where m1 already signs.
func lockedJoin(t *testing.T) (*db.DB, Local, *synclog.MemTransport) {
	t.Helper()
	shared, local := joinPair(t, "m1")
	passGate(t, shared)
	seedJoin(t, shared, joinBinding("b1", "m1"))
	log := synclog.NewMemTransport("m1")
	_, known := shared.SchemaVersions()
	signed, err := synclog.NewUpsert("m1", "binding", `["old"]`, known, farBody(t, "old"), time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	signed.MAC = synclog.EntryMAC(syncTestKey, signed)
	boundary := synclog.Boundary{Seq: 1, Tag: synclog.BoundaryTag(syncTestKey, "m1", 1)}
	if _, err := log.OnLog("m1").AppendSigned([]synclog.Entry{signed}, boundary); err != nil {
		t.Fatal(err)
	}
	return shared, local, log
}

func TestALockedJoinOnASignedOriginStillTurnsTheMachineOn(t *testing.T) {
	t.Parallel()
	shared, local, log := lockedJoin(t)

	res, err := runEnable(t, shared, local, []byte(joinToken), log)
	if err != nil {
		t.Fatalf("enable: %v, want a locked machine to finish its join", err)
	}
	if !res.Locked {
		t.Error("the result does not say the reconcile was held for the lock")
	}
	if on, _ := Enabled(local); !on {
		t.Error("the machine is off, so unlock could never run on it")
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("the join marker was cleared while this origin's history is still owed")
	}
	if has, _ := shared.HasOutboxEntries(); !has {
		t.Error("the outbox was emptied while the history is still owed")
	}
}
