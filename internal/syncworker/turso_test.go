package syncworker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	turso "turso.tech/database/tursogo"

	"github.com/fuad-daoud/relevo/internal/synclog"
)

// localDriver is the replica without an engine: it opens the file a plain local
// driver can read, records every push, pull and stats call, and can be told to
// fail any of them. It is the double the backend's rules are tested against, so
// CI reaches no network and starts no driver.
type localDriver struct {
	path string
	db   *sql.DB

	opens  int
	pushes int
	pulls  int
	stats  int

	openErr  error
	pushErr  error
	pullErr  error
	statsErr error
}

func (d *localDriver) Open(context.Context, Spec) (*sql.DB, error) {
	d.opens++
	if d.openErr != nil {
		return nil, d.openErr
	}
	db, err := sql.Open("sqlite", d.path)
	if err != nil {
		return nil, err
	}
	// One connection keeps a write from racing the handle the test reads back
	// through.
	db.SetMaxOpenConns(1)
	d.db = db
	return db, nil
}

func (d *localDriver) Push(context.Context) error {
	d.pushes++
	return d.pushErr
}

func (d *localDriver) Pull(context.Context) error {
	d.pulls++
	return d.pullErr
}

func (d *localDriver) Stats(context.Context) error {
	d.stats++
	return d.statsErr
}

// tempReplica is a replica path no other test shares.
func tempReplica(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "relevo-sync.db")
}

// backendFor opens a backend on a fresh local replica as origin and closes it
// when the test ends.
func backendFor(t *testing.T, driver ReplicaDriver, origin string) *TursoBackend {
	t.Helper()
	b := NewTursoBackend(driver)
	if err := b.Open(Spec{
		Version: ProtocolVersion,
		Origin:  origin,
		URL:     "libsql://scratch.example",
		Token:   "the-token",
	}); err != nil {
		t.Fatalf("open backend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// openAt is a second, independent handle on a replica path, so a test reads what
// the backend wrote through the connection the driver opened.
func openAt(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// execOn runs statements against a replica path directly, which is how a test
// seeds a remote that is not this log.
func execOn(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db := openAt(t, path)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
}

// replicaTables is the replica's own table names.
func replicaTables(t *testing.T, path string) map[string]bool {
	t.Helper()
	db := openAt(t, path)
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return out
}

// metaFormat is the format version the replica's meta carries.
func metaFormat(t *testing.T, path string) string {
	t.Helper()
	var value string
	if err := openAt(t, path).QueryRow("SELECT value FROM meta WHERE key = ?", metaFormatKey).Scan(&value); err != nil {
		t.Fatalf("read meta format: %v", err)
	}
	return value
}

// assertSeqs pins the sequence numbers of a batch, so a test states the
// numbering it expects rather than reading it out of place.
func assertSeqs(t *testing.T, entries []Entry, seqs ...int) {
	t.Helper()
	if len(entries) != len(seqs) {
		t.Fatalf("got %d entries, want %d", len(entries), len(seqs))
	}
	for i, want := range seqs {
		if entries[i].Seq != want {
			t.Errorf("entry %d Seq = %d, want %d", i, entries[i].Seq, want)
		}
	}
}

// pageHead walks one origin's head a row at a time and returns the rows in the
// order they came back, so a test can assert the page order itself.
func pageHead(t *testing.T, b *TursoBackend, origin string) []HeadRow {
	t.Helper()
	var got []HeadRow
	after := ""
	for {
		page, err := b.Head(origin, after, 1)
		if err != nil {
			t.Fatalf("head page after %q: %v", after, err)
		}
		if len(page) == 0 {
			return got
		}
		if len(page) != 1 {
			t.Fatalf("page after %q = %d rows, want one", after, len(page))
		}
		got = append(got, page...)
		after = strconv.Itoa(page[len(page)-1].Seq)
		if len(got) > 10 {
			t.Fatal("paging did not end")
		}
	}
}

// TestReplicaOpensOnlyThroughTheSyncConstructor pins the one rule that keeps a
// second opener from invalidating the engine's view of the file: the backend
// reaches the replica only through the connection the driver opened, and never
// opens a path of its own. A second handle on the same file seeing the row the
// backend wrote is what proves it.
func TestReplicaOpensOnlyThroughTheSyncConstructor(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")

	if driver.opens != 1 {
		t.Fatalf("the sync constructor opened %d times, want exactly once", driver.opens)
	}
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	var n int
	if err := openAt(t, path).QueryRow("SELECT COUNT(*) FROM log").Scan(&n); err != nil {
		t.Fatalf("read the replica: %v", err)
	}
	if n != 1 {
		t.Errorf("the replica the constructor opened holds %d entries, want 1: the backend wrote elsewhere", n)
	}
}

// TestFirstUseCreatesTheRemoteLogTables pins the first-use contract: an empty
// remote gets log, head and meta with the format row, the schema is pushed so
// the remote learns it, and a re-run against the same replica changes nothing
// and does not error.
func TestFirstUseCreatesTheRemoteLogTables(t *testing.T) {
	path := tempReplica(t)
	driver := &localDriver{path: path}
	b := backendFor(t, driver, "origin-a")

	tables := replicaTables(t, path)
	for _, want := range []string{"head", "log", "meta"} {
		if !tables[want] {
			t.Errorf("first use did not create %q; the replica holds %v", want, tables)
		}
	}
	if len(tables) != 3 {
		t.Errorf("first use created %v, want exactly log, head and meta", tables)
	}
	if got := metaFormat(t, path); got != strconv.Itoa(logFormatVersion) {
		t.Errorf("meta format = %q, want %d", got, logFormatVersion)
	}
	if driver.pushes != 1 {
		t.Errorf("first use pushed %d times, want once: the remote learns the schema from a push", driver.pushes)
	}
	_ = b.Close()

	again := &localDriver{path: path}
	backendFor(t, again, "origin-a")
	if again.pushes != 0 {
		t.Errorf("a re-run pushed %d times, want none: the log was already there", again.pushes)
	}
	if tables := replicaTables(t, path); len(tables) != 3 {
		t.Errorf("a re-run left %v, want the same three tables", tables)
	}
}

// TestWorkerRefusesARemoteThatIsNotALog pins the refusal a remote that is not
// this log gets: it is answered with the URL it named and a reason, before any
// table is created or row is written.
func TestWorkerRefusesARemoteThatIsNotALog(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"another database's tables": func(t *testing.T, path string) {
			execOn(t, path, "CREATE TABLE secrets (s TEXT NOT NULL)")
		},
		"a log with no meta": func(t *testing.T, path string) {
			execOn(t, path, createStatements[0], createStatements[1])
		},
		"a meta with no format row": func(t *testing.T, path string) {
			execOn(t, path, createStatements[0], createStatements[1], createStatements[2])
		},
		"a different format": func(t *testing.T, path string) {
			execOn(t, path, createStatements[0], createStatements[1], createStatements[2],
				"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '99')")
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			path := tempReplica(t)
			seed(t, path)
			b := NewTursoBackend(&localDriver{path: path})
			resp := startWorker(t, b).ask(helloRequest("h"))
			if resp.OK {
				t.Fatalf("hello = %+v, want a refusal", resp)
			}
			if resp.Code != CodeRemote {
				t.Errorf("code = %q, want %q: the refusal repeats on every attempt", resp.Code, CodeRemote)
			}
			if !strings.Contains(resp.Error, "libsql://remote.example") {
				t.Errorf("refusal %q does not name the remote", resp.Error)
			}
			if !strings.Contains(resp.Error, "not a relevo sync log") {
				t.Errorf("refusal %q does not say what the remote is not", resp.Error)
			}
		})
	}
}

// TestExportAssignsMaxPlusOneAndRepliesOnlyAfterPush pins sequence assignment
// and reply ordering. The request names numbers of its own, which the replica's
// own high-water mark overrides; and a push that fails turns the append into a
// refusal, so the daemon keeps the outbox rows for entries the remote never got.
func TestExportAssignsMaxPlusOneAndRepliesOnlyAfterPush(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")

	written, err := b.Append([]Entry{
		{Origin: "origin-a", Seq: 99, Batch: 99, Tbl: "board", PK: `["b"]`, Op: opUpsert,
			SchemaVersion: 3, Body: json.RawMessage(`{"title":"one"}`), At: pinnedAt},
		{Origin: "origin-a", Seq: 100, Batch: 100, Tbl: "task", PK: `["t"]`, Op: opDelete,
			SchemaVersion: 3, At: pinnedAt},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	assertSeqs(t, written, 1, 2)
	if written[0].Batch != 1 || written[1].Batch != 1 {
		t.Errorf("batch = %d, %d; want both 1, the first sequence of the append", written[0].Batch, written[1].Batch)
	}

	// A later append continues past what the replica already holds.
	again, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b3"]`, `{"title":"three"}`)})
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	assertSeqs(t, again, 3)
	if again[0].Batch != 3 {
		t.Errorf("second batch = %d, want 3", again[0].Batch)
	}

	driver.pushErr = errors.New("remote unreachable")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b4"]`, `{"title":"four"}`)}); err == nil {
		t.Fatal("append was answered over a push that failed")
	}
}

// TestPullReturnsOnlyEntriesPastTheMarks pins the read side: another origin's
// entries past its mark, in whole batches, with nothing of this origin's own
// and nothing at or below the mark.
func TestPullReturnsOnlyEntriesPastTheMarks(t *testing.T) {
	path := tempReplica(t)
	a := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := a.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"mine"}`)}); err != nil {
		t.Fatalf("append as origin-a: %v", err)
	}
	b := backendFor(t, &localDriver{path: path}, "origin-b")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-b", "board", `["b1"]`, `{"title":"one"}`),
		upsertEntry("origin-b", "board", `["b2"]`, `{"title":"two"}`),
	}); err != nil {
		t.Fatalf("append batch one: %v", err)
	}
	if _, err := b.Append([]Entry{upsertEntry("origin-b", "task", `["t"]`, `{"title":"three"}`)}); err != nil {
		t.Fatalf("append batch two: %v", err)
	}

	got, err := a.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3)
	for _, e := range got {
		if e.Origin != "origin-b" {
			t.Errorf("pull returned %s's entry, want only origin-b's", e.Origin)
		}
	}

	// A mark inside the first batch skips that batch whole, so only the second
	// batch's entry comes back.
	got, err = a.Pull(map[string]int{"origin-b": 1})
	if err != nil {
		t.Fatalf("pull past one: %v", err)
	}
	assertSeqs(t, got, 3)

	got, err = a.Pull(map[string]int{"origin-b": 3})
	if err != nil {
		t.Fatalf("pull past three: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("pull past the highest sequence = %+v, want nothing", got)
	}
}

// TestPullReadsOnlyWhatIsPastTheMarks pins that a pull narrows its read in SQL
// per origin: one mark filter per origin, so the log is not read whole and
// filtered here, bounded to one page. An origin with no mark is read from its
// first sequence. The backend reads through that statement, so an entry at or
// below a mark does not come back even when the mark sits inside its batch.
func TestPullReadsOnlyWhatIsPastTheMarks(t *testing.T) {
	t.Parallel()
	origins := []string{"origin-b", "origin-c"}
	query, args := pullQuery(origins, map[string]int{"origin-b": 7}, 100)

	if got := strings.Count(query, "(origin = ? AND seq > ?)"); got != len(origins) {
		t.Fatalf("the pull filters %d origins in SQL, want one clause per origin:\n%s", got, query)
	}
	if !strings.Contains(query, "ORDER BY origin, seq") || !strings.Contains(query, "LIMIT ?") {
		t.Fatalf("the pull is not ordered and bounded:\n%s", query)
	}
	want := []any{"origin-b", 7, "origin-c", 0, 100}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("pull args = %v, want %v", args, want)
	}

	path := tempReplica(t)
	a := backendFor(t, &localDriver{path: path}, "origin-a")
	b := backendFor(t, &localDriver{path: path}, "origin-b")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-b", "board", `["b1"]`, `{"title":"one"}`),
		upsertEntry("origin-b", "board", `["b2"]`, `{"title":"two"}`),
	}); err != nil {
		t.Fatalf("append batch one: %v", err)
	}
	if _, err := b.Append([]Entry{upsertEntry("origin-b", "task", `["t"]`, `{"title":"three"}`)}); err != nil {
		t.Fatalf("append batch two: %v", err)
	}

	got, err := a.Pull(map[string]int{"origin-b": 1})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	assertSeqs(t, got, 3)
}

// TestPullCompletesABatchThePageCut pins that a page small enough to end inside
// a batch still returns the batch whole: a batch is applied whole and the mark
// moves to its tail, so half of one would let the next pull drop the other half
// as though the mark covered it.
func TestPullCompletesABatchThePageCut(t *testing.T) {
	t.Parallel()
	path := tempReplica(t)
	a := backendFor(t, &localDriver{path: path}, "origin-a")
	b := backendFor(t, &localDriver{path: path}, "origin-b")
	a.page = 2
	if _, err := b.Append([]Entry{
		upsertEntry("origin-b", "board", `["b1"]`, `{"title":"one"}`),
		upsertEntry("origin-b", "board", `["b2"]`, `{"title":"two"}`),
		upsertEntry("origin-b", "board", `["b3"]`, `{"title":"three"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := a.Pull(nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	assertSeqs(t, got, 1, 2, 3)
	for _, e := range got {
		if e.Batch != 1 {
			t.Errorf("entry %d carries batch %d, want the one batch kept whole", e.Seq, e.Batch)
		}
	}
}

// TestHeadPagesInSeqOrder pins head paging: pages ascend by sequence, cover the
// head once, end when it is spent, and never carry another origin's rows.
func TestHeadPagesInSeqOrder(t *testing.T) {
	path := tempReplica(t)
	a := backendFor(t, &localDriver{path: path}, "origin-a")
	if _, err := a.Append([]Entry{upsertEntry("origin-a", "board", `["a"]`, `{"title":"mine"}`)}); err != nil {
		t.Fatalf("append as origin-a: %v", err)
	}
	b := backendFor(t, &localDriver{path: path}, "origin-b")
	if _, err := b.Append([]Entry{
		upsertEntry("origin-b", "board", `["b1"]`, `{"title":"one"}`),
		upsertEntry("origin-b", "board", `["b2"]`, `{"title":"two"}`),
		upsertEntry("origin-b", "task", `["t1"]`, `{"title":"three"}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// A later update moves b1's head to the newest sequence and a delete drops
	// b2, so the head is b1 at 4 and t1 at 3.
	if _, err := b.Append([]Entry{
		upsertEntry("origin-b", "board", `["b1"]`, `{"title":"one changed"}`),
		{Origin: "origin-b", Tbl: "board", PK: `["b2"]`, Op: opDelete, SchemaVersion: 3, At: pinnedAt},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	got := pageHead(t, b, "origin-b")
	if len(got) != 2 {
		t.Fatalf("paged head = %+v, want two rows", got)
	}
	if got[0].Seq >= got[1].Seq {
		t.Errorf("pages came back %d then %d, want ascending", got[0].Seq, got[1].Seq)
	}
	for _, row := range got {
		if row.PK == `["a"]` {
			t.Errorf("head page carried origin-a's row: %+v", row)
		}
	}

	whole, err := b.Head("origin-b", "", 0)
	if err != nil {
		t.Fatalf("whole head: %v", err)
	}
	if len(whole) != len(got) {
		t.Errorf("whole head = %d rows, paged = %d", len(whole), len(got))
	}

	mine, err := a.Head("origin-a", "", 0)
	if err != nil {
		t.Fatalf("own head: %v", err)
	}
	if len(mine) != 1 || mine[0].PK != `["a"]` {
		t.Errorf("origin-a's head = %+v, want only its own row", mine)
	}

	if _, err := a.Head("origin-a", "not a number", 0); err == nil {
		t.Error("head accepted a cursor that is not a sequence")
	}
	if _, err := a.Head("", "", 0); err == nil {
		t.Error("head accepted a request with no origin")
	}
}

// TestBodyHashMatchesTheExchange pins the digest the worker stores against the
// one the exchange compares with. The worker cannot import synclog (that would
// drag internal/db across the boundary the worker keeps), so the canonical
// encoding is restated; a drift between the two would make reconcile re-export
// every row forever, which is what this catches.
func TestBodyHashMatchesTheExchange(t *testing.T) {
	bodies := []json.RawMessage{
		json.RawMessage(`{"title":"one"}`),
		json.RawMessage(`{"title":"two","body":"three"}`),
		json.RawMessage(`{"count":42,"ratio":1.5,"gone":null}`),
		json.RawMessage(`{"body":{"$blob":"AQID"},"codec":"zstd"}`),
		json.RawMessage(`{"b":"two","a":"one"}`),
	}
	for _, body := range bodies {
		got, err := bodyHash(body)
		if err != nil {
			t.Fatalf("bodyHash(%s): %v", body, err)
		}
		want, err := synclog.BodyHash(body)
		if err != nil {
			t.Fatalf("synclog.BodyHash(%s): %v", body, err)
		}
		if got != want {
			t.Errorf("bodyHash(%s) = %s, want %s", body, got, want)
		}
	}
	if _, err := bodyHash(json.RawMessage(`not json`)); err == nil {
		t.Error("bodyHash accepted a body that is not JSON")
	}
}

// fakeSyncDb is the sync constructor's handle with no engine behind it, so a
// test can drive the driver's own open path without a network or a library.
type fakeSyncDb struct {
	db  *sql.DB
	err error

	pushes int
	pulls  int
	stats  int
}

func (f *fakeSyncDb) Connect(context.Context) (*sql.DB, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.db, nil
}

func (f *fakeSyncDb) Push(context.Context) error {
	f.pushes++
	return f.err
}

func (f *fakeSyncDb) Pull(context.Context) (bool, error) {
	f.pulls++
	return false, f.err
}

func (f *fakeSyncDb) Stats(context.Context) (turso.TursoSyncDbStats, error) {
	f.stats++
	return turso.TursoSyncDbStats{}, f.err
}

// TestTursoDriverOpensThroughTheSyncConstructor pins the production driver's
// shape: it bootstraps through the sync constructor with this replica path and
// the remote hello named, every connection comes from Connect(), and the two
// transfer bounds are the recorded ones.
func TestTursoDriverOpensThroughTheSyncConstructor(t *testing.T) {
	restore := newSyncDb
	t.Cleanup(func() { newSyncDb = restore })

	replica := tempReplica(t)
	fake := &fakeSyncDb{db: openAt(t, replica)}
	var got turso.TursoSyncDbConfig
	newSyncDb = func(_ context.Context, cfg turso.TursoSyncDbConfig) (syncDb, error) {
		got = cfg
		return fake, nil
	}

	d := newTursoDriver(replica)
	db, err := d.Open(context.Background(), Spec{
		Version: ProtocolVersion, Origin: "origin-a", URL: "libsql://r.example", Token: "tok",
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if db != fake.db {
		t.Error("Connect() did not return the connection every statement must use")
	}
	if got.Path != replica || got.RemoteUrl != "libsql://r.example" || got.AuthToken != "tok" {
		t.Errorf("constructor config = %+v, want this replica and remote", got)
	}
	if got.BootstrapIfEmpty == nil || !*got.BootstrapIfEmpty {
		t.Error("bootstrap is not asked for: an empty remote must yield the replica this worker fills")
	}
	if got.PushOperationsThreshold != pushOperationsThreshold || got.PullBytesThreshold != pullBytesThreshold {
		t.Errorf("thresholds = %d/%d, want %d/%d",
			got.PushOperationsThreshold, got.PullBytesThreshold, pushOperationsThreshold, pullBytesThreshold)
	}
	if err := d.Push(context.Background()); err != nil || fake.pushes != 1 {
		t.Errorf("Push err=%v calls=%d, want one call", err, fake.pushes)
	}
	if err := d.Pull(context.Background()); err != nil || fake.pulls != 1 {
		t.Errorf("Pull err=%v calls=%d, want one call", err, fake.pulls)
	}
	if err := d.Stats(context.Background()); err != nil || fake.stats != 1 {
		t.Errorf("Stats err=%v calls=%d, want one call", err, fake.stats)
	}
	if _, err := d.Open(context.Background(), Spec{}); err == nil {
		t.Error("a second open replaced the replica instead of refusing")
	}
}

// TestTursoDriverPropagatesConstructorFailures pins that a constructor that
// cannot reach the remote, and a connection that cannot be opened, each stop
// the driver rather than leaving a half-open replica behind.
func TestTursoDriverPropagatesConstructorFailures(t *testing.T) {
	restore := newSyncDb
	t.Cleanup(func() { newSyncDb = restore })

	newSyncDb = func(context.Context, turso.TursoSyncDbConfig) (syncDb, error) {
		return nil, errors.New("no remote")
	}
	if _, err := newTursoDriver(tempReplica(t)).Open(context.Background(), Spec{URL: "u", Token: "t"}); err == nil {
		t.Error("open succeeded over a constructor that failed")
	}

	d := newTursoDriver(tempReplica(t))
	newSyncDb = func(context.Context, turso.TursoSyncDbConfig) (syncDb, error) {
		return &fakeSyncDb{err: errors.New("cannot connect")}, nil
	}
	if _, err := d.Open(context.Background(), Spec{URL: "u", Token: "t"}); err == nil {
		t.Error("open succeeded over a connection that failed")
	}
	// The failed handle is cleared, so a later open is not refused as a double
	// open: the replica was never held.
	newSyncDb = func(context.Context, turso.TursoSyncDbConfig) (syncDb, error) {
		return &fakeSyncDb{db: openAt(t, tempReplica(t))}, nil
	}
	if _, err := d.Open(context.Background(), Spec{URL: "u", Token: "t"}); err != nil {
		t.Errorf("a failed connect left the driver holding a replica: %v", err)
	}
}

// TestBackendRefusesBeforeHello pins that a verb without a replica refuses
// rather than answering from a log that was never opened.
func TestBackendRefusesBeforeHello(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t)})
	if _, err := b.Append([]Entry{upsertEntry("", "board", `["b"]`, `{}`)}); err == nil {
		t.Error("append before open succeeded")
	}
	if _, err := b.Pull(nil); err == nil {
		t.Error("pull before open succeeded")
	}
	if _, err := b.Head("origin-a", "", 0); err == nil {
		t.Error("head before open succeeded")
	}
	if _, err := b.Stats(); err == nil {
		t.Error("stats before open succeeded")
	}
}

// TestBackendRefusesAnotherOriginsEntry pins the ownership rule at the backend
// too, so a caller that reached Append without the worker's own check is still
// stopped before anything is written.
func TestBackendRefusesAnotherOriginsEntry(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-b", "board", `["b"]`, `{}`)}); err == nil {
		t.Fatal("append accepted another origin's entry")
	}
	var n int
	if err := openAt(t, driver.path).QueryRow("SELECT COUNT(*) FROM log").Scan(&n); err != nil {
		t.Fatalf("read the replica: %v", err)
	}
	if n != 0 {
		t.Errorf("a refused append wrote %d log rows, want none", n)
	}
}

// TestBackendStatsAndErrors pins the stats verb and that a driver which cannot
// report health, or a replica that cannot be read, refuses rather than reports
// a size for a log nobody can reach.
func TestBackendStatsAndErrors(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	stats, err := b.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Entries != 1 || stats.Origins != 1 || stats.Seq != 1 {
		t.Errorf("stats = %+v, want one entry, one origin, seq 1", stats)
	}
	if driver.stats != 1 {
		t.Errorf("driver stats called %d times, want once", driver.stats)
	}

	driver.statsErr = errors.New("engine wedged")
	if _, err := b.Stats(); err == nil {
		t.Error("stats answered while the engine could not report its own health")
	}
}

// TestBackendAppendFailsWhenTheWriteFails pins that a write the replica refuses
// is a refusal, not a batch reported as written.
func TestBackendAppendFailsWhenTheWriteFails(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	// A body that is not JSON fails head's hash before the transaction commits.
	if _, err := b.Append([]Entry{{
		Origin: "origin-a", Tbl: "board", PK: `["b"]`, Op: opUpsert,
		SchemaVersion: 3, Body: json.RawMessage(`not json`), At: pinnedAt,
	}}); err == nil {
		t.Error("append accepted a body that is not JSON")
	}
}

// TestBackendPullFailsWhenTheDriverFails pins that a pull the remote refused is
// reported rather than answered from a replica the driver never refreshed.
func TestBackendPullFailsWhenTheDriverFails(t *testing.T) {
	driver := &localDriver{path: tempReplica(t), pullErr: errors.New("no remote")}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Pull(nil); err == nil {
		t.Error("pull succeeded while the driver could not reach the remote")
	}
}

// TestNewTursoBackendForPathBuildsTheProductionBackend pins that the factory
// names the worker's real backend, so the production path exists and is not
// left for a later wiring to invent.
func TestNewTursoBackendForPathBuildsTheProductionBackend(t *testing.T) {
	if _, ok := NewTursoBackendForPath(tempReplica(t)).(*TursoBackend); !ok {
		t.Fatalf("NewTursoBackendForPath returned %T, want *TursoBackend",
			NewTursoBackendForPath(tempReplica(t)))
	}
}

// TestBackendRefusesWhenTheReplicaCannotOpen pins that a driver which cannot
// open the replica is reported and the backend holds nothing after it.
func TestBackendRefusesWhenTheReplicaCannotOpen(t *testing.T) {
	b := NewTursoBackend(&localDriver{path: tempReplica(t), openErr: errors.New("no replica")})
	if err := b.Open(Spec{Origin: "origin-a", URL: "libsql://r.example", Token: "t"}); err == nil {
		t.Fatal("open succeeded over a driver that failed")
	}
	if err := b.Close(); err != nil {
		t.Errorf("close after a failed open: %v", err)
	}
}

// TestFirstUseRefusesWhenTheSchemaPushFails pins that a remote which never
// learned the schema is reported rather than used.
func TestFirstUseRefusesWhenTheSchemaPushFails(t *testing.T) {
	driver := &localDriver{path: tempReplica(t), pushErr: errors.New("no remote")}
	b := NewTursoBackend(driver)
	if err := b.Open(Spec{Origin: "origin-a", URL: "libsql://r.example", Token: "t"}); err == nil {
		t.Fatal("open succeeded over a push that failed")
	}
}

// TestBackendAnswersAnEmptyBatch pins that an export that drained nothing is
// answered without a write.
func TestBackendAnswersAnEmptyBatch(t *testing.T) {
	b := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	got, err := b.Append(nil)
	if err != nil || len(got) != 0 {
		t.Errorf("append of no entries = %+v, %v; want empty", got, err)
	}
}

// TestBackendRefusesWhenTheLogIsGone pins that a replica whose tables vanished
// is reported, not answered from.
func TestBackendRefusesWhenTheLogIsGone(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	execOn(t, driver.path, "DROP TABLE head")
	if _, err := b.Head("origin-a", "", 0); err == nil {
		t.Error("head succeeded over a missing head table")
	}
	execOn(t, driver.path, "DROP TABLE log")
	if _, err := b.Pull(nil); err == nil {
		t.Error("pull succeeded over a missing log table")
	}
	if _, err := b.Append([]Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}); err == nil {
		t.Error("append succeeded over a missing log table")
	}
	if _, err := b.Stats(); err == nil {
		t.Error("stats succeeded over a missing log table")
	}
}

// TestBackendPullRefusesARowItCannotScan pins that a log row whose column types
// do not match the log is reported rather than read as a zero value.
func TestBackendPullRefusesARowItCannotScan(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	execOn(t, driver.path,
		"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at) "+
			"VALUES ('origin-b', 'not a number', 1, 'board', '[\"b\"]', 'delete', 1, NULL, '2026-10-08T00:00:00Z')")
	if _, err := b.Pull(nil); err == nil {
		t.Error("pull read a row whose sequence is not a number")
	}
}

// TestBackendOpenTwiceReleasesTheFirstReplica pins that a second handshake on
// one pipe does not leave the first replica held.
func TestBackendOpenTwiceReleasesTheFirstReplica(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	if err := b.Open(Spec{Origin: "origin-a", URL: "libsql://scratch.example", Token: "the-token"}); err != nil {
		t.Fatalf("second open: %v", err)
	}
	if driver.opens != 2 {
		t.Errorf("the constructor opened %d times, want two", driver.opens)
	}
}

// TestBackendPullRefusesARowItCannotRead pins that a log row whose time cannot
// be parsed is reported rather than read as a zero moment.
func TestBackendPullRefusesARowItCannotRead(t *testing.T) {
	driver := &localDriver{path: tempReplica(t)}
	b := backendFor(t, driver, "origin-a")
	execOn(t, driver.path,
		"INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at) "+
			"VALUES ('origin-b', 1, 1, 'board', '[\"b\"]', 'delete', 1, NULL, 'not a time')")
	if _, err := b.Pull(nil); err == nil {
		t.Error("pull read a row whose time is not a time")
	}
}

// TestBackendUnreadableMetaRefuses pins that a remote whose meta cannot be read
// is refused rather than treated as this log.
func TestBackendUnreadableMetaRefuses(t *testing.T) {
	path := tempReplica(t)
	execOn(t, path,
		"CREATE TABLE log (origin TEXT)",
		"CREATE TABLE head (origin TEXT)",
		"CREATE TABLE meta (name TEXT)") // a meta without the column the log reads
	b := NewTursoBackend(&localDriver{path: path})
	if err := b.Open(Spec{Origin: "origin-a", URL: "libsql://r.example", Token: "t"}); err == nil {
		t.Fatal("open succeeded over a meta that cannot be read")
	}
}

// refusingBackend opens and then refuses every verb, so a worker's own
// error-return path for each verb is exercised rather than the backend's.
type refusingBackend struct{ fail error }

func (r refusingBackend) Open(Spec) error                      { return nil }
func (r refusingBackend) Append([]Entry) ([]Entry, error)      { return nil, r.fail }
func (r refusingBackend) Pull(map[string]int) ([]Entry, error) { return nil, r.fail }
func (r refusingBackend) Head(string, string, int) ([]HeadRow, error) {
	return nil, r.fail
}
func (r refusingBackend) Stats() (Stats, error) { return Stats{}, r.fail }
func (r refusingBackend) Close() error          { return nil }

// TestWorkerCarriesEveryBackendRefusal pins that each verb hands the backend's
// own reason back over the pipe rather than a paraphrase or a silence.
func TestWorkerCarriesEveryBackendRefusal(t *testing.T) {
	w := startWorker(t, refusingBackend{fail: errors.New("backend refused")})
	w.askHello()
	requests := []Request{
		{ID: "1", Verb: VerbExport, Entries: []Entry{upsertEntry("origin-a", "board", `["b"]`, `{"title":"one"}`)}},
		{ID: "2", Verb: VerbPull},
		{ID: "3", Verb: VerbHead, Origin: "origin-a"},
		{ID: "4", Verb: VerbStats},
	}
	for _, req := range requests {
		resp := w.ask(req)
		if resp.OK || !strings.Contains(resp.Error, "backend refused") {
			t.Errorf("%s = %+v, want the backend's refusal", req.Verb, resp)
		}
		if resp.Code != "" {
			t.Errorf("%s carried code %q, want none: the backend marked no class", req.Verb, resp.Code)
		}
	}
}

// verify interface satisfaction at compile time, so a verb cannot lose its
// backend, or the driver seam its doubles, without the test build failing.
var (
	_ ReplicaDriver = (*localDriver)(nil)
	_ syncDb        = (*fakeSyncDb)(nil)
	_ Backend       = (refusingBackend{})
)
