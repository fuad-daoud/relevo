package synclog

// The pre-pass's cases: a walk of every other origin's log that keeps only the
// documents it was asked for, applies them before the rows above them, and
// leaves every refusal to the import below.
//
// The two properties a case pins here are that the named document lands while
// the import mark stays where it was -- which is what makes the import below it
// a set of redeliveries rather than a second application -- and that no refusal
// a remote can hand this walk is allowed to become the whole join's failure.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// rangedLog is a log that answers differently from the specification, so a case
// can pin what the walk does with a transport it does not get to choose. It is
// MemTransport under a three-line seam: the pages PullRange answers with, what
// ReadMarks answers or refuses with, and a count of the ranges asked for.
type rangedLog struct {
	*MemTransport
	// pages are the answers PullRange gives, one per call. Past the last it
	// answers with nothing, which is how a transport says the range is done.
	pages [][]Entry
	// rangeErr is what PullRange refuses with instead of answering.
	rangeErr error
	// marks, when set, replaces what ReadMarks answers with.
	marks *MarkState
	// marksErr is what ReadMarks refuses with.
	marksErr error
	// calls is how many ranges the walk asked for, so a case can say what the
	// walk cost rather than only what it found.
	calls int
}

func (r *rangedLog) PullRange(origin string, after, upto int) ([]Entry, error) {
	r.calls++
	if r.rangeErr != nil {
		return nil, r.rangeErr
	}
	if r.pages == nil {
		return r.MemTransport.PullRange(origin, after, upto)
	}
	if r.calls > len(r.pages) {
		return nil, nil
	}
	return r.pages[r.calls-1], nil
}

func (r *rangedLog) ReadMarks() (MarkState, error) {
	if r.marksErr != nil {
		return MarkState{}, r.marksErr
	}
	if r.marks != nil {
		return *r.marks, nil
	}
	return r.MemTransport.ReadMarks()
}

// prepassImporter is the importer a case drives the walk through, on the log
// given rather than on the log the machine happens to be on.
func prepassImporter(d *db.DB, log *MemTransport, key []byte, docs ...SharedDoc) *Importer {
	imp := NewImporter(d, log.OnLog(d.Origin()))
	imp.SetKey(key)
	imp.UseDocs(docs...)
	return imp
}

// overLog is prepassImporter over a transport that is not a plain MemTransport.
func overLog(d *db.DB, log LogTransport, key []byte, docs ...SharedDoc) *Importer {
	imp := NewImporter(d, log)
	imp.SetKey(key)
	imp.UseDocs(docs...)
	return imp
}

// highSchemaDoc appends one doc entry whose writer claims a schema this build
// does not know, which is what a peer on a newer binary writes.
func highSchemaDoc(t *testing.T, log *MemTransport, doc SharedDoc, origin, key string, stamp Stamp) {
	t.Helper()
	e, err := NewDocUpsert(origin, doc, key, 1_000_000, stamp, []byte(`{"a":1}`), time.Now())
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	writer := keyed{key: keyA}
	batch := []Entry{e}
	writer.sign(batch)
	if _, err := writer.appendTo(log.OnLog(origin), origin, batch); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestPrepassDocsAppliesTheNamedDocumentBeforeItsRows pins the reason the walk
// exists: the document lands while the import mark stays where it was, so the
// import below it finds each entry a redelivery rather than applying it twice.
func TestPrepassDocsAppliesTheNamedDocumentBeforeItsRows(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	other := newTxDoc("board")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	if err := appendDocEntry(t, log, other, "m1", "cards", 6, `{"b":1}`, keyA); err != nil {
		t.Fatalf("append: %v", err)
	}
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc, other).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 1 {
		t.Errorf("applied %d, want the one entry of the named document", n)
	}
	if got := string(doc.items["policy"]); got != `{"a":1}` {
		t.Errorf("policy = %s, want the document applied", got)
	}
	if len(other.items) != 0 {
		t.Errorf("items %v: a document the pass was not asked for must not be applied", other.items)
	}
	if got := markOf(t, m2, "m1"); got != 0 {
		t.Errorf("mark = %d, want 0: the walk moves no mark", got)
	}
	if got := strings.Join(doc.revisions, "|"); got != `sync:m1 policy={"a":1}` {
		t.Errorf("revisions = %q, want exactly the named document's entry", got)
	}
}

// TestPrepassDocsIsInertWithoutAKey pins that a locked machine runs no walk at
// all: it cannot verify an entry, and an entry it took on trust would be the
// one path in the file that does.
func TestPrepassDocsIsInertWithoutAKey(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, nil, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("locked PrepassDocs: %v", err)
	}
	if n != 0 || len(doc.revisions) != 0 {
		t.Errorf("applied %d, revisions %v: a locked machine applies nothing", n, doc.revisions)
	}
}

// TestPrepassDocsAskedForNothing pins that naming no document is a walk over
// nothing rather than a walk over every document.
func TestPrepassDocsAskedForNothing(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc).PrepassDocs()
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 || len(doc.revisions) != 0 {
		t.Errorf("applied %d, revisions %v: naming nothing applies nothing", n, doc.revisions)
	}
}

// TestPrepassDocsReportsALogThatWillNotAnswer pins that a refusal to read the
// marks is reported rather than swallowed: the caller cannot tell an empty walk
// from a walk that never ran, and the join's guard below depends on the
// difference.
func TestPrepassDocsReportsALogThatWillNotAnswer(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := &rangedLog{MemTransport: NewMemTransport("m1"), marksErr: errors.New("no marks")}
	signedDoc(t, log.MemTransport, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")

	_, err := overLog(m2, log, keyA, doc).PrepassDocs("config")
	if err == nil || !strings.Contains(err.Error(), "read the floors") {
		t.Fatalf("PrepassDocs error = %v, want the marks refusal reported", err)
	}
	if len(doc.revisions) != 0 {
		t.Errorf("revisions %v: a walk that never read the log applies nothing", doc.revisions)
	}
}

// TestPrepassDocsReportsAFileThatWillNotAnswer pins that a failure computing the
// per-origin signed-from boundaries is reported too, and that it is the boundary
// step's own error rather than one dressed as the walk's.
func TestPrepassDocsReportsAFileThatWillNotAnswer(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	m2, _ := peerFile(t, "m2")
	imp := prepassImporter(m2, log, keyA, doc)
	if err := m2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if _, err := imp.PrepassDocs("config"); err == nil {
		t.Fatal("PrepassDocs over a closed file = nil error")
	}
}

// TestPrepassDocsLeavesAnOriginWithABadBoundary pins that an origin whose remote
// boundary does not verify under this machine's key is left to the import, which
// reports it properly. A pre-pass has no business turning one origin's refusal
// into the whole join's failure.
func TestPrepassDocsLeavesAnOriginWithABadBoundary(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	state.SignedTags["m1"] = []byte("not the tag this key signs")
	logged := &rangedLog{MemTransport: log, marks: &state}
	m2, _ := peerFile(t, "m2")

	n, err := overLog(m2, logged, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 || len(doc.revisions) != 0 {
		t.Errorf("applied %d, revisions %v: an origin behind a bad boundary must be left alone", n, doc.revisions)
	}
	if logged.calls != 0 {
		t.Errorf("ranges asked for = %d, want none for an origin already refused", logged.calls)
	}
}

// TestPrepassDocsReportsARefusedRange pins that a log that refuses to hand back
// a range fails the walk with the origin's name in the error, because the
// refusal is the import's to retry and report.
func TestPrepassDocsReportsARefusedRange(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	base := NewMemTransport("m1")
	signedDoc(t, base, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	logged := &rangedLog{MemTransport: base, rangeErr: errors.New("no such range")}
	m2, _ := peerFile(t, "m2")

	_, err := overLog(m2, logged, keyA, doc).PrepassDocs("config")
	if err == nil || !strings.Contains(err.Error(), "prepass: pull m1") {
		t.Fatalf("PrepassDocs error = %v, want the refused range named", err)
	}
}

// TestPrepassDocsSkipsAnOriginWithNoNamedDocument pins that an origin whose log
// holds none of the documents asked for is walked and found empty, which is not
// a failure and not an application.
func TestPrepassDocsSkipsAnOriginWithNoNamedDocument(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	other := newTxDoc("board")
	absent := newTxDoc("audit")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	if err := appendDocEntry(t, log, other, "m1", "cards", 6, `{"b":1}`, keyA); err != nil {
		t.Fatalf("append: %v", err)
	}
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc, other, absent).PrepassDocs("audit")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 {
		t.Errorf("applied %d, want none: the named document is not in that origin's log", n)
	}
	if len(doc.items) != 0 || len(other.items) != 0 {
		t.Errorf("items %v and %v: a walk that found nothing applies nothing", doc.items, other.items)
	}
}

// TestPrepassDocsLeavesAnEntryFromANewerBinary pins that a document written by a
// peer on a schema this build does not know is left to the import, which holds
// that origin with the reason a person can act on.
func TestPrepassDocsLeavesAnEntryFromANewerBinary(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	highSchemaDoc(t, log, doc, "m1", "policy", 5)
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 || len(doc.revisions) != 0 {
		t.Errorf("applied %d, revisions %v: an entry a newer binary wrote must not be taken here", n, doc.revisions)
	}
}

// TestPrepassDocsLeavesAnUnsignedDocument pins that a document entry with no
// signature is left to the import too: the walk verifies every entry it takes,
// and an unsigned one is the case that must not slip through it.
func TestPrepassDocsLeavesAnUnsignedDocument(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, nil)
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 || len(doc.revisions) != 0 {
		t.Errorf("applied %d, revisions %v: an unsigned document entry must not be taken by the walk", n, doc.revisions)
	}
	if got := markOf(t, m2, "m1"); got != 0 {
		t.Errorf("mark = %d, want 0: the walk moves no mark whatever it finds", got)
	}
}

// TestPrepassDocsSkipsThisMachinesOwnOrigin pins that a log holding this
// machine's own entries alongside a peer's does not send the walk back over its
// own log: a machine's own writes are already in its file, and applying them a
// second time through the walk is what the import's own origin check exists to
// prevent.
func TestPrepassDocsSkipsThisMachinesOwnOrigin(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	other := newTxDoc("board")
	log := NewMemTransport("m1")
	signedDoc(t, log, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	if err := appendDocEntry(t, log, other, "m2", "cards", 6, `{"b":1}`, keyA); err != nil {
		t.Fatalf("append as m2: %v", err)
	}
	m2, _ := peerFile(t, "m2")

	n, err := prepassImporter(m2, log, keyA, doc, other).PrepassDocs("config", "board")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 1 {
		t.Errorf("applied %d, want only the peer's entry", n)
	}
	if len(other.items) != 0 {
		t.Errorf("items %v: this machine's own entry must not be walked", other.items)
	}
	if got := string(doc.items["policy"]); got != `{"a":1}` {
		t.Errorf("policy = %s, want the peer's entry applied", got)
	}
}

// TestPrepassDocsWalksTheRangeInPages pins that a transport answering with fewer
// entries than the range holds is asked again from the last sequence it was
// given, until the range is spent.
func TestPrepassDocsWalksTheRangeInPages(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	base := NewMemTransport("m1")
	signedDoc(t, base, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, base, doc, "m1", "policy", 6, `{"a":2}`, keyA)
	one, two := entriesFor(t, base, "m1")
	logged := &rangedLog{MemTransport: base, pages: [][]Entry{{one}, {two}}}
	m2, _ := peerFile(t, "m2")

	n, err := overLog(m2, logged, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 2 {
		t.Errorf("applied %d, want both entries the paged walk reached", n)
	}
	if logged.calls != 2 {
		t.Errorf("ranges asked for = %d, want 2: one page per sequence the walk still owed", logged.calls)
	}
	if got := string(doc.items["policy"]); got != `{"a":2}` {
		t.Errorf("policy = %s, want the newest of the two entries", got)
	}
}

// TestPrepassDocsIgnoresEntriesOutsideTheRangeAndStopsWhenAPageDoesNotAdvance
// pins the two rules the transport is not trusted for: an entry from another
// origin, or one outside the range, is dropped, and a page that does not move
// the read ends it rather than spinning on the same answer.
func TestPrepassDocsIgnoresEntriesOutsideTheRangeAndStopsWhenAPageDoesNotAdvance(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	base := NewMemTransport("m1")
	signedDoc(t, base, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	signedDoc(t, base, doc, "m1", "policy", 6, `{"a":2}`, keyA)
	one, two := entriesFor(t, base, "m1")
	foreign := one
	foreign.Origin = "m9"
	beyond := two
	beyond.Seq = 99
	logged := &rangedLog{MemTransport: base, pages: [][]Entry{{foreign, one, beyond}, {}}}
	m2, _ := peerFile(t, "m2")

	n, err := overLog(m2, logged, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 1 {
		t.Errorf("applied %d, want only the one entry inside the range", n)
	}
	if logged.calls != 2 {
		t.Errorf("ranges asked for = %d, want 2: the walk must stop on the page that did not advance", logged.calls)
	}
	if got := string(doc.items["policy"]); got != `{"a":1}` {
		t.Errorf("policy = %s, want the one entry the range held", got)
	}
}

// TestPrepassDocsStopsOnAnEmptyFirstPage pins that a log which answers nothing
// at all ends the walk rather than spinning on the same empty answer.
func TestPrepassDocsStopsOnAnEmptyFirstPage(t *testing.T) {
	t.Parallel()
	doc := newTxDoc("config")
	base := NewMemTransport("m1")
	signedDoc(t, base, doc, "m1", "policy", 5, `{"a":1}`, keyA)
	logged := &rangedLog{MemTransport: base, pages: [][]Entry{{}}}
	m2, _ := peerFile(t, "m2")

	n, err := overLog(m2, logged, keyA, doc).PrepassDocs("config")
	if err != nil {
		t.Fatalf("PrepassDocs: %v", err)
	}
	if n != 0 {
		t.Errorf("applied %d, want none", n)
	}
	if logged.calls != 1 {
		t.Errorf("ranges asked for = %d, want 1: an empty page ends the walk", logged.calls)
	}
}

// entriesFor is one origin's first two entries as the log holds them, so a case
// can hand the walk a page built from what the log itself recorded.
func entriesFor(t *testing.T, log *MemTransport, origin string) (Entry, Entry) {
	t.Helper()
	page, err := log.PullRange(origin, 0, 2)
	if err != nil {
		t.Fatalf("PullRange: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("PullRange returned %d entries, want 2", len(page))
	}
	return page[0], page[1]
}

// appendDocEntry appends one doc entry for doc under its own handle, answering
// with an error the caller reports rather than dying on.
func appendDocEntry(t *testing.T, log *MemTransport, doc SharedDoc, origin, key string, stamp Stamp, value string, mac []byte) error {
	t.Helper()
	e, err := NewDocUpsert(origin, doc, key, 1, stamp, []byte(value), time.Now())
	if err != nil {
		return err
	}
	writer := keyed{key: mac}
	batch := []Entry{e}
	writer.sign(batch)
	_, err = writer.appendTo(log.OnLog(origin), origin, batch)
	return err
}
