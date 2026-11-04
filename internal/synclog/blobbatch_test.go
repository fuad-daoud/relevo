package synclog

// The bounds one mover call carries and the rules that put bodies inside them:
// the chunk split as a pure function, the dedupe that keeps two equal bodies
// off one staging file name, the per-key verdicts a batch returns while a
// sibling failed, and the stop that sends no further chunk after one fails.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/blobstore"
)

// scriptedMover answers mover calls from the mem bucket behind it while
// recording every batch it was sent, so a test counts calls and keys rather
// than reading the store. getFails scripts a per-key failure beside keys the
// store would serve, and noVerdicts stands in for a mover that answers a batch
// with nothing at all.
type scriptedMover struct {
	*MemBlobMover
	putCalls   [][]BlobBatchItem
	getCalls   [][]BlobBatchItem
	getFails   map[string]error
	noVerdicts bool
	// callErr answers every batch as a whole-call failure: no verdicts at all,
	// the way a pipe miss arrives.
	callErr error
	// unstage keeps the verdicts but swallows the files they claim, standing
	// for a mover that says it wrote a body it did not leave.
	unstage bool
}

func newScriptedMover() *scriptedMover {
	return &scriptedMover{MemBlobMover: NewMemBlobMover()}
}

func (m *scriptedMover) PutBlobs(items []BlobBatchItem) ([]BlobBatchResult, error) {
	m.putCalls = append(m.putCalls, items)
	if m.noVerdicts {
		return nil, nil
	}
	if m.callErr != nil {
		return nil, m.callErr
	}
	return m.MemBlobMover.PutBlobs(items)
}

func (m *scriptedMover) GetBlobs(items []BlobBatchItem) ([]BlobBatchResult, error) {
	m.getCalls = append(m.getCalls, items)
	if m.noVerdicts {
		return nil, nil
	}
	if m.callErr != nil {
		return nil, m.callErr
	}
	results, err := m.MemBlobMover.GetBlobs(items)
	for n := range results {
		if fail, ok := m.getFails[results[n].Key]; ok {
			results[n].Err = fail
		}
		if m.unstage && results[n].Err == nil {
			if rmErr := os.Remove(items[n].Staging); rmErr != nil {
				return nil, rmErr
			}
		}
	}
	return results, err
}

// callsOf is the shape of recorded mover calls: keys per call, in order.
func callsOf(calls [][]BlobBatchItem) []int {
	out := make([]int, len(calls))
	for n, call := range calls {
		out[n] = len(call)
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n] != b[n] {
			return false
		}
	}
	return true
}

// pendingFor is the pending body a row hands uploadPending: the key the origin
// gives the digest, and the bytes under it.
func pendingFor(origin string, body []byte) pendingBlob {
	ref := RefFor(body, 0)
	return pendingBlob{Key: blobstore.Key(origin, ref.SHA256), Bytes: body, Ref: ref}
}

// The split is one rule for both directions: sixteen keys and the declared
// bytes cap each call, in order, and a body bigger than the whole byte cap
// travels alone because no sibling fits beside it. Bodies past MaxBlobBytes
// cannot reach a real batch -- the row and the ref refuse them -- so the
// alone-branch is pinned on the pure function, where any size is declarable
// without holding it in memory.
func TestBatchChunkHoldsTheKeyAndByteCaps(t *testing.T) {
	t.Parallel()
	// The cases name the bounds as literals: a bound moved in the code should
	// move the tests only by decision, and these numbers are what bounds the
	// transfer, not constants to paraphrase.
	small := int64(4 << 10)
	capBytes := int64(64 << 20)
	cases := map[string]struct {
		sizes  []int64
		chunks []int
	}{
		"an empty batch makes no calls": {sizes: nil, chunks: nil},
		"seventeen keys split after the cap": {
			sizes: repeatSizes(small, 17), chunks: []int{16, 1},
		},
		"sixteen keys stay one call": {
			sizes: repeatSizes(small, 16), chunks: []int{16},
		},
		"declared bytes just over the cap split": {
			sizes:  []int64{capBytes/2 + 1, capBytes/2 + 1},
			chunks: []int{1, 1},
		},
		"declared bytes exactly at the cap fit": {
			sizes:  []int64{capBytes / 2, capBytes / 2},
			chunks: []int{2},
		},
		"a body past the cap travels alone": {
			sizes: []int64{1, capBytes + 1, 1}, chunks: []int{1, 1, 1},
		},
		"a body exactly at the cap leaves no room for a sibling": {
			sizes: []int64{capBytes, 1}, chunks: []int{1, 1},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bodies := make([]batchBody, len(tc.sizes))
			for n, size := range tc.sizes {
				bodies[n] = batchBody{key: fmt.Sprintf("k%d", n), size: size}
			}
			chunks := batchChunk(bodies)
			if got := sizesOfChunks(chunks); !equalInts(got, tc.chunks) {
				t.Fatalf("chunk sizes = %v, want %v", got, tc.chunks)
			}
			// Every chunk's declared bytes fit the cap, or the chunk is one
			// body alone that is itself over it.
			for _, chunk := range chunks {
				var declared int64
				for _, i := range chunk {
					declared += bodies[i].size
				}
				lone := len(chunk) == 1 && bodies[chunk[0]].size > moveBatchMaxBytes
				if declared > moveBatchMaxBytes && !lone {
					t.Errorf("chunk %v declares %d bytes, past the cap without being one lone oversize body",
						chunk, declared)
				}
			}
		})
	}
}

func repeatSizes(size int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = size
	}
	return out
}

// The caps are what bounds the pipe call inside its transfer timeout and the
// worker's buffering, not an implementation taste, and the split cases above
// read the constants so freely that they cannot see a cap move. The numbers
// themselves are pinned here, so widening a bound is a decided change and not
// a silent one.
func TestMoverBatchCapsAreTheirDeclaredNumbers(t *testing.T) {
	t.Parallel()
	if moveBatchMaxKeys != 16 {
		t.Errorf("the batch key cap is %d, want the sixteen that bounds one call", moveBatchMaxKeys)
	}
	if moveBatchMaxBytes != 64<<20 {
		t.Errorf("the batch byte cap is %d, want the 64 MiB that bounds one call", moveBatchMaxBytes)
	}
}

func sizesOfChunks(chunks [][]int) []int {
	out := make([]int, len(chunks))
	for n, chunk := range chunks {
		out[n] = len(chunk)
	}
	return out
}

// Seventeen distinct bodies are two mover calls, sixteen and one: the key cap
// is what bounds a batch of small bodies, and a test that only ever sent
// sixteen would not notice the cap moving to seventeen.
func TestUploadSendsSeventeenKeysAsTwoBatches(t *testing.T) {
	t.Parallel()
	mover := newScriptedMover()
	var pending []pendingBlob
	for n := 0; n < 17; n++ {
		pending = append(pending, pendingFor("m1", incompressible(BlobRefThreshold+1+n)))
	}
	if err := uploadPending(mover, t.TempDir(), pending); err != nil {
		t.Fatalf("uploadPending: %v", err)
	}
	if got := callsOf(mover.putCalls); !equalInts(got, []int{16, 1}) {
		t.Errorf("calls = %v keys, want [16 1]", got)
	}
}

// A batch with nothing pending is not a call: an export whose every value
// travelled inline must not wake the worker for an empty batch.
func TestUploadMakesNoCallForNoBodies(t *testing.T) {
	t.Parallel()
	mover := newScriptedMover()
	if err := uploadPending(mover, t.TempDir(), nil); err != nil {
		t.Fatalf("uploadPending with nothing pending: %v", err)
	}
	if len(mover.putCalls) != 0 {
		t.Errorf("%d mover calls for no bodies, want none", len(mover.putCalls))
	}
}

// Two rows holding one body stage and send it once: the staging file is named
// by the digest, so two equal bodies in one batch would collide on disk --
// the same reason fetchAll dedupes downloads.
func TestUploadDedupesEqualBodies(t *testing.T) {
	t.Parallel()
	mover := newScriptedMover()
	body := incompressible(BlobRefThreshold + 1)
	if err := uploadPending(mover, t.TempDir(), []pendingBlob{
		pendingFor("m1", body), pendingFor("m1", body),
	}); err != nil {
		t.Fatalf("uploadPending: %v", err)
	}
	if got := callsOf(mover.putCalls); !equalInts(got, []int{1}) {
		t.Fatalf("calls = %v keys, want the one body sent once", got)
	}
	if mover.Puts != 1 {
		t.Errorf("stored puts = %d, want one", mover.Puts)
	}
}

// A mover that answers a batch without answering any of its keys has told the
// exporter nothing about whether its bodies are in the store, and an entry
// naming an object nobody put would latch the importer. The upload fails
// instead, with the staged files gone.
func TestUploadFailsWhenTheMoverAnswersNoVerdicts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := newScriptedMover()
	mover.noVerdicts = true
	err := uploadPending(mover, dir, []pendingBlob{pendingFor("m1", incompressible(BlobRefThreshold+1))})
	if !errors.Is(err, errNoVerdict) {
		t.Fatalf("uploadPending = %v, want the no-verdict failure", err)
	}
	assertEmptyStaging(t, dir, "after a verdictless batch")
}

// The bodies staged before a mid-chunk staging failure are scratch of a put
// that will never be called: they are removed, or the folder keeps a body the
// store never took and nothing will fetch.
func TestUploadRemovesStagedFilesWhenStagingFailsMidChunk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, second := incompressible(BlobRefThreshold+1), incompressible(BlobRefThreshold+2)
	// The second body's staging name is taken by a folder, so its write fails
	// while the first body's already sits on disk.
	if err := os.Mkdir(filepath.Join(dir, RefFor(second, 0).SHA256), 0o700); err != nil {
		t.Fatalf("occupy the staging name: %v", err)
	}
	err := uploadPending(newScriptedMover(), dir, []pendingBlob{
		pendingFor("m1", first), pendingFor("m1", second),
	})
	if err == nil || !strings.Contains(err.Error(), "stage") {
		t.Fatalf("uploadPending = %v, want the staging failure", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, RefFor(first, 0).SHA256)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("the staged file of the body whose sibling failed to stage stayed: %v", statErr)
	}
}

// The mover answers a batch through the staging files, so a never-written file
// surfaces as that key's failure while its siblings store -- and the fetch half
// works through the files too: a body the store holds arrives where the item
// named it, and a key the store does not hold keeps the store's own not-found
// answer for the importer to latch on.
func TestMemMoverBatchWorksThroughStagingFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := NewMemBlobMover()
	body := incompressible(BlobRefThreshold + 1)
	pending := pendingFor("m1", body)
	if err := os.WriteFile(filepath.Join(dir, pending.Ref.SHA256), body, 0o600); err != nil {
		t.Fatalf("stage: %v", err)
	}
	results, err := mover.PutBlobs([]BlobBatchItem{
		{Key: pending.Key, Staging: filepath.Join(dir, "missing")},
		{Key: pending.Key, Staging: filepath.Join(dir, pending.Ref.SHA256)},
	})
	if err != nil {
		t.Fatalf("PutBlobs: %v", err)
	}
	if results[0].Err == nil {
		t.Error("an item whose staged file was never written answered success")
	}
	if results[1].Err != nil || results[1].Bytes != int64(len(body)) {
		t.Errorf("the staged item = %+v, want its bytes stored", results[1])
	}
	if mover.Puts != 1 {
		t.Errorf("stored puts = %d, want the one body the batch carried", mover.Puts)
	}

	fetched := filepath.Join(dir, "fetched")
	absent := blobstore.Key("m1", strings.Repeat("0a", 32))
	results, err = mover.GetBlobs([]BlobBatchItem{
		{Key: pending.Key, Staging: fetched, Max: int64(len(body))},
		{Key: absent, Staging: filepath.Join(dir, "absent"), Max: 64 << 20},
		{Key: pending.Key, Staging: filepath.Join(dir, "refused"), Max: int64(len(body)) - 1},
	})
	if err != nil {
		t.Fatalf("GetBlobs: %v", err)
	}
	if results[0].Err != nil || results[0].Bytes != int64(len(body)) {
		t.Fatalf("the held body's verdict = %+v, want it at the item's path", results[0])
	}
	got, err := os.ReadFile(fetched)
	if err != nil || !bytes.Equal(got, body) {
		t.Errorf("the fetched body is not the one stored (err %v)", err)
	}
	if !errors.Is(results[1].Err, blobstore.ErrNotFound) {
		t.Errorf("the absent key = %v, want the store's not-found", results[1].Err)
	}
	if results[2].Err == nil || !strings.Contains(results[2].Err.Error(), "past") {
		t.Errorf("a cap below the stored body = %v, want the fetch refused before the body lands", results[2].Err)
	}
}

// storedBody puts a body in the scripted mover's bucket under the key its own
// digest gives it, and returns the entry whose ref names it.
func storedBody(t *testing.T, mover *scriptedMover, name string, body []byte) Entry {
	t.Helper()
	ref := RefFor(body, 0)
	if err := mover.Store().Put(context.Background(), blobstore.Key("A", ref.SHA256),
		bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	return refEntryFor(t, name, ref)
}

// refEntryFor is the upsert whose body column is a ref rather than the bytes.
func refEntryFor(t *testing.T, name string, ref BlobRef) Entry {
	t.Helper()
	raw, err := json.Marshal(map[string]json.RawMessage{"body": EncodeRef(ref)})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return Entry{Origin: "A", Table: "round_file", PK: `["01A","` + name + `"]`, Op: OpUpsert, Body: raw}
}

// A fetched batch resolves through the same per-body gates the fetch has always
// imposed, in the mover's bounded chunks: seventeen keys are two calls,
// sixteen and one, and every body comes back as the bytes its ref digests.
func TestFetchResolvesAWholeBatchInChunks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := newScriptedMover()
	var batch []Entry
	bodies := make(map[string][]byte)
	for n := 0; n < 17; n++ {
		body := incompressible(BlobRefThreshold + 1 + n)
		entry := storedBody(t, mover, fmt.Sprintf("f%d", n), body)
		bodies[blobstore.Key("A", RefFor(body, 0).SHA256)] = body
		batch = append(batch, entry)
	}
	if err := resolveRefs(mover, dir, batch); err != nil {
		t.Fatalf("resolveRefs: %v", err)
	}
	if got := callsOf(mover.getCalls); !equalInts(got, []int{16, 1}) {
		t.Errorf("calls = %v keys, want [16 1]", got)
	}
	for n, e := range batch {
		value := resolvedBody(t, e)
		if !bytes.Equal(value, bodies[blobstore.Key("A", RefFor(value, 0).SHA256)]) {
			t.Errorf("entry %d did not resolve to the body its ref names", n)
		}
	}
	assertEmptyStaging(t, dir, "after a clean fetch")
}

// One missing key among siblings is that row's own verdict: the batch names
// the row the store lost, while the keys the store held still answered with
// their bodies and no staging file was left behind.
func TestFetchNamesTheRowOfOneMissingKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := newScriptedMover()
	batch := []Entry{
		storedBody(t, mover, "first", incompressible(BlobRefThreshold+1)),
		refEntryFor(t, "lost", RefFor(incompressible(BlobRefThreshold+99), 0)),
		storedBody(t, mover, "third", incompressible(BlobRefThreshold+2)),
	}
	err := resolveRefs(mover, dir, batch)
	var missing *missingBlob
	if !errors.As(err, &missing) || missing.entry.PK != batch[1].PK {
		t.Fatalf("resolveRefs = %v, want the missing row named", err)
	}
	if !errors.Is(err, ErrBlobMissing) {
		t.Errorf("the missing row's error = %v, want the latch sentinel under it", err)
	}
	if len(mover.getCalls) != 1 || len(mover.getCalls[0]) != 3 {
		t.Errorf("calls = %v, want the batch's three keys asked in one call", callsOf(mover.getCalls))
	}
	assertEmptyStaging(t, dir, "after a batch with one missing key")
}

// Several failed keys are decided by the first in order, not by a later
// permanent one: the mover answers its batch in order, and a report that
// skipped ahead would make a repeated import name a row the first attempt
// never mentioned. (Preferring the permanent failure is the supervisor's
// settle, before the mover's caller ever sees the order.)
func TestFetchNamesTheFirstFailedKeyInOrder(t *testing.T) {
	t.Parallel()
	firstErr := errors.New("synclog: the bucket answered 503")
	secondErr := errors.New("synclog: the bucket answered 500")
	mover := newScriptedMover()
	first, second := incompressible(BlobRefThreshold+1), incompressible(BlobRefThreshold+2)
	batch := []Entry{
		storedBody(t, mover, "first", first),
		storedBody(t, mover, "second", second),
	}
	mover.getFails = map[string]error{
		blobstore.Key("A", RefFor(first, 0).SHA256):  firstErr,
		blobstore.Key("A", RefFor(second, 0).SHA256): secondErr,
	}
	err := resolveRefs(mover, t.TempDir(), batch)
	if !errors.Is(err, firstErr) {
		t.Fatalf("resolveRefs = %v, want the first failed key in order", err)
	}
	var missing *missingBlob
	if errors.As(err, &missing) {
		t.Errorf("per-key failures collapsed into a row verdict: %+v, want a stall instead", missing)
	}
}

// A chunk that failed decides the batch: no later chunk is sent, so a store
// that is refusing bodies is not asked for the whole drain, and the files the
// failed chunk staged are gone all the same.
func TestFetchSendsNoChunkAfterAFailedOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := newScriptedMover()
	var batch []Entry
	for n := 0; n < moveBatchMaxKeys+2; n++ {
		batch = append(batch, storedBody(t, mover, fmt.Sprintf("f%d", n), incompressible(BlobRefThreshold+1+n)))
	}
	// The first chunk's first body leaves the store; a later chunk would have
	// fetched cleanly if the run went on.
	lost := blobstore.Key("A", RefFor(incompressible(BlobRefThreshold+1), 0).SHA256)
	if err := mover.Store().Delete(context.Background(), lost); err != nil {
		t.Fatalf("delete the first body: %v", err)
	}
	if err := resolveRefs(mover, dir, batch); !errors.Is(err, ErrBlobMissing) {
		t.Fatalf("resolveRefs = %v, want the deleted body's row", err)
	}
	if len(mover.getCalls) != 1 {
		t.Errorf("calls = %v, want no chunk sent after the failed one", callsOf(mover.getCalls))
	}
	assertEmptyStaging(t, dir, "after a failed chunk")
}

// A mover that answers a fetch with no verdict has told the importer nothing
// about the bytes it is about to apply; refusing the batch holds the origin,
// while applying an unanswered ref would write a row no body stands under.
func TestFetchRefusesAMoverThatAnswersNoVerdicts(t *testing.T) {
	t.Parallel()
	mover := newScriptedMover()
	mover.noVerdicts = true
	batch := []Entry{storedBody(t, mover, "first", incompressible(BlobRefThreshold+1))}
	err := resolveRefs(mover, t.TempDir(), batch)
	if !errors.Is(err, errNoVerdict) {
		t.Fatalf("resolveRefs = %v, want the no-verdict failure", err)
	}
	if _, stillRef := refStillThere(batch[0]); !stillRef {
		t.Error("the ref was rewritten although the mover answered no verdicts")
	}
}

// A chunk whose put failed decides the batch: no later chunk is sent to a
// mover that is already refusing, and the export reports the failure without
// having woken the worker for the rest of the drain.
func TestUploadSendsNoChunkAfterAFailedOne(t *testing.T) {
	t.Parallel()
	mover := newScriptedMover()
	var pending []pendingBlob
	for n := 0; n < 17; n++ {
		p := pendingFor("m1", incompressible(BlobRefThreshold+1+n))
		if n == 3 {
			mover.PutFail = map[string]error{
				p.Key: errors.New("synclog: the bucket refused one key of the batch"),
			}
		}
		pending = append(pending, p)
	}
	if err := uploadPending(mover, t.TempDir(), pending); err == nil {
		t.Fatal("uploadPending = nil, want the failed chunk's error")
	}
	if len(mover.putCalls) != 1 {
		t.Errorf("calls = %v, want no chunk sent after the failed one", callsOf(mover.putCalls))
	}
}

// A batch whose call failed whole -- no verdicts and an error, the shape of a
// pipe miss -- names the first key it asked under and keeps the mover's own
// reason, so the exporter reports the chunk the call could not move.
func TestUploadReportsTheWholeCallOfABatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	gone := errors.New("the sync worker is gone")
	mover := newScriptedMover()
	mover.callErr = gone
	first := pendingFor("m1", incompressible(BlobRefThreshold+1))
	err := uploadPending(mover, dir, []pendingBlob{first})
	if !errors.Is(err, gone) {
		t.Fatalf("uploadPending = %v, want the call's own failure", err)
	}
	if !strings.Contains(err.Error(), first.Key) {
		t.Errorf("uploadPending = %v, want the first key of the failed chunk named", err)
	}
	assertEmptyStaging(t, dir, "after a whole-call failure")
}

// The same whole-call shape on the fetch side holds the batch unapplied: a
// miss is not a per-key verdict, and a mover that answered nothing leaves the
// refs exactly as they were.
func TestFetchReportsTheWholeCallOfABatch(t *testing.T) {
	t.Parallel()
	stall := errors.New("synclog: the bucket answered 503")
	mover := newScriptedMover()
	mover.callErr = stall
	batch := []Entry{storedBody(t, mover, "first", incompressible(BlobRefThreshold+1))}
	err := resolveRefs(mover, t.TempDir(), batch)
	if !errors.Is(err, stall) {
		t.Fatalf("resolveRefs = %v, want the call's own failure", err)
	}
	var missing *missingBlob
	if errors.As(err, &missing) {
		t.Errorf("a whole-call failure collapsed into a row verdict: %+v", missing)
	}
	if _, stillRef := refStillThere(batch[0]); !stillRef {
		t.Error("the ref was rewritten although the call never answered")
	}
}

// A success verdict whose staged file is not there is no body: the check that
// gates every fetched file against its ref refuses the batch rather than
// applying an empty value under the ref's digest.
func TestFetchRefusesASuccessVerdictWithNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mover := newScriptedMover()
	mover.unstage = true
	batch := []Entry{storedBody(t, mover, "first", incompressible(BlobRefThreshold+1))}
	err := resolveRefs(mover, dir, batch)
	if err == nil {
		t.Fatal("resolveRefs over a verdict with no staged file = nil, want the fetch refused")
	}
	if _, stillRef := refStillThere(batch[0]); !stillRef {
		t.Error("the ref was rewritten from a verdict that carried no file")
	}
}

// A body that arrives with the length its ref declares but not its digest is
// the mismatch the fetch has always gated on, checked before it enters the
// batch: a call that moved bytes is not a call that moved the right ones.
func TestFetchGatesEachBodyAgainstItsRef(t *testing.T) {
	t.Parallel()
	body := incompressible(BlobRefThreshold + 1)
	ref := RefFor(body, 0)
	swapped := incompressible(BlobRefThreshold + 1)
	swapped[0] ^= 0xff
	if int64(len(swapped)) != ref.Bytes || bytes.Equal(body, swapped) {
		t.Fatal("the swapped body must match the declared length and differ in bytes")
	}
	mover := newScriptedMover()
	if err := mover.Store().Put(context.Background(), blobstore.Key("A", ref.SHA256),
		bytes.NewReader(swapped), int64(len(swapped))); err != nil {
		t.Fatalf("seed the store: %v", err)
	}
	dir := t.TempDir()
	batch := []Entry{refEntryFor(t, "swapped", ref)}
	err := resolveRefs(mover, dir, batch)
	if !errors.Is(err, ErrBlobMismatch) {
		t.Fatalf("resolveRefs = %v, want the digest mismatch", err)
	}
	assertEmptyStaging(t, dir, "after a mismatched body")
}

// resolvedBody reads the bytes one resolved entry now carries inline.
func resolvedBody(t *testing.T, e Entry) []byte {
	t.Helper()
	var f map[string]json.RawMessage
	if err := json.Unmarshal(e.Body, &f); err != nil {
		t.Fatalf("decode resolved body: %v", err)
	}
	var tagged map[string]string
	if err := json.Unmarshal(f["body"], &tagged); err != nil {
		t.Fatalf("resolved column %s is not a tagged body", f["body"])
	}
	value, err := base64.StdEncoding.DecodeString(tagged[blobTag])
	if err != nil {
		t.Fatalf("decode the resolved value: %v", err)
	}
	return value
}

// refStillThere reports whether an entry's column still decodes as a ref.
func refStillThere(e Entry) (BlobRef, bool) {
	var f map[string]json.RawMessage
	if json.Unmarshal(e.Body, &f) != nil {
		return BlobRef{}, false
	}
	ref, isRef, err := DecodeRef(f["body"])
	return ref, err == nil && isRef
}
