package synclog

import (
	"log/slog"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
)

// OwnHeadFact is the kv fact naming the highest sequence this origin's own
// transport confirmed appending. It is what the owner's cut extension vouches
// for: a far larger tail than what has actually landed is one the machine
// itself never confirmed, so the fact is written after every successful
// append and read again for the extension.
const OwnHeadFact = "sync.own_head.v1"

// noteOwnHead advances the own-head fact to written's highest sequence, when
// that is past what the fact already holds. It runs through the machine-local
// file -- the fact is this machine's own record, nothing a peer benefits from
// -- and a write that fails is a logged warning rather than an export
// failure: the fact recovers on the next append, and gating the exchange on
// it would stop the whole fleet for one bookkeeping row.
func noteOwnHead(kv db.KV, written []Entry) {
	head := 0
	for _, e := range written {
		if e.Seq > head {
			head = e.Seq
		}
	}
	if head == 0 {
		return
	}
	if have, ok, err := OwnHead(kv); err != nil {
		// Unreadable is overwritable: the fact is replaced with what was
		// confirmed now, which is at least as true as the row it cannot read.
	} else if ok && have >= head {
		return
	}
	if err := kv.KVPut(OwnHeadFact, []byte(strconv.Itoa(head))); err != nil {
		slog.Warn("synclog: write the own-head fact", "err", err)
	}
}

// OwnHead reads the own-head fact; ok is false when nothing has appended on
// this machine's transport since the fact was introduced. A row that does not
// parse reads as absent for the same reason noteOwnHead overwrites an
// unreadable one: the next confirmed append rewrites it.
func OwnHead(kv db.KV) (int, bool, error) {
	value, ok, err := kv.KVGet(OwnHeadFact)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	head, err := strconv.Atoi(string(value))
	if err != nil || head < 1 {
		return 0, false, nil
	}
	return head, true, nil
}
