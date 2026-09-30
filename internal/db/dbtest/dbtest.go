// Package dbtest builds one migrated database template per test binary and
// points db.Open at it, so fresh test databases are seeded from a copy instead
// of running the migrations.
package dbtest

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire/owner"
)

// ownerEnv selects OwnerMode: any non-empty value routes every test database
// through an in-process owner, and the empty (unset) value open the file
// directly, which is what the default CI shards run.
const ownerEnv = "RELEVO_DBTEST_OWNER"

// Install migrates a template database in a fresh temp directory and points
// db.Open at it. cleanup clears the template and removes the directory so a
// test binary leaves nothing behind.
func Install() (cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "relevo-dbtest-")
	if err != nil {
		return nil, err
	}
	cleanup = func() {
		db.SetFreshTemplate("")
		_ = os.RemoveAll(dir)
	}

	tpl := filepath.Join(dir, "template.db")
	d, err := db.Open(tpl)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := d.Close(); err != nil {
		cleanup()
		return nil, err
	}
	// A copy of the main file without the template's WAL would miss the schema,
	// so a -wal sibling left with content is a failure, not a warning.
	if fi, serr := os.Stat(tpl + "-wal"); serr == nil && fi.Size() > 0 {
		cleanup()
		return nil, fmt.Errorf("dbtest: %s has a non-empty -wal sibling (%d bytes)", tpl, fi.Size())
	}

	db.SetFreshTemplate(tpl)
	return cleanup, nil
}

// Main installs the template, runs the tests, and clears the template before
// returning their exit code. A binary's TestMain is
// os.Exit(dbtest.Main(m)). The template is migrated before the owner switch is
// installed: a hop-opened template would be left with a live owner and a
// non-empty -wal, which Install refuses.
func Main(m *testing.M) int {
	cleanup, err := Install()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ownerCleanup, err := OwnerMode()
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	ownerCleanup()
	cleanup()
	return code
}

// RawOpen opens path with the package's selected engine, without migrating it
// and without an owner hop, so a test can drive the raw driver directly. It
// registers a cleanup that closes the pool.
func RawOpen(t testing.TB, path string) *sql.DB {
	t.Helper()
	pool, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// OwnerMode routes every database opened with db.Open or db.OpenWith through an
// in-process owner when ownerEnv is set, and does nothing when it is not. Each
// path gets one owner, started on the first open of that path and reused by
// every later open. The returned cleanup closes every owner and removes the
// socket directory; a binary that installs it runs cleanup after its tests.
func OwnerMode() (cleanup func(), err error) {
	_, cleanup, err = ownerMode()
	return cleanup, err
}

// InstallOwner installs the same switch with no environment gate: a test
// binary that is itself the switch's subject -- the e2e round, which must run
// whole over a /tmp socket -- calls it directly instead of through
// RELEVO_DBTEST_OWNER. The returned cleanup closes every owner and removes the
// socket directory.
func InstallOwner() (cleanup func(), err error) {
	_, cleanup, err = ownerModeForced()
	return cleanup, err
}

// ownerMode is OwnerMode plus the registry, which this package's own test
// inspects. A nil registry means the switch is off.
func ownerMode() (reg *registry, cleanup func(), err error) {
	if os.Getenv(ownerEnv) == "" {
		return nil, func() {}, nil
	}
	return ownerModeForced()
}

// ownerModeForced is the switch itself, without the environment gate.
func ownerModeForced() (reg *registry, cleanup func(), err error) {
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		return nil, nil, err
	}
	r := &registry{
		dir:    dir,
		socks:  make(map[string]string),
		paths:  make(map[string]string),
		opens:  make(map[string]int),
		dbs:    make(map[string]*db.DB),
		owners: make(map[string]*owner.Server),
		lns:    make(map[string]net.Listener),
	}
	db.SetOwnerHop(r.hop)
	// The hop tells the registry when a dialled handle closes, so the last one
	// for a path stops that path's owner and closes its direct handle, which
	// checkpoints the WAL exactly as DB.Close does on the daemon's own handle.
	db.SetOwnerHopClosed(r.handleClosed)
	cleanup = func() {
		db.SetOwnerHop(nil)
		db.SetOwnerHopClosed(nil)
		r.close()
		_ = os.RemoveAll(dir)
	}
	return r, cleanup, nil
}

// registry maps each opened path to the socket its owner listens on, keeps that
// path's direct handle, and counts the dialled handles still open on it. One
// owner per path, so a path opened twice -- or concurrently, as the db tests
// open theirs -- reaches the same database; the owner is stopped, and its direct
// handle closed, once the last dialled handle is gone.
type registry struct {
	dir string
	mu  sync.Mutex
	// socks is path -> socket and paths is socket -> path, so a close told only
	// the socket can find its entry.
	socks  map[string]string
	paths  map[string]string
	opens  map[string]int
	dbs    map[string]*db.DB
	owners map[string]*owner.Server
	lns    map[string]net.Listener
	next   int
}

func (r *registry) hop(path string, _ db.Options, direct func() (*db.DB, error)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sock, ok := r.socks[path]; ok {
		r.opens[path]++
		return sock, nil
	}
	d, err := direct()
	if err != nil {
		return "", err
	}
	sock := filepath.Join(r.dir, fmt.Sprintf("o-%d.sock", r.next))
	r.next++
	if len(sock) >= 104 {
		_ = d.Close()
		return "", fmt.Errorf("dbtest: socket path %q is %d bytes, over the 104-byte sun_path", sock, len(sock))
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		_ = d.Close()
		return "", err
	}
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	r.socks[path] = sock
	r.paths[sock] = path
	r.opens[path] = 1
	r.dbs[path] = d
	r.owners[path] = srv
	r.lns[path] = l
	return sock, nil
}

// handleClosed runs when a handle the hop dialled has closed. The last one for a
// path stops that path's owner and closes its direct handle, so the direct
// handle checkpoints the WAL as it would on the daemon's own stop.
func (r *registry) handleClosed(sock string) {
	r.mu.Lock()
	path, ok := r.paths[sock]
	if !ok {
		r.mu.Unlock()
		return
	}
	r.opens[path]--
	if r.opens[path] > 0 {
		r.mu.Unlock()
		return
	}
	h := r.take(path)
	r.mu.Unlock()
	h.shutdown()
}

// pathHandles is one path's owner, listener and direct handle, taken out of the
// registry so they can be closed without holding its lock.
type pathHandles struct {
	srv *owner.Server
	ln  net.Listener
	db  *db.DB
}

// take removes path's entry and returns what must be stopped. The caller holds
// r.mu and closes the result after releasing it, so a close that reaches back
// into the registry finds nothing and returns.
func (r *registry) take(path string) pathHandles {
	h := pathHandles{srv: r.owners[path], ln: r.lns[path], db: r.dbs[path]}
	sock := r.socks[path]
	delete(r.socks, path)
	delete(r.paths, sock)
	delete(r.opens, path)
	delete(r.dbs, path)
	delete(r.owners, path)
	delete(r.lns, path)
	return h
}

// shutdown stops one path's owner, closes its listener and closes its direct
// handle, which checkpoints the WAL.
func (h pathHandles) shutdown() {
	if h.srv != nil {
		_ = h.srv.Close()
	}
	if h.ln != nil {
		_ = h.ln.Close()
	}
	if h.db != nil {
		_ = h.db.Close()
	}
}

func (r *registry) close() {
	r.mu.Lock()
	paths := make([]string, 0, len(r.dbs))
	for path := range r.dbs {
		paths = append(paths, path)
	}
	hs := make([]pathHandles, 0, len(paths))
	for _, path := range paths {
		hs = append(hs, r.take(path))
	}
	r.mu.Unlock()
	for _, h := range hs {
		h.shutdown()
	}
}
