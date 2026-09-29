package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func TestWhoAmIPinned(t *testing.T) {
	cl, fix := startTestServer(t)

	who, err := cl.WhoAmI(context.Background(), "zen")
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if who.ID != remote.IDOf(fix.kp.Public) {
		t.Fatalf("whoami id = %q, want %q", who.ID, remote.IDOf(fix.kp.Public))
	}
	if who.Label != "alice" {
		t.Fatalf("whoami label = %q, want 'alice'", who.Label)
	}
}

func TestServerErrors(t *testing.T) {
	_, fix := startTestServer(t)
	unenrolled := generateKey(t)

	tests := []struct {
		name       string
		client     *client.Client
		server     string
		wantErr    error
		wantStatus int
		wantCode   remote.Code
	}{
		{name: "unknown server", client: fix.cl, server: "unknown-server", wantErr: client.ErrUnknownServer},
		{name: "pinned fingerprint mismatch", client: client.New(pinnedServers(fix.ts.URL, "sha256:"+strings.Repeat("0", 64)), fix.kp, time.Now), server: "zen", wantErr: client.ErrCertChanged},
		{name: "closed port", client: closedPortClient(t, fix.kp), server: "closed", wantErr: client.ErrUnreachable},
		{name: "not enrolled", client: client.New(pinnedServers(fix.ts.URL, fix.fingerprint), unenrolled, time.Now), server: "zen", wantStatus: 401, wantCode: remote.CodeNotEnrolled},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.client.WhoAmI(context.Background(), tc.server)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("WhoAmI got %v, want %v", err, tc.wantErr)
				}
				return
			}
			var httpErr *client.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("got error %T (%v), want *HTTPError", err, err)
			}
			if httpErr.Status != tc.wantStatus {
				t.Fatalf("http status = %d, want %d", httpErr.Status, tc.wantStatus)
			}
			if httpErr.Body.Code != tc.wantCode {
				t.Fatalf("error code = %q, want %q", httpErr.Body.Code, tc.wantCode)
			}
		})
	}
}

func TestCreateStartFilesBundleAck(t *testing.T) {
	cl, fix := startTestServer(t)
	ctx := context.Background()
	clientDir, headSHA, repoID := initClientRepo(t, fix.gitClient)

	view, err := cl.CreateBinding(ctx, "zen", remote.CreateBindingRequest{Name: "api", RepoID: repoID, BaseCommit: headSHA, Role: "builder"})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if view.Name != "api" {
		t.Fatalf("binding name = %q, want api", view.Name)
	}

	startView, err := startRemoteRound(t, ctx, cl, fix, clientDir, headSHA)
	if err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if startView.RoundState != remote.RoundRunning {
		t.Fatalf("startView RoundState = %q, want %q", startView.RoundState, remote.RoundRunning)
	}

	st, b := serverBinding(t, fix, "api")
	commitServerResult(t, b.Worktree)
	finishRound(t, fix, st, ctx, "api", 1)

	closedView, err := cl.GetBinding(ctx, "zen", "api")
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if closedView.RoundState != remote.RoundClosed || closedView.ClosedRound != 1 {
		t.Fatalf("closedView = %+v, want state closed at round 1", closedView)
	}

	report, err := readRoundFile(t, cl, ctx, "report")
	if err != nil {
		t.Fatalf("RoundFile: %v", err)
	}
	if !strings.Contains(string(report), "status: done") {
		t.Fatalf("reportData = %q, want status: done", string(report))
	}

	if err := absorbRoundBundle(t, ctx, cl, fix, clientDir, headSHA); err != nil {
		t.Fatalf("RoundBundle: %v", err)
	}

	ackView, err := cl.Ack(ctx, "zen", "api", 1)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if ackView.AckedRound != 1 {
		t.Fatalf("ackView.AckedRound = %d, want 1", ackView.AckedRound)
	}
}

// TestReaderArtifactsRoundTrip pins the reader transport: the listing decodes,
// a nested rel downloads byte-for-byte, and an unlisted rel surfaces the
// server's 404 as *client.HTTPError.
func TestReaderArtifactsRoundTrip(t *testing.T) {
	cl, fix := startTestServer(t)
	ctx := context.Background()
	_, headSHA, repoID := initClientRepo(t, fix.gitClient)

	view, err := cl.CreateBinding(ctx, "zen", remote.CreateBindingRequest{Name: "review", RepoID: repoID, BaseCommit: headSHA, Role: "reviewer"})
	if err != nil {
		t.Fatalf("CreateBinding: %v", err)
	}
	if view.Shape != store.ShapeReader {
		t.Fatalf("Shape = %q, want %q", view.Shape, store.ShapeReader)
	}

	st, b := serverBinding(t, fix, "review")
	dir := st.ArtifactDir("review", 1, "reviewer")
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site", "index.html"), []byte("<html>hi</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.OutputPath("review", 1, "reviewer", "findings"), []byte("the findings\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b.Serve.ClosedRound = 1
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	list, err := cl.RoundArtifacts(ctx, "zen", "review", 1)
	if err != nil {
		t.Fatalf("RoundArtifacts: %v", err)
	}
	if list.Actor != "reviewer" || list.Output != "findings.md" {
		t.Fatalf("list = %+v, want actor reviewer and output findings.md", list)
	}
	var rels []string
	for _, f := range list.Files {
		rels = append(rels, f.Rel)
	}
	if !slices.Equal(rels, []string{"findings.md", "site/index.html"}) {
		t.Fatalf("rels = %v, want the output first then the nested rel", rels)
	}

	rc, err := cl.RoundArtifact(ctx, "zen", "review", 1, "site/index.html")
	if err != nil {
		t.Fatalf("RoundArtifact: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if string(got) != "<html>hi</html>\n" {
		t.Fatalf("nested artifact = %q", got)
	}

	_, err = cl.RoundArtifact(ctx, "zen", "review", 1, "nope.txt")
	var httpErr *client.HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("unlisted rel err = %v, want *HTTPError 404", err)
	}
}

func startRemoteRound(t *testing.T, ctx context.Context, cl *client.Client, fix *testServerFixture, clientDir, headSHA string) (remote.BindingView, error) {
	t.Helper()
	runGit(t, clientDir, "branch", "relevo/api", headSHA)
	if err := fix.gitClient.UpdateRef(ctx, clientDir, "refs/relevo/api/out", headSHA, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	snap, err := fix.transport.Snapshot(ctx, clientDir, []string{"refs/relevo/api/out"}, "")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer func() { _ = snap.Body.Close() }()
	return cl.StartRound(ctx, "zen", "api", 1, []byte("# Round 1 Plan\nImplement feature"), snap.Body, "", "", nil, false)
}

func commitServerResult(t *testing.T, worktree string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, worktree, "add", "result.txt")
	runGit(t, worktree, "commit", "-m", "round 1 result")
}

func finishRound(t *testing.T, fix *testServerFixture, st *store.Store, ctx context.Context, name string, round int) {
	t.Helper()
	reportText := "Finished round\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(st.ReportPath(name, round), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.DonePath(name, round), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	fix.runner.setAlive(false)
	if err := fix.srv.Tick(ctx); err != nil {
		t.Fatalf("srv.Tick: %v", err)
	}
}

func readRoundFile(t *testing.T, cl *client.Client, ctx context.Context, kind string) ([]byte, error) {
	t.Helper()
	rc, err := cl.RoundFile(ctx, "zen", "api", 1, kind)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func absorbRoundBundle(t *testing.T, ctx context.Context, cl *client.Client, fix *testServerFixture, clientDir, since string) error {
	t.Helper()
	rc, err := cl.RoundBundle(ctx, "zen", "api", 1, since)
	if err != nil {
		return err
	}
	if rc == nil {
		t.Fatal("RoundBundle returned nil, want a bundle stream")
	}
	defer func() { _ = rc.Close() }()
	absorbed, err := fix.transport.Absorb(ctx, clientDir, remote.ContentTypeGitBundle, rc, []string{"refs/heads/relevo/api"})
	if err != nil {
		return err
	}
	if len(absorbed) == 0 {
		t.Fatal("no refs absorbed")
	}
	return nil
}

func TestAvailableRoundTrip(t *testing.T) {
	var gotPath, gotBody, gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(remote.AvailableResponse{Provider: "anthropic", Removed: 2})
	}))
	defer ts.Close()

	cl := client.New(
		remote.Servers{"zen": remote.ServerEntry{URL: ts.URL, Insecure: true}},
		generateKey(t),
		time.Now,
	)

	resp, err := cl.Available(context.Background(), "zen", "claude/anthropic/haiku")
	if err != nil {
		t.Fatalf("Available: %v", err)
	}
	if resp.Provider != "anthropic" || resp.Removed != 2 {
		t.Fatalf("Available = %+v, want provider anthropic, removed 2", resp)
	}
	if gotPath != "/v1/available" {
		t.Errorf("path = %q, want /v1/available", gotPath)
	}
	if !strings.Contains(gotBody, `"subject":"claude/anthropic/haiku"`) {
		t.Errorf("body = %q, want it naming the subject", gotBody)
	}
	if gotContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", gotContentType)
	}
}

// TestStartRoundSendsTagsField pins the tags field's wire shape.
func TestStartRoundSendsTagsField(t *testing.T) {
	ctx := context.Background()

	var mu sync.Mutex
	var gotTags string
	var hadTags bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("server ParseMultipartForm: %v", err)
		}
		mu.Lock()
		gotTags = r.FormValue("tags")
		_, hadTags = r.MultipartForm.Value["tags"]
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"api","round_state":"running"}`))
	}))
	defer ts.Close()

	cl := client.New(
		remote.Servers{"zen": remote.ServerEntry{URL: ts.URL, Insecure: true}},
		generateKey(t),
		time.Now,
	)

	tags := []remote.TagRef{
		{Name: "v0", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "v1", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if _, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Plan"), nil, "", "", tags, false); err != nil {
		t.Fatalf("StartRound with tags: %v", err)
	}
	want, err := json.Marshal(tags)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got, had := gotTags, hadTags
	mu.Unlock()
	if !had {
		t.Fatal("no tags field on the request when tags were given")
	}
	if got != string(want) {
		t.Fatalf("tags field = %q, want %q", got, string(want))
	}

	if _, err := cl.StartRound(ctx, "zen", "api", 1, []byte("# Plan"), nil, "", "", nil, false); err != nil {
		t.Fatalf("StartRound without tags: %v", err)
	}
	mu.Lock()
	_, had = gotTags, hadTags
	mu.Unlock()
	if had {
		t.Fatal("tags field sent when tags were nil")
	}
}
