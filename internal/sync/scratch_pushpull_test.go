//go:build !modernc

package sync_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	turso "turso.tech/database/tursogo"
)

// The scratch remote this file talks to is named by two environment variables, so
// nothing here carries a credential and CI never has one to carry:
//
//	RELEVO_SCRATCH_SYNC_URL    libsql://scratch-pp-001-<account>.<region>.turso.io
//	RELEVO_SCRATCH_SYNC_TOKEN  a token for that database alone
//
// Every file this file opens is created under t.TempDir(). No live database is
// named anywhere below, and no verb is run against a live state: these are driver
// calls over fixtures, which is the only way the question "did the push land" can
// be answered from this side.
func scratchRemote(t *testing.T) (string, string) {
	t.Helper()
	url := os.Getenv("RELEVO_SCRATCH_SYNC_URL")
	token := os.Getenv("RELEVO_SCRATCH_SYNC_TOKEN")
	if url == "" || token == "" {
		t.Skip("no scratch remote named: set RELEVO_SCRATCH_SYNC_URL and RELEVO_SCRATCH_SYNC_TOKEN")
	}
	return url, token
}

// scratchCtx bounds one driver call. The pull long-polls, so the bound is the
// only thing standing between a quiet remote and a test that never returns.
func scratchCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 90*time.Second)
}

// makeSharedTable builds the one table every half of this file writes, through
// the plain connector: the route internal/db opens every file it owns with.
func makeSharedTable(t *testing.T, path string, rows int) {
	t.Helper()
	raw, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the plain pool over %s: %v", path, err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the scratch table: %v", err)
	}
	for i := range rows {
		if _, err := raw.Exec(`INSERT INTO scratch (k, v) VALUES (?, ?)`, i, "owner-path"); err != nil {
			t.Fatalf("insert %d through the plain pool: %v", i, err)
		}
	}
}

// countScratch reads the scratch table back over whatever pool it is handed, so
// the same count is read the same way on both sides of the comparison. A remote
// the push never reached has no such table at all, which is a count of zero
// rather than a failure to read: that absence is the measurement.
func countScratch(t *testing.T, pool *sql.DB) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM scratch`).Scan(&n); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return 0
		}
		t.Fatalf("count the scratch table: %v", err)
	}
	return n
}

// openPeer opens a second local file against the same remote through the sync
// engine, and returns both the handle and its SQL pool. A peer is the only way
// this file can look at the remote's contents without a shell and without naming
// the live database: it is a file of its own, and everything it knows about the
// cloud it learned by pulling it.
func openPeer(t *testing.T, path, url, token string, bootstrap bool) *turso.TursoSyncDb {
	t.Helper()
	flag := bootstrap
	handle, err := turso.NewTursoSyncDb(context.Background(), turso.TursoSyncDbConfig{
		Path:             path,
		RemoteUrl:        url,
		AuthToken:        token,
		ClientName:       "relevo-scratch-peer",
		BootstrapIfEmpty: &flag,
	})
	if err != nil {
		t.Fatalf("open the peer at %s: %v", path, err)
	}
	return handle
}

// TestThePushOverTheOwnerPathReportsSuccessAndLandsNothing is the push-landing
// question, answered against a real remote.
//
// The local file is written the way the daemon writes one -- through
// internal/db's own handle, which opens with the plain turso connector -- and
// then pushed through the sync handle, the only route a push can take. The push
// is expected to report success; the remote is then read by a peer that learned
// about it by pulling it.
//
// The two answers differing is the whole finding: a push that reports success is
// evidence that the push operation finished, not that any row crossed the wire.
func TestThePushOverTheOwnerPathReportsSuccessAndLandsNothing(t *testing.T) {
	url, token := scratchRemote(t)
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	const ownerRows = 5
	makeSharedTable(t, live, ownerRows)

	shared, err := db.OpenSplit(live, db.Options{})
	if err != nil {
		t.Fatalf("open the split pair: %v", err)
	}
	defer func() { _ = shared.Close() }()

	ctx, cancel := scratchCtx(t)
	defer cancel()

	handle, err := relevosync.OpenRemote(ctx, relevosync.OpenConfig{
		Role:             relevosync.OpenSeed,
		Path:             live,
		RemoteURL:        url,
		AuthToken:        []byte(token),
		ClientName:       "relevo-scratch-owner",
		BootstrapIfEmpty: false,
	})
	if err != nil {
		t.Fatalf("open the member: %v", err)
	}

	before := openPeer(t, filepath.Join(dir, "before.db"), url, token, true)
	beforePool, err := before.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = beforePool.Close() }()
	was := countScratch(t, beforePool)

	if err := handle.Push(ctx); err != nil {
		t.Fatalf("the push over the owner path reported a failure: %v", err)
	}
	t.Logf("push over the owner path reported success with %d rows written locally", ownerRows)

	peer := openPeer(t, filepath.Join(dir, "peer.db"), url, token, true)
	peerPool, err := peer.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = peerPool.Close() }()

	// A delta, because the scratch remote outlives this test: what is asked is
	// what this push carried, not what the remote has ever held.
	if got := countScratch(t, peerPool) - was; got != 0 {
		t.Fatalf("the push over the owner path carried %d rows, want 0", got)
	}
	t.Logf("the push carried 0 rows although it reported success")
}

// TestAWriteOverTheSyncConnectionReachesTheRemote is the control for the test
// above, and it is what makes that test a finding rather than a broken remote: the
// same file, the same remote, the same push -- written through the sync engine's
// own connection instead of internal/db's plain one -- does land.
func TestAWriteOverTheSyncConnectionReachesTheRemote(t *testing.T) {
	url, token := scratchRemote(t)
	dir := t.TempDir()
	peerPath := filepath.Join(dir, "peer.db")

	ctx, cancel := scratchCtx(t)
	defer cancel()

	peer := openPeer(t, peerPath, url, token, true)
	peerPool, err := peer.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = peerPool.Close() }()
	if _, err := peerPool.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the scratch table over the sync connection: %v", err)
	}
	// The scratch remote outlives this test, so the key is stamped and the count
	// is a delta: what this push carried is the question, not what the remote has
	// ever held.
	if _, err := peerPool.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'sync-connection')`,
		time.Now().UnixNano()); err != nil {
		t.Fatalf("insert over the sync connection: %v", err)
	}

	before := openPeer(t, filepath.Join(dir, "before.db"), url, token, true)
	beforePool, err := before.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the before peer: %v", err)
	}
	defer func() { _ = beforePool.Close() }()
	was := countScratch(t, beforePool)

	if err := peer.Push(ctx); err != nil {
		t.Fatalf("the push over the sync connection reported a failure: %v", err)
	}

	watcherPath := filepath.Join(dir, "watcher.db")
	watcher := openPeer(t, watcherPath, url, token, true)
	watcherPool, err := watcher.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the watcher: %v", err)
	}
	defer func() { _ = watcherPool.Close() }()

	if got := countScratch(t, watcherPool) - was; got != 1 {
		t.Fatalf("the push over the sync connection carried %d rows, want 1", got)
	}
	t.Logf("the push carried exactly 1 row written over the sync connection")
}

// TestThePullDiesAtCheckpointAfterAPlainTruncate is the pull's failure, made
// deterministic.
//
// A pull that applies a remote change leaves the sync engine holding a WAL
// watermark. A truncate run over the same file by internal/db's own handle --
// which is what every Close, vacuum and compress pass does -- rewrites that WAL
// without telling the sync engine. The next pull asks the engine to checkpoint up
// to a frame the WAL no longer has, and it refuses with the driver message this
// round's live machine reported.
func TestThePullDiesAtCheckpointAfterAPlainTruncate(t *testing.T) {
	url, token := scratchRemote(t)
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	ctx, cancel := scratchCtx(t)
	defer cancel()

	// The peer owns the remote's changes: it writes and pushes so the file
	// under test has something to apply, which is what sets a watermark.
	peer := openPeer(t, filepath.Join(dir, "peer.db"), url, token, true)
	peerPool, err := peer.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = peerPool.Close() }()
	if _, err := peerPool.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create the scratch table: %v", err)
	}

	shared, err := db.OpenSplit(live, db.Options{})
	if err != nil {
		t.Fatalf("open the split pair: %v", err)
	}
	defer func() { _ = shared.Close() }()

	handle := openMember(t, ctx, live, url, token, true)

	// The scratch remote outlives this test, so a key is stamped rather than
	// fixed: a second run must add a row, not collide with the last one's.
	if _, err := peerPool.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'from-the-peer')`,
		time.Now().UnixNano()); err != nil {
		t.Fatalf("the peer's insert: %v", err)
	}
	if err := peer.Push(ctx); err != nil {
		t.Fatalf("the peer's push: %v", err)
	}
	applied, err := handle.Pull(ctx)
	if err != nil {
		t.Fatalf("the first pull: %v", err)
	}
	if !applied {
		t.Fatal("the first pull applied nothing, so no watermark was ever set")
	}
	t.Logf("first pull applied the remote's change")

	truncateOverPlainHandle(t, live)

	// The remote has to move again: a pull with nothing to apply never reaches
	// the apply half, which is where the checkpoint this round's failure names
	// is asked for. Without a change the second pull is a no-op that cannot fail.
	if _, err := peerPool.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'after-the-truncate')`,
		time.Now().UnixNano()); err != nil {
		t.Fatalf("the peer's insert after the truncate: %v", err)
	}
	if err := peer.Push(ctx); err != nil {
		t.Fatalf("the peer's push after the truncate: %v", err)
	}

	if err := handle.Push(ctx); err != nil {
		t.Fatalf("the push after the truncate: %v", err)
	}
	if _, err := handle.Pull(ctx); err == nil {
		t.Fatal("the pull after the truncate reported no failure")
	} else if !strings.Contains(err.Error(), "unable to checkpoint synced portion of WAL") {
		t.Fatalf("the pull failed with %v, want the driver's checkpoint refusal", err)
	} else {
		t.Logf("pull after the truncate: %v", err)
	}
}

// TestScratchOwnerPathWithTheCapturePragmaReachesTheRemote is the experiment
// that decides the fix: does setting the CDC pragma on the plain pool's own
// connections make the owner's writes land?
func TestScratchOwnerPathWithTheCapturePragmaReachesTheRemote(t *testing.T) {
	url, token := scratchRemote(t)
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	plain, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("open the plain pool: %v", err)
	}
	if _, err := plain.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The pragma the sync engine sets on its own connections.
	if _, err := plain.Exec(`PRAGMA capture_data_changes_conn('full,turso_cdc')`); err != nil {
		t.Fatalf("the capture pragma: %v", err)
	}
	if _, err := plain.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'with-the-pragma')`, time.Now().UnixNano()); err != nil {
		t.Fatalf("insert with the pragma: %v", err)
	}
	var cdc int
	if err := plain.QueryRow(`SELECT count(*) FROM turso_cdc`).Scan(&cdc); err != nil {
		t.Logf("turso_cdc is unreadable after the pragma: %v", err)
	} else {
		t.Logf("turso_cdc holds %d rows after one insert through a pragma'd connection", cdc)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	ctx, cancel := scratchCtx(t)
	defer cancel()
	handle, err := relevosync.OpenRemote(ctx, relevosync.OpenConfig{
		Role: relevosync.OpenSeed, Path: live, RemoteURL: url,
		AuthToken: []byte(token), ClientName: "relevo-scratch-owner", BootstrapIfEmpty: false,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// The scratch remote outlives this test, so the count is a delta: what the
	// remote held before the push is subtracted from what it holds after.
	before := openPeer(t, filepath.Join(dir, "before.db"), url, token, true)
	bp, err := before.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = bp.Close() }()
	was := countScratch(t, bp)

	if err := handle.Push(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	after := openPeer(t, filepath.Join(dir, "after.db"), url, token, true)
	ap, err := after.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = ap.Close() }()

	if got := countScratch(t, ap) - was; got != 1 {
		t.Fatalf("the push carried %d rows, want 1", got)
	}
	t.Logf("the push carried exactly 1 row written through a pragma'd plain connection")
}

// TestScratchTheMemberPoolCapturesItsOwnWrites is the fix's own check: the same
// owner-path write and the same push as the first test in this file, against a
// pool that has been through the member open, must now carry the row.
func TestScratchTheMemberPoolCapturesItsOwnWrites(t *testing.T) {
	url, token := scratchRemote(t)
	dir := t.TempDir()
	live := filepath.Join(dir, "relevo.db")

	shared, err := db.OpenSplit(live, db.Options{})
	if err != nil {
		t.Fatalf("open the split pair: %v", err)
	}
	defer func() { _ = shared.Close() }()

	raw, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("open the plain pool: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE IF NOT EXISTS scratch (k INTEGER PRIMARY KEY, v TEXT NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close the plain pool: %v", err)
	}

	ctx, cancel := scratchCtx(t)
	defer cancel()
	handle, err := relevosync.OpenRemote(ctx, relevosync.OpenConfig{
		Role: relevosync.OpenSeed, Path: live, RemoteURL: url,
		AuthToken: []byte(token), ClientName: "relevo-scratch-owner", BootstrapIfEmpty: false,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// A second pool over the same file, opened after the member open, is the
	// shape the daemon holds: it writes, and the engine has to see it.
	owner, err := db.OpenRaw(live)
	if err != nil {
		t.Fatalf("reopen the plain pool: %v", err)
	}
	defer func() { _ = owner.Close() }()
	if _, err := owner.Exec(`INSERT INTO scratch (k, v) VALUES (?, 'after-the-member-open')`,
		time.Now().UnixNano()); err != nil {
		t.Fatalf("insert through the member pool: %v", err)
	}

	before := openPeer(t, filepath.Join(dir, "before.db"), url, token, true)
	bp, err := before.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = bp.Close() }()
	was := countScratch(t, bp)

	if err := handle.Push(ctx); err != nil {
		t.Fatalf("push: %v", err)
	}
	after := openPeer(t, filepath.Join(dir, "after.db"), url, token, true)
	ap, err := after.Connect(ctx)
	if err != nil {
		t.Fatalf("connect the peer: %v", err)
	}
	defer func() { _ = ap.Close() }()

	if got := countScratch(t, ap) - was; got != 1 {
		t.Fatalf("the push carried %d rows, want 1", got)
	}
	t.Logf("the push carried exactly 1 row written through a pool opened after the member open")
}

// truncateOverPlainHandle rewrites the log of the file at path the way every
// close, vacuum and compress pass here does, and reports what the engine saw.
func truncateOverPlainHandle(t *testing.T, path string) {
	t.Helper()
	plain, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("open the plain pool: %v", err)
	}
	var busy, logFrames, checkpointed int
	if err := plain.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		t.Fatalf("truncate the WAL over the plain pool: %v", err)
	}
	if err := plain.Close(); err != nil {
		t.Fatalf("close the plain pool: %v", err)
	}
	t.Logf("plain handle truncated the WAL: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointed)
}

// openMember opens the file at path as a sync member, the role the enable's own
// open uses, with bootstrap decided by the caller.
func openMember(t *testing.T, ctx context.Context, path, url, token string, bootstrap bool) relevosync.SyncClient {
	t.Helper()
	handle, err := relevosync.OpenRemote(ctx, relevosync.OpenConfig{
		Role:             relevosync.OpenSeed,
		Path:             path,
		RemoteURL:        url,
		AuthToken:        []byte(token),
		ClientName:       "relevo-scratch-owner",
		BootstrapIfEmpty: bootstrap,
	})
	if err != nil {
		t.Fatalf("open the member at %s: %v", path, err)
	}
	return handle
}
