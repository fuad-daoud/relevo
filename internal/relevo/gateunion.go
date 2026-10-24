package relevo

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
)

// gateunion.go is the served gate source: the one ledger view a served pick
// and a served switch resolve over. The machine database holds two ledger
// rows -- the serve-wide one under "serve." that serve builds and forwards
// write, and the host's own unprefixed "ledger" that `relevo gate <token>` on
// the server host and the synced gates document write (the ledger map lives
// on Runtime.Gates, internal/relevo/runtime.go). AvailabilityDeps binds
// Deps.Gates to this union when a runtime sets GatesLocal, so both surfaces
// see both rows and neither row is ever written through the other.
//
// Reads are the union: every entry of the serve row, plus every entry of the
// host row whose (kind, subject) the serve row does not already hold live --
// a subject present in both rows is gated once, not twice, and the hidden
// copy needs no write because the view is recomputed from both rows at every
// read, so it shows again the moment the serve row's copy stops being live.
//
// Writes are routed, never merged: an entry written through the union lands
// in the serve row -- the primary -- and the host row is left exactly as its
// own writers keep it. An entry that verbatim equals one the host row already
// holds is dropped from the serve write instead of duplicated into it: that
// is the plan's "no duplicate write" for a gate both paths recorded, and the
// subject stays gated through the host's own copy.
//
// The ledger map: the one machine database holds two ledger rows, and every
// gate writer and reader names the row it touches.
//
// Writers (row of the machine database):
//
//  1. `relevo gate <token>` on any host, including the server host
//     (cmd/relevo/gate.go -> availability.Unavailable -> AppendEntryLocked ->
//     saveLedger): row "ledger", unprefixed -- buildRuntime binds Gates to
//     the machine-local file itself (cmd/relevo/wire.go) -- and stamped and
//     signalled there as the gates document (availability/gatestamp.go).
//  2. `relevo gate --serve <token>` (cmd/relevo/serve_admin.go ->
//     serve.AdminUnavailable, internal/serve/admin.go): row "serve.ledger"
//     through the Server's `serve.` PrefixKV (internal/serve/serve.go New);
//     stamped nowhere.
//  3. A served round's own 429/outage detection (daemon ticks on the
//     runtimeAt runtimes, internal/serve/daemon.go -> gateOnLimit/gateOnOutage
//     AppendEntryLocked, internal/relevo/switch.go): row "serve.ledger" --
//     the same row served picks and switches read.
//  4. A client's ForwardUnavailable (internal/relevo/remote_gates.go POSTs
//     /v1/unavailable, internal/serve/routes.go) lands on the server in
//     handleUnavailable (internal/serve/unavailable.go) on a runtimeAt
//     runtime: row "serve.ledger".
//  5. The gates-document sync apply (internal/relevo/syncdocs.go binds
//     availability.OpenGates to the machine-local file; GatesDoc.applyIn,
//     availability/gatedoc.go): row "ledger", unprefixed -- writer 1's row.
//
// Readers:
//
//  1. The served create pick (internal/serve/bindings.go pickServedTier ->
//     PickServedCandidateFor, internal/relevo/served.go): availability.Gates
//     reads only Deps.Gates (availability/gates_projection.go).
//  2. The served chain member pick (internal/relevo/chain_served.go
//     servedActorPick -> the same call): the same view.
//  3. `switchBuilder`'s mid-round switch (internal/relevo/switch.go ->
//     switchGates, internal/relevo/inflight.go): the same view.
//  4. The local bind/send pick and the local daemon's switches, on a
//     buildRuntime runtime: Deps.Gates is the unprefixed row "ledger" there.
//
// Readers 1-3 on a served runtime read Deps.Gates = rt.Gates, which the
// server binds to "serve.ledger" alone -- so writers 1 and 5, which land in
// the unprefixed "ledger" on the very same host, were invisible to them
// until the union below made the served view their join.

// gateUnionKV is the union ledger surface: primary is the row served writes
// go to ("serve.ledger"), host is the machine-local row "ledger" they join on
// reads. now is the clock the covering test prunes against; it is the
// runtime's own, so the view and its readers agree on liveness.
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
		// The host's own row is a second surface a served reader joined on:
		// unreadable, it warns and reads as empty rather than taking the
		// server's gates -- the same rule gates_projection.go gives a ledger
		// read error.
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
// their home there (absent from the serve write, which cannot touch the host
// row anyway), and everything else -- new entries and the serve row's own --
// becomes the serve row. The host row is never written through the union.
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
// every field. An exact copy is the same recorded gate arriving through both
// paths, not a second one to write.
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
