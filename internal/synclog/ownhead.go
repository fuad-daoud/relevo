package synclog

import (
	"log/slog"
	"strconv"

	"github.com/fuad-daoud/relevo/internal/db"
)

// OwnHeadFact names the highest sequence this origin's transport confirmed
// appending. The owner's cut extension vouches for it: a tail beyond what landed
// is one the machine never confirmed.
const OwnHeadFact = "sync.own_head.v1"

// noteOwnHead advances the own-head fact to written's highest sequence. It runs
// through the machine-local file, and a failing write is a logged warning, not an
// export failure: the fact recovers on the next append.
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
		// Unreadable is overwritable: what was confirmed now is at least as true
		// as the row it cannot read.
	} else if ok && have >= head {
		return
	}
	if err := kv.KVPut(OwnHeadFact, []byte(strconv.Itoa(head))); err != nil {
		slog.Warn("synclog: write the own-head fact", "err", err)
	}
}

// OwnHead reads the own-head fact; ok is false when nothing appended since its
// introduction. An unparsable row reads as absent: the next confirmed append
// rewrites it.
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
