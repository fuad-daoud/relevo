package client_test

import (
	"context"
	"crypto/tls"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// runGit runs git with a test identity and no global config, and turns
// auto-maintenance off: a detached `git maintenance run` outliving the command
// writes under .git/objects while t.TempDir's RemoveAll removes the tree.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\nOutput: %s", strings.Join(args, " "), dir, err, string(out))
	}
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return strings.TrimSpace(string(out))
}

// scriptRunner is a fake spawn.Runner whose process stays alive until killed.
type scriptRunner struct {
	mu           sync.Mutex
	specs        []spawn.ProcSpec
	aliveHandles []spawn.ProcHandle
	pidSeq       int
	alive        bool
}

func newScriptRunner() *scriptRunner {
	return &scriptRunner{alive: true}
}

func (r *scriptRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.specs = append(r.specs, spec)
	r.pidSeq++
	r.alive = true
	if spec.LogPath != "" {
		_ = os.WriteFile(spec.LogPath, []byte("builder started\n"), 0o644)
	}
	return spawn.ProcHandle{PID: 1000 + r.pidSeq, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliveHandles = append(r.aliveHandles, h)
	return r.alive, nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (code int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.alive {
		return 0, false
	}
	return 0, true
}

func (r *scriptRunner) Kill(ctx context.Context, h spawn.ProcHandle, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

func (r *scriptRunner) setAlive(a bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive = a
}

type testServerFixture struct {
	cl          *client.Client
	srv         *serve.Server
	ts          *httptest.Server
	kp          remote.Keypair
	fingerprint string
	runner      *scriptRunner
	gitClient   *git.Client
	transport   *remote.BundleTransport
	serverRoot  string
	machineDB   *db.DB
}

func startTestServer(t *testing.T) (*client.Client, *testServerFixture) {
	t.Helper()
	serverRoot := t.TempDir()
	machineDB := openTestDB(t)
	fingerprint, cert := initTestTLS(t, serve.SecretStore{DB: machineDB})
	cSet := loadTestCandidates(t)
	gitClient := git.NewClient("git", 0, 0)
	runner := newScriptRunner()
	kp := generateKey(t)
	enrollTestClient(t, machineDB, serverRoot, kp)
	srv := newTestServer(t, serverRoot, machineDB, cSet, runner, gitClient)
	ts := newTestTLSServer(t, srv, cert)

	cl := client.New(pinnedServers(ts.URL, fingerprint), kp, time.Now)
	fix := &testServerFixture{
		cl:          cl,
		srv:         srv,
		ts:          ts,
		kp:          kp,
		fingerprint: fingerprint,
		runner:      runner,
		gitClient:   gitClient,
		transport:   remote.NewBundleTransport(gitClient, t.TempDir()),
		serverRoot:  serverRoot,
		machineDB:   machineDB,
	}
	return cl, fix
}

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	machineDB, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open machine db: %v", err)
	}
	t.Cleanup(func() { _ = machineDB.Close() })
	return machineDB
}

func initTestTLS(t *testing.T, secrets serve.SecretStore) (string, tls.Certificate) {
	t.Helper()
	fp, err := serve.InitTLS(secrets, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	cert, err := serve.LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}
	return fp, cert
}

func loadTestCandidates(t *testing.T) *candidate.Set {
	t.Helper()
	const singleBuilder = `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(singleBuilder), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cSet
}

func enrollTestClient(t *testing.T, machineDB *db.DB, serverRoot string, kp remote.Keypair) {
	t.Helper()
	cls, err := serve.LoadClients(machineDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cls.Add("alice", remote.MarshalPublic(kp.Public, "alice@test"), time.Now()); err != nil {
		t.Fatal(err)
	}
}

func newTestServer(t *testing.T, root string, machineDB *db.DB, cSet *candidate.Set, runner spawn.Runner, gitClient *git.Client) *serve.Server {
	t.Helper()
	srv, err := serve.New(serve.Config{
		Root:       root,
		DB:         machineDB,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func newTestTLSServer(t *testing.T, srv *serve.Server, cert tls.Certificate) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}

func generateKey(t *testing.T) remote.Keypair {
	t.Helper()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return kp
}

func pinnedServers(url, fingerprint string) remote.Servers {
	return remote.Servers{"zen": remote.ServerEntry{URL: url, Fingerprint: fingerprint}}
}

func initClientRepo(t *testing.T, gitClient *git.Client) (dir, headSHA, repoID string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "initial commit")

	ctx := context.Background()
	headSHA, err := gitClient.HeadCommit(ctx, dir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	rootSHA, err := gitClient.RootCommit(ctx, dir)
	if err != nil {
		t.Fatalf("RootCommit: %v", err)
	}
	repoID, err = remote.RepoID(rootSHA)
	if err != nil {
		t.Fatalf("RepoID: %v", err)
	}
	return dir, headSHA, repoID
}

func serverBinding(t *testing.T, fix *testServerFixture, name string) (*store.Store, store.Binding) {
	t.Helper()
	id := remote.IDOf(fix.kp.Public)
	idDir, ok := id.Dir()
	if !ok {
		t.Fatal("failed to get dir from client ID")
	}
	st := store.NewShared(filepath.Join(fix.serverRoot, "bindings", idDir), string(id), fix.machineDB)
	b, err := st.Load(name)
	if err != nil {
		t.Fatalf("st.Load(%s): %v", name, err)
	}
	return st, b
}

func closedPortClient(t *testing.T, kp remote.Keypair) *client.Client {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return client.New(
		remote.Servers{"closed": remote.ServerEntry{URL: "http://" + addr, Insecure: true}},
		kp,
		time.Now,
	)
}
