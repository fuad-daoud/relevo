package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestServedBuilderLaunchesAtPolicyTier is Part A's proof that a served
// binding's tier actually reaches the harness's argv: a server whose
// policy.json sets tier.builder above harness renders the corresponding
// permission flag into the headless builder's ProcSpec (#141 remote half).
func TestServedBuilderLaunchesAtPolicyTier(t *testing.T) {
	ctx := context.Background()

	srvCtx, srvCancel := context.WithCancel(context.Background())
	pol := policy.Policy{
		Tier:    map[string]string{"builder": "yolo"},
		MaxTier: "yolo",
	}
	_, url, fp, enroll, srvStore, runner := newServerWithContext(t, srvCtx, srvCancel, pol)
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	}); err != nil {
		t.Fatalf("relevo.Add: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send: %v", err)
	}

	runner.mu.Lock()
	specsLen := len(runner.specs)
	var spec spawn.ProcSpec
	if specsLen > 0 {
		spec = runner.specs[0]
	}
	runner.mu.Unlock()
	if specsLen != 1 {
		t.Fatalf("runner.specs count = %d, want 1", specsLen)
	}

	if !slices.Contains(spec.Argv, "--dangerously-skip-permissions") {
		t.Fatalf("spec.Argv = %v, want --dangerously-skip-permissions", spec.Argv)
	}

	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("server store Load(api): %v", err)
	}
	if sb.Tier != "yolo" {
		t.Fatalf("server binding Tier = %q, want yolo", sb.Tier)
	}
}

// TestServedBuilderDefaultsToHarness is the same round with no policy tier
// configured: the served builder launches at tier harness, so relevo adds no
// permission flag at all (#141 remote half).
func TestServedBuilderDefaultsToHarness(t *testing.T) {
	ctx := context.Background()

	srvCtx, srvCancel := context.WithCancel(context.Background())
	_, url, fp, enroll, srvStore, runner := newServerWithContext(t, srvCtx, srvCancel, policy.Policy{})
	rt, kp := newClient(t, url, fp)
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	if _, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
	}); err != nil {
		t.Fatalf("relevo.Add: %v", err)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send: %v", err)
	}

	runner.mu.Lock()
	specsLen := len(runner.specs)
	var spec spawn.ProcSpec
	if specsLen > 0 {
		spec = runner.specs[0]
	}
	runner.mu.Unlock()
	if specsLen != 1 {
		t.Fatalf("runner.specs count = %d, want 1", specsLen)
	}

	if slices.Contains(spec.Argv, "--dangerously-skip-permissions") || slices.Contains(spec.Argv, "--permission-mode") {
		t.Fatalf("spec.Argv = %v, want no permission flag", spec.Argv)
	}

	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("server store Load(api): %v", err)
	}
	if sb.Tier != "harness" {
		t.Fatalf("server binding Tier = %q, want harness", sb.Tier)
	}
}

// TestRemoteTierOverWire is Part B's round trip: WhoAmI advertises the
// feature, --tier on Add reaches the server and is echoed back, a one-round
// Tier override on Send reaches the harness's argv, and the next round falls
// back to the binding's own persisted Tier once RoundTier clears (#141
// remote half).
func TestRemoteTierOverWire(t *testing.T) {
	ctx := context.Background()

	srvCtx, srvCancel := context.WithCancel(context.Background())
	srv, url, fp, enroll, srvStore, runner := newServerWithContext(t, srvCtx, srvCancel, policy.Policy{MaxTier: "yolo"})
	rt, kp := newClient(t, url, fp)
	rt.Policy = policy.Policy{MaxTier: "yolo"}
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	who, err := rt.Remote.WhoAmI(ctx, "zen")
	if err != nil {
		t.Fatalf("WhoAmI: %v", err)
	}
	if !slices.Contains(who.Features, remote.FeatureTier) {
		t.Fatalf("who.Features = %v, want to contain %q", who.Features, remote.FeatureTier)
	}
	if who.BuilderTier != "harness" {
		t.Fatalf("who.BuilderTier = %q, want harness", who.BuilderTier)
	}

	res, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:   "api",
		Server: "zen",
		Repo:   repo,
		Tier:   "edit",
	})
	if err != nil {
		t.Fatalf("relevo.Add: %v", err)
	}
	if res.Binding.Tier != "edit" {
		t.Fatalf("res.Binding.Tier = %q, want edit", res.Binding.Tier)
	}

	serverStore := srvStore(owner)
	sb, err := serverStore.Load("api")
	if err != nil {
		t.Fatalf("server store Load(api): %v", err)
	}
	if sb.Tier != "edit" {
		t.Fatalf("server binding Tier = %q, want edit", sb.Tier)
	}

	planFile := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planFile, []byte("# Plan for api\nDo work.\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile, relevo.SendOptions{Tier: "yolo", AllowYolo: true}); err != nil {
		t.Fatalf("relevo.Send round 1: %v", err)
	}

	runner.mu.Lock()
	specsLen := len(runner.specs)
	var spec0 spawn.ProcSpec
	if specsLen > 0 {
		spec0 = runner.specs[0]
	}
	runner.mu.Unlock()
	if specsLen != 1 {
		t.Fatalf("runner.specs count after round 1 = %d, want 1", specsLen)
	}
	if !slices.Contains(spec0.Argv, "--dangerously-skip-permissions") {
		t.Fatalf("round 1 spec.Argv = %v, want --dangerously-skip-permissions", spec0.Argv)
	}

	// Close round 1 exactly as TestRemoteRoundEndToEnd does.
	gitClient := git.NewClient("git", 10*time.Second, 0)
	serverRT := relevo.Runtime{
		Store:  serverStore,
		Git:    gitClient,
		Runner: runner,
	}
	finishRound(t, serverRT, "api", 1, "round 1 done")

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("srv.Tick: %v", err)
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

	// RoundTier cleared by queueReport; Tier "edit" persists on the binding.
	cb, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("read client binding after round 1: %v", err)
	}
	if cb.RoundTier != "" {
		t.Fatalf("client RoundTier = %q, want empty after round 1 closed", cb.RoundTier)
	}
	if cb.Tier != "edit" {
		t.Fatalf("client Tier = %q, want edit to persist", cb.Tier)
	}

	planFile2 := filepath.Join(t.TempDir(), "plan2.md")
	if err := os.WriteFile(planFile2, []byte("# Plan for api round 2\nDo more work.\n"), 0o644); err != nil {
		t.Fatalf("write plan2: %v", err)
	}
	if _, err := relevo.Send(ctx, rt, "api", planFile2, relevo.SendOptions{}); err != nil {
		t.Fatalf("relevo.Send round 2: %v", err)
	}

	runner.mu.Lock()
	specsLen = len(runner.specs)
	var spec1 spawn.ProcSpec
	if specsLen > 1 {
		spec1 = runner.specs[1]
	}
	runner.mu.Unlock()
	if specsLen != 2 {
		t.Fatalf("runner.specs count after round 2 = %d, want 2", specsLen)
	}
	if !slices.Contains(spec1.Argv, "--permission-mode") || !slices.Contains(spec1.Argv, "acceptEdits") {
		t.Fatalf("round 2 spec.Argv = %v, want --permission-mode acceptEdits", spec1.Argv)
	}
}

// TestRemoteTierAboveServerMax pins the server-side refusal: a --tier the
// client's own policy allows (with --allow-yolo) but the server's max_tier
// forbids leaves no trace anywhere -- no server binding, no local binding,
// no local branch (#141 remote half).
func TestRemoteTierAboveServerMax(t *testing.T) {
	ctx := context.Background()

	srvCtx, srvCancel := context.WithCancel(context.Background())
	_, url, fp, enroll, srvStore, _ := newServerWithContext(t, srvCtx, srvCancel, policy.Policy{MaxTier: "edit"})
	rt, kp := newClient(t, url, fp)
	rt.Policy = policy.Policy{MaxTier: "yolo"}
	pubLine := remote.MarshalPublic(kp.Public, "test client")
	owner := enroll(pubLine)
	repo := newRepo(t)

	_, err := relevo.Add(ctx, rt, relevo.AddOptions{
		Name:      "api",
		Server:    "zen",
		Repo:      repo,
		Tier:      "yolo",
		AllowYolo: true,
	})
	if !errors.Is(err, relevo.ErrTierAboveMax) {
		t.Fatalf("relevo.Add err = %v, want ErrTierAboveMax", err)
	}

	serverStore := srvStore(owner)
	if _, err := serverStore.Load("api"); err == nil {
		t.Fatal("server binding exists despite tier_above_max refusal")
	}
	if _, err := rt.Store.Load("api"); err == nil {
		t.Fatal("local binding exists despite tier_above_max refusal")
	}

	repoGit := git.NewClient("git", 10*time.Second, 0)
	exists, err := repoGit.BranchExists(ctx, repo, "relevo/api")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Fatal("local branch relevo/api exists despite tier_above_max refusal")
	}
}
