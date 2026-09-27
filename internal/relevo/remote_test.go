package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/planner"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// addRemotePlanner is the planner registry the hand-built Runtimes in this
// file need: `add --server` resolves the caller's planner before it contacts
// the server (the fix that gives a remote binding its PlannerID), so an Add
// with Server set and no registry is the hard ErrNoPlannerSession. It holds
// the same record newRuntime seeds, on its own temp dir, and exports it as
// $RELEVO_PLANNER the way a real planner session does, so the Runtime below
// resolves it without a --planner flag.
func addRemotePlanner(t *testing.T) *planner.DBRegistry {
	t.Helper()
	t.Setenv("RELEVO_PLANNER", testPlannerName)
	reg, _ := testPlannerRegistry(t, planner.Record{
		ID:          testPlannerID,
		Name:        testPlannerName,
		HarnessKind: "claude",
		SessionID:   "sess-architect",
		CWD:         "/repo",
	})
	return reg
}

type fakeRemote struct {
	calls []string

	whoAmIResp          remote.WhoAmI
	whoAmIErr           error
	candidatesResp      remote.CandidatesResponse
	candidatesErr       error
	createBindingResp   remote.BindingView
	createBindingErr    error
	createBindingReq    remote.CreateBindingRequest
	getBindingResp      remote.BindingView
	getBindingErr       error
	startRoundResp      remote.BindingView
	startRoundErr       error
	startRoundTier      string
	startRoundCandidate string
	startRoundTags      []remote.TagRef
	startRoundRetry     bool
	roundFileResp       io.ReadCloser
	roundFileErr        error
	roundFileFromResp   io.ReadCloser
	roundFileFromRange  remote.FileRange
	roundFileFromErr    error
	roundFileFromFunc   func(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error)
	roundFileFromCalls  []int64
	roundBundleResp     io.ReadCloser
	roundBundleErr      error
	roundFileFunc       func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	roundBundleFunc     func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
	ackResp             remote.BindingView
	ackErr              error
	unavailableErr      error
	availableResp       remote.AvailableResponse
	availableErr        error
	doneErr             error
	unbindErr           error
	resumeResp          remote.BindingView
	resumeErr           error
	stopResp            remote.BindingView
	stopErr             error

	beforeCall func(call string)

	onCreateBinding func()
	onUnbind        func()
}

func (f *fakeRemote) WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error) {
	f.calls = append(f.calls, "WhoAmI:"+server)
	return f.whoAmIResp, f.whoAmIErr
}

func (f *fakeRemote) Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error) {
	f.calls = append(f.calls, "Candidates:"+server)
	return f.candidatesResp, f.candidatesErr
}

func (f *fakeRemote) CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error) {
	f.calls = append(f.calls, "CreateBinding:"+server+":"+req.Name)
	f.createBindingReq = req
	if f.onCreateBinding != nil {
		f.onCreateBinding()
	}
	return f.createBindingResp, f.createBindingErr
}

func (f *fakeRemote) GetBinding(ctx context.Context, server, name string) (remote.BindingView, error) {
	call := "GetBinding:" + server + ":" + name
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	return f.getBindingResp, f.getBindingErr
}

func (f *fakeRemote) StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, tags []remote.TagRef, retryOnUnreachable bool) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("StartRound:%s:%s:%d", server, name, round))
	f.startRoundTier = tier
	f.startRoundCandidate = candidate
	f.startRoundTags = tags
	f.startRoundRetry = retryOnUnreachable
	return f.startRoundResp, f.startRoundErr
}

func (f *fakeRemote) RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
	call := fmt.Sprintf("RoundFile:%s:%s:%d:%s", server, name, round, kind)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	if f.roundFileFunc != nil {
		return f.roundFileFunc(ctx, server, name, round, kind)
	}
	return f.roundFileResp, f.roundFileErr
}

func (f *fakeRemote) RoundFileFrom(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error) {
	call := fmt.Sprintf("RoundFileFrom:%s:%s:%d:%s:%d", server, name, round, kind, from)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	f.roundFileFromCalls = append(f.roundFileFromCalls, from)
	if f.roundFileFromFunc != nil {
		return f.roundFileFromFunc(ctx, server, name, round, kind, from)
	}
	if f.roundFileFromResp != nil || f.roundFileFromErr != nil {
		return f.roundFileFromResp, f.roundFileFromRange, f.roundFileFromErr
	}
	return f.roundFileResp, remote.FileRange{}, f.roundFileErr
}

func (f *fakeRemote) RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
	call := fmt.Sprintf("RoundBundle:%s:%s:%d:%s", server, name, round, since)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	if f.roundBundleFunc != nil {
		return f.roundBundleFunc(ctx, server, name, round, since)
	}
	return f.roundBundleResp, f.roundBundleErr
}

func (f *fakeRemote) Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error) {
	call := fmt.Sprintf("Ack:%s:%s:%d", server, name, round)
	if f.beforeCall != nil {
		f.beforeCall(call)
	}
	f.calls = append(f.calls, call)
	return f.ackResp, f.ackErr
}

func (f *fakeRemote) Unavailable(ctx context.Context, server, name, token, reason string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unavailable:%s:%s:%s", server, name, token))
	return f.unavailableErr
}

func (f *fakeRemote) Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error) {
	f.calls = append(f.calls, fmt.Sprintf("Available:%s:%s", server, subject))
	return f.availableResp, f.availableErr
}

func (f *fakeRemote) Done(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Done:%s:%s", server, name))
	return f.doneErr
}

func (f *fakeRemote) Unbind(ctx context.Context, server, name string) error {
	f.calls = append(f.calls, fmt.Sprintf("Unbind:%s:%s", server, name))
	if f.onUnbind != nil {
		f.onUnbind()
	}
	return f.unbindErr
}

func (f *fakeRemote) Resume(ctx context.Context, server, name string) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("Resume:%s:%s", server, name))
	return f.resumeResp, f.resumeErr
}

func (f *fakeRemote) Stop(ctx context.Context, server, name string) (remote.BindingView, error) {
	f.calls = append(f.calls, fmt.Sprintf("Stop:%s:%s", server, name))
	return f.stopResp, f.stopErr
}

type fakeTransport struct {
	snapshotCalls []snapshotCall
	snapshotResp  remote.Snapshot
	snapshotErr   error
	absorbCalls   []absorbCall
	absorbResp    map[string]string
	absorbErr     error

	beforeAbsorb func()
}

type snapshotCall struct {
	Repo  string
	Refs  []string
	Since string
}

type absorbCall struct {
	Repo        string
	ContentType string
	Refs        []string
}

func (f *fakeTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (remote.Snapshot, error) {
	f.snapshotCalls = append(f.snapshotCalls, snapshotCall{Repo: repo, Refs: refs, Since: since})
	if f.snapshotErr != nil {
		return remote.Snapshot{}, f.snapshotErr
	}
	return f.snapshotResp, nil
}

func (f *fakeTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	if f.beforeAbsorb != nil {
		f.beforeAbsorb()
	}
	f.absorbCalls = append(f.absorbCalls, absorbCall{Repo: repo, ContentType: contentType, Refs: refs})
	if f.absorbErr != nil {
		return nil, f.absorbErr
	}
	return f.absorbResp, nil
}

// recordingTransport drives a real transport -- bundles and all -- and keeps
// what the last Absorb did, so a test can show why an absorb was rejected
// (#274) instead of inferring it from the refs that did not move.
type recordingTransport struct {
	inner remote.TreeTransport
	moved map[string]string
	err   error
}

func (r *recordingTransport) Snapshot(ctx context.Context, repo string, refs []string, since string) (remote.Snapshot, error) {
	return r.inner.Snapshot(ctx, repo, refs, since)
}

func (r *recordingTransport) Absorb(ctx context.Context, repo, contentType string, body io.Reader, refs []string) (map[string]string, error) {
	moved, err := r.inner.Absorb(ctx, repo, contentType, body, refs)
	r.moved, r.err = moved, err
	return moved, err
}

// TestProbeServersStates checks ProbeServers' one-state-per-outcome mapping
// over fakeRemote (#100 step 5): no client key, enrolled, not enrolled, a
// changed certificate, unreachable, and an unrecognised error.
func TestProbeServersStates(t *testing.T) {
	t.Parallel()

	servers := map[string]remote.ServerEntry{"zen": {URL: "https://zen:7777"}}

	cases := []struct {
		name       string
		rt         Runtime
		enrollLine string
		wantState  string
		wantLabel  string
		wantDetail string
	}{
		{
			name:      "no key",
			rt:        Runtime{},
			wantState: "no key", wantDetail: "run relevo config server key",
		},
		{
			name:      "enrolled",
			rt:        Runtime{Remote: &fakeRemote{whoAmIResp: remote.WhoAmI{Label: "laptop"}}},
			wantState: "enrolled", wantLabel: "laptop",
		},
		{
			name:       "not enrolled",
			rt:         Runtime{Remote: &fakeRemote{whoAmIErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: "not_enrolled"}}}},
			enrollLine: "ed25519 AAAA... me@laptop",
			wantState:  "not enrolled", wantDetail: "ed25519 AAAA... me@laptop",
		},
		{
			name:      "cert changed",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: client.ErrCertChanged}},
			wantState: "cert changed",
		},
		{
			name:      "unreachable",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}},
			wantState: "unreachable", wantDetail: "dial tcp: connection refused",
		},
		{
			name:      "error",
			rt:        Runtime{Remote: &fakeRemote{whoAmIErr: errors.New("boom")}},
			wantState: "error", wantDetail: "boom",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := ProbeServers(context.Background(), tc.rt, servers, tc.enrollLine)
			if len(probes) != 1 {
				t.Fatalf("got %d probes, want 1", len(probes))
			}
			p := probes[0]
			if p.Name != "zen" || p.URL != "https://zen:7777" {
				t.Fatalf("Name/URL = %q/%q, want zen/https://zen:7777", p.Name, p.URL)
			}
			if p.State != tc.wantState {
				t.Fatalf("State = %q, want %q", p.State, tc.wantState)
			}
			if p.Label != tc.wantLabel {
				t.Fatalf("Label = %q, want %q", p.Label, tc.wantLabel)
			}
			if p.Detail != tc.wantDetail {
				t.Fatalf("Detail = %q, want %q", p.Detail, tc.wantDetail)
			}
		})
	}
}

// TestProbeServersFillsTierFields pins that ProbeServers copies
// WhoAmI.Features/BuilderTier/MaxTier into the enrolled probe, and leaves
// them zero for a pre-tier (non-aware) server (#141 remote half).
func TestProbeServersFillsTierFields(t *testing.T) {
	t.Parallel()

	servers := map[string]remote.ServerEntry{
		"zen":   {URL: "https://zen:7777"},
		"other": {URL: "https://other:7777"},
	}
	rt := Runtime{Remote: &fakeRemote{
		whoAmIResp: remote.WhoAmI{
			Label:       "laptop",
			Features:    []string{remote.FeatureTier},
			BuilderTier: "harness",
			MaxTier:     "edit",
		},
	}}

	probes := ProbeServers(context.Background(), rt, servers, "")
	if len(probes) != 2 {
		t.Fatalf("got %d probes, want 2", len(probes))
	}
	for _, p := range probes {
		if !p.TierAware {
			t.Fatalf("probe %s: TierAware = false, want true", p.Name)
		}
		if p.BuilderTier != "harness" {
			t.Fatalf("probe %s: BuilderTier = %q, want harness", p.Name, p.BuilderTier)
		}
		if p.MaxTier != "edit" {
			t.Fatalf("probe %s: MaxTier = %q, want edit", p.Name, p.MaxTier)
		}
	}
}

// TestProbeServersPreTierLeavesTierFieldsZero pins the pre-tier (v1) server
// case: WhoAmI carries no Features, so the probe is not TierAware and its
// tier fields stay empty (#141 remote half).
func TestProbeServersPreTierLeavesTierFieldsZero(t *testing.T) {
	t.Parallel()

	servers := map[string]remote.ServerEntry{"zen": {URL: "https://zen:7777"}}
	rt := Runtime{Remote: &fakeRemote{
		whoAmIResp: remote.WhoAmI{Label: "laptop"}, // no Features: pre-tier server
	}}

	probes := ProbeServers(context.Background(), rt, servers, "")
	if len(probes) != 1 {
		t.Fatalf("got %d probes, want 1", len(probes))
	}
	p := probes[0]
	if p.TierAware {
		t.Fatalf("TierAware = true, want false")
	}
	if p.BuilderTier != "" || p.MaxTier != "" {
		t.Fatalf("BuilderTier/MaxTier = %q/%q, want empty", p.BuilderTier, p.MaxTier)
	}
}

// TestServerTierWarning pins the one condition that warns: an enrolled,
// tier-aware server whose builder tier is harness. Every other combination
// -- not aware, or aware at a tier above harness -- is silent (#141 remote
// half).
func TestServerTierWarning(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		p    ServerProbe
		want bool
	}{
		{"tier aware at harness", ServerProbe{TierAware: true, BuilderTier: "harness"}, true},
		{"tier aware at edit", ServerProbe{TierAware: true, BuilderTier: "edit"}, false},
		{"not tier aware", ServerProbe{TierAware: false, BuilderTier: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ServerTierWarning(tc.p) != ""
			if got != tc.want {
				t.Fatalf("ServerTierWarning(%+v) non-empty = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

// TestRenderServersShowsTierAndWarning pins the two lines RenderServers adds
// per probe: the builder-tier annotation on the row, and a "!!" warning line
// underneath when ServerTierWarning fires (#141 remote half).
func TestRenderServersShowsTierAndWarning(t *testing.T) {
	t.Parallel()

	probes := []ServerProbe{
		{Name: "zen", URL: "https://zen:7777", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "harness", MaxTier: "edit"},
		{Name: "old", URL: "https://old:7777", State: "enrolled", Label: "laptop", TierAware: false},
	}
	out := RenderServers(probes)
	if !strings.Contains(out, "default tier: harness (max edit)") {
		t.Fatalf("output missing builder tier line; got:\n%s", out)
	}
	if !strings.Contains(out, "!! headless builders at tier harness") {
		t.Fatalf("output missing !! warning line; got:\n%s", out)
	}
	if !strings.Contains(out, "builder tier: unknown (pre-tier server)") {
		t.Fatalf("output missing unknown pre-tier line; got:\n%s", out)
	}
}

// TestRenderServersBuilders pins #285's queue line and #295's quota:
// an enrolled, queue-aware server's row gains "builders %d/%d, %d queued,
// scopes %s", with the scopes word covering off/on/on (<slice>)/on (<quota>)/
// on (<slice>, <quota>); an enrolled server that is not queue-aware (a
// pre-queue server) gets no builders text.
func TestRenderServersBuilders(t *testing.T) {
	t.Parallel()

	probes := []ServerProbe{
		{
			Name: "zen", URL: "https://zen:7777", State: "enrolled", Label: "laptop",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: false},
		},
		{
			Name: "contabo", URL: "https://contabo:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: true, Slice: "relevo.slice", Quota: "200%"},
		},
		{
			Name: "quotaonly", URL: "https://quota:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Quota: "150%"},
		},
		{
			Name: "sliceonly", URL: "https://slice:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Slice: "relevo.slice"},
		},
		{
			Name: "plain", URL: "https://plain:7777", State: "enrolled", Label: "vps",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true},
		},
		{
			Name: "old", URL: "https://old:7777", State: "enrolled", Label: "laptop",
		},
	}
	out := RenderServers(probes)
	for _, want := range []string{
		"runners 2/3, 1 queued, scopes off",
		"runners 2/3, 1 queued, scopes on (relevo.slice, 200%)",
		"runners 1/3, 0 queued, scopes on (150%)",
		"runners 1/3, 0 queued, scopes on (relevo.slice)",
		"runners 1/3, 0 queued, scopes on\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q; got:\n%s", want, out)
		}
	}
	oldLine := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "https://old:7777") {
			oldLine = line
			break
		}
	}
	if oldLine == "" {
		t.Fatalf("output missing the old server's row; got:\n%s", out)
	}
	if strings.Contains(oldLine, "runners ") {
		t.Errorf("non-queue-aware server row must not carry builders text; got:\n%s", oldLine)
	}
}

func TestAddRemoteCreatesBranchAfterServerAgrees(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}

	var events []string
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
		onCreateBinding: func() {
			events = append(events, "CreateBinding")
		},
	}

	origCreateBranch := fg.CreateBranch
	_ = origCreateBranch
	// We wrap CreateBranch call via the custom tracker
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	res, err := Add(ctx, rt, opts)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if len(events) != 1 || events[0] != "CreateBinding" {
		t.Fatalf("events = %v, want [CreateBinding]", events)
	}
	if len(fg.createBranchCalls) != 1 {
		t.Fatalf("createBranchCalls = %d, want 1", len(fg.createBranchCalls))
	}
	if res.Branch != "relevo/api" {
		t.Fatalf("res.Branch = %q, want relevo/api", res.Branch)
	}

	// #100 step 6: the server picked the candidate (opts.Candidate == ""),
	// so the pick entry must say so rather than ExplainResolution's
	// "explicit, policy bypassed", which would misdescribe a token nobody
	// on this side named.
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var pickNote string
	for _, e := range entries {
		if e.Kind == store.KindPick {
			pickNote = e.Note
		}
	}
	if pickNote != "picked claude/anthropic/haiku on zen: server's pick" {
		t.Fatalf("pick note = %q, want it to name the server's own pick", pickNote)
	}
}

// TestAddRemoteRecordsPlanner pins the fix: `add --server` resolves the
// caller's planner before it contacts the server, and records it on the
// client-side binding exactly as the local path does -- PlannerID is the
// record's id, Planner carries the record's kind and session. Before the fix
// the binding was written with PlannerID "" and a zero planner, so
// planner-filtered status hid it and the channel route could not key on it.
func TestAddRemoteRecordsPlanner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newRuntime(t)
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	rt.Remote = &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}

	res, err := Add(ctx, rt, AddOptions{
		Name:      "api",
		Server:    "zen",
		Repo:      "/fake/repo",
		PlannerID: testPlannerName,
	})
	if err != nil {
		t.Fatalf("Add --server: %v", err)
	}
	if res.Binding.PlannerID != testPlannerID {
		t.Errorf("res.Binding.PlannerID = %q, want %q", res.Binding.PlannerID, testPlannerID)
	}

	stored, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.PlannerID != testPlannerID {
		t.Errorf("stored PlannerID = %q, want the record's %q", stored.PlannerID, testPlannerID)
	}
	if stored.Planner.Kind != "claude" {
		t.Errorf("stored Planner.Kind = %q, want claude", stored.Planner.Kind)
	}
	if stored.Planner.SessionID != "sess-architect" {
		t.Errorf("stored Planner.SessionID = %q, want sess-architect", stored.Planner.SessionID)
	}
}

// TestAddRemoteNoPlannerIsHardError pins §4.3 for the remote path: with
// nothing resolving -- no --planner, no $RELEVO_PLANNER, no host and no
// detectable session -- `add --server` fails with exactly the local path's
// no-planner text, before any binding is created on the server.
func TestAddRemoteNoPlannerIsHardError(t *testing.T) {
	t.Setenv("RELEVO_PLANNER", "")
	t.Setenv("CLAUDECODE", "")

	ctx := context.Background()
	rt := newRuntime(t)
	rt.Planners = testPlanners(t)
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt.Remote = fr

	_, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"})
	if err == nil {
		t.Fatal("Add --server with no resolvable planner must fail")
	}
	if err.Error() != ErrNoPlannerSession.Error() {
		t.Errorf("err = %q, want exactly %q", err.Error(), ErrNoPlannerSession.Error())
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding before the hard error", fr.calls)
		}
	}
}

// TestStatusShowsRemoteBindingForItsPlanner pins the status half of the fix:
// the remote binding's row carries the client planner's id, which is the field
// cmd/relevo's filterReportPlanner keys on, so a planner-filtered `relevo
// status` keeps it. Before the fix the row's PlannerID was "" and the filter
// dropped it.
func TestStatusShowsRemoteBindingForItsPlanner(t *testing.T) {
	ctx := context.Background()
	rt := newRuntime(t)
	rt.Git = &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	rt.Remote = &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}

	if _, err := Add(ctx, rt, AddOptions{
		Name:      "api",
		Server:    "zen",
		Repo:      "/fake/repo",
		PlannerID: testPlannerName,
	}); err != nil {
		t.Fatalf("Add --server: %v", err)
	}

	rep, err := Status(ctx, rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	// The filter the CLI applies keeps every row whose PlannerID is the
	// calling planner's; the remote binding must survive it.
	kept := 0
	var keptName string
	for _, b := range rep.Bindings {
		if b.Name == "api" && b.PlannerID != testPlannerID {
			t.Fatalf("remote row PlannerID = %q, want %q", b.PlannerID, testPlannerID)
		}
		if b.PlannerID == testPlannerID {
			kept++
			keptName = b.Name
		}
	}
	if kept != 1 || keptName != "api" {
		t.Fatalf("planner-filtered rows = %d (%q), want just the remote binding api", kept, keptName)
	}
}

// TestAddRemoteTierPreTierServerRefused pins ErrServerPreTier: a server that
// does not advertise FeatureTier refuses --tier before any binding is
// created there (#141 remote half).
func TestAddRemoteTierPreTierServerRefused(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{}, // Features nil: pre-tier server
		createBindingResp: remote.BindingView{
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	_, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Tier:   "edit",
	})
	if !errors.Is(err, ErrServerPreTier) {
		t.Fatalf("Add err = %v, want ErrServerPreTier", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
}

// TestAddRemoteTierWiresRequestAndEchoesBinding pins the wire contract: a
// tier-aware server gets Tier on the CreateBindingRequest, and the local
// binding records the tier the server echoed back, not the request's own
// (#141 remote half).
func TestAddRemoteTierWiresRequestAndEchoesBinding(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
			Tier:      "edit",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	res, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Tier:   "edit",
	})
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if fr.createBindingReq.Tier != "edit" {
		t.Fatalf("CreateBindingRequest.Tier = %q, want edit", fr.createBindingReq.Tier)
	}
	if res.Binding.Tier != "edit" {
		t.Fatalf("res.Binding.Tier = %q, want edit", res.Binding.Tier)
	}
	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Tier != "edit" {
		t.Fatalf("stored.Tier = %q, want edit", stored.Tier)
	}
}

// TestAddRemoteNoTierSkipsProbe pins the no-op path: omitting --tier never
// probes WhoAmI and sends no Tier on the wire, so a pre-tier server is
// unaffected by a plain `relevo bind --server` (#141 remote half).
func TestAddRemoteNoTierSkipsProbe(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	if _, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "WhoAmI") {
			t.Fatalf("calls = %v, want no WhoAmI", fr.calls)
		}
	}
	if fr.createBindingReq.Tier != "" {
		t.Fatalf("CreateBindingRequest.Tier = %q, want empty", fr.createBindingReq.Tier)
	}
}

// TestAddRemoteRolePreRolesServerRefused pins #382 §4: a server that does not
// advertise remote.FeatureRoles refuses a custom --actor before any binding is
// created there. An old server would ignore the field and run its builder, so
// the add is refused with no CreateBinding call and no branch or worktree.
func TestAddRemoteRolePreRolesServerRefused(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{whoAmIResp: remote.WhoAmI{}} // Features nil: a server without roles
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	_, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Role:   "ui-builder",
	})
	if err == nil {
		t.Fatal("Add(--role on a pre-roles server) = nil, want a refusal")
	}
	want := `server zen does not run custom actors (actor "ui-builder"); upgrade it`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
	if len(fg.createBranchCalls) != 0 || len(fg.addWorktreeCalls) != 0 {
		t.Fatalf("git calls = %v / %v, want no branch or worktree", fg.createBranchCalls, fg.addWorktreeCalls)
	}
}

// TestAddRemoteRoleWiresRequest pins #382 §5.3's client half: a roles-aware
// server gets Role on the CreateBindingRequest, and the local mirror records
// it so `relevo status` shows it. The client's registry names only the
// builder, so an add the client's own roles.json cannot explain still
// succeeds: the client never checks its own roles for a server add.
func TestAddRemoteRoleWiresRequest(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureRoles}},
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{
		Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t),
		Registry: rolesFileRegistry(t, candidateSet(t, testCandidatesJSON), policy.Policy{}, map[string]roles.Row{
			"builder": {Candidates: []string{testClaudeRef}},
		}),
	}

	res, err := Add(ctx, rt, AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
		Role:   "ui-builder",
	})
	if err != nil {
		t.Fatalf("Add --server --role: %v", err)
	}
	if fr.createBindingReq.Role != "ui-builder" {
		t.Fatalf("CreateBindingRequest.Role = %q, want ui-builder", fr.createBindingReq.Role)
	}
	if res.Binding.Role != "ui-builder" {
		t.Fatalf("res.Binding.Role = %q, want ui-builder", res.Binding.Role)
	}
	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Role != "ui-builder" {
		t.Fatalf("stored.Role = %q, want ui-builder", stored.Role)
	}
}

// TestCreateBindingRequestCarriesTheActor pins the A4 wire contract: the
// client always names the actor on create, "builder" for a default binding and
// the custom actor otherwise.
func TestCreateBindingRequestCarriesTheActor(t *testing.T) {
	cases := []struct {
		name string
		role string
		want string
	}{
		{"default binding sends builder", "", "builder"},
		{"custom binding sends its actor", "designer", "designer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(t.TempDir())
			fg := &fakeGit{
				headCommitID:  "1111111111111111111111111111111111111111",
				rootCommitSHA: "2222222222222222222222222222222222222222",
			}
			fr := &fakeRemote{
				whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureRoles}},
				createBindingResp: remote.BindingView{
					Name:      "api",
					Candidate: "claude/anthropic/haiku",
				},
			}
			rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

			if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo", Role: tc.role}); err != nil {
				t.Fatalf("Add: %v", err)
			}
			if fr.createBindingReq.Role != tc.want {
				t.Fatalf("CreateBindingRequest.Role = %q, want %q", fr.createBindingReq.Role, tc.want)
			}
		})
	}
}

// TestAddRemoteBuilderUnchanged pins that a default add to a server that lacks
// remote.FeatureRoles still succeeds: the client names the default actor
// "builder" on the wire (an old server ignores the field), and the local mirror
// keeps "" as the default.
func TestAddRemoteBuilderUnchanged(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{}, // Features nil: a server without roles
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	res, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"})
	if err != nil {
		t.Fatalf("Add --server (builder) failed: %v", err)
	}
	if fr.createBindingReq.Role != "builder" {
		t.Fatalf("CreateBindingRequest.Role = %q, want builder", fr.createBindingReq.Role)
	}
	if res.Binding.Role != "builder" {
		t.Fatalf("res.Binding.Role = %q, want builder", res.Binding.Role)
	}
}

// TestAddRemoteSendsGitAuthor pins #335's client half: the identity the
// client's repo resolves rides on the CreateBindingRequest, so the server can
// run this binding's builders as the client.
func TestAddRemoteSendsGitAuthor(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
		identityName:  "Ada Lovelace",
		identityEmail: "ada@example.com",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	author := fr.createBindingReq.Author
	if author == nil {
		t.Fatal("CreateBindingRequest.Author = nil, want the client's git identity")
	}
	if author.Name != "Ada Lovelace" || author.Email != "ada@example.com" {
		t.Errorf("CreateBindingRequest.Author = %+v, want Ada Lovelace <ada@example.com>", author)
	}
}

// TestAddRemoteRefusesWithoutGitIdentity pins #335's refusal: a repo with no
// user.email resolves no identity, so `add --server` fails with
// ErrNoGitIdentity before it touches the server -- no CreateBinding call, no
// local binding, no branch.
func TestAddRemoteRefusesWithoutGitIdentity(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
		identityName:  "Ada Lovelace",
		// identityEmail left unset: the fake's default identity applies only
		// when neither field is set.
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name:      "api",
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

	_, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"})
	if !errors.Is(err, ErrNoGitIdentity) {
		t.Fatalf("Add err = %v, want ErrNoGitIdentity", err)
	}
	if !strings.Contains(err.Error(), "git config user.email") {
		t.Errorf("Add err = %v, want it to mention git config user.email", err)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "CreateBinding") {
			t.Fatalf("calls = %v, want no CreateBinding", fr.calls)
		}
	}
	if _, lerr := st.Load("api"); !errors.Is(lerr, store.ErrNotFound) {
		t.Fatalf("st.Load(api) err = %v, want store.ErrNotFound (nothing may be saved)", lerr)
	}
}

// TestAddRemoteTwicePerRepo pins #100: a remote binding's CWD is the repo its
// branch is cut from, not a working tree it drives, so two remote bindings
// may share one repo the same way two `relevo add` worktrees do. (Mutation
// target: revert the assertCWDFree remote exemption in
// internal/store/store.go and this fails with ErrCWDTaken on the second
// addRemote call.)
func TestAddRemoteTwicePerRepo(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Candidate: "claude/anthropic/haiku",
		},
	}
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	if _, err := Add(ctx, rt, AddOptions{Name: "e2e2", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if _, err := Add(ctx, rt, AddOptions{Name: "e2e3", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("second Add with the same repo: %v", err)
	}
}

func TestAddRemoteRefusesCWD(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      &fakeGit{},
		Remote:   &fakeRemote{},
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		CWD:    "/some/dir",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "remote builders are add-only: --cwd and --server cannot be combined") {
		t.Fatalf("got err %v, want '--cwd and --server cannot be combined'", err)
	}
}

func TestAddRemoteServerConflict(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    "conflict",
				Message: "already exists",
			},
		},
	}
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "binding api already exists on zen for this client but not here; relevo serve unbind --owner <your label> api on the server, or choose another name") {
		t.Fatalf("got err %v, want 409 conflict message", err)
	}
	if len(fg.createBranchCalls) != 0 {
		t.Fatalf("createBranch called %d times, want 0", len(fg.createBranchCalls))
	}
}

// TestAddRemoteCleansUpServerOnSaveFailure pins #100: any failure after
// CreateBinding succeeds -- not just ErrBranchExists -- unbinds the server
// binding it just created, best-effort, before addRemote returns. A
// fakeGit.CreateBranch error that is not ErrBranchExists exercises the
// general defer path (the ErrBranchExists path already has its own test,
// TestAddRemoteBranchExistsUnbindsServer). Mutation target: remove the
// deferred cleanup in addRemote and this fails, since Unbind is never
// called for this generic error.
func TestAddRemoteCleansUpServerOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:    "1111111111111111111111111111111111111111",
		rootCommitSHA:   "2222222222222222222222222222222222222222",
		createBranchErr: errors.New("disk full"),
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name: "api",
		},
	}
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got err %v, want the CreateBranch error", err)
	}

	if !slices.Contains(fr.calls, "Unbind:zen:api") {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
	}
}

func TestAddRemoteBranchExistsUnbindsServer(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:    "1111111111111111111111111111111111111111",
		rootCommitSHA:   "2222222222222222222222222222222222222222",
		createBranchErr: git.ErrBranchExists,
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name: "api",
		},
	}
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	_, err := Add(ctx, rt, opts)
	if err == nil || !strings.Contains(err.Error(), "branch relevo/api exists; delete it or pick another name") {
		t.Fatalf("got err %v, want 'branch relevo/api exists...'", err)
	}

	// Verify unbind was called on server
	foundUnbind := false
	for _, call := range fr.calls {
		if strings.HasPrefix(call, "Unbind:zen:api") {
			foundUnbind = true
			break
		}
	}
	if !foundUnbind {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
	}
}

// TestAddRemoteExistingBranch pins `add --server --branch`: the client adopts
// a branch it did not create, sends the branch tip as BaseCommit, creates no
// branch, and a failure after the server agreed never deletes the branch.
func TestAddRemoteExistingBranch(t *testing.T) {
	ctx := context.Background()

	t.Run("adopts the branch and sends its tip as base", func(t *testing.T) {
		st := store.New(t.TempDir())
		fg := &fakeGit{
			branchExists:  true,
			refSHA:        map[string]string{"refs/heads/feature/x": "tip"},
			rootCommitSHA: "2222222222222222222222222222222222222222",
		}
		fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
		rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

		got, err := Add(ctx, rt, AddOptions{
			Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
		})
		if err != nil {
			t.Fatalf("Add --server --branch: %v", err)
		}
		if fr.createBindingReq.BaseCommit != "tip" {
			t.Errorf("BaseCommit = %q, want the branch tip tip", fr.createBindingReq.BaseCommit)
		}
		if len(fg.createBranchCalls) != 0 {
			t.Errorf("an adopted branch must not be created: %+v", fg.createBranchCalls)
		}
		if len(fg.createTrackingBranchCalls) != 0 {
			t.Errorf("a local branch needs no tracking branch: %+v", fg.createTrackingBranchCalls)
		}
		if !got.Binding.ExistingBranch {
			t.Error("ExistingBranch must record that relevo did not create the branch")
		}
		if got.Binding.Branch != "feature/x" {
			t.Errorf("Branch = %q, want feature/x", got.Binding.Branch)
		}
	})

	t.Run("a failure after CreateBinding unbinds the server and deletes no branch", func(t *testing.T) {
		root := t.TempDir()
		st := store.New(root)
		fg := &fakeGit{
			branchExists:  true,
			refSHA:        map[string]string{"refs/heads/feature/x": "tip"},
			rootCommitSHA: "2222222222222222222222222222222222222222",
		}
		fr := &fakeRemote{createBindingResp: remote.BindingView{Name: "x"}}
		rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, Planners: addRemotePlanner(t)}

		// Same provocation as TestAddRemoteCleansUpLocalBranchOnSaveFailure:
		// the lock file exists, then the state root goes read-only, so the
		// save after CreateBinding fails.
		if err := st.WithLock(func(tx *store.Tx) error { return nil }); err != nil {
			t.Fatalf("seed lock file: %v", err)
		}
		if err := os.Chmod(root, 0o500); err != nil {
			t.Fatalf("chmod root: %v", err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

		_, err := Add(ctx, rt, AddOptions{
			Name: "x", Branch: "feature/x", Server: "zen", Repo: "/fake/repo",
		})
		if err == nil {
			t.Fatal("Add expected error, got nil")
		}
		if !slices.Contains(fr.calls, "Unbind:zen:x") {
			t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
		}
		if len(fg.deleteBranchCalls) != 0 {
			t.Errorf("relevo must not delete a branch it did not create: %+v", fg.deleteBranchCalls)
		}
	})
}

// TestAddRemoteCleansUpLocalBranchOnSaveFailure pins #100 round 4: a failure
// that happens after CreateBranch has already succeeded also removes the
// local branch relevo just cut, not only the server binding -- otherwise the
// branch is left behind and the next `add` with the same name is refused
// with "branch relevo/<name> exists; delete it or pick another name", even
// though relevo itself created it. The provocation makes Save fail (a real
// store, not fakeGit, since fakeGit.CreateBranch must succeed here): the
// binding's own state directory is pre-occupied by a plain file, so the
// store's os.MkdirAll refuses it -- exercising the same "some failure after
// CreateBinding succeeds" path TestAddRemoteCleansUpServerOnSaveFailure pins
// for CreateBranch, but one step later, after the branch already exists.
// Mutation target: drop the DeleteBranch call in addRemote's deferred
// cleanup and this fails, since DeleteBranch is then never called.
func TestAddRemoteCleansUpLocalBranchOnSaveFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := store.New(root)

	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		createBindingResp: remote.BindingView{
			Name: "api",
		},
	}

	var order []string
	fr.onUnbind = func() { order = append(order, "unbind") }
	fg.deleteBranchFunc = func(ctx context.Context, dir, branch string) error {
		order = append(order, "delete-branch")
		return nil
	}

	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	opts := AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   "/fake/repo",
	}

	// Make the store's own root read-only, but only after its lock file
	// already exists: addRemote's first step (rt.Store.Load) must still see
	// a plain "no such file" for a binding that has never been saved, not a
	// permission error, or this would fail before CreateBinding and
	// CreateBranch ever ran. tx.Save's later os.MkdirAll(root/api, ...) does
	// need to create a new directory entry, though, so it fails.
	if err := st.WithLock(func(tx *store.Tx) error { return nil }); err != nil {
		t.Fatalf("seed lock file: %v", err)
	}
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatalf("chmod root: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o755) })

	_, err := Add(ctx, rt, opts)
	if err == nil {
		t.Fatal("Add expected error, got nil")
	}

	if !slices.Contains(fr.calls, "Unbind:zen:api") {
		t.Fatalf("Unbind was not called on server, calls = %v", fr.calls)
	}
	if len(fg.deleteBranchCalls) != 1 {
		t.Fatalf("DeleteBranch calls = %v, want exactly one", fg.deleteBranchCalls)
	}
	want := deleteBranchCall{Dir: "/fake/repo", Branch: "relevo/api"}
	if fg.deleteBranchCalls[0] != want {
		t.Fatalf("DeleteBranch call = %+v, want %+v", fg.deleteBranchCalls[0], want)
	}
	if len(order) != 2 || order[0] != "unbind" || order[1] != "delete-branch" {
		t.Fatalf("expected Unbind before DeleteBranch, got order %v", order)
	}
}

func TestSendRemoteRecordsOnlyOnSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable),
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if err == nil || !strings.Contains(err.Error(), "zen unreachable") {
		t.Fatalf("Send got %v, want zen unreachable", err)
	}

	// No plan file written
	if _, err := os.Stat(st.PlanPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("PlanPath exists after failure: %v", err)
	}

	// No log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(entries))
	}

	// Round unchanged
	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Round != 1 {
		t.Fatalf("reloaded.Round = %d, want 1", reloaded.Round)
	}
	if !reloaded.RoundStartedAt.IsZero() {
		t.Fatalf("reloaded.RoundStartedAt is not zero: %v", reloaded.RoundStartedAt)
	}
}

func TestSendRemoteRoundStartedIsSuccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 409,
			Body: remote.ErrorBody{
				Code:    remote.CodeRoundStarted,
				Message: "already started",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if res.Round != 1 {
		t.Fatalf("res.Round = %d, want 1", res.Round)
	}

	// Plan file written
	if _, err := os.Stat(st.PlanPath("api", 1)); err != nil {
		t.Fatalf("PlanPath missing: %v", err)
	}

	// Log entry written
	entries, _ := st.ReadLog("api")
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
}

// TestSendRemoteHaltedIsAnError pins #250 items 1 and 3 client-side: a
// StartRound response that says the round could not start -- whether an old
// server's 201 with a needs_you view, or a new server's 409 round_halted --
// is reported as an error, and nothing is written locally: a resend must
// not look like it succeeded.
func TestSendRemoteHaltedIsAnError(t *testing.T) {
	t.Parallel()

	newRT := func(t *testing.T, fr *fakeRemote) (Runtime, *store.Store) {
		t.Helper()
		st := store.New(t.TempDir())
		b := store.Binding{
			Name:   "api",
			CWD:    "/fake/repo",
			Repo:   "/fake/repo",
			Branch: "relevo/api",
			Round:  1,
			State:  store.StateActive,
			Builder: store.Endpoint{
				Mode:        store.ModeRemote,
				Server:      "zen",
				LastShipped: "0000000000000000000000000000000000000000",
			},
		}
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		fg := &fakeGit{
			refSHA: map[string]string{
				"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
			},
		}
		ft := &fakeTransport{
			snapshotResp: remote.Snapshot{
				Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
			},
		}
		return Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}, st
	}

	assertNothingWritten := func(t *testing.T, st *store.Store, err error, wantSubstrs ...string) {
		t.Helper()
		if err == nil {
			t.Fatal("Send succeeded, want an error")
		}
		for _, want := range wantSubstrs {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("Send error = %q, want it to contain %q", err.Error(), want)
			}
		}
		if _, statErr := os.Stat(st.PlanPath("api", 1)); !os.IsNotExist(statErr) {
			t.Errorf("PlanPath exists after failure: stat err = %v", statErr)
		}
		entries, _ := st.ReadLog("api")
		if len(entries) != 0 {
			t.Errorf("entries = %d, want 0", len(entries))
		}
		reloaded, loadErr := st.Load("api")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if reloaded.State != store.StateActive {
			t.Errorf("State = %s, want unchanged active", reloaded.State)
		}
		if reloaded.Builder.LastShipped != "0000000000000000000000000000000000000000" {
			t.Errorf("LastShipped = %q, want unchanged", reloaded.Builder.LastShipped)
		}
	}

	t.Run("201 needs_you view (old server)", func(t *testing.T) {
		fr := &fakeRemote{
			startRoundResp: remote.BindingView{
				RoundState: remote.RoundNeedsYou,
				Halt:       "builder spawn failed: boom",
			},
		}
		rt, st := newRT(t, fr)

		planFile := filepath.Join(t.TempDir(), "plan.md")
		if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := Send(context.Background(), rt, "api", planFile, SendOptions{})
		assertNothingWritten(t, st, err, "could not start", "boom")
	})

	t.Run("409 round_halted (new server)", func(t *testing.T) {
		fr := &fakeRemote{
			startRoundErr: &client.HTTPError{
				Status: 409,
				Body: remote.ErrorBody{
					Code:    remote.CodeRoundHalted,
					Message: "already switched 2 time(s) this round (max_switches 2)",
				},
			},
		}
		rt, st := newRT(t, fr)

		planFile := filepath.Join(t.TempDir(), "plan.md")
		if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := Send(context.Background(), rt, "api", planFile, SendOptions{})
		assertNothingWritten(t, st, err, "could not start", "already switched 2 time(s)")
	})
}

// TestSendRemoteTierPassedToStartRound pins that a Send with a Tier option
// reaches StartRound over the wire, after the pre-tier probe succeeds
// (#141 remote half).
func TestSendRemoteTierPassedToStartRound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(ctx, rt, "api", planFile, SendOptions{Tier: "edit"}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if fr.startRoundTier != "edit" {
		t.Fatalf("startRoundTier = %q, want edit", fr.startRoundTier)
	}
}

// TestSendRemoteStartRoundRetryFollowsTheFeature pins #373 §4.5's flag: the
// StartRound retry is asked for exactly when WhoAmI advertises
// idempotent_send, and never otherwise.
func TestSendRemoteStartRoundRetryFollowsTheFeature(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		features []string
		want     bool
	}{
		{"idempotent_send advertised", []string{remote.FeatureIdempotentSend}, true},
		{"feature absent", []string{remote.FeatureQueue}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := store.New(t.TempDir())
			b := store.Binding{
				Name:   "api",
				CWD:    "/fake/repo",
				Repo:   "/fake/repo",
				Branch: "relevo/api",
				Round:  1,
				State:  store.StateActive,
				Builder: store.Endpoint{
					Mode:        store.ModeRemote,
					Server:      "zen",
					LastShipped: "0000000000000000000000000000000000000000",
				},
			}
			if err := st.Save(b); err != nil {
				t.Fatal(err)
			}

			fg := &fakeGit{
				refSHA: map[string]string{
					"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
				},
			}
			fr := &fakeRemote{
				whoAmIResp:     remote.WhoAmI{Features: tc.features},
				startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
			}
			ft := &fakeTransport{
				snapshotResp: remote.Snapshot{
					Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
				},
			}
			rt := Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}

			planFile := filepath.Join(t.TempDir(), "plan.md")
			if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Send(ctx, rt, "api", planFile, SendOptions{}); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if fr.startRoundRetry != tc.want {
				t.Fatalf("StartRound retry = %v, want %v (features %v)", fr.startRoundRetry, tc.want, tc.features)
			}
		})
	}
}

// TestSendRemoteTierAboveMaxWraps pins that a 422 tier_above_max from
// StartRound maps back to ErrTierAboveMax client-side, matching the local
// refusal's wording (#141 remote half).
func TestSendRemoteTierAboveMaxWraps(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundErr: &client.HTTPError{
			Status: 422,
			Body: remote.ErrorBody{
				Code:    remote.CodeTierAboveMax,
				Message: "tier yolo exceeds this server's max_tier edit",
			},
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if !errors.Is(err, ErrTierAboveMax) {
		t.Fatalf("Send err = %v, want ErrTierAboveMax", err)
	}
}

func TestSendRemoteFirstSendFullBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "", // first send
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(ft.snapshotCalls) != 1 {
		t.Fatalf("snapshotCalls = %d, want 1", len(ft.snapshotCalls))
	}
	if ft.snapshotCalls[0].Since != "" {
		t.Fatalf("snapshot Since = %q, want empty string for first send", ft.snapshotCalls[0].Since)
	}
}

func TestSendRemoteShipsTags(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:        store.ModeRemote,
			Server:      "zen",
			LastShipped: "",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
		tags: map[string]string{
			"v1": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"v0": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Send(ctx, rt, "api", planFile, SendOptions{}); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	want := []remote.TagRef{
		{Name: "v0", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{Name: "v1", SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if !slices.Equal(fr.startRoundTags, want) {
		t.Fatalf("StartRound tags = %+v, want %+v (sorted by name)", fr.startRoundTags, want)
	}
}

func TestSendRemoteSetsLastShipped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:   "api",
		CWD:    "/fake/repo",
		Repo:   "/fake/repo",
		Branch: "relevo/api",
		Round:  1,
		State:  store.StateActive,
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	outSHA := "3333333333333333333333333333333333333333"
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	fr := &fakeRemote{
		startRoundResp: remote.BindingView{
			RoundState: remote.RoundRunning,
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": outSHA},
		},
	}
	rt := Runtime{
		Store:     st,
		Git:       fg,
		Remote:    fr,
		Transport: ft,
		Now:       time.Now,
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	reloaded, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Builder.LastShipped != outSHA {
		t.Fatalf("reloaded LastShipped = %q, want %q", reloaded.Builder.LastShipped, outSHA)
	}
}

// remoteBinding is a minimal active remote binding, round 1, planner
// pointed at plannerAgent()'s pane so deliverAndSettle's FindAgent succeeds
// and does not turn the binding orphaned.
func remoteBinding(server string) store.Binding {
	return store.Binding{
		Name:    "api",
		CWD:     "/fake/repo",
		Repo:    "/fake/repo",
		Branch:  "relevo/api",
		Round:   1,
		State:   store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"},
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: server,
		},
	}
}

// TestObserveRemoteCopiesStalledSince pins #252's remote half: a running
// round's stall stamp rides from the server view onto the client binding,
// status shows it, and a later view without one clears it.
// TestObserveRemoteQueuedKeepsFacts pins #285's client tolerance: a queued
// view copies the server's queue facts onto Builder.RemoteQueue, sets
// RemoteStatus to "queued", and neither halts nor catches up.
// TestObserveRemoteRunningClearsQueue pins #285's clear rule: a binding that
// was queued and now observes a running view drops its stale RemoteQueue.
// TestReconcileRemoteRefreshesCandidate checks that a server-side switch
// (the round is now running a different candidate than this binding last
// recorded) updates the token and harness kind and logs a switch entry
// naming the server, and that a later tick whose view still names the same
// candidate adds nothing more (#100 step 2).
// TestSyncRemoteCollectsClosedRoundWithoutDelivery checks that SyncRemote
// (the read path `relevo status`/`pull`/`wait` share, spec §2.2) collects a
// closed round -- the report entry lands, pending -- without ever
// delivering it to a planner pane.
// TestSyncRemoteSkipsDoneAndLocal checks that SyncRemote never touches the
// network for a binding it should not sync: one already DONE, and one that
// is not a remote binding at all.
// newRemoteClientRepo creates a client-side repo (what a remote binding's
// Repo points at day to day) with one commit and a "relevo/<name>" branch ref
// at that commit -- not checked out, matching the ordinary case where the
// planner's own repo sits on its own branch.
func newRemoteClientRepo(t *testing.T, name string) (dir, headSHA string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "seed.txt")
	runGit(t, dir, "commit", "-m", "seed")
	headSHA = strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	runGit(t, dir, "update-ref", "refs/heads/relevo/"+name, headSHA)
	return dir, headSHA
}

// newRoundResultBundle clones base (sitting at baseSHA on ref), adds one
// commit there, and returns a real bundle -- built the same way
// BundleTransport.Snapshot always does -- carrying just that new commit,
// plus its sha. ref is the full ref the server ships, which for an
// adopted-branch binding is refs/heads/relevo/<name> even though the client's
// own branch keeps the adopted name (#274).
func newRoundResultBundle(t *testing.T, ctx context.Context, g *git.Client, base, ref, baseSHA string) (io.ReadCloser, string) {
	t.Helper()
	resultDir := t.TempDir()
	runGit(t, resultDir, "clone", base, ".")
	runGit(t, resultDir, "checkout", strings.TrimPrefix(ref, "refs/heads/"))
	if err := os.WriteFile(filepath.Join(resultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resultDir, "add", "result.txt")
	runGit(t, resultDir, "commit", "-m", "round result")
	headSHA := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD"))

	transport := remote.NewBundleTransport(g, t.TempDir())
	snap, err := transport.Snapshot(ctx, resultDir, []string{ref}, baseSHA)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Empty || snap.Body == nil {
		t.Fatal("expected a non-empty bundle")
	}
	return snap.Body, headSHA
}

// TestCatchUpWritesDiffEntryFromView checks that catchUp writes the
// client's own diff log entry from the server's view facts (Note, Commits,
// Tree, plus the downloaded patch as Path) rather than letting queueReport
// capture its own diff from a local baseline that a remote binding never
// has.
// TestCatchUpAdoptedBranchAbsorbsServerRef pins the adopted-branch fix
// (#274): `add --server --branch feature/x` adopts a branch relevo did not
// create, but the server still cuts its own branch and the round bundle it
// ships always names refs/heads/relevo/<name> (handleRoundBundle snapshots
// refs/heads/<server branch>, and the server's branch is relevo/<name>).
// Catch-up must allow the server's ref through Absorb, then fast-forward the
// adopted branch to it, so the planner's own branch is the one carrying the
// round's result.
// TestCatchUpKeepsServerUsage checks that catch-up keeps the usage the
// server measured and shipped with the round (#216): the client's report
// entry stores it verbatim instead of re-measuring a round whose record
// lives on the server.
// TestCatchUpRecordsRusage extends TestCatchUpKeepsServerUsage's pattern:
// the server's cgroup measurement rides the same view as Usage, and lands
// on the report entry catchUp writes (#244, #216).
// TestCatchUpPreUsageServerNotes checks the honest answer for a server
// built before this (#216): with no usage in the view, the client's report
// entry says the server sent none -- basis unknown -- rather than reading
// a record the client does not have.
// captureHandler is a slog.Handler that records every record it sees, so a
// test can count how often a message was logged (#253).
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(ctx context.Context, level slog.Level) bool { return true }

func (h *captureHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *captureHandler) WithGroup(name string) slog.Handler { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.records)
}

func TestDoneRemoteForwardsFirst(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{doneErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeRoundOpen, Message: "round is open"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	_, err := Done(ctx, rt, "api")
	if err == nil || !strings.Contains(err.Error(), "round 1 is running on zen; relevo stop api to stop it and keep the binding, or relevo unbind api to drop it") {
		t.Fatalf("Done err = %v, want the round-open refusal", err)
	}
	if err != nil && strings.Contains(err.Error(), "--force") {
		t.Fatalf("Done err = %v, must not name --force: the local unbind has no such flag", err)
	}

	reloaded, lerr := st.Load("api")
	if lerr != nil {
		t.Fatal(lerr)
	}
	if reloaded.State != store.StateActive {
		t.Fatalf("state = %s, want unchanged active: a refused Done must not mark the binding done", reloaded.State)
	}
}

func TestUnbindRemote404Proceeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "gone"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := Unbind(ctx, rt, "api", false); err != nil {
		t.Fatalf("Unbind: %v, want a 404 to proceed locally", err)
	}

	if _, err := st.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after unbind: err = %v, want ErrNotFound", err)
	}
	foundUnbind := false
	for _, c := range fr.calls {
		if c == "Unbind:zen:api" {
			foundUnbind = true
		}
	}
	if !foundUnbind {
		t.Fatalf("server Unbind never called: %v", fr.calls)
	}
}

// TestUnbindRemoteRunningRoundForwardsAndDeletes pins #331's premise for the
// local verb the hints now name: `relevo unbind` has no running-round guard,
// and does not need one. It tells the server first -- whose wire unbind kills
// the headless builder and archives its copy -- then deletes the local
// record, whether or not a round is open.
func TestUnbindRemoteRunningRoundForwardsAndDeletes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundStartedAt = baseTime
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := Unbind(ctx, rt, "api", false); err != nil {
		t.Fatalf("Unbind: %v, want a running round to proceed", err)
	}

	foundUnbind := false
	for _, c := range fr.calls {
		if c == "Unbind:zen:api" {
			foundUnbind = true
		}
	}
	if !foundUnbind {
		t.Fatalf("server Unbind never called: %v", fr.calls)
	}

	if _, err := st.Load("api"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Load after unbind: err = %v, want ErrNotFound", err)
	}
}

func TestGCRemoteOnlyWhenDone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	doneB := remoteBinding("zen")
	doneB.Name = "done-remote"
	doneB.CWD = "/fake/done-remote"
	doneB.State = store.StateDone
	if err := st.Save(doneB); err != nil {
		t.Fatal(err)
	}

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	results, err := GC(ctx, rt, GCOptions{AllPlanners: true})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if len(results) != 1 || results[0].Name != "done-remote" {
		t.Fatalf("results = %+v, want only done-remote", results)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("GC made a server call: %v, want none", fr.calls)
	}
	if _, lerr := st.Load("open-remote"); lerr != nil {
		t.Fatalf("open-remote must survive GC: %v", lerr)
	}
}

func TestForwardUnavailable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	idleB := remoteBinding("zen")
	idleB.Name = "idle-remote"
	idleB.CWD = "/fake/idle-remote"
	if err := st.Save(idleB); err != nil {
		t.Fatal(err)
	}
	// No plan entry for idle-remote's round: its round is not open.

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: "some-token"}}},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardUnavailable(ctx, rt, "some-token", "hit a limit")
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none: the fake never fails", lines)
	}

	unavailableCalls := 0
	var target string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailableCalls++
			target = c
		}
	}
	if unavailableCalls != 1 {
		t.Fatalf("Unavailable calls = %d, want exactly 1: %v", unavailableCalls, fr.calls)
	}
	if !strings.Contains(target, "open-remote") {
		t.Fatalf("the one call = %q, want it naming open-remote", target)
	}
}

// forwardClientCandidatesJSON is one candidate named test-model-1 under a
// provider the servers in the forward tests below do not use: the same name
// and model as the server's, on another machine's quota group.
const forwardClientCandidatesJSON = `[
  {"harness":"opencode","provider":"laptop-group","model":"test-model-1","roles":["builder"]}
]`

// TestForwardUnavailableSendsTheServerTokenForTheSameName: the two machines
// name the same candidate but the server lists it under another provider. The
// server's own token is what travels, and the mapping is announced rather than
// silent.
func TestForwardUnavailableSendsTheServerTokenForTheSameName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/server-group/test-model-1", Name: "test-model-1"},
		}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit")
	want := []string{"zen: gated opencode/server-group/test-model-1 for opencode/laptop-group/test-model-1"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:opencode/server-group/test-model-1"}) {
		t.Fatalf("Unavailable calls = %v, want exactly one for the server token", unavailable)
	}
}

// TestForwardUnavailableSendsTheServerTokenForARenamedProvider: the server
// predates CandidateView.Name and sends only tokens. The provider renamed
// between the two machines is still matched from the harness and model in the
// token itself.
func TestForwardUnavailableSendsTheServerTokenForARenamedProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	openB := remoteBinding("zen")
	openB.Name = "open-remote"
	openB.CWD = "/fake/open-remote"
	if err := st.Save(openB); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-remote", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/server-group/test-model-1"},
		}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit")
	want := []string{"zen: gated opencode/server-group/test-model-1 for opencode/laptop-group/test-model-1"}
	if !slices.Equal(lines, want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}

	var unavailable []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			unavailable = append(unavailable, c)
		}
	}
	if !slices.Equal(unavailable, []string{"Unavailable:zen:open-remote:opencode/server-group/test-model-1"}) {
		t.Fatalf("Unavailable calls = %v, want exactly one for the server token", unavailable)
	}
}

// TestForwardUnavailableReportsOneLinePerServerWhenNothingMatches: two open
// bindings on one server, and the server lists no candidate the client's
// candidate maps to. One line names the mismatch -- the token, its provider
// and the server's candidates -- and nothing is posted.
func TestForwardUnavailableReportsOneLinePerServerWhenNothingMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	for _, name := range []string{"open-one", "open-two"} {
		b := remoteBinding("zen")
		b.Name = name
		b.CWD = "/fake/" + name
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}
		if err := st.AppendLog(name, store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
			t.Fatal(err)
		}
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/other-group/other-model", Name: "other-model"},
		}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardUnavailable(ctx, rt, "test-model-1", "hit a limit")
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want exactly one line for the one server", lines)
	}
	for _, want := range []string{"opencode/laptop-group/test-model-1", "laptop-group", "other-model"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("line = %q, want it naming %q", lines[0], want)
		}
	}

	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Unavailable:") {
			t.Errorf("Unavailable was called with no match: %v", fr.calls)
		}
	}
}

// TestServerCandidateMatchesInOrder pins each rule of serverCandidate in order:
// exact token; same provider+model; same provider, another model; same name
// under another provider; an unnamed view with the same harness+model (pre-Name
// server); a named view with the same harness+model but different name (must not
// match); and no common element.
func TestServerCandidateMatchesInOrder(t *testing.T) {
	t.Parallel()

	views := []remote.CandidateView{
		{Token: "opencode/same-group/same-model", Name: "same-model"},
		{Token: "opencode/same-group/other-model", Name: "other-model"},
		{Token: "opencode/other-group/name-match-model", Name: "my-name"},
		{Token: "opencode/another-group/harness-model", Name: ""},
		{Token: "opencode/yet-another-group/harness-model", Name: "different-name"},
	}

	tests := []struct {
		name      string
		token     string
		candName  string
		wantToken string
		wantOK    bool
	}{
		{
			name:      "rule 1: exact token",
			token:     "opencode/same-group/same-model",
			wantToken: "opencode/same-group/same-model",
			wantOK:    true,
		},
		{
			name:      "rule 2a: same provider and same model",
			token:     "opencode/same-group/same-model",
			wantToken: "opencode/same-group/same-model",
			wantOK:    true,
		},
		{
			name:      "rule 2b: same provider, another model",
			token:     "opencode/same-group/unknown-model",
			wantToken: "opencode/same-group/same-model",
			wantOK:    true,
		},
		{
			name:      "rule 3: same name under another provider",
			token:     "claude/laptop-group/name-match-model",
			candName:  "my-name",
			wantToken: "opencode/other-group/name-match-model",
			wantOK:    true,
		},
		{
			name:      "rule 4: unnamed view with same harness and model",
			token:     "opencode/renamed-group/harness-model",
			candName:  "",
			wantToken: "opencode/another-group/harness-model",
			wantOK:    true,
		},
		{
			name:      "rule 4: named view with same harness+model but different name must not match over unnamed",
			token:     "opencode/renamed-group/harness-model",
			candName:  "other-name",
			wantToken: "opencode/another-group/harness-model",
			wantOK:    true,
		},
		{
			name:     "nothing in common",
			token:    "agy/unknown-group/unknown-model",
			candName: "",
			wantOK:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := serverCandidate(views, tc.token, tc.candName)
			if ok != tc.wantOK {
				t.Fatalf("serverCandidate ok = %v, want %v (got token %q)", ok, tc.wantOK, got)
			}
			if ok && got != tc.wantToken {
				t.Errorf("serverCandidate = %q, want %q", got, tc.wantToken)
			}
		})
	}
}

// TestForwardAvailablePostsToEveryServerOnce: a gate can matter most when
// nothing runs, so ForwardAvailable names every server this client's remote
// bindings point at -- open round or not, active or DONE -- once each, sorted,
// with one answer line per server. Filtering to open rounds (as
// ForwardUnavailable does) must fail this: alpha would still be called, beta
// would not.
func TestForwardAvailablePostsToEveryServerOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	// Two remote bindings on alpha: one with an open round, one idle.
	openA := remoteBinding("alpha")
	openA.Name = "open-alpha"
	openA.CWD = "/fake/open-alpha"
	if err := st.Save(openA); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("open-alpha", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan}); err != nil {
		t.Fatal(err)
	}

	idleA := remoteBinding("alpha")
	idleA.Name = "idle-alpha"
	idleA.CWD = "/fake/idle-alpha"
	if err := st.Save(idleA); err != nil {
		t.Fatal(err)
	}

	// One remote binding on beta, whose work is done.
	doneB := remoteBinding("beta")
	doneB.Name = "done-beta"
	doneB.CWD = "/fake/done-beta"
	doneB.State = store.StateDone
	if err := st.Save(doneB); err != nil {
		t.Fatal(err)
	}

	// One local binding: not a server, so it must never be called.
	local := remoteBinding("")
	local.Name = "local"
	local.CWD = "/fake/local"
	local.Builder = store.Endpoint{PaneID: "w2:p4", Mode: store.Mode("pane")}
	if err := st.Save(local); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "anthropic", Removed: 1}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	lines := ForwardAvailable(ctx, rt, "claude/anthropic/haiku")
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want one line per server (alpha, beta)", lines)
	}

	var calls []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("Available calls = %d, want exactly 2 (one per server): %v", len(calls), fr.calls)
	}
	if calls[0] != "Available:alpha:claude/anthropic/haiku" {
		t.Errorf("call 0 = %q, want alpha first", calls[0])
	}
	if calls[1] != "Available:beta:claude/anthropic/haiku" {
		t.Errorf("call 1 = %q, want beta second", calls[1])
	}
	if !strings.Contains(lines[0], "alpha") {
		t.Errorf("line 0 = %q, want it naming alpha", lines[0])
	}
	if !strings.Contains(lines[1], "beta") {
		t.Errorf("line 1 = %q, want it naming beta", lines[1])
	}
}

func TestServerInUse(t *testing.T) {
	t.Parallel()

	bindings := []store.Binding{
		remoteBinding("zen"),
		func() store.Binding { b := remoteBinding("mars"); b.Name = "other"; return b }(),
		{Name: "local", Builder: store.Endpoint{Mode: store.Mode("pane")}},
	}

	if got := ServerInUse(bindings, "zen"); len(got) != 1 || got[0] != "api" {
		t.Fatalf("ServerInUse(zen) = %v, want [api]", got)
	}
	if got := ServerInUse(bindings, "mars"); len(got) != 1 || got[0] != "other" {
		t.Fatalf("ServerInUse(mars) = %v, want [other]", got)
	}
	if got := ServerInUse(bindings, "pluto"); len(got) != 0 {
		t.Fatalf("ServerInUse(pluto) = %v, want none", got)
	}
}

func TestReconcileRemoteRunningMirrorsLog(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundRunning) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundRunning)
	}
	logPath := st.BuilderLogPath("api", 1)
	data, err := rt.Store.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mirrored log: %v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log exists on disk: %v", err)
	}
	if string(data) != "builder log line 1\n" {
		t.Fatalf("log = %q, want mirrored content", string(data))
	}
	foundGet, foundLog := false, false
	for _, c := range fr.calls {
		if c == "GetBinding:zen:api" {
			foundGet = true
		}
		if strings.HasPrefix(c, "RoundFileFrom:zen:api:1:log") {
			foundLog = true
		}
	}
	if !foundGet || !foundLog {
		t.Fatalf("calls = %v, want GetBinding and RoundFileFrom(log)", fr.calls)
	}
	if got.State != store.StateActive {
		t.Fatalf("state = %s, want active while running", got.State)
	}
}

func TestMirrorLogAppends(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp:  io.NopCloser(strings.NewReader("b\n")),
		roundFileFromRange: remote.FileRange{Honored: true, From: 2, Size: 4},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "a\nb\n" {
		t.Fatalf("log data = %q, want %q", string(data), "a\nb\n")
	}
	if len(fr.roundFileFromCalls) != 1 || fr.roundFileFromCalls[0] != 2 {
		t.Fatalf("roundFileFromCalls = %v, want [2]", fr.roundFileFromCalls)
	}
}

func TestMirrorLogOldServerReplaces(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("prior log content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp:  io.NopCloser(strings.NewReader("whole\n")),
		roundFileFromRange: remote.FileRange{Honored: false},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "whole\n" {
		t.Fatalf("log data = %q, want %q", string(data), "whole\n")
	}
}

func TestMirrorLogShrankRefetches(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	callCount := 0
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromFunc: func(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error) {
			callCount++
			if callCount == 1 {
				// from is 10, server reports file shrunk to size 4
				return io.NopCloser(strings.NewReader("")), remote.FileRange{Honored: true, From: from, Size: 4}, nil
			}
			// Second call: from is 0
			return io.NopCloser(strings.NewReader("shrunk\n")), remote.FileRange{Honored: true, From: 0, Size: 7}, nil
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "shrunk\n" {
		t.Fatalf("log data = %q, want %q", string(data), "shrunk\n")
	}
	if len(fr.roundFileFromCalls) != 2 || fr.roundFileFromCalls[0] != 10 || fr.roundFileFromCalls[1] != 0 {
		t.Fatalf("roundFileFromCalls = %v, want [10, 0]", fr.roundFileFromCalls)
	}
}

func TestMirrorLogUnchangedLogWritesNothing(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	base := filepath.Base(logPath)

	d, err := db.Open(st.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rec, ok, err := d.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: ok=%v err=%v", ok, err)
	}
	// Seed the row with a stamp far from now, so a rewrite during the mirror
	// shows up as a changed mtime even though the bytes are identical.
	if err := d.Tx(func(tx *db.Tx) error {
		return tx.RoundFilePut(rec.ID, base, 1, []byte("abc"), baseTime, baseTime)
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp:  io.NopCloser(strings.NewReader("")),
		roundFileFromRange: remote.FileRange{Honored: true, From: 3, Size: 3},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	body, mtime, ok, err := d.RoundFileGet(rec.ID, base)
	if err != nil {
		t.Fatalf("RoundFileGet: %v", err)
	}
	if !ok {
		t.Fatalf("round file row %q missing", base)
	}
	if string(body) != "abc" {
		t.Fatalf("row body = %q, want %q", string(body), "abc")
	}
	if !mtime.Equal(baseTime) {
		t.Fatalf("row mtime = %v, want unchanged %v: the row was rewritten", mtime, baseTime)
	}
	if len(fr.roundFileFromCalls) != 1 || fr.roundFileFromCalls[0] != 3 {
		t.Fatalf("roundFileFromCalls = %v, want [3]", fr.roundFileFromCalls)
	}
}

func TestMirrorLogWritesARow(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp:  io.NopCloser(strings.NewReader("abc")),
		roundFileFromRange: remote.FileRange{Honored: true, From: 0, Size: 3},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	// First tick: server serves "abc" with from honored
	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile tick 1: %v", err)
	}

	logPath := st.BuilderLogPath("api", 1)
	data, err := rt.Store.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile tick 1: %v", err)
	}
	if string(data) != "abc" {
		t.Fatalf("log tick 1 = %q, want %q", string(data), "abc")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log on disk tick 1: %v", err)
	}
	if len(fr.roundFileFromCalls) != 1 || fr.roundFileFromCalls[0] != 0 {
		t.Fatalf("roundFileFromCalls tick 1 = %v, want [0]", fr.roundFileFromCalls)
	}

	// Second tick: server now "abcdef", requests from=3, served "def"
	fr.roundFileFromResp = io.NopCloser(strings.NewReader("def"))
	fr.roundFileFromRange = remote.FileRange{Honored: true, From: 3, Size: 6}

	if _, err := reconcile(t, rt, got); err != nil {
		t.Fatalf("Reconcile tick 2: %v", err)
	}

	if len(fr.roundFileFromCalls) != 2 || fr.roundFileFromCalls[1] != 3 {
		t.Fatalf("roundFileFromCalls tick 2 = %v, want [0, 3]", fr.roundFileFromCalls)
	}

	data2, err := rt.Store.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile tick 2: %v", err)
	}
	if string(data2) != "abcdef" {
		t.Fatalf("log tick 2 = %q, want %q", string(data2), "abcdef")
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log on disk tick 2: %v", err)
	}
}

func TestMirrorLogRowRewritesWhenServerShrank(t *testing.T) {
	t.Parallel()

	t.Run("server shrank", func(t *testing.T) {
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}

		logPath := st.BuilderLogPath("api", 1)
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("api", 1, logPath, []byte("abcdef"))
		}); err != nil {
			t.Fatal(err)
		}

		callCount := 0
		fr := &fakeRemote{
			getBindingResp: remote.BindingView{RoundState: remote.RoundRunning},
			roundFileFromFunc: func(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error) {
				callCount++
				if callCount == 1 {
					// from is 6, server reports size 3
					return io.NopCloser(strings.NewReader("")), remote.FileRange{Honored: true, From: from, Size: 3}, nil
				}
				// Second call: from is 0
				return io.NopCloser(strings.NewReader("xyz")), remote.FileRange{Honored: true, From: 0, Size: 3}, nil
			},
		}
		rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		data, err := rt.Store.ReadFile(logPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != "xyz" {
			t.Fatalf("log data = %q, want %q", string(data), "xyz")
		}
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("log on disk: %v", err)
		}
		if len(fr.roundFileFromCalls) != 2 || fr.roundFileFromCalls[0] != 6 || fr.roundFileFromCalls[1] != 0 {
			t.Fatalf("roundFileFromCalls = %v, want [6, 0]", fr.roundFileFromCalls)
		}
	})

	t.Run("old server ignores from", func(t *testing.T) {
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}

		logPath := st.BuilderLogPath("api", 1)
		if err := st.WithLock(func(tx *store.Tx) error {
			return tx.PutRoundFile("api", 1, logPath, []byte("abcdef"))
		}); err != nil {
			t.Fatal(err)
		}

		fr := &fakeRemote{
			getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
			roundFileFromResp:  io.NopCloser(strings.NewReader("full body")),
			roundFileFromRange: remote.FileRange{Honored: false},
		}
		rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

		if _, err := reconcile(t, rt, b); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}

		data, err := rt.Store.ReadFile(logPath)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if string(data) != "full body" {
			t.Fatalf("log data = %q, want %q", string(data), "full body")
		}
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("log on disk: %v", err)
		}
	})
}

func TestMirrorLogKeepsALegacyFile(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("legacy content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:     remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp:  io.NopCloser(strings.NewReader("appended\n")),
		roundFileFromRange: remote.FileRange{Honored: true, From: 15, Size: 24},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if string(data) != "legacy content\nappended\n" {
		t.Fatalf("file content = %q, want %q", string(data), "legacy content\nappended\n")
	}

	// Verify no round_file row is written in the DB
	d, err := db.Open(st.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rec, ok, err := d.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: %v, %v", ok, err)
	}
	names, err := d.RoundFileList(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == filepath.Base(logPath) {
			t.Fatalf("RoundFileList contains %q, want no round_file row", n)
		}
	}
}

func TestMirrorDriftOnce(t *testing.T) {
	t.Cleanup(func() {
		mirrorDriftAttempts = sync.Map{}
	})
	mirrorDriftAttempts = sync.Map{}

	t.Run("fetches and caches drift", func(t *testing.T) {
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}

		driftCalls := 0
		fr := &fakeRemote{
			getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
			roundFileFromResp: io.NopCloser(strings.NewReader("")),
			roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
				if kind == "drift" {
					driftCalls++
					return io.NopCloser(strings.NewReader("drift diff\n")), nil
				}
				return io.NopCloser(strings.NewReader("")), nil
			},
		}
		rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

		// First poll: fetches drift
		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("reconcile 1: %v", err)
		}
		data, err := rt.Store.ReadFile(st.DriftPath("api", 1))
		if err != nil {
			t.Fatalf("ReadFile drift: %v", err)
		}
		if string(data) != "drift diff\n" {
			t.Fatalf("drift data = %q, want %q", string(data), "drift diff\n")
		}
		if driftCalls != 1 {
			t.Fatalf("driftCalls = %d, want 1", driftCalls)
		}

		// Second poll: makes no drift request
		if _, err := reconcile(t, rt, got); err != nil {
			t.Fatalf("reconcile 2: %v", err)
		}
		if driftCalls != 1 {
			t.Fatalf("driftCalls after poll 2 = %d, want 1", driftCalls)
		}
	})

	t.Run("404 asked once across two polls", func(t *testing.T) {
		mirrorDriftAttempts = sync.Map{}
		st := store.New(t.TempDir())
		b := remoteBinding("zen")
		b.Name = "api-404"
		b.Branch = "relevo/api-404"
		if err := st.Save(b); err != nil {
			t.Fatal(err)
		}

		driftCalls := 0
		fr := &fakeRemote{
			getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
			roundFileFromResp: io.NopCloser(strings.NewReader("")),
			roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
				if kind == "drift" {
					driftCalls++
					return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: remote.CodeNotFound, Message: "not found"}}
				}
				return io.NopCloser(strings.NewReader("")), nil
			},
		}
		rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

		// First poll: returns 404
		got, err := reconcile(t, rt, b)
		if err != nil {
			t.Fatalf("reconcile 1: %v", err)
		}
		if driftCalls != 1 {
			t.Fatalf("driftCalls = %d, want 1", driftCalls)
		}

		// Second poll: asked once across two polls (still 1)
		if _, err := reconcile(t, rt, got); err != nil {
			t.Fatalf("reconcile 2: %v", err)
		}
		if driftCalls != 1 {
			t.Fatalf("driftCalls after poll 2 = %d, want 1 (should not be asked again on 404)", driftCalls)
		}
	})
}

// TestObserveRemoteCopiesStalledSince pins #252's remote half: a running
// round's stall stamp rides from the server view onto the client binding,
// status shows it, and a later view without one clears it.

// TestObserveRemoteCopiesStalledSince pins #252's remote half: a running
// round's stall stamp rides from the server view onto the client binding,
// status shows it, and a later view without one clears it.
func TestObserveRemoteCopiesStalledSince(t *testing.T) {
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	stalled := baseTime.Add(-20 * time.Minute)
	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning, StalledSince: stalled},
		roundFileFromResp: io.NopCloser(strings.NewReader("builder log line 1\n")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !got.StalledSince.Equal(stalled) {
		t.Fatalf("StalledSince = %s, want the view's %s", got.StalledSince, stalled)
	}

	// Status reads the stamp and labels the remote row stalled.
	if err := st.Save(got); err != nil {
		t.Fatal(err)
	}
	rep, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("got %d bindings, want 1", len(rep.Bindings))
	}
	if status := rep.Bindings[0].BuilderStatus; !strings.HasPrefix(status, "stalled ") {
		t.Fatalf("BuilderStatus = %q, want it to start with %q", status, "stalled ")
	} else if !strings.Contains(status, view.AgeText(baseTime.Sub(stalled))) {
		t.Errorf("BuilderStatus = %q, want it to contain the age %q", status, view.AgeText(baseTime.Sub(stalled)))
	}

	// A later view with a zero stamp clears it.
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundRunning}
	got2, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (cleared): %v", err)
	}
	if !got2.StalledSince.IsZero() {
		t.Fatalf("StalledSince = %s, want zero once the view has none", got2.StalledSince)
	}
}

// TestObserveRemoteQueuedKeepsFacts pins #285's client tolerance: a queued
// view copies the server's queue facts onto Builder.RemoteQueue, sets
// RemoteStatus to "queued", and neither halts nor catches up.

// TestObserveRemoteQueuedKeepsFacts pins #285's client tolerance: a queued
// view copies the server's queue facts onto Builder.RemoteQueue, sets
// RemoteStatus to "queued", and neither halts nor catches up.
func TestObserveRemoteQueuedKeepsFacts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("contabo")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	since := baseTime.Add(-4 * time.Minute)
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundQueued,
			Queue:      &remote.QueueView{Position: 3, Ahead: 2, Running: 3, Cap: 3, Since: since},
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.RemoteStatus != string(remote.RoundQueued) {
		t.Fatalf("RemoteStatus = %q, want %q", got.Builder.RemoteStatus, remote.RoundQueued)
	}
	if got.Builder.RemoteQueue == nil {
		t.Fatal("RemoteQueue = nil, want the view's facts")
	}
	want := store.QueueFacts{Position: 3, Ahead: 2, Running: 3, Cap: 3, Since: since}
	if *got.Builder.RemoteQueue != want {
		t.Errorf("RemoteQueue = %+v, want %+v", *got.Builder.RemoteQueue, want)
	}
	if got.State == store.StateNeedsYou || got.State == store.StateBroken {
		t.Errorf("state = %s, want no halt while queued", got.State)
	}
}

// TestObserveRemoteRunningClearsQueue pins #285's clear rule: a binding that
// was queued and now observes a running view drops its stale RemoteQueue.

// TestObserveRemoteRunningClearsQueue pins #285's clear rule: a binding that
// was queued and now observes a running view drops its stale RemoteQueue.
func TestObserveRemoteRunningClearsQueue(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("contabo")
	b.Builder.RemoteStatus = string(remote.RoundQueued)
	b.Builder.RemoteQueue = &store.QueueFacts{Position: 1, Ahead: 0, Running: 2, Cap: 3, Since: baseTime.Add(-time.Minute)}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning},
		roundFileFromResp: io.NopCloser(strings.NewReader("")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.RemoteQueue != nil {
		t.Errorf("RemoteQueue = %+v, want nil once the round is running", got.Builder.RemoteQueue)
	}
}

func TestObserveRemoteRunningStoresLive(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("contabo")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	live := &remote.LiveView{
		At:             baseTime,
		PID:            4242,
		StartedAt:      baseTime.Add(-10 * time.Minute),
		ExitCode:       "3",
		Tail:           []string{"line1", "line2"},
		Usage:          &usage.Usage{Tokens: usage.Tokens{In: 100, Out: 50}},
		PriorTokens:    usage.Tokens{In: 400, Out: 150},
		Diff:           &remote.DiffStat{Files: 2, Added: 10, Removed: 3},
		LastProgressAt: baseTime.Add(-2 * time.Minute),
		ExploringSince: baseTime.Add(-5 * time.Minute),
		GatingSince:    baseTime.Add(-1 * time.Minute),
	}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundRunning,
			Live:       live,
		},
		roundFileFromResp: io.NopCloser(strings.NewReader("")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	rl := got.Builder.RemoteLive
	if rl == nil {
		t.Fatal("RemoteLive = nil, want non-nil")
	}
	if !rl.At.Equal(live.At) {
		t.Errorf("At = %v, want %v", rl.At, live.At)
	}
	if rl.PID != live.PID {
		t.Errorf("PID = %d, want %d", rl.PID, live.PID)
	}
	if !rl.StartedAt.Equal(live.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", rl.StartedAt, live.StartedAt)
	}
	if rl.ExitCode != live.ExitCode {
		t.Errorf("ExitCode = %q, want %q", rl.ExitCode, live.ExitCode)
	}
	if len(rl.Tail) != len(live.Tail) || rl.Tail[0] != live.Tail[0] || rl.Tail[1] != live.Tail[1] {
		t.Errorf("Tail = %v, want %v", rl.Tail, live.Tail)
	}
	if rl.Usage == nil || rl.Usage.Tokens != live.Usage.Tokens {
		t.Errorf("Usage = %+v, want %+v", rl.Usage, live.Usage)
	}
	if rl.PriorTokens != live.PriorTokens {
		t.Errorf("PriorTokens = %+v, want %+v", rl.PriorTokens, live.PriorTokens)
	}
	if rl.Diff == nil || rl.Diff.Files != live.Diff.Files || rl.Diff.Added != live.Diff.Added || rl.Diff.Removed != live.Diff.Removed {
		t.Errorf("Diff = %+v, want %+v", rl.Diff, live.Diff)
	}
	if !rl.LastProgressAt.Equal(live.LastProgressAt) {
		t.Errorf("LastProgressAt = %v, want %v", rl.LastProgressAt, live.LastProgressAt)
	}
	if !rl.ExploringSince.Equal(live.ExploringSince) {
		t.Errorf("ExploringSince = %v, want %v", rl.ExploringSince, live.ExploringSince)
	}
	if !rl.GatingSince.Equal(live.GatingSince) {
		t.Errorf("GatingSince = %v, want %v", rl.GatingSince, live.GatingSince)
	}
}

func TestObserveRemoteClosedClearsLive(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("contabo")
	b.Builder.RemoteStatus = string(remote.RoundRunning)
	b.Builder.RemoteLive = &store.LiveFacts{PID: 4242, At: baseTime}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundQueued},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Builder.RemoteLive != nil {
		t.Errorf("RemoteLive = %+v, want nil once the round is queued", got.Builder.RemoteLive)
	}
}

// TestReconcileRemoteRefreshesCandidate checks that a server-side switch
// (the round is now running a different candidate than this binding last
// recorded) updates the token and harness kind and logs a switch entry
// naming the server, and that a later tick whose view still names the same
// candidate adds nothing more (#100 step 2).

// TestReconcileRemoteRefreshesCandidate checks that a server-side switch
// (the round is now running a different candidate than this binding last
// recorded) updates the token and harness kind and logs a switch entry
// naming the server, and that a later tick whose view still names the same
// candidate adds nothing more (#100 step 2).
func TestReconcileRemoteRefreshesCandidate(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.BuilderCandidate = "claude/anthropic/haiku"
	b.Builder.Kind = "claude"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp:    remote.BindingView{RoundState: remote.RoundRunning, Candidate: "opencode/anthropic/sonnet"},
		roundFileFromResp: io.NopCloser(strings.NewReader("")),
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != "opencode/anthropic/sonnet" {
		t.Fatalf("BuilderCandidate = %q, want the server's candidate", got.BuilderCandidate)
	}
	if got.Builder.Kind != "opencode" {
		t.Fatalf("Builder.Kind = %q, want opencode", got.Builder.Kind)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	switches := 0
	var note string
	for _, e := range entries {
		if e.Kind == store.KindSwitch {
			switches++
			note = e.Note
		}
	}
	if switches != 1 {
		t.Fatalf("switch entries = %d, want 1", switches)
	}
	if !strings.Contains(note, "zen") || !strings.Contains(note, "claude/anthropic/haiku") || !strings.Contains(note, "opencode/anthropic/sonnet") {
		t.Fatalf("switch note = %q, want it to name the server and both tokens", note)
	}

	// Mutation target: compare view.Candidate against something other than
	// b.BuilderCandidate (or drop the comparison entirely) and a second,
	// identical view adds a second switch entry instead of nothing.
	got2, err := reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (second tick): %v", err)
	}
	if got2.BuilderCandidate != "opencode/anthropic/sonnet" {
		t.Fatalf("BuilderCandidate after second tick = %q, want unchanged", got2.BuilderCandidate)
	}
	entries2, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	switches2 := 0
	for _, e := range entries2 {
		if e.Kind == store.KindSwitch {
			switches2++
		}
	}
	if switches2 != 1 {
		t.Fatalf("switch entries after an identical second view = %d, want still 1", switches2)
	}
}

func TestReconcileRemoteNeedsYouHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundNeedsYou, Halt: "stuck at a dialog"},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if got.Halt != "stuck at a dialog" {
		t.Fatalf("Halt = %q, want the server's halt text", got.Halt)
	}
}

func TestReconcileRemoteUnreachableIsNotHalt(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000 // 1s budget; unreachableGrace (30m) is what keeps this from halting below
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.RemoteUnreachableSince.IsZero() {
		t.Fatal("RemoteUnreachableSince must be set once unreachable")
	}
	if got.Builder.RemoteStatus != "unreachable" {
		t.Fatalf("RemoteStatus = %q, want unreachable", got.Builder.RemoteStatus)
	}

	// Five minutes later: well past the 1s budget alone, but inside the 30m
	// grace, so this must not halt.
	got, err = reconcile(t, at(rt, 5*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("halted after 5m unreachable; the grace should have covered it")
	}
}

func TestReconcileRemoteUnreachablePastBudgetHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.RoundTimeoutMS = 1000 // 1s budget
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.AppendLog("api", store.LogEntry{Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan})
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: fmt.Errorf("%w: dial tcp: connection refused", client.ErrUnreachable)}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// Past the 1s budget plus the 30m unreachableGrace: this must halt.
	// Mutation target: drop "+ unreachableGrace" from the comparison in
	// reconcileRemote and this halt fires one tick early (or the
	// not-halt test above starts halting instead).
	got, err = reconcile(t, at(rt, 31*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you past budget+grace", got.State)
	}
	if !strings.Contains(got.Halt, "unreachable for") || !strings.Contains(got.Halt, "may still be running there") {
		t.Fatalf("Halt = %q, want the unreachable-past-budget halt", got.Halt)
	}
}

func TestReconcileRemote401Halts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: "revoked", Message: "key revoked"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on 401", got.State)
	}
	if !strings.Contains(got.Halt, "key revoked") {
		t.Fatalf("Halt = %q, want the server's message", got.Halt)
	}
}

// TestReconcileRemote401StaleIsTransientWithoutGrace pins #373 §4.6's client
// half for a CLI one-shot: a stale 401 with no AuthGrace is shown in
// RemoteStatus and never halts, because a one-shot has no clock to measure a
// grace against.
func TestReconcileRemote401StaleIsTransientWithoutGrace(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: remote.CodeStale, Message: "stale signature"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("halted on a transient 401 with no AuthGrace (%s)", got.Halt)
	}
	if got.Builder.RemoteStatus != "auth: stale" {
		t.Fatalf("RemoteStatus = %q, want auth: stale", got.Builder.RemoteStatus)
	}
}

// TestReconcileRemote401StaleHaltsAfterGrace pins the daemon's rule: the first
// sight of a stale 401 does not halt, the same error 16 minutes later does,
// and the halt names the code, how long it persisted and the clock hint.
//
// Mutation: make a non-revoked 401 halt immediately and this fails on the
// first sight.
func TestReconcileRemote401StaleHaltsAfterGrace(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: remote.CodeStale, Message: "stale signature"}}}
	rt := Runtime{
		Store: st, Remote: fr,
		Now:       func() time.Time { return baseTime },
		AuthGrace: NewAuthGrace(),
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("halted on the first sight of a stale 401 (%s)", got.Halt)
	}
	if got.Builder.RemoteStatus != "auth: stale" {
		t.Fatalf("RemoteStatus = %q, want auth: stale", got.Builder.RemoteStatus)
	}

	got, err = reconcile(t, at(rt, 16*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you after 16m of stale auth", got.State)
	}
	if !strings.Contains(got.Halt, "stale for 16m") {
		t.Fatalf("Halt = %q, want the code and how long it persisted", got.Halt)
	}
	if !strings.Contains(got.Halt, "check this machine's clock and relevo config server list") {
		t.Fatalf("Halt = %q, want the clock and relevo config server list hint", got.Halt)
	}
}

// TestReconcileRemote401GraceResetsOnSuccess pins the other half of the grace:
// a poll that gets through forgets it, so a later transient 401 starts its own
// 15 minutes rather than inheriting the old clock.
func TestReconcileRemote401GraceResetsOnSuccess(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: remote.CodeStale, Message: "stale signature"}}}
	rt := Runtime{
		Store: st, Remote: fr,
		Now:       func() time.Time { return baseTime },
		AuthGrace: NewAuthGrace(),
	}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile (stale): %v", err)
	}

	// A success 16m later would have been past the limit had the grace kept
	// running; it clears instead, and the stale that follows is a fresh sight.
	fr.getBindingErr = nil
	fr.getBindingResp = remote.BindingView{RoundState: remote.RoundIdle}
	got, err := reconcile(t, at(rt, 16*time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile (success): %v", err)
	}

	fr.getBindingErr = &client.HTTPError{Status: 401, Body: remote.ErrorBody{Code: remote.CodeStale, Message: "stale signature"}}
	got, err = reconcile(t, at(rt, 17*time.Minute), got)
	if err != nil {
		t.Fatalf("Reconcile (stale again): %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("halted on a stale 401 one minute after a success (%s)", got.Halt)
	}
	if got.Builder.RemoteStatus != "auth: stale" {
		t.Fatalf("RemoteStatus = %q, want auth: stale", got.Builder.RemoteStatus)
	}
}

func TestReconcileRemote404Halts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "gone"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you on 404", got.State)
	}
	if !strings.Contains(got.Halt, "binding removed by the server admin") {
		t.Fatalf("Halt = %q, want the removed-by-admin message", got.Halt)
	}
}

// TestSyncRemoteCollectsClosedRoundWithoutDelivery checks that SyncRemote
// (the read path `relevo status`/`pull`/`wait` share, spec §2.2) collects a
// closed round -- the report entry lands, pending -- without ever
// delivering it to a planner pane.

// TestSyncRemoteCollectsClosedRoundWithoutDelivery checks that SyncRemote
// (the read path `relevo status`/`pull`/`wait` share, spec §2.2) collects a
// closed round -- the report entry lands, pending -- without ever
// delivering it to a planner pane.
func TestSyncRemoteCollectsClosedRoundWithoutDelivery(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 1 {
		t.Fatalf("synced = %d, want 1", synced)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			found = true
			if e.Confirmed {
				t.Fatal("report entry must still be pending: SyncRemote must never deliver it")
			}
		}
	}
	if !found {
		t.Fatal("no report entry after SyncRemote collected the closed round")
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	// Mutation target: have SyncRemote call reconcileRemote instead of
	// observeRemote, and this assertion fails: a delivering tick would move
	// the binding off ACTIVE.
	if got.State != store.StateActive {
		t.Fatalf("state = %s, want active: SyncRemote must not deliver", got.State)
	}
}

// TestSyncRemoteSkipsDoneAndLocal checks that SyncRemote never touches the
// network for a binding it should not sync: one already DONE, and one that
// is not a remote binding at all.

// TestSyncRemoteSkipsDoneAndLocal checks that SyncRemote never touches the
// network for a binding it should not sync: one already DONE, and one that
// is not a remote binding at all.
func TestSyncRemoteSkipsDoneAndLocal(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())

	doneRemote := remoteBinding("zen")
	doneRemote.Name = "done-remote"
	doneRemote.State = store.StateDone
	if err := st.Save(doneRemote); err != nil {
		t.Fatal(err)
	}

	local := store.Binding{
		Name: "local", CWD: "/fake/repo", State: store.StateActive,
		Planner: store.Endpoint{PaneID: "w2:p3"}, Builder: store.Endpoint{PaneID: "w2:p4"},
	}
	if err := st.Save(local); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 0 {
		t.Fatalf("synced = %d, want 0: a DONE remote binding and a local binding must both be skipped", synced)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("fakeRemote calls = %v, want none: SyncRemote must not touch the network for a skipped binding", fr.calls)
	}
}

// TestSyncRemoteSkipsPausedBinding checks that a paused remote binding is
// skipped like a DONE one: a paused binding is not being relayed, and the
// daemon's Reconcile skips it too.
func TestSyncRemoteSkipsPausedBinding(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.State = store.StatePaused
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, err := SyncRemote(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}
	if synced != 0 {
		t.Fatalf("synced = %d, want 0: a paused binding must be skipped", synced)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "GetBinding:") {
			t.Fatalf("fakeRemote calls = %v, want no GetBinding for a paused binding", fr.calls)
		}
	}
}

// TestSyncRemoteUnlessDaemonSkipsWhileDaemonRuns checks that a held daemon
// lock makes the read verbs leave remote sync entirely to the daemon: no
// network call, no state lock.
func TestSyncRemoteUnlessDaemonSkipsWhileDaemonRuns(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	lock, err := st.AcquireDaemonLock()
	if err != nil {
		t.Fatalf("AcquireDaemonLock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	synced, skipped, err := SyncRemoteUnlessDaemon(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemoteUnlessDaemon: %v", err)
	}
	if !skipped {
		t.Fatal("skipped = false, want true while a daemon holds the lock")
	}
	if synced != 0 {
		t.Fatalf("synced = %d, want 0", synced)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("fakeRemote calls = %v, want none: a running daemon means no network", fr.calls)
	}
}

// TestSyncRemoteUnlessDaemonSyncsWithoutDaemon checks that with no daemon
// lock the read verbs sync as before: the poll is the only collector.
func TestSyncRemoteUnlessDaemonSyncsWithoutDaemon(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingResp: remote.BindingView{RoundState: remote.RoundRunning}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	_, skipped, err := SyncRemoteUnlessDaemon(context.Background(), rt)
	if err != nil {
		t.Fatalf("SyncRemoteUnlessDaemon: %v", err)
	}
	if skipped {
		t.Fatal("skipped = true, want false with no daemon running")
	}
	found := false
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "GetBinding:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("fakeRemote calls = %v, want a GetBinding call", fr.calls)
	}
}

// stoppedRoundRemote is a remote whose closed round 1 the server says was
// stopped. Its report, diff, log and stream files all answer 404: a stopped
// builder that wrote nothing. dirtyCommit rides the view when non-empty.
func stoppedRoundRemote(stopped, dirtyCommit string) *fakeRemote {
	return &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			Stopped: stopped, DirtyCommit: dirtyCommit,
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
}

// TestCatchUpStoppedNoReport pins #344: a closed round the server says was
// stopped, with no report file, closes the client's round instead of halting
// the binding, queues the stopped payload, and files the stopped KindStop
// entry for the stopped round.
//
// Mutation check: make the report-404 halt ignore view.Stopped and this fails
// on the binding halting.
func TestCatchUpStoppedNoReport(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: stoppedRoundRemote("killed", ""), Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("binding halted (%s); a stopped close must not halt", got.Halt)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2: the round closed", got.Round)
	}

	pending, found, err := st.PendingForPlanner("api")
	if err != nil || !found {
		t.Fatalf("report must be pending: found=%v err=%v", found, err)
	}
	wantText := "The runner was stopped (killed) for round 1 on zen; no report was written."
	if !strings.Contains(pending.Payload, wantText) {
		t.Errorf("payload = %q, want it to carry %q", pending.Payload, wantText)
	}
	if !strings.Contains(pending.Note, "noreport stopped") {
		t.Errorf("note = %q, want noreport stopped", pending.Note)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	foundStop := false
	for _, e := range entries {
		if e.Kind == store.KindStop && e.Round == 1 && e.Note == "stopped/killed" {
			foundStop = true
		}
	}
	if !foundStop {
		t.Errorf("no stopped/killed KindStop entry for round 1: %+v", entries)
	}
}

// TestCatchUpStoppedWithReport is TestCatchUpStoppedNoReport with a report
// file on the server: the payload names it and the note says stopped, not
// noreport stopped.
func TestCatchUpStoppedWithReport(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := stoppedRoundRemote("killed", "")
	fr.roundFileFunc = func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
		if kind == "report" {
			return io.NopCloser(strings.NewReader("I stopped where I was\n")), nil
		}
		return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	pending, found, err := st.PendingForPlanner("api")
	if err != nil || !found {
		t.Fatalf("report must be pending: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Payload, ". Report: ") {
		t.Errorf("payload = %q, want it to name the report", pending.Payload)
	}
	if !strings.Contains(pending.Note, "stopped") {
		t.Errorf("note = %q, want it to say stopped", pending.Note)
	}
	if strings.Contains(pending.Note, "noreport") {
		t.Errorf("note = %q, must not say noreport when the report was on disk", pending.Note)
	}
}

// TestCatchUpNotStoppedNoReportHalts is the regression guard: a round the
// server does not say was stopped, with no report file, halts exactly as it
// did before #344.
func TestCatchUpNotStoppedNoReportHalts(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: stoppedRoundRemote("", ""), Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you: a reportless close that was not stopped must halt", got.State)
	}
	if !strings.Contains(got.Halt, "closed round 1 without a report file") {
		t.Errorf("Halt = %q, want the existing reportless-close text", got.Halt)
	}
}

// TestCatchUpStoppedDirtyNote pins #344's note join: a stopped round that
// also carried uncommitted work keeps both facts, where the dirty clause used
// to replace the note.
func TestCatchUpStoppedDirtyNote(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: stoppedRoundRemote("killed", "c0ffee"), Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	pending, found, err := st.PendingForPlanner("api")
	if err != nil || !found {
		t.Fatalf("report must be pending: found=%v err=%v", found, err)
	}
	if !strings.Contains(pending.Note, "stopped") {
		t.Errorf("note = %q, want it to say stopped", pending.Note)
	}
	if !strings.Contains(pending.Note, "uncommitted work at") {
		t.Errorf("note = %q, want it to carry the uncommitted-work clause too", pending.Note)
	}
}

// newRemoteClientRepo creates a client-side repo (what a remote binding's
// Repo points at day to day) with one commit and a "relevo/<name>" branch ref
// at that commit -- not checked out, matching the ordinary case where the
// planner's own repo sits on its own branch.

func TestCatchUpOrderAndIdempotence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	rt := Runtime{
		Store:     st,
		Remote:    fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}

	// First tick: the server hands back a bundle that carries an extra ref
	// the client never allowed (view.DirtyCommit == "", so only the branch
	// itself is permitted). A real Absorb genuinely fails on this --
	// ErrUnexpectedRef -- which is what lets this test tell "Ack moved
	// before Absorb" apart from "Ack never runs at all": the RPC round-trip
	// (RoundFile, RoundBundle) succeeds, catchUp actually reaches Absorb, and
	// only Absorb itself rejects the bundle.
	badResultDir := t.TempDir()
	runGit(t, badResultDir, "clone", clientRepo, ".")
	runGit(t, badResultDir, "checkout", "relevo/api")
	if err := os.WriteFile(filepath.Join(badResultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, badResultDir, "add", "result.txt")
	runGit(t, badResultDir, "commit", "-m", "round result")
	runGit(t, badResultDir, "branch", "extra", "HEAD")
	badBundleTransport := remote.NewBundleTransport(g, t.TempDir())
	badSnap, err := badBundleTransport.Snapshot(ctx, badResultDir, []string{"refs/heads/relevo/api", "refs/heads/extra"}, c1)
	if err != nil {
		t.Fatalf("Snapshot (bad): %v", err)
	}
	if badSnap.Empty || badSnap.Body == nil {
		t.Fatal("expected a non-empty bad bundle")
	}
	fr.roundBundleResp = badSnap.Body

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile (absorb fails): %v", err)
	}
	if got.Builder.LastKnown != "" {
		t.Fatalf("LastKnown = %q after a failed absorb, want empty", got.Builder.LastKnown)
	}
	if got.RemoteAbsorbFailures != 1 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 1", got.RemoteAbsorbFailures)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d after a failed absorb, want unchanged 1", got.Round)
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			t.Fatalf("Ack called before a successful absorb: %v", fr.calls)
		}
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Kind == store.KindReport {
			t.Fatalf("a report entry exists after a failed absorb: %+v", e)
		}
	}

	// Next tick: a clean bundle carrying only the allowed ref, so the absorb
	// succeeds.
	bundle, headSHA := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relevo/api", c1)
	fr.roundBundleResp = bundle
	fr.getBindingResp.ResultCommit = headSHA

	got, err = reconcile(t, rt, got)
	if err != nil {
		t.Fatalf("Reconcile (bundle succeeds): %v", err)
	}
	if got.Builder.LastKnown != headSHA {
		t.Fatalf("LastKnown = %q, want %q", got.Builder.LastKnown, headSHA)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0 after success", got.RemoteAbsorbFailures)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}
	foundAck := false
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			foundAck = true
		}
	}
	if !foundAck {
		t.Fatalf("Ack never called after a successful absorb: %v", fr.calls)
	}

	entries, err = st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	reportCount := 0
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportCount++
		}
	}
	if reportCount != 1 {
		t.Fatalf("report entries = %d, want exactly 1", reportCount)
	}

	branchHead, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/relevo/api")
	if err != nil || !ok || branchHead != headSHA {
		t.Fatalf("client branch after absorb: got (%q, %v, %v), want (%q, true, nil)", branchHead, ok, err, headSHA)
	}
}

// TestObserveRemoteIdleCatchUpRecoversLostReport pins #373 §4.5's Idle
// catch-up: the server says Idle with this round closed and the client holds
// no report entry, so the round's report is collected and acked again -- the
// recovery for an /ack reply that never reached the client.
//
// Mutation: drop the Idle catch-up and no report entry or Ack appears.
func TestObserveRemoteIdleCatchUpRecoversLostReport(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundIdle, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	if !slices.Contains(fr.calls, "Ack:zen:api:1") {
		t.Fatalf("Ack was not called after an Idle catch-up: %v", fr.calls)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("no round 1 report entry after the Idle catch-up: %+v", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round was caught up", got.Round)
	}
}

// TestObserveRemoteIdleWithReportDoesNotCatchUp is the guard beside it: a
// report entry already on disk means the ack (or the local bookkeeping) got
// through, so an Idle view must not collect the round a second time.
func TestObserveRemoteIdleWithReportDoesNotCatchUp(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("api", store.LogEntry{
		Round: 1, Direction: store.DirToPlanner, Kind: store.KindReport, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundIdle, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
		},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(context.Background(), rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") || strings.HasPrefix(c, "RoundFile:") {
			t.Fatalf("a round with a report entry was caught up again: %v", fr.calls)
		}
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want 1: the round must not close twice", got.Round)
	}
}

func TestCatchUpDirtyNote(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")

	// Build the round's result: relevo/api advances to c2, and a dirty side
	// ref (round-1) sits on top of it, exactly as a real dirty close does.
	resultDir := t.TempDir()
	runGit(t, resultDir, "clone", clientRepo, ".")
	runGit(t, resultDir, "checkout", "relevo/api")
	if err := os.WriteFile(filepath.Join(resultDir, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, resultDir, "add", "result.txt")
	runGit(t, resultDir, "commit", "-m", "round result")
	c2 := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runGit(t, resultDir, "rev-parse", "HEAD^{tree}"))
	c3, err := g.CommitTree(ctx, resultDir, tree, c2, "[relevo] api: round 1, uncommitted work")
	if err != nil {
		t.Fatalf("CommitTree: %v", err)
	}
	sideRef := "refs/relevo/api/round-1"
	if err := g.UpdateRef(ctx, resultDir, sideRef, c3, ""); err != nil {
		t.Fatalf("UpdateRef side: %v", err)
	}

	transport := remote.NewBundleTransport(g, t.TempDir())
	snap, err := transport.Snapshot(ctx, resultDir, []string{"refs/heads/relevo/api", sideRef}, c1)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Empty || snap.Body == nil {
		t.Fatal("expected a non-empty bundle")
	}

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1, DirtyCommit: c3, ResultCommit: c2},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleResp: snap.Body,
	}
	rt := Runtime{
		Store: st, Remote: fr, Transport: transport,
		Now: func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2", got.Round)
	}
	if got.Builder.LastKnown != c2 {
		t.Fatalf("LastKnown = %q, want %q", got.Builder.LastKnown, c2)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportNote string
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportNote = e.Note
			found = true
		}
	}
	if !found || !strings.Contains(reportNote, "uncommitted work at "+sideRef) {
		t.Fatalf("report note = %q (found=%v), want it to name %s", reportNote, found, sideRef)
	}

	sideSHA, ok, err := g.RefSHA(ctx, clientRepo, sideRef)
	if err != nil || !ok || sideSHA != c3 {
		t.Fatalf("client side ref: got (%q, %v, %v), want (%q, true, nil)", sideSHA, ok, err, c3)
	}
}

// TestCatchUpWritesDiffEntryFromView checks that catchUp writes the
// client's own diff log entry from the server's view facts (Note, Commits,
// Tree, plus the downloaded patch as Path) rather than letting queueReport
// capture its own diff from a local baseline that a remote binding never
// has.

// TestCatchUpWritesDiffEntryFromView checks that catchUp writes the
// client's own diff log entry from the server's view facts (Note, Commits,
// Tree, plus the downloaded patch as Path) rather than letting queueReport
// capture its own diff from a local baseline that a remote binding never
// has.
func TestCatchUpWritesDiffEntryFromView(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	// Mutation target: skip writing the diff entry in catchUp, and
	// queueReport falls back to its own CaptureRoundDiff, which either
	// calls fakeGit.SnapshotTree (raising this count above 0) or, since a
	// remote binding never carries a local RoundBaselineTree, writes a
	// diff entry whose note reads "unavailable: no baseline" instead of
	// the server's own note.
	if fg.snapshotCalls != 0 {
		t.Fatalf("fakeGit.SnapshotTree called %d times, want 0: queueReport must not capture its own diff", fg.snapshotCalls)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var diffEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindDiff {
			diffEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no diff entry written")
	}
	if diffEntry.Note != "1 file, +1 -0; 1 commit, clean" {
		t.Fatalf("diff entry Note = %q, want the server's own note", diffEntry.Note)
	}
	if diffEntry.Commits != 1 {
		t.Fatalf("diff entry Commits = %d, want 1", diffEntry.Commits)
	}
	if diffEntry.Tree != "clean" {
		t.Fatalf("diff entry Tree = %q, want clean", diffEntry.Tree)
	}
	if diffEntry.Path != st.DiffPath("api", 1) {
		t.Fatalf("diff entry Path = %q, want the downloaded patch's path %q", diffEntry.Path, st.DiffPath("api", 1))
	}
	if !diffEntry.Confirmed {
		t.Fatal("diff entry must be Confirmed, like every other relevo-bookkeeping entry")
	}
	if _, err := os.Stat(st.DiffPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("expected no patch file on disk, got err %v", err)
	}
	diffBody, err := st.ReadFile(st.DiffPath("api", 1))
	if err != nil {
		t.Fatalf("read stored diff: %v", err)
	}
	if string(diffBody) != "--- a/file\n+++ b/file\n" {
		t.Fatalf("diff body = %q, want %q", string(diffBody), "--- a/file\n+++ b/file\n")
	}

	var reportEntry store.LogEntry
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry = e
		}
	}
	if !strings.Contains(reportEntry.Payload, "Diff:") {
		t.Fatalf("report payload = %q, want a Diff: line from capture.DiffLineFromNote", reportEntry.Payload)
	}
}

// TestCatchUpAppendsPathsLineFromView pins #216's remote half: when the
// server's DiffNote carries the "paths: report N, diff M" clause, catchUp's
// queued payload repeats it as a Paths: line right after its Diff: line. A
// note without the clause adds no line.
func TestCatchUpAppendsPathsLineFromView(t *testing.T) {
	t.Parallel()

	const joinedNote = "24 files, +1 -2; 1 commit on relevo/x, tree clean paths: report 0, diff 24"

	cases := []struct {
		name     string
		diffNote string
		wantLine string
	}{
		{"note carries the clause", joinedNote, capture.PathsLine(0, 24)},
		{"note without the clause", "1 file, +1 -0; 1 commit, clean", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			b := remoteBinding("zen")
			if err := st.Save(b); err != nil {
				t.Fatal(err)
			}

			fr := &fakeRemote{
				getBindingResp: remote.BindingView{
					RoundState: remote.RoundClosed, ClosedRound: 1,
					DiffNote: tc.diffNote, DiffCommits: 1, DiffTree: "clean",
				},
				roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
					switch kind {
					case "report":
						return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
					case "diff":
						return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
					default:
						return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
					}
				},
			}
			fg := &fakeGit{}
			rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

			if _, err := reconcile(t, rt, b); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}

			entries, err := st.ReadLog("api")
			if err != nil {
				t.Fatal(err)
			}
			var reportEntry store.LogEntry
			for _, e := range entries {
				if e.Kind == store.KindReport {
					reportEntry = e
				}
			}
			if tc.wantLine == "" {
				if strings.Contains(reportEntry.Payload, "Paths:") {
					t.Fatalf("payload = %q, want no Paths: line", reportEntry.Payload)
				}
				return
			}
			if !strings.Contains(reportEntry.Payload, tc.wantLine) {
				t.Fatalf("payload = %q, want it to contain %q", reportEntry.Payload, tc.wantLine)
			}
			if diffAt, lineAt := strings.Index(reportEntry.Payload, "Diff:"), strings.Index(reportEntry.Payload, tc.wantLine); lineAt < diffAt {
				t.Fatalf("payload = %q, want the Paths: line after the Diff: line", reportEntry.Payload)
			}
		})
	}
}

// TestCatchUpAdoptedBranchAbsorbsServerRef pins the adopted-branch fix
// (#274): `add --server --branch feature/x` adopts a branch relevo did not
// create, but the server still cuts its own branch and the round bundle it
// ships always names refs/heads/relevo/<name> (handleRoundBundle snapshots
// refs/heads/<server branch>, and the server's branch is relevo/<name>).
// Catch-up must allow the server's ref through Absorb, then fast-forward the
// adopted branch to it, so the planner's own branch is the one carrying the
// round's result.

// TestCatchUpAdoptedBranchAbsorbsServerRef pins the adopted-branch fix
// (#274): `add --server --branch feature/x` adopts a branch relevo did not
// create, but the server still cuts its own branch and the round bundle it
// ships always names refs/heads/relevo/<name> (handleRoundBundle snapshots
// refs/heads/<server branch>, and the server's branch is relevo/<name>).
// Catch-up must allow the server's ref through Absorb, then fast-forward the
// adopted branch to it, so the planner's own branch is the one carrying the
// round's result.
func TestCatchUpAdoptedBranchAbsorbsServerRef(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)

	// The client repo: an adopted branch feature/x at base. relevo creates no
	// local relevo/<name> branch when it adopts feature/x, so the round bundle
	// is cut here from refs/heads/relevo/api -- the server's own branch name,
	// this repo standing in for the server repo the bundle really comes from.
	// Catch-up absorbs that ref (moving it here) and then fast-forwards the
	// adopted feature/x to it.
	clientRepo := t.TempDir()
	runGit(t, clientRepo, "init")
	if err := os.WriteFile(filepath.Join(clientRepo, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientRepo, "add", "seed.txt")
	runGit(t, clientRepo, "commit", "-m", "seed")
	c1 := strings.TrimSpace(runGit(t, clientRepo, "rev-parse", "HEAD"))
	runGit(t, clientRepo, "update-ref", "refs/heads/feature/x", c1)
	runGit(t, clientRepo, "update-ref", "refs/heads/relevo/api", c1)

	bundle, c2 := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relevo/api", c1)

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Branch = "feature/x"
	b.ExistingBranch = true
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1, ResultCommit: c2,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
		roundBundleResp: bundle,
	}
	trans := &recordingTransport{inner: remote.NewBundleTransport(g, t.TempDir())}
	rt := Runtime{
		Store: st, Remote: fr, Transport: trans, Git: g,
		Now: func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Mutation target: allow refs/heads/<b.Branch> instead of the server's
	// ref and this reads "unexpected ref: refs/heads/relevo/api" -- the
	// bundle is rejected, nothing is absorbed, and no ref moves below.
	if trans.err != nil {
		t.Fatalf("Absorb rejected the server's bundle: %v", trans.err)
	}
	if moved := trans.moved["refs/heads/relevo/api"]; moved != c2 {
		t.Fatalf("Absorb moved refs/heads/relevo/api to %q, want the bundle tip %q", moved, c2)
	}
	if got.State == store.StateNeedsYou {
		t.Fatal("the binding needs you after catch-up, want the round absorbed")
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}
	if got.Builder.LastKnown != c2 {
		t.Fatalf("LastKnown = %q, want the bundle tip %q", got.Builder.LastKnown, c2)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0: the server's ref must absorb, not fail", got.RemoteAbsorbFailures)
	}

	adoptedSHA, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/feature/x")
	if err != nil || !ok || adoptedSHA != c2 {
		t.Fatalf("refs/heads/feature/x: got (%q, %v, %v), want (%q, true, nil): the adopted branch must be fast-forwarded to the round's result", adoptedSHA, ok, err, c2)
	}
	serverSHA, ok, err := g.RefSHA(ctx, clientRepo, "refs/heads/relevo/api")
	if err != nil || !ok || serverSHA != c2 {
		t.Fatalf("refs/heads/relevo/api: got (%q, %v, %v), want (%q, true, nil): the server's own ref stays beside the adopted branch", serverSHA, ok, err, c2)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	foundReport := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			foundReport = true
		}
	}
	if !foundReport {
		t.Fatal("no report entry queued after catch-up")
	}
}

func TestCatchUpFetchesStream(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	streamBody := "{\"type\":\"assistant\"}\n{\"type\":\"error\",\"message\":\"boom\"}\n"
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "stream":
				return io.NopCloser(strings.NewReader(streamBody)), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(st.BuilderStreamPath("api", 1))
	if err != nil {
		t.Fatalf("read stream file: %v", err)
	}
	if string(got) != streamBody {
		t.Fatalf("stream file = %q, want %q", string(got), streamBody)
	}
	found := false
	for _, c := range fr.calls {
		if c == "RoundFile:zen:api:1:stream" {
			found = true
		}
	}
	if !found {
		t.Fatalf("catch-up never fetched the stream file: %v", fr.calls)
	}
}

func TestCatchUpStreamMissingIsFine(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("a missing stream file must not halt: %+v", got)
	}
	if _, err := os.Stat(st.BuilderStreamPath("api", 1)); !os.IsNotExist(err) {
		t.Fatalf("stream file exists (stat err = %v), want none", err)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	hasReport := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			hasReport = true
		}
	}
	if !hasReport {
		t.Fatal("no report entry: catch-up must still complete")
	}
}

func TestCatchUpWritesTheLogAsARow(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	// Seed a stale mirror row
	if err := st.WithLock(func(tx *store.Tx) error {
		return tx.PutRoundFile("api", 1, logPath, []byte("stale mirror row\n"))
	}); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "log":
				return io.NopCloser(strings.NewReader("final round 1 log\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := rt.Store.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "final round 1 log\n" {
		t.Fatalf("log = %q, want final round 1 log", string(got))
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("log file exists on disk: %v", err)
	}
}

func TestCatchUpKeepsALegacyLogFile(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	logPath := st.BuilderLogPath("api", 1)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("legacy local log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "log":
				return io.NopCloser(strings.NewReader("catchUp overwrite\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	if _, err := reconcile(t, rt, b); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if string(got) != "catchUp overwrite\n" {
		t.Fatalf("log = %q, want catchUp overwrite", string(got))
	}

	// Verify no round_file row is written in the DB
	d, err := db.Open(st.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	rec, ok, err := d.RecordGet("", "api")
	if err != nil || !ok {
		t.Fatalf("RecordGet: %v, %v", ok, err)
	}
	names, err := d.RoundFileList(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == filepath.Base(logPath) {
			t.Fatalf("RoundFileList contains %q, want no round_file row", n)
		}
	}
}

// TestCatchUpKeepsServerUsage checks that catch-up keeps the usage the
// server measured and shipped with the round (#216): the client's report
// entry stores it verbatim instead of re-measuring a round whose record
// lives on the server.

// TestCatchUpKeepsServerUsage checks that catch-up keeps the usage the
// server measured and shipped with the round (#216): the client's report
// entry stores it verbatim instead of re-measuring a round whose record
// lives on the server.
func TestCatchUpKeepsServerUsage(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	sent := usage.Usage{
		Harness: "opencode",
		Model:   "haiku",
		Tokens:  usage.Tokens{In: 1000, Out: 200},
		Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
	}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
			Usage: &sent,
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.Usage == nil {
		t.Fatal("report entry Usage = nil, want the server's figure")
	}
	if *reportEntry.Usage != sent {
		t.Fatalf("report entry Usage = %+v, want the server's figure %+v", *reportEntry.Usage, sent)
	}
	// The server's round is not re-measured here: the client's own record
	// of a shared-cwd read would be wrong, and its note must not say
	// otherwise.
	if strings.Contains(reportEntry.Usage.Note, "shared cwd") {
		t.Fatalf("Usage.Note = %q, must not mention a shared cwd", reportEntry.Usage.Note)
	}
}

func TestCatchUpRecordsPriorTokens(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	prior := usage.Tokens{In: 4000, Out: 1500}
	sent := usage.Usage{
		Harness: "opencode",
		Model:   "haiku",
		Tokens:  usage.Tokens{In: 1000, Out: 200},
		Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
	}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
			Usage:       &sent,
			PriorTokens: &prior,
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.PriorTokens == nil {
		t.Fatal("report entry PriorTokens = nil, want the server's figure")
	}
	if *reportEntry.PriorTokens != prior {
		t.Errorf("report entry PriorTokens = %+v, want %+v", *reportEntry.PriorTokens, prior)
	}
	clientPrior := view.PriorTokensOf(entries, 1)
	if clientPrior != prior {
		t.Errorf("view.PriorTokensOf(entries, 1) = %+v, want %+v", clientPrior, prior)
	}
}

// TestCatchUpRecordsRusage extends TestCatchUpKeepsServerUsage's pattern:
// the server's cgroup measurement rides the same view as Usage, and lands
// on the report entry catchUp writes (#244, #216).

// TestCatchUpRecordsRusage extends TestCatchUpKeepsServerUsage's pattern:
// the server's cgroup measurement rides the same view as Usage, and lands
// on the report entry catchUp writes (#244, #216).
func TestCatchUpRecordsRusage(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	sentRusage := store.Rusage{CPUMS: 12300, PeakMemBytes: 850 << 20}
	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
			Rusage: &sentRusage,
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	fg := &fakeGit{}
	rt := Runtime{Store: st, Remote: fr, Git: fg, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.Rusage == nil {
		t.Fatal("report entry Rusage = nil, want the server's figure")
	}
	if *reportEntry.Rusage != sentRusage {
		t.Fatalf("report entry Rusage = %+v, want the server's figure %+v", *reportEntry.Rusage, sentRusage)
	}
}

// TestCatchUpPreUsageServerNotes checks the honest answer for a server
// built before this (#216): with no usage in the view, the client's report
// entry says the server sent none -- basis unknown -- rather than reading
// a record the client does not have.

// TestCatchUpPreUsageServerNotes checks the honest answer for a server
// built before this (#216): with no usage in the view, the client's report
// entry says the server sent none -- basis unknown -- rather than reading
// a record the client does not have.
func TestCatchUpPreUsageServerNotes(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState: remote.RoundClosed, ClosedRound: 1,
			DiffNote: "1 file, +1 -0; 1 commit, clean", DiffCommits: 1, DiffTree: "clean",
		},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			switch kind {
			case "report":
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			case "diff":
				return io.NopCloser(strings.NewReader("--- a/file\n+++ b/file\n")), nil
			default:
				return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
			}
		},
	}
	rt := Runtime{Store: st, Remote: fr, Git: &fakeGit{}, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 2 {
		t.Fatalf("Round = %d, want 2 after the round closed", got.Round)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var reportEntry store.LogEntry
	found := false
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reportEntry, found = e, true
		}
	}
	if !found {
		t.Fatal("no report entry written")
	}
	if reportEntry.Usage == nil {
		t.Fatal("report entry Usage = nil, want an honest unknown")
	}
	if reportEntry.Usage.Cost.Basis != usage.Unknown {
		t.Fatalf("Usage.Cost.Basis = %q, want unknown", reportEntry.Usage.Cost.Basis)
	}
	if reportEntry.Usage.Note != "remote: server sent no usage" {
		t.Fatalf("Usage.Note = %q, want %q", reportEntry.Usage.Note, "remote: server sent no usage")
	}
}

func TestCatchUpBranchCheckedOutRetries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")
	// Unlike the ordinary case, check the binding's own branch out as this
	// repo's current branch, which is what makes the fetch below collide.
	runGit(t, clientRepo, "checkout", "relevo/api")

	bundle, _ := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relevo/api", c1)

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleResp: bundle,
	}
	rt := Runtime{
		Store: st, Remote: fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.Round != 1 {
		t.Fatalf("Round = %d, want unchanged 1 (retried, not halted)", got.Round)
	}
	if got.Builder.LastKnown != "" {
		t.Fatalf("LastKnown = %q, want empty: the absorb never happened", got.Builder.LastKnown)
	}
	if got.RemoteAbsorbFailures != 0 {
		t.Fatalf("RemoteAbsorbFailures = %d, want 0: a checked-out branch is retried, not counted as a failure", got.RemoteAbsorbFailures)
	}
	if got.State == store.StateNeedsYou {
		t.Fatal("a checked-out branch must retry quietly, not halt")
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Ack:") {
			t.Fatalf("Ack called despite the absorb never completing: %v", fr.calls)
		}
	}
}

func TestCatchUpBranchCheckedOutLogsOnce(t *testing.T) {
	ctx := context.Background()
	g := git.NewClient("git", 5*time.Second, git.DefaultMaxPatchBytes)
	clientRepo, c1 := newRemoteClientRepo(t, "api")
	// Check the binding's own branch out, which makes the absorb below collide.
	runGit(t, clientRepo, "checkout", "relevo/api")

	bundle, _ := newRoundResultBundle(t, ctx, g, clientRepo, "refs/heads/relevo/api", c1)
	bundleBytes, err := io.ReadAll(bundle)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	_ = bundle.Close()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Repo = clientRepo
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleFunc: func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(bundleBytes)), nil
		},
	}
	rt := Runtime{
		Store: st, Remote: fr,
		Transport: remote.NewBundleTransport(g, t.TempDir()),
		Now:       func() time.Time { return baseTime },
	}

	// Ordering between tests cannot leak: this binding starts unwarned.
	checkedOutWarned.Delete("api")

	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cur := b
	for i := 0; i < 3; i++ {
		next, err := reconcile(t, rt, cur)
		if err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
		cur = next
	}

	count := 0
	for _, r := range h.snapshot() {
		if r.Level == slog.LevelInfo && r.Message == "checkout another branch, then relevo wait" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Info 'checkout another branch, then relevo wait' logged %d times, want exactly 1", count)
	}
}

func TestCatchUpAbsorbFailuresHaltAtTen(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundClosed, ClosedRound: 1},
		roundFileFunc: func(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error) {
			if kind == "report" {
				return io.NopCloser(strings.NewReader("Finished round 1\n")), nil
			}
			return nil, &client.HTTPError{Status: 404, Body: remote.ErrorBody{Code: "not_found", Message: "no " + kind}}
		},
		roundBundleFunc: func(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("not a real bundle")), nil
		},
	}
	ft := &fakeTransport{absorbErr: errors.New("disk full")}
	rt := Runtime{Store: st, Remote: fr, Transport: ft, Now: func() time.Time { return baseTime }}

	got := b
	var err error
	for i := 1; i <= 10; i++ {
		got, err = reconcile(t, rt, got)
		if err != nil {
			t.Fatalf("Reconcile iteration %d: %v", i, err)
		}
		if i < 10 {
			if got.State == store.StateNeedsYou {
				t.Fatalf("halted after only %d absorb failures", i)
			}
			if got.RemoteAbsorbFailures != i {
				t.Fatalf("iteration %d: RemoteAbsorbFailures = %d, want %d", i, got.RemoteAbsorbFailures, i)
			}
		}
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you at 10 absorb failures", got.State)
	}
	if !strings.Contains(got.Halt, "cannot absorb round") {
		t.Fatalf("Halt = %q, want it to name the absorb failure", got.Halt)
	}
}

func TestResumeRemoteRefusesRebind(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	rt := newRuntime(t)
	rt.Store = st
	rt.Now = func() time.Time { return baseTime }

	_, _, err := BindResolved(ctx, rt, BindOptions{
		Name: "api", Resume: true, Rebind: true, PlannerID: testPlannerName, CWD: "/fake/repo",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot change a remote builder; unbind and add") {
		t.Fatalf("BindResolved err = %v, want the remote-rebind refusal", err)
	}
}

// remoteBuilderRT is the client runtime for the `send --candidate` remote tests
// (#318): an active remote binding on zen with a current candidate, a fake git
// whose branch resolves, and a fake transport.
func remoteBuilderRT(t *testing.T, fr *fakeRemote) (Runtime, *store.Store, *fakeTransport) {
	t.Helper()
	st := store.New(t.TempDir())
	b := store.Binding{
		Name:             "api",
		CWD:              "/fake/repo",
		Repo:             "/fake/repo",
		Branch:           "relevo/api",
		Round:            1,
		State:            store.StateActive,
		BuilderCandidate: "agy/test/m",
		Builder: store.Endpoint{
			Mode:   store.ModeRemote,
			Server: "zen",
			Kind:   "agy",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	fg := &fakeGit{
		refSHA: map[string]string{
			"refs/heads/relevo/api": "1111111111111111111111111111111111111111",
		},
	}
	ft := &fakeTransport{
		snapshotResp: remote.Snapshot{
			Heads: map[string]string{"refs/relevo/api/out": "1111111111111111111111111111111111111111"},
		},
	}
	return Runtime{Store: st, Git: fg, Remote: fr, Transport: ft, Now: time.Now}, st, ft
}

// TestSendRemoteBuilderChangesCandidate pins §5.3 (a): the token reaches
// StartRound, the client records the candidate the server canonicalised, a
// pick entry lands under the sent round, and a following observeRemote with
// the same view writes no spurious switch.
func TestSendRemoteBuilderChangesCandidate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	view := remote.BindingView{RoundState: remote.RoundRunning, Candidate: "opencode/test/m"}
	fr := &fakeRemote{
		whoAmIResp:        remote.WhoAmI{Features: []string{remote.FeatureTier, remote.FeatureBuilder}},
		startRoundResp:    view,
		getBindingResp:    view,
		roundFileFromResp: io.NopCloser(strings.NewReader("")),
	}
	rt, st, _ := remoteBuilderRT(t, fr)

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Send(ctx, rt, "api", planFile, SendOptions{Builder: "opencode/test/m"})
	if err != nil {
		t.Fatalf("Send --builder: %v", err)
	}
	if fr.startRoundCandidate != "opencode/test/m" {
		t.Errorf("fake saw candidate %q, want opencode/test/m", fr.startRoundCandidate)
	}
	if !strings.Contains(res.Pick, "picked opencode/test/m on zen: explicit") {
		t.Errorf("res.Pick = %q, want the remote pick note", res.Pick)
	}

	stored, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if stored.BuilderCandidate != "opencode/test/m" {
		t.Errorf("BuilderCandidate = %q, want opencode/test/m", stored.BuilderCandidate)
	}
	if stored.Builder.Kind != "opencode" {
		t.Errorf("Builder.Kind = %q, want opencode", stored.Builder.Kind)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	planIdx := -1
	for i, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPlan {
			planIdx = i
			break
		}
	}
	if planIdx < 1 {
		t.Fatalf("no plan entry after a pick entry: %+v", entries)
	}
	if e := entries[planIdx-1]; e.Kind != store.KindPick || e.Round != 1 || !strings.Contains(e.Note, "opencode/test/m") {
		t.Errorf("entry before the plan = %+v, want a round 1 pick naming opencode/test/m", e)
	}

	if err := st.WithLock(func(tx *store.Tx) error {
		_, _, err := observeRemote(ctx, rt, tx, stored)
		return err
	}); err != nil {
		t.Fatalf("observeRemote: %v", err)
	}
	after, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range after {
		if e.Kind == store.KindSwitch {
			t.Errorf("observeRemote wrote a spurious switch: %+v", e)
		}
	}
}

// TestSendRemoteBuilderPreBuilderServerRefused pins §5.3 (b): a server
// without the builder feature is refused before anything is shipped.
func TestSendRemoteBuilderPreBuilderServerRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier}},
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	rt, st, ft := remoteBuilderRT(t, fr)

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{Builder: "opencode/test/m"})
	if err == nil {
		t.Fatal("Send --builder against a pre-builder server must be refused")
	}
	if !strings.Contains(err.Error(), "cannot change a binding's candidate") {
		t.Errorf("err = %q, want the pre-builder refusal", err.Error())
	}
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "StartRound:") {
			t.Errorf("StartRound was called: %v", fr.calls)
		}
	}
	if len(ft.snapshotCalls) != 0 {
		t.Errorf("a snapshot was taken before the refusal: %+v", ft.snapshotCalls)
	}
	if _, statErr := os.Stat(st.PlanPath("api", 1)); !os.IsNotExist(statErr) {
		t.Errorf("a plan was staged: %v", statErr)
	}
}

// TestSendRemoteBuilderRefusedWhileRoundOpen pins §5.3 (c): the client's own
// open round refuses before any server contact.
func TestSendRemoteBuilderRefusedWhileRoundOpen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fr := &fakeRemote{
		whoAmIResp:     remote.WhoAmI{Features: []string{remote.FeatureTier, remote.FeatureBuilder}},
		startRoundResp: remote.BindingView{RoundState: remote.RoundRunning},
	}
	rt, st, _ := remoteBuilderRT(t, fr)
	if err := st.AppendLog("api", store.LogEntry{
		TS: time.Now().UTC(), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true,
	}); err != nil {
		t.Fatal(err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Send(ctx, rt, "api", planFile, SendOptions{Builder: "opencode/test/m"})
	if err == nil {
		t.Fatal("Send --builder on an open round must be refused")
	}
	if !strings.Contains(err.Error(), "has round 1 open") || !strings.Contains(err.Error(), "relevo stop api ends it") {
		t.Errorf("err = %q, want the round-open refusal", err.Error())
	}
	if len(fr.calls) != 0 {
		t.Errorf("the refusal contacted the server: %v", fr.calls)
	}
}

// TestAddRemoteMatchesCandidateByName pins A1 §4.2: --candidate may name a
// server candidate by its short name, and the canonical token is what the
// server is asked to run. An unknown value with no "/" is refused, naming the
// server's candidates as `name (token)`.
func TestAddRemoteMatchesCandidateByName(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "claude/anthropic/haiku", Name: "haiku", Kind: "claude"},
			{Token: "codex/openai/gpt-5.6-terra:high", Name: "gpt-5.6-terra", Kind: "codex"},
		}},
		createBindingResp: remote.BindingView{Name: "api", Candidate: "claude/anthropic/haiku"},
	}
	rt := Runtime{
		Store:    st,
		Planners: addRemotePlanner(t),
		Git:      fg,
		Remote:   fr,
		Now:      time.Now,
	}

	if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo", Candidate: "haiku"}); err != nil {
		t.Fatalf("Add(haiku): %v", err)
	}
	if fr.createBindingReq.Candidate != "claude/anthropic/haiku" {
		t.Errorf("CreateBinding candidate = %q, want the canonical token", fr.createBindingReq.Candidate)
	}

	_, err := Add(ctx, rt, AddOptions{Name: "api2", Server: "zen", Repo: "/fake/repo", Candidate: "nope"})
	if err == nil {
		t.Fatal("Add(nope) = nil error, want a refusal")
	}
	want := `candidate "nope" not available on zen (available: haiku (claude/anthropic/haiku), gpt-5.6-terra (codex/openai/gpt-5.6-terra:high))`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Add(nope) err = %q, want it containing %q", err.Error(), want)
	}
}

// TestForwardAvailableResolvesNameToToken pins A1 §4.2: a candidate name is
// forwarded to every server as its canonical token.
func TestForwardAvailableResolvesNameToToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	b := remoteBinding("alpha")
	b.Name = "open-alpha"
	b.CWD = "/fake/open-alpha"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{availableResp: remote.AvailableResponse{Provider: "test", Removed: 1}}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, testCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	ForwardAvailable(ctx, rt, "claude-m")

	for _, c := range fr.calls {
		if c == "Available:alpha:"+testClaudeRef {
			return
		}
	}
	t.Errorf("calls = %v, want one Available:alpha:%s", fr.calls, testClaudeRef)
}

// TestForwardAvailableSendsTheServerTokenForTheSameName is the clear mirror of
// TestForwardUnavailableSendsTheServerTokenForTheSameName: the server's own
// token reaches Available, so the server's ledger clears the provider it gates.
func TestForwardAvailableSendsTheServerTokenForTheSameName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())

	b := remoteBinding("zen")
	b.Name = "open-remote"
	b.CWD = "/fake/open-remote"
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{
			{Token: "opencode/server-group/test-model-1", Name: "test-model-1"},
		}},
	}
	rt := Runtime{
		Store:      st,
		Remote:     fr,
		Candidates: candidateSet(t, forwardClientCandidatesJSON),
		Now:        func() time.Time { return baseTime },
	}

	lines := ForwardAvailable(ctx, rt, "test-model-1")
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want one line for the one server", lines)
	}

	var available []string
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "Available:") {
			available = append(available, c)
		}
	}
	if !slices.Equal(available, []string{"Available:zen:opencode/server-group/test-model-1"}) {
		t.Fatalf("Available calls = %v, want exactly one for the server token", available)
	}
}

// TestReconcileRemoteUnknownServerIsNotRunning pins both halves of the
// unknown-server classification: the status stops claiming a running round,
// and a config mistake never halts a round that may be fine on the server.
func TestReconcileRemoteUnknownServerIsNotRunning(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	b.Builder.RemoteStatus = string(remote.RoundRunning)
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{getBindingErr: client.ErrUnknownServer}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateActive {
		t.Fatalf("state = %s, want active: an unknown server must not halt the round", got.State)
	}
	if got.Builder.RemoteStatus != "unknown server" {
		t.Fatalf("RemoteStatus = %q, want the unknown-server status", got.Builder.RemoteStatus)
	}
}

// TestCatchUpAckFailureLeavesTheReportUnqueued pins that the report waits for
// the ack: a failed ack leaves the absorbed bookkeeping committed but queues
// nothing, and the next pass re-collects the still-closed round and retries.
func TestCatchUpAckFailureLeavesTheReportUnqueued(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.roundBundleResp = nil
	fr.ackErr = errors.New("boom")
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("report queued despite the ack failure: %+v", entries)
	}
	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d after the failed ack, want 1", got.Round)
	}
	if got.Builder.LastKnown != "c0ffee" {
		t.Errorf("LastKnown = %q, want the committed result commit", got.Builder.LastKnown)
	}
	if n := countCalls(fr, "Ack:"); n != 1 {
		t.Errorf("Ack calls = %d, want 1", n)
	}

	fr.ackErr = nil
	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("Tick (retry): %v", err)
	}

	entries, err = st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	reports := 0
	for _, e := range entries {
		if e.Kind == store.KindReport {
			reports++
		}
	}
	if reports != 1 {
		t.Errorf("report entries = %d, want exactly 1 after the retry", reports)
	}
	got, err = st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d after the retry, want 2", got.Round)
	}
	if n := countCalls(fr, "Ack:"); n != 2 {
		t.Errorf("Ack calls = %d, want 2", n)
	}
}

// TestCatchUpSettleSkipsAChangedBinding pins the phase-B guard: a binding that
// moved on between the ack and the report keeps its new state and gets no
// report entry. The downloaded report file stays in place; the Idle recovery
// collects a live binding on its next pass.
func TestCatchUpSettleSkipsAChangedBinding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := roundClosedRemote()
	fr.roundBundleResp = nil
	fr.beforeCall = func(call string) {
		if !strings.HasPrefix(call, "Ack:") {
			return
		}
		cur, err := st.Load("api")
		if err != nil {
			t.Fatalf("load during ack: %v", err)
		}
		cur.State = store.StateDone
		if err := st.Save(cur); err != nil {
			t.Fatalf("mark done during ack: %v", err)
		}
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := SyncRemote(ctx, rt); err != nil {
		t.Fatalf("SyncRemote: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateDone {
		t.Fatalf("state = %s, want done: a binding that moved on must keep its state", got.State)
	}
	if got.Round != 1 {
		t.Errorf("Round = %d, want 1", got.Round)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if HasEntry(entries, 1, store.DirToPlanner, store.KindReport) {
		t.Fatalf("report queued for a binding that moved on: %+v", entries)
	}
}
