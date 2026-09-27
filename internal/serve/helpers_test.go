package serve

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func testServeDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open test machine db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func signedRequest(t *testing.T, kp remote.Keypair, method, target string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	var sum []byte
	if body != nil {
		bodyReader = bytes.NewReader(body)
		s := sha256.Sum256(body)
		sum = s[:]
	}
	req, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	hdr := remote.Sign(kp, method, target, sum, time.Now(), nonce)
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return req
}

func doSigned(t *testing.T, ts *httptest.Server, kp remote.Keypair, method, path string, body []byte, contentType string) (*http.Response, []byte) {
	t.Helper()
	req := signedRequest(t, kp, method, path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(ts.URL, "http://")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, respBody
}

// runGit runs one git command in dir. A repo these tests create gets
// auto-maintenance off: every `git commit` would otherwise spawn a detached
// `git maintenance run --auto` that outlives the command and races t.TempDir's
// RemoveAll.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	if len(args) > 0 && args[0] == "init" {
		runGit(t, dir, "config", "maintenance.auto", "false")
		runGit(t, dir, "config", "gc.auto", "0")
	}
	return string(out)
}

// scriptRunner is a fake spawn.Runner tracking liveness per pid, so two
// bindings' processes are tellable apart: setAlive flips every pid, finish one.
type scriptRunner struct {
	mu           sync.Mutex
	specs        []spawn.ProcSpec
	aliveHandles []spawn.ProcHandle
	alive        map[int]bool
	nextPID      int
	// startErr, when set, is returned by Start instead of starting anything.
	startErr error
}

func newScriptRunner() *scriptRunner {
	return &scriptRunner{alive: map[int]bool{}, nextPID: 4242}
}

func (r *scriptRunner) Start(ctx context.Context, spec spawn.ProcSpec) (spawn.ProcHandle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.startErr != nil {
		return spawn.ProcHandle{}, r.startErr
	}
	pid := r.nextPID
	r.nextPID++
	r.specs = append(r.specs, spec)
	r.alive[pid] = true
	if spec.LogPath != "" {
		_ = os.WriteFile(spec.LogPath, []byte("builder started\n"), 0o644)
	}
	return spawn.ProcHandle{PID: pid, StartedAt: time.Now()}, nil
}

func (r *scriptRunner) Alive(ctx context.Context, h spawn.ProcHandle) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.aliveHandles = append(r.aliveHandles, h)
	return r.alive[h.PID], nil
}

func (r *scriptRunner) ExitCode(ctx context.Context, h spawn.ProcHandle, logPath string) (code int, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	alive, tracked := r.alive[h.PID]
	if !tracked {
		// A pid this runner never started (a fresh runner standing in for a
		// daemon restart) reports "unknown", not "dead, code 0": headless.go's
		// restart-requeue check keys on that difference.
		return 0, false
	}
	return 0, !alive
}

func (r *scriptRunner) Kill(ctx context.Context, h spawn.ProcHandle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[h.PID] = false
	return nil
}

func (r *scriptRunner) Rusage(ctx context.Context, h spawn.ProcHandle, streamPath string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

// setAlive flips every pid this runner has started to a.
func (r *scriptRunner) setAlive(a bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for pid := range r.alive {
		r.alive[pid] = a
	}
}

// finish marks pid exited without disturbing any other pid.
func (r *scriptRunner) finish(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alive[pid] = false
}

func makeRoundForm(t *testing.T, round int, plan string, bundleBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	writeBundlePart(t, mw, bundleBytes)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func writeBundlePart(t *testing.T, mw *multipart.Writer, bundleBytes []byte) {
	t.Helper()
	if len(bundleBytes) == 0 {
		return
	}
	part, err := mw.CreateFormFile("bundle", "bundle.bundle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(bundleBytes); err != nil {
		t.Fatal(err)
	}
}

func makeRoundFormTags(t *testing.T, round int, plan string, bundleBytes []byte, tagsJSON string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if tagsJSON != "" {
		if err := mw.WriteField("tags", tagsJSON); err != nil {
			t.Fatal(err)
		}
	}
	writeBundlePart(t, mw, bundleBytes)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// makeRoundFormWithTier is makeRoundForm plus an optional "tier" field, written
// after "plan" and before "bundle" per the wire contract.
func makeRoundFormWithTier(t *testing.T, round int, plan, tier string, bundleBytes []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if tier != "" {
		if err := mw.WriteField("tier", tier); err != nil {
			t.Fatal(err)
		}
	}
	writeBundlePart(t, mw, bundleBytes)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// roundFormCandidate is makeRoundForm with the optional "candidate" field.
func roundFormCandidate(t *testing.T, round int, plan string, bundleBytes []byte, candidate string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("round", strconv.Itoa(round)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("plan", plan); err != nil {
		t.Fatal(err)
	}
	if candidate != "" {
		if err := mw.WriteField("candidate", candidate); err != nil {
			t.Fatal(err)
		}
	}
	writeBundlePart(t, mw, bundleBytes)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

// fakeStreamUsage answers a headless source pointed at the round's stream, and
// records every source it was asked to read.
type fakeStreamUsage struct {
	samples []usage.Sample
	note    string
	sources []usage.Source
}

func (f *fakeStreamUsage) Read(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	f.sources = append(f.sources, src)
	if src.Mode == usage.ModeHeadless && strings.HasSuffix(src.StreamPath, "001-runner.jsonl") {
		return f.samples, f.note
	}
	return nil, "not the round's stream"
}

func (f *fakeStreamUsage) Peek(ctx context.Context, src usage.Source) ([]usage.Sample, string) {
	return nil, ""
}

type testEnv struct {
	srv       *Server
	ts        *httptest.Server
	kp        remote.Keypair
	id        remote.ClientID
	clientDir string
	rootSHA   string
	headSHA   string
	repoID    string
	runner    *scriptRunner
	gitClient *git.Client
	transport *remote.BundleTransport
}

// newClientRepo builds a one-commit git repo for name and returns its dir, HEAD
// sha, root commit and repo id.
func newClientRepo(t *testing.T, gitClient *git.Client, name string) (dir, headSHA, rootSHA, repoID string) {
	t.Helper()
	ctx := context.Background()
	dir = t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", name)
	runGit(t, dir, "config", "user.email", name+"@example.com")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "file.txt")
	runGit(t, dir, "commit", "-m", "initial commit")

	headSHA, ok, err := gitClient.RefSHA(ctx, dir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("headSHA: %v, ok=%v", err, ok)
	}
	rootSHA, err = gitClient.RootCommit(ctx, dir)
	if err != nil {
		t.Fatalf("rootCommit: %v", err)
	}
	repoID, err = remote.RepoID(rootSHA)
	if err != nil {
		t.Fatalf("repoID: %v", err)
	}
	return dir, headSHA, rootSHA, repoID
}

func setupTestEnv(t *testing.T, cfgOpts ...func(*Config)) *testEnv {
	t.Helper()
	gitClient := git.NewClient("git", 0, 0)
	clientDir, headSHA, rootSHA, repoID := newClientRepo(t, gitClient, "test")

	serverRoot := t.TempDir()
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	srvCfg := Config{
		DB:         testServeDB(t),
		Root:       serverRoot,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	}
	for _, opt := range cfgOpts {
		opt(&srvCfg)
	}
	srv, err := New(srvCfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	transport := remote.NewBundleTransport(gitClient, t.TempDir())

	return &testEnv{
		srv:       srv,
		ts:        ts,
		kp:        kp,
		id:        id,
		clientDir: clientDir,
		rootSHA:   rootSHA,
		headSHA:   headSHA,
		repoID:    repoID,
		runner:    runner,
		gitClient: gitClient,
		transport: transport,
	}
}

// builderCandidateSet writes and loads the one claude/anthropic/haiku builder
// candidate every env starts from.
func builderCandidateSet(t *testing.T) (*candidate.Set, error) {
	t.Helper()
	body := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	return candidate.Load(path)
}

func (env *testEnv) runtime(t *testing.T) relevo.Runtime {
	t.Helper()
	return testRuntime(t, env.srv, env.id)
}

func testRuntime(t *testing.T, s *Server, id remote.ClientID) relevo.Runtime {
	t.Helper()
	rt, err := s.runtime(id)
	if err != nil {
		t.Fatalf("runtime(%s): %v", id, err)
	}
	return rt
}

func newTestServer(t *testing.T, maxBundleBytes int64) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	cfg := Config{
		DB:             testServeDB(t),
		Root:           root,
		MaxBundleBytes: maxBundleBytes,
		Now:            time.Now,
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	return s, root
}

// ownerEnv is another enrolled client with its own tiny git repo, for tests
// that need more than setupTestEnv's single owner.
type ownerEnv struct {
	kp        remote.Keypair
	id        remote.ClientID
	clientDir string
	headSHA   string
	repoID    string
}

func addOwner(t *testing.T, env *testEnv, label string) ownerEnv {
	t.Helper()
	clientDir, headSHA, _, repoID := newClientRepo(t, env.gitClient, label)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := env.srv.clients.Add(label, remote.MarshalPublic(kp.Public, label), time.Now()); err != nil {
		t.Fatal(err)
	}

	return ownerEnv{kp: kp, id: id, clientDir: clientDir, headSHA: headSHA, repoID: repoID}
}

func sendRound(t *testing.T, env *testEnv, kp remote.Keypair, clientDir, repoID, headSHA, name, plan string) (*http.Response, []byte) {
	t.Helper()
	return sendRoundAs(t, env, kp, clientDir, repoID, headSHA, name, plan, nil)
}

// sendRoundAs is sendRound with the client's git identity on the create request.
func sendRoundAs(t *testing.T, env *testEnv, kp remote.Keypair, clientDir, repoID, headSHA, name, plan string, author *remote.GitIdentity) (*http.Response, []byte) {
	t.Helper()
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       name,
		RepoID:     repoID,
		BaseCommit: headSHA,
		Role:       "builder",
		Author:     author,
	})
	resp, body := doSigned(t, env.ts, kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding %s status = %d, want 201; body: %s", name, resp.StatusCode, string(body))
	}

	outRef := "refs/relevo/" + name + "/out"
	if err := env.gitClient.UpdateRef(ctx, clientDir, outRef, headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	bundleBytes := snapshotRef(t, env, clientDir, outRef)

	formBytes, ct := makeRoundForm(t, 1, plan, bundleBytes)
	return doSigned(t, env.ts, kp, "POST", "/v1/bindings/"+name+"/rounds", formBytes, ct)
}

// snapshotRef snapshots one ref of dir into bundle bytes.
func snapshotRef(t *testing.T, env *testEnv, dir, ref string) []byte {
	t.Helper()
	snap, err := env.transport.Snapshot(context.Background(), dir, []string{ref}, "")
	if err != nil {
		t.Fatalf("snapshot %s: %v", ref, err)
	}
	bundleBytes, err := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	return bundleBytes
}

// startedSpecs copies the runner's specs under its mutex.
func startedSpecs(env *testEnv) []spawn.ProcSpec {
	env.runner.mu.Lock()
	defer env.runner.mu.Unlock()
	return append([]spawn.ProcSpec(nil), env.runner.specs...)
}

// closeRound writes round 1's report and completion marker for name, and marks
// pid exited without disturbing any other pid the runner tracks.
func closeRound(t *testing.T, rt relevo.Runtime, name string, pid int, runner *scriptRunner) {
	t.Helper()
	reportText := "# Report 1\nDone.\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath(name, 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath(name, 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	runner.finish(pid)
}

// createAndAbsorb creates binding name over the wire and returns the bundle
// bytes a round start ships from its out ref.
func createAndAbsorb(t *testing.T, env *testEnv, name string) []byte {
	t.Helper()
	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       name,
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create binding status = %d, want 201; body: %s", resp.StatusCode, string(body))
	}
	outRef := "refs/relevo/" + name + "/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	return snapshotRef(t, env, env.clientDir, outRef)
}

// finishRound writes round n's report and done marker, marks the fake builder
// exited, and ticks once so the round closes.
func finishRound(t *testing.T, env *testEnv, rt relevo.Runtime, name string, n int) {
	t.Helper()
	reportText := "# Report 1\nDone.\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath(name, n), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath(name, n), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

func requireStatus(t *testing.T, resp *http.Response, body []byte, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, want, string(body))
	}
}

func requireCreated(t *testing.T, resp *http.Response, body []byte, label string) {
	t.Helper()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%s start status = %d, want 201; body: %s", label, resp.StatusCode, string(body))
	}
}

func decodeView(t *testing.T, body []byte) remote.BindingView {
	t.Helper()
	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal binding view: %v; body: %s", err, string(body))
	}
	return view
}

// queuePosition is owner's 1-based position in a fresh census, or 0 if it is
// not currently queued.
func queuePosition(t *testing.T, env *testEnv, owner remote.ClientID) int {
	t.Helper()
	c, err := env.srv.census()
	if err != nil {
		t.Fatalf("census: %v", err)
	}
	for i, q := range c.Queued {
		if q.Owner == owner {
			return i + 1
		}
	}
	return 0
}

// seedServedBinding creates a bare repo with a branch, a checked-out worktree
// and two refs/relevo/<name>/* refs, then saves a DONE served binding over it.
func seedServedBinding(t *testing.T, env *testEnv, name string, facts store.ServeFacts) (bare, worktree string) {
	t.Helper()
	ctx := context.Background()

	ownerDir, ok := env.id.Dir()
	if !ok {
		t.Fatal("client id has no owner dir")
	}
	bare = filepath.Join(env.srv.cfg.Root, "repos", ownerDir, env.repoID+".git")
	if err := env.gitClient.InitBare(ctx, bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	branch := "relevo/" + name
	runGit(t, env.clientDir, "push", bare, env.headSHA+":refs/heads/"+branch)

	worktree = filepath.Join(t.TempDir(), name+"-wt")
	if err := env.gitClient.CheckoutWorktree(ctx, bare, worktree, branch); err != nil {
		t.Fatalf("checkout worktree: %v", err)
	}
	for _, ref := range []string{
		"refs/relevo/" + name + "/out",
		"refs/relevo/" + name + "/round-1",
	} {
		if err := env.gitClient.UpdateRef(ctx, bare, ref, env.headSHA, ""); err != nil {
			t.Fatalf("update-ref %s: %v", ref, err)
		}
	}

	facts.BareRepo = bare
	b := store.Binding{
		Name:     name,
		Owner:    string(env.id),
		CWD:      worktree,
		Worktree: worktree,
		Branch:   branch,
		State:    store.StateDone,
		Round:    2,
		Serve:    &facts,
	}
	rt := env.runtime(t)
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding %s: %v", name, err)
	}
	return bare, worktree
}

// writeServeTarball writes a flat <name>/<member> .tar.gz, the legacy archive
// layout.
func writeServeTarball(t *testing.T, dest, name string, members map[string]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for base, body := range members {
		hdr := &tar.Header{
			Name:     name + "/" + base,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
