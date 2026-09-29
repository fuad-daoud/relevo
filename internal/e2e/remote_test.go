package e2e

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "test")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test Repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func tickUntil(t *testing.T, deadline time.Duration, step func() bool) {
	t.Helper()
	start := time.Now()
	for {
		if step() {
			return
		}
		if time.Since(start) > deadline {
			t.Fatalf("tickUntil: timed out after %v", deadline)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func newServer(t *testing.T) (*serve.Server, string, string, func(pub string) remote.ClientID, func(owner remote.ClientID) *store.Store, *scriptRunner) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	return newServerWithContext(t, ctx, cancel, policy.Policy{})
}

func newServerWithContext(t *testing.T, ctx context.Context, cancel context.CancelFunc, pol policy.Policy) (*serve.Server, string, string, func(pub string) remote.ClientID, func(owner remote.ClientID) *store.Store, *scriptRunner) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "serve")
	now := time.Now()

	machineDB, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("open machine db: %v", err)
	}
	t.Cleanup(func() { _ = machineDB.Close() })
	secrets := serve.SecretStore{DB: machineDB}

	fp, err := serve.InitTLS(secrets, []string{"localhost", "127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	cert, err := serve.LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}

	candDir := t.TempDir()
	candPath := filepath.Join(candDir, "candidates.json")
	candJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatalf("write candidates.json: %v", err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}

	runner := &scriptRunner{alive: true}
	gitClient := git.NewClient("git", 10*time.Second, 0)

	cfg := serve.Config{
		Root:           root,
		DB:             machineDB,
		Candidates:     cSet,
		Policy:         pol,
		Runner:         runner,
		Git:            gitClient,
		Now:            time.Now,
		Interval:       time.Second,
		MaxBundleBytes: 64 << 20,
	}

	enroll := func(pub string) remote.ClientID {
		cls, err := serve.LoadClients(machineDB)
		if err != nil {
			t.Fatalf("LoadClients: %v", err)
		}
		cl, err := cls.Add("testclient", pub, time.Now())
		if err != nil {
			t.Fatalf("Add client: %v", err)
		}
		return cl.ID
	}

	srv, err := serve.New(cfg)
	if err != nil {
		t.Fatalf("serve.New: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, serve.ListenConfig{
			Addr: "127.0.0.1:0",
			TLS:  &cert,
		})
	}()

	var addr net.Addr
	start := time.Now()
	for time.Since(start) < 5*time.Second {
		if a := srv.Addr(); a != nil {
			addr = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if addr == nil {
		t.Fatal("timed out waiting for server address")
	}

	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	url := "https://" + addr.String()
	srvStore := func(owner remote.ClientID) *store.Store {
		dir, _ := owner.Dir()
		return store.NewShared(filepath.Join(root, "bindings", dir), string(owner), machineDB)
	}

	return srv, url, fp, enroll, srvStore, runner
}

func newClient(t *testing.T, url, fingerprint string) (relevo.Runtime, remote.Keypair) {
	t.Helper()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}

	servers := remote.Servers{
		"zen": remote.ServerEntry{
			URL:         url,
			Fingerprint: fingerprint,
		},
	}

	gitClient := git.NewClient("git", 10*time.Second, 0)
	stRoot := t.TempDir()
	st := store.New(stRoot)

	// The machine database: the mastermind records live in it (P3b round 2
	// §4.1), so it is opened before the record is created below.
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("open store db: %v", err)
	}

	// `relevo bind --server` resolves the caller's mastermind before it contacts
	// the server and records it on the client binding. Export one the way a
	// real mastermind session does, so the client runtime resolves a record
	// instead of failing the add with ErrNoMasterMindSession.
	reg := &mastermind.DBRegistry{KV: db.TxKV{DB: mdb}, Now: time.Now}
	prec, err := reg.Create(mastermind.Record{
		ID:          "pl_eeeeeeeeeeee",
		Name:        "e2e-mastermind",
		HarnessKind: "claude",
		SessionID:   "sess-e2e",
		CWD:         "/repo",
	})
	if err != nil {
		t.Fatalf("create mastermind record: %v", err)
	}
	t.Setenv("RELEVO_MASTERMIND", prec.Name)

	candDir := t.TempDir()
	candPath := filepath.Join(candDir, "candidates.json")
	candJSON := `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatalf("write candidates.json: %v", err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatalf("load candidates: %v", err)
	}

	rt := relevo.Runtime{
		Git:         gitClient,
		Store:       st,
		Candidates:  cSet,
		MasterMinds: reg,
		Gates:       mdb,
		Latency:     mdb,
		Now:         time.Now,
		Remote:      client.New(servers, kp, time.Now),
		Transport:   remote.NewBundleTransport(gitClient, t.TempDir()),
	}

	return rt, kp
}

func TestRemoteRoundEndToEnd(t *testing.T) {
	ctx := context.Background()

	// 1. server, client (enrolled), repo.
	srv, url, fp, enroll, srvStore, runner := newServer(t)
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	})
	if err != nil {
		t.Fatalf("step 1: relevo.Add: %v", err)
	}

	// Assert: local branch relevo/api exists at the repo's HEAD; server store for the owner has api.
	repoGit := git.NewClient("git", 10*time.Second, 0)
	exists, err := repoGit.BranchExists(ctx, repo, "relevo/api")
	if err != nil || !exists {
		t.Fatalf("step 1: branch relevo/api exists = %v, err = %v", exists, err)
	}
	headSHA, _, err := repoGit.RefSHA(ctx, repo, "HEAD")
	if err != nil {
		t.Fatalf("step 1: ref HEAD: %v", err)
	}
	branchSHA, _, err := repoGit.RefSHA(ctx, repo, "refs/heads/relevo/api")
	if err != nil {
		t.Fatalf("step 1: ref relevo/api: %v", err)
	}
	if branchSHA != headSHA {
		t.Fatalf("step 1: branch SHA = %s, want HEAD %s", branchSHA, headSHA)
	}

	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("step 1: server store Load(api): %v", err)
	}
	if sb.Name != "api" {
		t.Fatalf("step 1: server store binding name = %q, want api", sb.Name)
	}

	// 2. relevo.Send(ctx, rt, "api", planFile) with a one-line plan.
	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("step 2: write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("step 2: relevo.Send: %v", err)
	}

	// Assert: runner.specs has one entry whose Dir is the server worktree; the worktree has README.md;
	// client binding Builder.LastShipped == repo HEAD.
	runner.mu.Lock()
	specsLen := len(runner.specs)
	var spec spawn.ProcSpec
	if specsLen > 0 {
		spec = runner.specs[0]
	}
	runner.mu.Unlock()

	if specsLen != 1 {
		t.Fatalf("step 2: runner.specs count = %d, want 1", specsLen)
	}

	serverWorktree := sb.Worktree
	if serverWorktree == "" {
		serverWorktree = serverStore.WorktreePath("api")
	}
	if spec.Dir != serverWorktree {
		t.Fatalf("step 2: spec.Dir = %q, want server worktree %q", spec.Dir, serverWorktree)
	}
	if _, err := os.Stat(filepath.Join(serverWorktree, "README.md")); err != nil {
		t.Fatalf("step 2: server worktree missing README.md: %v", err)
	}

	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 2: read client binding: %v", err)
	}
	if cb.Builder.LastShipped != headSHA {
		t.Fatalf("step 2: Builder.LastShipped = %q, want repo HEAD %q", cb.Builder.LastShipped, headSHA)
	}

	// 3. finishRound(...); srv.Tick; then tickUntil the client daemon's Tick leaves a KindReport entry for round 1.
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relevo.Runtime{
		Store:  serverStore,
		Git:    gitClient,
		Runner: runner,
	}
	finishRound(t, serverRT, "api", 1, "round 1 done")

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("step 3: srv.Tick: %v", err)
	}

	clientDaemon := relevo.NewDaemon(rt, time.Second)
	tickUntil(t, 10*time.Second, func() bool {
		_ = clientDaemon.Tick(ctx)
		entries, err := rt.Store.ReadLog("api")
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Kind == store.KindReport && e.Round == 1 {
				return true
			}
		}
		return false
	})

	// Assert: the round's report waits as exactly one pending mastermind entry
	// whose payload contains Report: and Diff: 1 file and 1 commit on
	// relevo/api; the client repo's relevo/api is one commit ahead of init with
	// hello.txt; the server view (via rt.Remote.GetBinding) has acked_round
	// == 1 and round_state == idle; client Builder.RemoteStatus == "idle";
	// BuilderCandidate equals the server's candidate token.
	pending, pText := pendingReports(t, rt, "api")
	if pending != 1 {
		t.Fatalf("step 3: pending reports = %d, want 1", pending)
	}
	for _, substr := range []string{"Report:", "Diff: 1 file", "1 commit on relevo/api"} {
		if !strings.Contains(pText, substr) {
			t.Fatalf("step 3: pending report payload missing %q; got:\n%s", substr, pText)
		}
	}

	diffOut := runGit(t, repo, "diff", "HEAD..refs/heads/relevo/api", "--name-only")
	if !strings.Contains(diffOut, "hello.txt") {
		t.Fatalf("step 3: branch relevo/api does not have hello.txt; diff:\n%s", diffOut)
	}
	revCount := strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD..refs/heads/relevo/api"))
	if revCount != "1" {
		t.Fatalf("step 3: commits ahead = %s, want 1", revCount)
	}

	sv, err := rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 3: GetBinding: %v", err)
	}
	if sv.AckedRound != 1 {
		t.Fatalf("step 3: server AckedRound = %d, want 1", sv.AckedRound)
	}
	if sv.RoundState != "idle" {
		t.Fatalf("step 3: server RoundState = %q, want idle", sv.RoundState)
	}

	cb, err = rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 3: read client binding: %v", err)
	}
	if cb.Builder.RemoteStatus != "idle" {
		t.Fatalf("step 3: client Builder.RemoteStatus = %q, want idle", cb.Builder.RemoteStatus)
	}
	if cb.BuilderCandidate != "claude/anthropic/haiku" {
		t.Fatalf("step 3: client BuilderCandidate = %q, want claude/anthropic/haiku", cb.BuilderCandidate)
	}

	// 4. relevo.Done(ctx, rt, "api"). Assert: server view state done; client state done.
	if _, err := relevo.Done(ctx, rt, "api"); err != nil {
		t.Fatalf("step 4: relevo.Done: %v", err)
	}
	sv, err = rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 4: server view after Done: %v", err)
	}
	if sv.State != "done" {
		t.Fatalf("step 4: server view state = %q, want done", sv.State)
	}
	cb, err = rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 4: read client binding after Done: %v", err)
	}
	if cb.State != store.StateDone {
		t.Fatalf("step 4: client binding state = %q, want done", cb.State)
	}
}

func TestRemoteRoundCollectedAfterClientWasAway(t *testing.T) {
	ctx := context.Background()

	srv, url, fp, enroll, srvStore, runner := newServer(t)
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	})
	if err != nil {
		t.Fatalf("step 1: relevo.Add: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("step 2: write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("step 2: relevo.Send: %v", err)
	}

	serverStore := srvStore(owner)
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relevo.Runtime{
		Store:  serverStore,
		Git:    gitClient,
		Runner: runner,
	}
	finishRound(t, serverRT, "api", 1, "round 1 done")

	// several srv.Ticks with no client ticks (the laptop is asleep)
	for i := 0; i < 3; i++ {
		if err := srv.Tick(ctx); err != nil {
			t.Fatalf("step 3: srv.Tick %d: %v", i, err)
		}
	}

	// Assert the server view is closed with acked_round 0
	sv, err := rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 3: GetBinding: %v", err)
	}
	if sv.RoundState != "closed" {
		t.Fatalf("step 3: server RoundState = %q, want closed", sv.RoundState)
	}
	if sv.AckedRound != 0 {
		t.Fatalf("step 3: server AckedRound = %d, want 0", sv.AckedRound)
	}

	// Then client ticks:
	clientDaemon := relevo.NewDaemon(rt, time.Second)
	tickUntil(t, 10*time.Second, func() bool {
		_ = clientDaemon.Tick(ctx)
		entries, err := rt.Store.ReadLog("api")
		if err != nil {
			return false
		}
		for _, e := range entries {
			if e.Kind == store.KindReport && e.Round == 1 {
				return true
			}
		}
		return false
	})

	// exactly one pending report for the mastermind, acked_round == 1
	if pending, _ := pendingReports(t, rt, "api"); pending != 1 {
		t.Fatalf("step 3: pending reports = %d, want 1", pending)
	}

	sv, err = rt.Remote.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("step 3: GetBinding after client tick: %v", err)
	}
	if sv.AckedRound != 1 {
		t.Fatalf("step 3: server AckedRound = %d, want 1", sv.AckedRound)
	}

	// and a second batch of client ticks adds no second report entry and no
	// second pending delivery (idempotence)
	for i := 0; i < 3; i++ {
		_ = clientDaemon.Tick(ctx)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("step 4: ReadLog: %v", err)
	}
	reportEntries := 0
	for _, e := range entries {
		if e.Kind == store.KindReport && e.Round == 1 {
			reportEntries++
		}
	}
	if reportEntries != 1 {
		t.Fatalf("step 4: report entries count = %d, want 1", reportEntries)
	}

	if pending, _ := pendingReports(t, rt, "api"); pending != 1 {
		t.Fatalf("step 4: pending reports after second batch = %d, want 1", pending)
	}
}

func TestRemoteServerUnreachableIsNotAHalt(t *testing.T) {
	ctx := context.Background()

	srvCtx, srvCancel := context.WithCancel(context.Background())
	srv, url, fp, enroll, _, _ := newServerWithContext(t, srvCtx, srvCancel, policy.Policy{})
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	_ = enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	})
	if err != nil {
		t.Fatalf("step 1: relevo.Add: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("step 2: write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("step 2: relevo.Send: %v", err)
	}

	// cancel the server's context (listener down)
	srvCancel()
	tickUntil(t, 5*time.Second, func() bool {
		return srv.Addr() == nil
	})

	// client Tick
	clientDaemon := relevo.NewDaemon(rt, time.Second)
	if err := clientDaemon.Tick(ctx); err != nil {
		t.Fatalf("step 3: clientDaemon.Tick: %v", err)
	}

	// Assert: state stays active, Builder.RemoteStatus == "unreachable",
	// RemoteUnreachableSince set. Restart a server? Not needed: the assertion
	// is the non-halt.
	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 3: load client binding: %v", err)
	}
	if cb.State != store.StateActive {
		t.Fatalf("step 3: state = %q, want active", cb.State)
	}
	if cb.Builder.RemoteStatus != "unreachable" {
		t.Fatalf("step 3: Builder.RemoteStatus = %q, want unreachable", cb.Builder.RemoteStatus)
	}
	if cb.RemoteUnreachableSince.IsZero() {
		t.Fatalf("step 3: RemoteUnreachableSince is zero, want non-zero")
	}

}

func TestRemoteSyncOnReadWithoutDaemon(t *testing.T) {
	ctx := context.Background()

	srv, url, fp, enroll, srvStore, runner := newServer(t)
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	})
	if err != nil {
		t.Fatalf("step 1: relevo.Add: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("step 2: write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("step 2: relevo.Send: %v", err)
	}

	serverStore := srvStore(owner)
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relevo.Runtime{
		Store:  serverStore,
		Git:    gitClient,
		Runner: runner,
	}
	finishRound(t, serverRT, "api", 1, "round 1 done")

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("step 3: srv.Tick: %v", err)
	}

	// then relevo.SyncRemote(ctx, rt) (no daemon tick)
	if _, err := relevo.SyncRemote(ctx, rt); err != nil {
		t.Fatalf("step 3: SyncRemote: %v", err)
	}

	// Assert: report entry exists and is pending (nothing has delivered it), state is not orphaned;
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("step 3: ReadLog: %v", err)
	}
	foundPendingReport := false
	for _, e := range entries {
		if e.Kind == store.KindReport && e.Round == 1 {
			if !e.Confirmed {
				foundPendingReport = true
			}
		}
	}
	if !foundPendingReport {
		t.Fatalf("step 3: pending report entry not found in log: %+v", entries)
	}

	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("step 3: load client binding: %v", err)
	}
	if cb.State != store.StateActive {
		t.Fatalf("step 3: state = %q; a read-only sync must not change it", cb.State)
	}

	// then one daemon Tick leaves it pending: this mastermind has no channel
	// and no deliverer, so the entry waits for `relevo wait` (route=pull).
	clientDaemon := relevo.NewDaemon(rt, time.Second)
	if err := clientDaemon.Tick(ctx); err != nil {
		t.Fatalf("step 4: clientDaemon.Tick: %v", err)
	}

	if pending, _ := pendingReports(t, rt, "api"); pending != 1 {
		t.Fatalf("step 4: pending reports after daemon tick = %d, want 1", pending)
	}
}

// captureLogs swaps slog's default handler for one that writes to a buffer
// for the test's lifetime and returns the buffer. Restores the previous
// default in t.Cleanup. Not safe under t.Parallel (no e2e test uses it).
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestRemoteRoundTicksWithoutEscapeWarning checks that a running served round
// does not trigger "escape check skipped" / "must be run in a work tree" on
// server ticks (#192).
func TestRemoteRoundTicksWithoutEscapeWarning(t *testing.T) {
	logs := captureLogs(t)
	ctx := context.Background()

	// steps 1-2: server, client (enrolled), repo, Add, Send
	srv, url, fp, enroll, srvStore, _ := newServer(t)
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	})
	if err != nil {
		t.Fatalf("relevo.Add: %v", err)
	}

	planFile := t.TempDir() + "/plan.md"
	if err := os.WriteFile(planFile, []byte("# Plan\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send: %v", err)
	}

	// tick the server twice -- this is where the warning fired before the fix
	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("srv.Tick 1: %v", err)
	}
	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("srv.Tick 2: %v", err)
	}

	// assert no escape warning in the logs
	logStr := logs.String()
	if strings.Contains(logStr, "escape check skipped") {
		t.Errorf("unexpected 'escape check skipped' in logs:\n%s", logStr)
	}
	if strings.Contains(logStr, "must be run in a work tree") {
		t.Errorf("unexpected 'must be run in a work tree' in logs:\n%s", logStr)
	}

	// prove the round is otherwise healthy: server binding has Serve != nil and RoundBaselineTree != ""
	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("serverStore.Load(api): %v", err)
	}
	if sb.Serve == nil {
		t.Errorf("server binding Serve is nil, want non-nil")
	}
	if sb.RoundBaselineTree == "" {
		t.Errorf("server binding RoundBaselineTree is empty, want non-empty")
	}
}

// pendingReports counts a binding's unconfirmed mastermind-bound report entries
// -- the mailbox no route has taken yet (#303 §5.4) -- and returns the newest
// one's payload. The pane prompt these tests used to assert on is gone: a
// report now waits as a pending entry for the channel, a deliverer or
// `relevo wait`.
func pendingReports(t *testing.T, rt relevo.Runtime, name string) (int, string) {
	t.Helper()
	entries, err := rt.Store.ReadLog(name)
	if err != nil {
		t.Fatalf("ReadLog %s: %v", name, err)
	}
	n := 0
	payload := ""
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Kind == store.KindReport && !e.Confirmed {
			n++
			payload = e.Payload
		}
	}
	return n, payload
}
