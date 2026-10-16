package syncworker

import (
	"bytes"
	"errors"
	"strconv"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// The remote's format: which older shapes this build brings forward, which it
// refuses, and what a MAC that never existed looks like in the rows the upgrade
// left alone.

// TestHelloUpgradesFormatTwoToThreeInPlace pins the upgrade a machine that
// synced unsigned and has not synced since needs. Its tables are this build's
// already, so the only thing that changes is the mac column -- and the entries
// it holds have to survive with that column NULL, which reads as unsigned
// rather than as a signature that failed.
func TestHelloUpgradesFormatTwoToThreeInPlace(t *testing.T) {
	path := tempReplica(t)
	seedFormatTwoRemote(t, path)
	// Two entries written by a build that had no mac column at all.
	for seq, pk := range []string{`["a"]`, `["b"]`} {
		execOn(t, path, "INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at)"+
			" VALUES ('origin-a', "+strconv.Itoa(seq+1)+", 1, 'board', '"+pk+"', 'upsert', 3,"+
			" '{\"title\":\"one\"}', '2026-10-08T09:30:00Z')")
	}

	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-b")

	if got := metaFormat(t, path); got != strconv.Itoa(logFormatVersion) {
		t.Errorf("meta format = %q, want %d after the upgrade", got, logFormatVersion)
	}
	if !tablesHaveColumn(t, path, "log", "mac") {
		t.Error("the upgrade left the log without a mac column")
	}
	if driver.pushes != 1 {
		t.Errorf("the upgrade pushed %d times, want once: the remote learns the column from a push", driver.pushes)
	}

	// Both rows are still there, and both carry NULL rather than an empty blob:
	// an unsigned entry and a signature that failed are different states.
	var rows int
	if err := openAt(t, path).QueryRow("SELECT COUNT(*) FROM log").Scan(&rows); err != nil {
		t.Fatalf("read the log: %v", err)
	}
	if rows != 2 {
		t.Errorf("the upgraded log holds %d entries, want the 2 it had", rows)
	}
	for _, seq := range []int{1, 2} {
		mac, present := logMAC(t, path, seq)
		if present {
			t.Errorf("seq %d came back with a MAC of %x, want NULL: it was written before signing", seq, mac)
		}
	}

	pulled, err := b.Pull(map[string]int{"origin-a": 0})
	if err != nil {
		t.Fatalf("pull from the upgraded log: %v", err)
	}
	if len(pulled) != 2 {
		t.Errorf("the upgraded log pulled %d entries, want the 2 it held", len(pulled))
	}
	for _, e := range pulled {
		if e.MAC != nil {
			t.Errorf("seq %d came back with a MAC, want none: the remote never held one", e.Seq)
		}
	}

	// A re-run finds a log at the current format and writes nothing.
	_ = b.Close()
	again := &localDriver{path: path}
	backendFor(t, again, "origin-b")
	if again.pushes != 0 {
		t.Errorf("a re-run pushed %d times, want none: the log was already at the current format", again.pushes)
	}
}

// TestHelloRefusesFormatFour pins that the upgrade path stops where this build's
// knowledge does. A remote one format ahead defines the log differently, so it
// is refused with the same reason and class every other foreign remote gets
// rather than opened as something it is not.
func TestHelloRefusesFormatFour(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path, append(append([]string(nil), createStatements...),
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '4')")...)

	b := NewTursoBackend(&localDriver{path: path})
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatal("hello = ok over a remote a newer build wrote, want a refusal")
	}
	if resp.Code != CodeRemote {
		t.Errorf("code = %q, want %q: the refusal repeats on every attempt", resp.Code, CodeRemote)
	}
	for _, want := range []string{"remote", "is not a relevo sync log"} {
		if !contains(resp.Error, want) {
			t.Errorf("refusal %q does not say %q", resp.Error, want)
		}
	}
	_ = b.Close()
}

// TestKeyMetaAbsentOnAFreshRemote pins what a remote that has never been given
// a key answers. It is an absent answer rather than a refusal, because the
// question every caller asks first is "has this log been set up yet" and an
// error would read as a remote that could not be asked at all.
func TestKeyMetaAbsentOnAFreshRemote(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")

	key, err := b.KeyMeta()
	if err != nil {
		t.Fatalf("KeyMeta on a fresh remote: %v", err)
	}
	if key.Present {
		t.Errorf("key = %+v, want absent: nothing has written a salt", key)
	}
	if key.Salt != nil || key.Check != nil {
		t.Errorf("key = %+v, want no salt and no check", key)
	}

	// The same answer comes back over the pipe, rather than as a reply that
	// leaves the field out and reads as a worker that did not do what it was
	// asked.
	w := startWorker(t, b)
	w.askHello()
	resp := w.ask(Request{ID: "1", Verb: VerbKeyMeta})
	if !resp.OK {
		t.Fatalf("key_meta = %+v, want ok", resp)
	}
	if resp.KeyMeta == nil {
		t.Fatal("key_meta answered with no key at all")
	}
	if resp.KeyMeta.Present {
		t.Errorf("key_meta = %+v, want absent", resp.KeyMeta)
	}
}

// TestKeyInitIsInsertIfAbsent pins what two machines racing the first one both
// learn. The second init carries a different salt and is answered with the first
// one's, which is the whole point: a caller that got its own value back would
// believe the log is keyed by a salt no other machine has, and would sign under
// a key nothing could verify.
func TestKeyInitIsInsertIfAbsent(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")

	first := KeyMeta{
		Salt: []byte("the first machine's salt"), KDF: "argon2id",
		Check: []byte("the first machine's check"), Present: true,
	}
	won, err := b.KeyInit(first)
	if err != nil {
		t.Fatalf("the first KeyInit: %v", err)
	}
	if !bytes.Equal(won.Salt, first.Salt) || !won.Present {
		t.Fatalf("the first KeyInit answered %+v, want its own material", won)
	}

	second := KeyMeta{
		Salt: []byte("a different salt"), KDF: "argon2id",
		Check: []byte("a different check"), Present: true,
	}
	got, err := b.KeyInit(second)
	if err != nil {
		t.Fatalf("the second KeyInit: %v", err)
	}
	if !bytes.Equal(got.Salt, first.Salt) {
		t.Errorf("the second KeyInit answered the salt %q, want the first one's %q: the second clobbered the log",
			got.Salt, first.Salt)
	}
	if !bytes.Equal(got.Check, first.Check) || got.KDF != first.KDF {
		t.Errorf("the second KeyInit answered %+v, want the first machine's material", got)
	}

	// A later read agrees with both, which is what tells the loser its key does
	// not match.
	reread, err := b.KeyMeta()
	if err != nil {
		t.Fatalf("KeyMeta after the race: %v", err)
	}
	if !bytes.Equal(reread.Salt, first.Salt) || !bytes.Equal(reread.Check, first.Check) {
		t.Errorf("the log now holds %+v, want the first machine's material", reread)
	}
}

// TestKeyInitRefusedWithNoKey pins that a key_init carrying nothing is refused
// rather than answered. Writing three empty rows would make the remote look like
// it holds a key, and every later caller would read a salt of "" as a real one.
func TestKeyInitRefusedWithNoKey(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")

	// Asked through the worker rather than on the backend beside it: the pipe
	// owns the backend for as long as the session is open, and calling into it
	// from the test would be a second caller on one connection.
	w := startWorker(t, b)
	w.askHello()
	resp := w.ask(Request{ID: "1", Verb: VerbKeyInit})
	if resp.OK {
		t.Fatal("key_init with no key was answered, want a refusal")
	}
	if !contains(resp.Error, "no key") {
		t.Errorf("refusal %q does not say the request carried none", resp.Error)
	}

	read := w.ask(Request{ID: "2", Verb: VerbKeyMeta})
	if !read.OK || read.KeyMeta == nil {
		t.Fatalf("key_meta after the refusal = %+v, want ok", read)
	}
	if read.KeyMeta.Present {
		t.Errorf("key = %+v, want absent: a refused key_init wrote nothing", read.KeyMeta)
	}
}

// TestKeyVerbsRefusedWithoutHello pins that both key verbs need the handshake
// like every other verb after it. Before hello there is no remote to read the
// material from and no connection to write it through, so the answer has to be a
// refusal rather than a nil dereference on an unopened backend.
func TestKeyVerbsRefusedWithoutHello(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	s := startWorker(t, b)
	for _, req := range []Request{
		{ID: "a", Verb: VerbKeyMeta},
		{ID: "b", Verb: VerbKeyInit, KeyMeta: &KeyMeta{Salt: []byte("s"), KDF: "argon2id", Present: true}},
		{ID: "c", Verb: VerbKeyRekey, Epoch: 1, KeyMeta: &KeyMeta{Epoch: 2, Present: true}},
	} {
		resp := s.ask(req)
		if resp.OK {
			t.Errorf("%s before hello = %+v, want a refusal", req.Verb, resp)
		}
		if resp.Error != ErrNoHello.Error() {
			t.Errorf("%s before hello = %q, want %q", req.Verb, resp.Error, ErrNoHello)
		}
	}
}

// TestKeyVerbsRefusedOnABackendThatNeverOpened pins the second gate: the
// backend is asked directly, with no pipe and no handshake, and refuses rather
// than reading a handle that was never taken.
func TestKeyVerbsRefusedOnABackendThatNeverOpened(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.KeyMeta(); !errorsIsNoReplica(err) {
		t.Errorf("KeyMeta = %v, want ErrNoHello", err)
	}
	if _, err := b.KeyInit(KeyMeta{Salt: []byte("s"), KDF: "argon2id", Present: true}); !errorsIsNoReplica(err) {
		t.Errorf("KeyInit = %v, want ErrNoHello", err)
	}
}

// TestKeyInitReportsAFailedPush pins that a key_init whose rows reached the
// replica but not the remote is a failure the caller sees. The replica now holds
// a salt the remote never learned of, so the next machine would read a log whose
// key it does not have unless this is reported.
func TestKeyInitReportsAFailedPush(t *testing.T) {
	path := tempReplica(t)
	// Opened while the driver still answers, so the failure under test is the one
	// key_init causes rather than the schema create that opens the log.
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")
	driver.pushErr = errors.New("the remote is not answering")

	if _, err := b.KeyInit(KeyMeta{
		Salt: []byte("the salt"), KDF: "argon2id", Check: []byte("the check"), Present: true,
	}); err == nil {
		t.Fatal("a key_init whose push failed came back as a success")
	} else if !contains(err.Error(), "push the key") {
		t.Errorf("refusal %q does not say the push is what failed", err)
	}
}

// TestAnAppendThatCannotRecordWhereSigningStartsFails pins that the marker is
// not a best-effort extra. It is written in the append's own transaction, so a
// remote whose meta table cannot be written fails the append rather than taking
// the entries and losing the boundary a reader is told to enforce from.
func TestAnAppendThatCannotRecordWhereSigningStartsFails(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path, "DROP TABLE meta")

	e := upsertEntry("origin-a", "board", `["s"]`, `{"title":"signed"}`)
	e.MAC = []byte("a signature")
	if _, err := b.AppendSigned([]Entry{e}, boundaryAt([]byte("k"), "origin-a", 1)); err == nil {
		t.Fatal("an append that could not record where signing starts came back as a success")
	} else if !contains(err.Error(), "record where signing starts") {
		t.Errorf("refusal %q does not name the marker it could not write", err)
	}
}

// TestKeyVerbsFailOnARemoteWithNoMetaTable pins that reading and writing the
// passphrase material fail on a remote whose meta table is gone, rather than
// answering "no key". The two are different to a machine deciding whether it is
// locked: one says there is nothing there, the other says it could not look.
func TestKeyVerbsFailOnARemoteWithNoMetaTable(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path, "DROP TABLE meta")

	if _, err := b.KeyMeta(); err == nil {
		t.Error("KeyMeta over a remote with no meta table came back as a success")
	}
	if _, err := b.KeyInit(KeyMeta{Salt: []byte("s"), KDF: "argon2id", Present: true}); err == nil {
		t.Error("KeyInit over a remote with no meta table came back as a success")
	}
}

// TestKeyVerbsCarryABackendRefusal pins that a key verb hands the backend's own
// reason back over the pipe rather than a paraphrase or a silence. Read as a
// zero value it would say "this log holds no key", which is the one answer a
// machine about to sign must not be given by accident.
func TestKeyVerbsCarryABackendRefusal(t *testing.T) {
	w := startWorker(t, refusingBackend{fail: errors.New("the meta table could not be read")})
	w.askHello()
	for _, req := range []Request{
		{ID: "1", Verb: VerbKeyMeta},
		{ID: "2", Verb: VerbKeyInit, KeyMeta: &KeyMeta{
			Salt: []byte("salt"), KDF: "argon2id", Check: []byte("check"), Present: true,
		}},
	} {
		resp := w.ask(req)
		if resp.OK || !contains(resp.Error, "the meta table could not be read") {
			t.Errorf("%s = %+v, want the backend's own reason", req.Verb, resp)
		}
		if resp.KeyMeta != nil {
			t.Errorf("%s answered with key material after a refusal: %+v", req.Verb, resp.KeyMeta)
		}
	}
}

// TestKeyMetaReadsAMalformedValueAsItsOwnBytes pins what a remote is trusted
// with here: nothing. A meta row this build did not write may hold anything at
// all, and a value that does not decode is handed back as the bytes it is rather
// than dropped -- dropping it would make the row read as absent, which is a
// different answer from "the remote put something here this build cannot read".
func TestKeyMetaReadsAMalformedValueAsItsOwnBytes(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('"+metaKDFSaltKey+"', 'not base64 at all')",
		"INSERT INTO meta (key, value) VALUES ('"+metaKDFKey+"', 'argon2id')",
		"INSERT INTO meta (key, value) VALUES ('"+metaKeyCheckKey+"', 'also not base64')")

	key, err := b.KeyMeta()
	if err != nil {
		t.Fatalf("KeyMeta: %v", err)
	}
	if !key.Present {
		t.Fatal("key = absent, want present: the remote wrote a salt")
	}
	if string(key.Salt) != "not base64 at all" {
		t.Errorf("salt = %q, want the row's own text: a value this build cannot decode must not vanish", key.Salt)
	}
	if string(key.Check) != "also not base64" {
		t.Errorf("check = %q, want the row's own text", key.Check)
	}
	if key.KDF != "argon2id" {
		t.Errorf("kdf = %q, want argon2id", key.KDF)
	}
}

// TestReadMarksReportsAMalformedSignedFromRowAsUnverifiable pins that one
// unreadable row does not stop a reader reading the rest, and is not hidden
// either: it comes back with no sequence or no tag, which the reader refuses.
func TestReadMarksReportsAMalformedSignedFromRowAsUnverifiable(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	tag := encodeKeyValue(synclog.BoundaryTag([]byte("k"), "origin-b", 7))
	execOn(t, path,
		"INSERT INTO meta (key, value) VALUES ('"+signedFromPrefix+"origin-a', 'not a sequence')",
		"INSERT INTO meta (key, value) VALUES ('"+signedFromPrefix+"origin-b', '7 "+tag+"')",
		"INSERT INTO meta (key, value) VALUES ('"+signedFromPrefix+"origin-c', '-1')",
		"INSERT INTO meta (key, value) VALUES ('"+signedFromPrefix+"origin-d', '5')")

	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	for _, origin := range []string{"origin-a", "origin-c"} {
		if seq, ok := state.SignedFrom[origin]; !ok || seq != 0 || state.SignedTags[origin] != nil {
			t.Errorf("%s = %d, %v: a row that is not a sequence must be reported as seq 0", origin, seq, state.SignedTags[origin])
		}
	}
	if seq, tags := state.SignedFrom["origin-d"], state.SignedTags["origin-d"]; seq != 5 || tags != nil {
		t.Errorf("origin-d = %d, %x; want 5 with no tag", seq, tags)
	}
	if got := state.SignedFrom["origin-b"]; got != 7 || !synclog.VerifyBoundary([]byte("k"), "origin-b", 7, state.SignedTags["origin-b"]) {
		t.Errorf("origin-b = %d: one bad row must not hide the good ones", got)
	}
}

// TestAppendWritesSignedFromOnceAtTheFirstSignedEntry pins the marker a reader
// is told to enforce: where each origin started signing. It lands on the first
// signed sequence with the tag the exporter sent, it does not move for the
// batches after it, and an unsigned batch ahead of it writes nothing at all.
func TestAppendWritesSignedFromOnceAtTheFirstSignedEntry(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	key := []byte("the passphrase this machine stretched")

	appendOne := func(pk string, mac []byte, boundary Boundary) {
		t.Helper()
		e := upsertEntry("origin-a", "board", pk, `{"title":"one"}`)
		e.MAC = mac
		if _, err := b.AppendSigned([]Entry{e}, boundary); err != nil {
			t.Fatalf("append %s: %v", pk, err)
		}
	}

	appendOne(`["u"]`, nil, Boundary{})
	if _, ok := signedFromMeta(t, path, "origin-a"); ok {
		t.Fatal("an unsigned batch recorded where signing starts")
	}

	appendOne(`["s"]`, []byte("the first signature"), boundaryAt(key, "origin-a", 2))
	value, ok := signedFromMeta(t, path, "origin-a")
	if !ok {
		t.Fatal("the first signed batch recorded nothing")
	}
	want := "2 " + encodeKeyValue(synclog.BoundaryTag(key, "origin-a", 2))
	if value != want {
		t.Errorf("signed_from = %q, want %q: the first signed sequence and its tag", value, want)
	}

	appendOne(`["t"]`, []byte("a later signature"), Boundary{})
	again, ok := signedFromMeta(t, path, "origin-a")
	if !ok || again != value {
		t.Errorf("signed_from = %q, want %q: a later signed batch moved the boundary", again, value)
	}

	// The answer the reader reads it through carries the same boundary.
	state, err := b.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	if got := state.SignedFrom["origin-a"]; got != 2 {
		t.Errorf("ReadMarks signed_from[origin-a] = %d, want 2", got)
	}
}
