package e2e

// TestForwardedRoundE2E proves a round forwards end to end: a head and a
// node run in process over httptest TLS with the fake harness behind both.
// The laptop sends two rounds to the head; the first fills the head's only
// local seat, so the second forwards to the node, runs there, closes there,
// and comes home. The laptop's branch holds the node's commit, show and the
// transcript render from the head's files, and the laptop row reads →node
// while the round is out and bare once it is home.
//
// Red with bursting off or the node down: the round then runs on the head,
// never names a node, and the row stays bare throughout.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// burstReportProvider is a burst.Provider that answers one fixed node row.
// It stands in for the provider command, which never runs in tests.
type burstReportProvider struct {
	mu   sync.Mutex
	resp burst.ReportResponse
}

func (f *burstReportProvider) Report(burst.ReportRequest) (burst.ReportResponse, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resp, nil, nil
}

// finishNodeRound ends the node's open round the way a builder finishing
// looks: a committed work file, the report and builder log a harness leaves,
// the done marker, and a dead runner behind them.
func finishNodeRound(t *testing.T, st *store.Store, runner *scriptRunner, name string, round int) string {
	t.Helper()
	b, err := st.Load(name)
	if err != nil {
		t.Fatalf("finish node round: load %s: %v", name, err)
	}
	worktree := b.Worktree
	if worktree == "" {
		worktree = st.WorktreePath(name)
	}
	const fileBody = "forwarded round work from the node\n"
	if err := os.WriteFile(filepath.Join(worktree, "node-result.txt"), []byte(fileBody), 0o644); err != nil {
		t.Fatalf("finish node round: write work file: %v", err)
	}
	runGit(t, worktree, "add", "node-result.txt")
	runGit(t, worktree, "commit", "-m", "node round result")
	sha := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD"))

	const reportText = "# Round 1 Report\n\nnode-report-E2E round 1 done\n\n```relevo\nstatus: done\nchanged_paths: [node-result.txt]\n```\n"
	if err := os.WriteFile(st.ReportPath(name, round), []byte(reportText), 0o644); err != nil {
		t.Fatalf("finish node round: write report: %v", err)
	}
	const logText = "node-builder-log-E2E line 1\n"
	if err := os.WriteFile(st.BuilderLogPath(name, round), []byte(logText), 0o644); err != nil {
		t.Fatalf("finish node round: write builder log: %v", err)
	}
	if err := os.WriteFile(st.DonePath(name, round), []byte(""), 0o644); err != nil {
		t.Fatalf("finish node round: write done marker: %v", err)
	}
	runner.mu.Lock()
	runner.alive = false
	runner.mu.Unlock()
	return sha
}

// headForwardPhase reads the head binding's forward phase: "" when the head
// holds no forward for it.
func headForwardPhase(t *testing.T, srvStore func(remote.ClientID) *store.Store, owner remote.ClientID, name string) string {
	t.Helper()
	b, err := srvStore(owner).Load(name)
	if err != nil {
		t.Fatalf("head load %s: %v", name, err)
	}
	if b.Burst == nil || b.Burst.Forward == nil {
		return ""
	}
	return b.Burst.Forward.Phase
}

// statusDisplays renders the laptop's status rows for the arrow assertions.
func statusDisplays(t *testing.T, ctx context.Context, rt relevo.Runtime) []view.StatusLineRow {
	t.Helper()
	rep, err := relevo.Status(ctx, rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return view.StatusLineRows(rep, time.Now())
}

// rowOf finds the named binding's rendered row.
func rowOf(t *testing.T, rows []view.StatusLineRow, name string) view.StatusLineRow {
	t.Helper()
	for _, r := range rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no status row for %s in %+v", name, rows)
	return view.StatusLineRow{}
}

func TestForwardedRoundE2E(t *testing.T) {
	ctx := context.Background()

	headKP, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	headPub := remote.MarshalPublic(headKP.Public, "head")
	headID := remote.IDOf(headKP.Public)
	headPEM, err := remote.MarshalPrivate(headKP)
	if err != nil {
		t.Fatalf("marshal head client key: %v", err)
	}

	// The node first: a plain server with the head enrolled, so the head's
	// later verify pass can mark it verified.
	nodeCtx, nodeCancel := context.WithCancel(context.Background())
	nodeSrv, nodeURL, nodeFP, nodeEnroll, nodeStoreFor, nodeRunner := newServerWithContext(t, nodeCtx, nodeCancel, policy.Policy{})
	nodeEnroll(headPub)

	provider := &burstReportProvider{resp: burst.ReportResponse{
		Version: burst.ReportVersion,
		Nodes: []burst.ReportNodeOut{{
			ID: "b1", Name: "b1 cx43@hel1", URL: nodeURL,
			Fingerprint: nodeFP, Cap: 2, State: "ready",
		}},
		ReportEvery: "30s",
	}}
	headCtx, headCancel := context.WithCancel(context.Background())
	headSrv, headURL, headFP, headEnroll, headStoreFor, headRunner := newServerWithContext(t, headCtx, headCancel, policy.Policy{},
		func(cfg *serve.Config) {
			cfg.Burst = &policy.BurstPolicy{Command: []string{"burst", "report"}}
			cfg.ClientKey = headPEM
			cfg.Provider = provider
			cfg.MaxBuilders = 1
		})

	rt, kp := newClient(t, headURL, headFP)
	owner := headEnroll(remote.MarshalPublic(kp.Public, "test client"))
	repo := newRepo(t)

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{Name: "zz", Server: "zen", Repo: repo}); err != nil {
		t.Fatalf("relevo.Add(zz): %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "zz", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send(zz): %v", err)
	}
	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{Name: "api", Server: "zen", Repo: repo}); err != nil {
		t.Fatalf("relevo.Add(api): %v", err)
	}

	// The filler takes the head's only local seat; api stays queued behind
	// it, which is what the next ticks can place onto the node.
	tickUntil(t, 30*time.Second, func() bool {
		_ = headSrv.Tick(ctx)
		headRunner.mu.Lock()
		defer headRunner.mu.Unlock()
		return len(headRunner.specs) == 1
	})
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send(api): %v", err)
	}

	clientDaemon := relevo.NewDaemon(rt, time.Second)
	tickUntil(t, 90*time.Second, func() bool {
		_ = headSrv.Tick(ctx)
		_ = nodeSrv.Tick(ctx)
		_ = clientDaemon.Tick(ctx)
		return headForwardPhase(t, headStoreFor, owner, "api") == "running"
	})

	// The laptop row names the node carrying the round.
	row, err := relevo.StatusRow(ctx, rt, "api")
	if err != nil {
		t.Fatalf("StatusRow(api): %v", err)
	}
	if row.Node != "b1" {
		t.Errorf("laptop row node = %q, want b1: the row must name the node while the round is out", row.Node)
	}
	rendered := rowOf(t, statusDisplays(t, ctx, rt), "api")
	t.Logf("laptop row during: node=%q on=%q", row.Node, rendered.On)
	if !strings.Contains(rendered.On, "→b1") {
		t.Errorf("laptop row on = %q, want it to read →b1 during the round", rendered.On)
	}

	// The round runs on the node under the head-side node binding; finish it
	// the way its builder would and tick the node to close it.
	nodeHeadStore := nodeStoreFor(headID)
	listed, err := nodeHeadStore.List()
	if err != nil {
		t.Fatalf("node list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("node bindings = %d, want the one forwarded round", len(listed))
	}
	nodeBinding := listed[0].Name
	nodeCommit := finishNodeRound(t, nodeHeadStore, nodeRunner, nodeBinding, 1)
	t.Logf("node closed round 1 at %s", nodeCommit)
	tickUntil(t, 30*time.Second, func() bool {
		_ = nodeSrv.Tick(ctx)
		closed, err := nodeHeadStore.Load(nodeBinding)
		if err != nil {
			return false
		}
		return closed.Serve != nil && closed.Serve.ClosedRound == 1
	})

	// The head observes the close and the report comes home to the laptop.
	tickUntil(t, 90*time.Second, func() bool {
		_ = headSrv.Tick(ctx)
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

	// The laptop's branch holds the node's own commit.
	branchSHA := strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/heads/relevo/api"))
	t.Logf("client relevo/api = %s", branchSHA)
	if branchSHA != nodeCommit {
		t.Errorf("client relevo/api = %s, want the node commit %s", branchSHA, nodeCommit)
	}
	contents := runGit(t, repo, "show", branchSHA+":node-result.txt")
	if contents != "forwarded round work from the node\n" {
		t.Errorf("client node-result.txt = %q, want the bytes the node committed", contents)
	}

	// Show and the transcript render from the head's files: the client only
	// configures the head, so a successful render proves no node fallback.
	show, err := relevo.Show(ctx, rt, relevo.ShowOptions{Name: "api", Section: relevo.ShowReport, Peek: true})
	if err != nil {
		t.Fatalf("Show report: %v", err)
	}
	if show.Missing || !strings.Contains(show.Text, "node-report-E2E round 1 done") {
		t.Errorf("show report missing=%v text=%q, want the node's report home through the head", show.Missing, show.Text)
	}
	// Round files land sealed, so the transcript reads through the store,
	// the same read its own drain uses, not through the live directory.
	readFile := func(path string) ([]byte, bool, error) {
		data, err := rt.Store.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return data, true, nil
	}
	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("read client binding: %v", err)
	}
	text, source, found, err := relevo.RoundTranscript(rt.Store, "api", 1, cb.Builder, readFile)
	if err != nil || !found {
		t.Fatalf("RoundTranscript: text=%q source=%q found=%v err=%v", text, source, found, err)
	}
	t.Logf("show bytes=%d transcript bytes=%d from %s", len(show.Text), len(text), source)
	if !strings.Contains(string(text), "node-builder-log-E2E line 1") {
		t.Errorf("transcript = %q, want the node's builder log home through the head", text)
	}

	// The round is home, so the row reads bare again.
	tickUntil(t, 30*time.Second, func() bool {
		_ = clientDaemon.Tick(ctx)
		r, err := relevo.StatusRow(ctx, rt, "api")
		return err == nil && r.Node == ""
	})
	row, err = relevo.StatusRow(ctx, rt, "api")
	if err != nil {
		t.Fatalf("StatusRow(api) after close: %v", err)
	}
	if row.Node != "" {
		t.Errorf("laptop row node = %q after the close, want bare", row.Node)
	}
	rendered = rowOf(t, statusDisplays(t, ctx, rt), "api")
	t.Logf("laptop row after: node=%q on=%q", row.Node, rendered.On)
	if strings.Contains(rendered.On, "→") {
		t.Errorf("laptop row on = %q after the close, want no node arrow", rendered.On)
	}
}
