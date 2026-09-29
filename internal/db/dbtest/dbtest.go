// Package dbtest builds one migrated database template per test binary and
// points db.Open at it, so fresh test databases are seeded from a copy instead
// of running the migrations.
package dbtest

import (
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

// OwnerMode routes every database opened with db.Open or db.OpenWith through an
// in-process owner when ownerEnv is set, and does nothing when it is not. Each
// path gets one owner, started on the first open of that path and reused by
// every later open. The returned cleanup closes every owner and removes the
// socket directory; a binary that installs it runs cleanup after its tests.
func OwnerMode() (cleanup func(), err error) {
	_, cleanup, err = ownerMode()
	return cleanup, err
}

// ownerMode is OwnerMode plus the registry, which this package's own test
// inspects. A nil registry means the switch is off.
func ownerMode() (reg *registry, cleanup func(), err error) {
	if os.Getenv(ownerEnv) == "" {
		return nil, func() {}, nil
	}
	dir, err := os.MkdirTemp("/tmp", "rvo-")
	if err != nil {
		return nil, nil, err
	}
	r := &registry{dir: dir, socks: make(map[string]string)}
	db.SetOwnerHop(r.hop)
	cleanup = func() {
		db.SetOwnerHop(nil)
		r.close()
		_ = os.RemoveAll(dir)
	}
	return r, cleanup, nil
}

// registry maps each opened path to the socket its owner listens on. One owner
// per path, so a path opened twice -- or concurrently, as the db tests open
// theirs -- reaches the same database.
type registry struct {
	dir    string
	mu     sync.Mutex
	socks  map[string]string
	owners []*owner.Server
	lns    []net.Listener
}

func (r *registry) hop(path string, _ db.Options, direct func() (*db.DB, error)) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if sock, ok := r.socks[path]; ok {
		return sock, nil
	}
	d, err := direct()
	if err != nil {
		return "", err
	}
	sock := filepath.Join(r.dir, fmt.Sprintf("o-%d.sock", len(r.socks)))
	if len(sock) >= 104 {
		return "", fmt.Errorf("dbtest: socket path %q is %d bytes, over the 104-byte sun_path", sock, len(sock))
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		return "", err
	}
	srv := db.NewOwner(d)
	go func() { _ = srv.Serve(l) }()
	r.owners = append(r.owners, srv)
	r.lns = append(r.lns, l)
	r.socks[path] = sock
	return sock, nil
}

func (r *registry) close() {
	for _, srv := range r.owners {
		_ = srv.Close()
	}
	for _, l := range r.lns {
		_ = l.Close()
	}
}
