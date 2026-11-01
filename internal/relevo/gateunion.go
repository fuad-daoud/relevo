package relevo

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// gateunion.go is the served gate source: reads are the serve row plus every
// host entry whose (kind, subject) the serve row does not hold live,
// recomputed at every read, so a hidden host copy shows again once the serve
// copy expires. Writes land in the serve row alone.

// gateUnionKV is the union ledger surface: primary is the row served writes
// go to ("serve.ledger"), host is the machine-local row "ledger" they join on
// reads. now is the runtime's own clock, so the view and its readers agree on
// liveness.
type gateUnionKV struct {
	primary db.KV
	host    db.KV
	now     func() time.Time
}

var _ db.KV = gateUnionKV{}

// ledgerRowKey repeats the one key availability reads and writes: the union
// exists for that row alone, every other key passes to the primary untouched.
const ledgerRowKey = "ledger"

func (u gateUnionKV) KVGet(key string) ([]byte, bool, error) {
	if key != ledgerRowKey {
		return u.primary.KVGet(key)
	}
	serve, err := availability.LoadLedger(u.primary)
	if err != nil {
		return nil, false, err
	}
	host, err := availability.LoadLedger(u.host)
	if err != nil {
		// The host's own row reads as empty when unreadable rather than
		// taking the server's gates: the same rule gates_projection.go gives
		// a ledger read error.
		fmt.Fprintf(os.Stderr, "relevo: could not read host ledger: %v\n", err)
		host = availability.Ledger{}
	}

	merged := availability.Ledger{Entries: serve.Entries, Other: serve.Other}
	live := map[gateUnionSubject]bool{}
	for _, e := range serve.Entries {
		if !e.Expired(u.now()) {
			live[gateUnionSubjectOf(e)] = true
		}
	}
	for _, e := range host.Entries {
		if live[gateUnionSubjectOf(e)] {
			continue
		}
		merged.Entries = append(merged.Entries, e)
	}
	for _, raw := range host.Other {
		if !containsRaw(merged.Other, raw) {
			merged.Other = append(merged.Other, raw)
		}
	}

	return encodeLedgerDoc(merged)
}

// KVPut routes the write: entries the host row already holds verbatim keep
// their home there, and everything else becomes the serve row. The host row is
// never written through the union.
func (u gateUnionKV) KVPut(key string, value []byte) error {
	if key != ledgerRowKey {
		return u.primary.KVPut(key, value)
	}
	doc, err := decodeLedgerDoc(value)
	if err != nil {
		return err
	}
	host, err := availability.LoadLedger(u.host)
	if err != nil {
		host = availability.Ledger{}
	}

	out := availability.Ledger{}
	for _, e := range doc.Entries {
		if hostHolds(host, e) {
			continue
		}
		out.Entries = append(out.Entries, e)
	}
	for _, raw := range doc.Other {
		if containsRaw(host.Other, raw) {
			continue
		}
		out.Other = append(out.Other, raw)
	}
	return availability.SaveLedger(u.primary, out)
}

func (u gateUnionKV) KVDelete(key string) error { return u.primary.KVDelete(key) }

// gateUnionSubject is the identity a double-count is decided on: one gate per
// kind and subject, whichever row it came from.
type gateUnionSubject struct {
	kind    availability.Kind
	subject string
}

func gateUnionSubjectOf(e availability.Entry) gateUnionSubject {
	return gateUnionSubject{kind: e.Kind, subject: e.Subject}
}

// hostHolds reports whether the host row already holds e exactly: the same
// recorded gate arriving through both paths, not a second one to write.
func hostHolds(host availability.Ledger, e availability.Entry) bool {
	for _, h := range host.Entries {
		if h.Kind == e.Kind && h.Subject == e.Subject && h.At.Equal(e.At) &&
			h.Until.Equal(e.Until) && h.Note == e.Note && h.Source == e.Source && h.Binding == e.Binding {
			return true
		}
	}
	return false
}

func containsRaw(raws []json.RawMessage, raw json.RawMessage) bool {
	for _, r := range raws {
		if string(r) == string(raw) {
			return true
		}
	}
	return false
}

// bytesKV serves one ledger document back through availability's own loader,
// so the union decodes a write's bytes with the ledger's real rules rather
// than a second copy of them.
type bytesKV struct{ data []byte }

func (b bytesKV) KVGet(string) ([]byte, bool, error) { return b.data, true, nil }
func (b bytesKV) KVPut(string, []byte) error         { return nil }
func (b bytesKV) KVDelete(string) error              { return nil }

func decodeLedgerDoc(data []byte) (availability.Ledger, error) {
	return availability.LoadLedger(bytesKV{data: data})
}

// encodeLedgerDoc renders a ledger the way availability.SaveLedger's row
// looks, so a reader's decode cannot tell the union's view from a stored
// document.
func encodeLedgerDoc(l availability.Ledger) ([]byte, bool, error) {
	var raws []json.RawMessage
	for _, e := range l.Entries {
		raw, err := json.Marshal(e)
		if err != nil {
			return nil, false, err
		}
		raws = append(raws, raw)
	}
	raws = append(raws, l.Other...)
	doc := struct {
		Entries []json.RawMessage `json:"entries"`
	}{Entries: raws}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}
