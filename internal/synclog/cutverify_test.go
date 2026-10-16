package synclog

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The cut rule's own cases: an entry above its origin's cut must verify under
// an epoch at least the cut's, history below it verifies by trial under any
// held key, a cut this machine verified stands no matter what a later attempt
// finds in the remote's meta, and an origin that never signed keeps appending
// unsigned history the way it always could.

// rekeyTo2 rekeys log to epoch 2, one cut per origin at the head the log's own
// state holds, tagged under keyNew, with the key the log keyed at epoch 1 --
// the caller's -- sealed as the wrapped one. It answers with the material the
// remote reads back, so a caller can check its claims.
func rekeyTo2(t *testing.T, log *MemTransport, keyOld, keyNew []byte) {
	t.Helper()
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	cuts := make(map[string]EpochCut, len(state.Heads))
	for origin, head := range state.Heads {
		cuts[origin] = EpochCut{Epoch: 2, Seq: head, Tag: EpochCutTag(keyNew, origin, 2, head)}
	}
	if _, err := log.KeyRekey(context.Background(), KeyRekeySpec{
		From: 1,
		Key: KeyMeta{
			Salt: []byte("the rekeyed salt"), KDF: "argon2id", Check: []byte("the rekeyed check"),
			Present: true, Epoch: 2, Wrapped: map[int][]byte{1: keyOld},
		},
		Cuts: cuts,
	}); err != nil {
		t.Fatalf("KeyRekey: %v", err)
	}
}

// ring2 is the ring a current reader holds: both epoch keys.
func ring2() Keyring {
	return Keyring{Epoch: 2, Keys: map[int][]byte{1: keyA, 2: keyB}}
}

func importWithRing(t *testing.T, d *db.DB, log *MemTransport, ring Keyring) (ImportResult, error) {
	t.Helper()
	imp := NewImporter(d, log.OnLog(d.Origin()))
	imp.SetKeyring(ring)
	return imp.Import()
}

// appendOldKey appends one entry MAC'd under the old key, as a machine that
// has not heard of the rekey would have it on the remote.
func appendOldKey(t *testing.T, log *MemTransport, origin, pk string) Entry {
	t.Helper()
	e := syncEntry(t, origin, "binding", pk, map[string]any{"id": "x"})
	e.SchemaVersion = 1
	e.MAC = EntryMAC(keyA, e)
	written, err := log.OnLog(origin).Append([]Entry{e})
	if err != nil {
		t.Fatalf("append under the old key: %v", err)
	}
	return written[0]
}

// TestImportRefusesAnOldKeyWriteAboveTheCut pins the rule above the cut: the
// entry verifies honestly under key 1, and a current reader refuses it
// anyway, because the cut says key 1 is dead above that point.
func TestImportRefusesAnOldKeyWriteAboveTheCut(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t) // m1 at epoch 1, head 2
	rekeyTo2(t, log, keyA, keyB)
	stale := appendOldKey(t, log, "m1", `["01C"]`)
	if !VerifyMAC(keyA, stale) {
		t.Fatal("the stale entry stopped verifying under the old key mid-test")
	}
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `entry 3 from "m1" has a bad signature`) {
		t.Errorf("stall %q lacks the sentence", reason)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]|binding ["01B"]` {
		t.Errorf("m2 holds %q, want exactly the pre-rekey history", got)
	}
	if mark := markOf(t, m2, "m1"); mark != 2 {
		t.Errorf("mark = %d, want 2: the refusal holds the origin there", mark)
	}
}

// TestImportVerifiesHistoryBelowTheCutByTrial pins the rule at or below the
// cut: every held key is on the table, so the machines' pre-rekey history
// verifies again under the key that wrote it.
func TestImportVerifiesHistoryBelowTheCutByTrial(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	rekeyTo2(t, log, keyA, keyB)
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Dropped) != 0 || len(res.Stalled) != 0 {
		t.Fatalf("import reported %+v", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]|binding ["01B"]` {
		t.Errorf("m2 holds %q, want both entries of the pre-rekey history", got)
	}
}

// TestImportAppliesUnsignedHistoryOfANeverSigningOriginAboveItsCut pins C1:
// the cut applies to signed entries. An origin with no signed-from boundary
// may still append unsigned history, below and above its cut, exactly as it
// could before any rekey happened.
func TestImportAppliesUnsignedHistoryOfANeverSigningOriginAboveItsCut(t *testing.T) {
	t.Parallel()
	m1, path := exporterFile(t, "m1")
	log := NewMemTransport("m1")
	seed(t, path, insertBinding("01A", "m1"))
	// m1 has never unlocked: its first entry is unsigned and the cut that
	// later rekey writes lands at its head.
	signedExport(t, m1, log, nil)
	rekeyTo2(t, log, keyA, keyB)
	// The same machine changes another row and exports again, still without a
	// key: another unsigned entry, above its cut now.
	seed(t, path, insertBinding("01B", "m1"))
	signedExport(t, m1, log, nil)
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if len(res.Stalled) != 0 || len(res.Dropped) != 0 {
		t.Fatalf("import reported %+v", res)
	}
	if got := sharedRows(t, m2); got != `binding ["01A"]|binding ["01B"]` {
		t.Errorf("m2 holds %q, want both unsigned rows", got)
	}
}

// TestImportRefusesAnOldKeyWriteAfterRemoteRowDeleted pins that a verified
// cut is recorded at once: the remote's meta is read anew on every attempt,
// and a row that is gone cannot switch checking off for a cut this machine
// has already raised.
func TestImportRefusesAnOldKeyWriteAfterRemoteRowDeleted(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	rekeyTo2(t, log, keyA, keyB)
	m2, _ := peerFile(t, "m2")
	if _, err := importWithRing(t, m2, log, ring2()); err != nil {
		t.Fatalf("first import: %v", err)
	}
	appendOldKey(t, log, "m1", `["01C"]`)
	log.log.mu.Lock()
	log.log.epochCut = nil
	log.log.mu.Unlock()

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("import = %+v, %v; want the old-key write refused under the local record", res, err)
	}
	if got, _ := m2.EpochCuts(); got["m1"] != (db.EpochCut{Epoch: 2, Seq: 2}) {
		t.Errorf("local record = %v, want the cut the first import verified", got)
	}
}

// TestImportRefusesAnOldKeyWriteAfterRemoteRowLowered pins lowering: a remote
// that rewrites its cut row for a lower sequence cannot shrink what this
// machine holds -- the local record was there first and stays.
func TestImportRefusesAnOldKeyWriteAfterRemoteRowLowered(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	rekeyTo2(t, log, keyA, keyB)
	m2, _ := peerFile(t, "m2")
	if _, err := importWithRing(t, m2, log, ring2()); err != nil {
		t.Fatalf("first import: %v", err)
	}
	appendOldKey(t, log, "m1", `["01C"]`)
	// Lower the row honestly: a real tag, at a real epoch, for a lower seq.
	log.log.mu.Lock()
	log.log.epochCut["m1"] = EpochCut{Epoch: 2, Seq: 1, Tag: EpochCutTag(keyB, "m1", 2, 1)}
	log.log.mu.Unlock()

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("import = %+v, %v; want the old-key write refused at seq 3", res, err)
	}
	if got, _ := m2.EpochCuts(); got["m1"] != (db.EpochCut{Epoch: 2, Seq: 2}) {
		t.Errorf("local record = %v, want it back at (2, 2)", got)
	}
}

// TestImportRefusesAReplayedCutRowOfTheDeadEpoch pins replay: a token holder
// that still holds the old key can write a cut tagged under epoch 1, and the
// most it can raise with it is nothing -- the epoch-2 record the reader
// already verified stands and the forgery narrows nothing it needs.
func TestImportRefusesAReplayedCutRowOfTheDeadEpoch(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	rekeyTo2(t, log, keyA, keyB)
	m2, _ := peerFile(t, "m2")
	if _, err := importWithRing(t, m2, log, ring2()); err != nil {
		t.Fatalf("first import: %v", err)
	}
	appendOldKey(t, log, "m1", `["01C"]`)
	// The replay claims a far higher sequence, but under epoch 1 -- the key
	// the attacker holds.
	log.log.mu.Lock()
	log.log.epochCut["m1"] = EpochCut{Epoch: 1, Seq: 99, Tag: EpochCutTag(keyA, "m1", 1, 99)}
	log.log.mu.Unlock()

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil || stallOf(res, "m1") == "" {
		t.Fatalf("import = %+v, %v; want the replay unable to bless the old-key tail", res, err)
	}
	if got, _ := m2.EpochCuts(); got["m1"] != (db.EpochCut{Epoch: 2, Seq: 2}) {
		t.Errorf("local record = %v, want it at (2, 2), not the replay's", got)
	}
}

// TestImportHoldsTheOriginWithARefusedCut pins the otherwise-safe side: a cut
// more... nothing verifies under, one bad row holds
// that origin the way a boundary whose tag fails would, and the rest stays
// readable.
func TestImportHoldsTheOriginWithARefusedCut(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	rekeyTo2(t, log, keyA, keyB)
	log.log.mu.Lock()
	log.log.epochCut["m1"] = EpochCut{Epoch: 2, Seq: 2} // the tag someone else wrote
	log.log.mu.Unlock()
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, ring2())
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `the epoch cut of "m1" does not verify under this machine's keys`) {
		t.Errorf("stall %q lacks the sentence", reason)
	}
	if got := sharedRows(t, m2); got != "" {
		t.Errorf("m2 holds %q after the hold", got)
	}
}

// TestImportAtEpochOneBehavesExactlyAsBefore pins the degeneration: a
// single-key machine without cut rows verifies every entry the way it always
// did, so an upgrade changes nothing until a rekey does.
func TestImportAtEpochOneBehavesExactlyAsBefore(t *testing.T) {
	t.Parallel()
	log, _ := signedLog(t)
	appendUnsigned(t, log, `["01C"]`)
	m2, _ := peerFile(t, "m2")

	res, err := importWithRing(t, m2, log, Keyring{Epoch: 1, Keys: map[int][]byte{1: keyA}})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if reason := stallOf(res, "m1"); !strings.Contains(reason, `"m1" signs from 2`) {
		t.Errorf("stall %q lacks the signed-from refusal", reason)
	}
	if got := markOf(t, m2, "m1"); got != 2 {
		t.Errorf("mark = %d, want the two entries before the refusal applied", got)
	}
}
