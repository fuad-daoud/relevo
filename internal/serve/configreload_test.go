package serve

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/remote"
)

const (
	reloadCandidateJSON = `[{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]}]`
	reloadPolicyCapJSON = `{"serve":{"max_builders":7}}`
)

// putConfig stores body as one config section of the machine database, the same
// validated write `relevo config` makes, so a reload sees exactly what a user
// edit leaves behind.
func putConfig(t *testing.T, s *Server, sec config.Section, body string) {
	t.Helper()
	if _, err := config.Open(s.DB()).Put(sec, []byte(body)); err != nil {
		t.Fatalf("put %s: %v", sec, err)
	}
}

// reloadingServer is newTestServer plus the refresh cmdServeRun wires: a
// refresher over the machine database's own config store. An empty config dir
// imports nothing, so these tests drive it purely through stored sections.
func reloadingServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, root := newTestServer(t, 0)
	s.cfg.Reloader = NewConfigRefresher(config.Open(s.DB()), "", time.Now)
	return s, root
}

// TestTickReloadsConfigWithoutRestart is the repro: a config edit stored in the
// machine database reaches the next tick's runtime and cap with no New, no
// restart and no second server. It fails while the server holds the startup
// snapshot for its whole life.
func TestTickReloadsConfigWithoutRestart(t *testing.T) {
	s, root := reloadingServer(t)

	if got := s.cap(); got == 7 {
		t.Fatalf("cap before any config = %d; the test cannot distinguish a reload", got)
	}
	if rt := s.runtimeAt(filepath.Join(root, "bindings", "owner")); rt.Candidates != nil {
		t.Fatalf("candidates before any config = %v; the test cannot distinguish a reload", rt.Candidates)
	}

	putConfig(t, s, config.Candidates, reloadCandidateJSON)
	putConfig(t, s, config.Policy, reloadPolicyCapJSON)

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if got := s.cap(); got != 7 {
		t.Errorf("cap after tick = %d, want 7: serve.max_builders did not take effect without a restart", got)
	}
	rt := s.runtimeAt(filepath.Join(root, "bindings", "owner"))
	if rt.Candidates == nil {
		t.Errorf("candidates after tick = nil, want the one stored set")
	} else if refs := rt.Candidates.Refs(); len(refs) != 1 {
		t.Errorf("candidate refs after tick = %v, want exactly one", refs)
	}
	if rt.Policy.Serve == nil || rt.Policy.Serve.MaxBuilders == nil {
		t.Errorf("policy after tick = %+v, want serve.max_builders set", rt.Policy.Serve)
	}
}

// TestReloadUnchangedVersionDoesNotLoad pins the short-circuit: with the config
// version unchanged, a tick performs no load at all, so the steady-state cost of
// the refresh is one version query rather than a full config read per tick.
func TestReloadUnchangedVersionDoesNotLoad(t *testing.T) {
	src := &countingSource{inner: config.Open(testServeDB(t)), version: 1}
	if _, err := src.inner.Load(); err != nil {
		t.Fatalf("seed load: %v", err)
	}
	src.loads = 0

	r := NewConfigRefresher(src, "", time.Now)
	cfg := Config{}
	// An absent candidates section is the empty set, not a nil set: Load always
	// hands back a usable one.
	if got := r.Refresh(cfg); got.Candidates != nil && len(got.Candidates.Refs()) != 0 {
		t.Fatalf("first refresh candidates = %v, want the empty set", got.Candidates.Refs())
	}
	if src.loads != 1 {
		t.Fatalf("loads after first refresh = %d, want 1", src.loads)
	}
	for i := 0; i < 5; i++ {
		r.Refresh(cfg)
	}
	if src.loads != 1 {
		t.Errorf("loads after 5 unchanged refreshes = %d, want 1: an unchanged version must not load", src.loads)
	}

	src.version = 2
	r.Refresh(cfg)
	if src.loads != 2 {
		t.Errorf("loads after a version bump = %d, want 2", src.loads)
	}
}

// TestReloadKeepsLastGoodConfigOnFailure pins the failure semantics: a broken
// reload keeps serving the last good snapshot and warns once per distinct
// error, so a bad edit neither halts admissions nor half-replaces the config.
func TestReloadKeepsLastGoodConfigOnFailure(t *testing.T) {
	s, _ := newTestServer(t, 0)
	putConfig(t, s, config.Policy, reloadPolicyCapJSON)
	src := &countingSource{inner: config.Open(s.DB())}
	src.version = 1

	var warnings []string
	r := NewConfigRefresher(src, "", time.Now)
	r.warn = func(msg string, _ ...any) { warnings = append(warnings, msg) }

	good := r.Refresh(Config{})
	if good.Policy.Serve == nil {
		t.Fatalf("first refresh did not load the policy: %+v", good.Policy.Serve)
	}

	// A failing load is a whole-reload failure: the good copy is handed back
	// unchanged, which is what the server keeps serving. The version has to
	// move for the load to be attempted at all -- that is the short-circuit a
	// quiet config relies on.
	src.version = 2
	src.loadErr = errors.New("config import exploded")
	for i := 0; i < 3; i++ {
		got := r.Refresh(good)
		if got.Policy.Serve == nil {
			t.Fatalf("policy after a failed reload = nil; the last good copy must keep being served")
		}
		good = got
	}
	if len(warnings) != 1 {
		t.Errorf("warnings for one repeated error = %d, want 1: %v", len(warnings), warnings)
	}

	// A second distinct error warns again; recovery clears the latch.
	src.loadErr = errors.New("a different failure")
	src.version = 3
	r.Refresh(good)
	if len(warnings) != 2 {
		t.Errorf("warnings after a second distinct error = %d, want 2: %v", len(warnings), warnings)
	}
	src.loadErr = nil
	src.version = 4
	r.Refresh(good)
	r.Refresh(good)
	if len(warnings) != 2 {
		t.Errorf("warnings after recovery = %d, want 2: a good load must not re-warn a stale error", len(warnings))
	}
}

// TestReloadFailedTickKeepsServingCap pins the same rule through the tick: a
// server whose config has become unloadable still enforces the cap it had, and
// still admits, rather than falling back to a default or a zero cap.
func TestReloadFailedTickKeepsServingCap(t *testing.T) {
	s, _ := newTestServer(t, 0)
	putConfig(t, s, config.Policy, reloadPolicyCapJSON)
	src := &countingSource{inner: config.Open(s.DB())}
	src.version = 1
	r := NewConfigRefresher(src, "", time.Now)
	r.warn = func(string, ...any) {}
	s.cfg.Reloader = r

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if got := s.cap(); got != 7 {
		t.Fatalf("cap after the first tick = %d, want 7", got)
	}

	// A new version with an unloadable body behind it: the tick must keep
	// serving the cap it had.
	src.version = 2
	src.loadErr = errors.New("unloadable config")
	for i := 0; i < 3; i++ {
		if err := s.Tick(context.Background()); err != nil {
			t.Fatalf("tick under a broken config must not fail: %v", err)
		}
	}
	if got := s.cap(); got != 7 {
		t.Errorf("cap after three failed ticks = %d, want the last good 7", got)
	}
}

// TestReloadLeavesRestartRequiredSectionsAlone pins the boundary: the refresh
// replaces candidates, policy, registry and accounts, and nothing else. A
// section that only a restart can change keeps the value New was given, so no
// tick can quietly widen the accepted audiences or move the tick interval.
func TestReloadLeavesRestartRequiredSectionsAlone(t *testing.T) {
	d := testServeDB(t)
	if _, err := config.Open(d).Put(config.Candidates, []byte(reloadCandidateJSON)); err != nil {
		t.Fatalf("put candidates: %v", err)
	}
	cfg := Config{
		Audiences:   []string{"host:pinned"},
		Interval:    7 * time.Second,
		StartedAt:   time.Unix(1000, 0),
		MaxBuilders: 3,
	}
	r := NewConfigRefresher(config.Open(d), "", time.Now)
	got := r.Refresh(cfg)

	if got.Candidates == nil {
		t.Error("candidates were not reloaded")
	}
	if strings.Join(got.Audiences, ",") != "host:pinned" {
		t.Errorf("audiences = %v, want the startup set: audiences are restart-only", got.Audiences)
	}
	if got.Interval != 7*time.Second {
		t.Errorf("interval = %v, want 7s: the tick interval is restart-only", got.Interval)
	}
	if !got.StartedAt.Equal(cfg.StartedAt) {
		t.Errorf("startedAt = %v, want %v: the process start time is restart-only", got.StartedAt, cfg.StartedAt)
	}
	if got.MaxBuilders != 3 {
		t.Errorf("maxBuilders = %d, want the flag's 3: the flag is restart-only", got.MaxBuilders)
	}
}

// TestTickReloadIsRaceCleanWithHandlers drives a tick against live WhoAmI and
// candidates requests while the config alternates between two candidate sets, so
// -race catches any unlocked read of the swapped config and any request that
// assembles a view from two different reloads.
func TestTickReloadIsRaceCleanWithHandlers(t *testing.T) {
	env := setupTestEnv(t)
	env.srv.cfg.Reloader = NewConfigRefresher(config.Open(env.srv.DB()), "", time.Now)
	if _, err := config.Open(env.srv.DB()).Put(config.Policy, []byte(reloadPolicyCapJSON)); err != nil {
		t.Fatalf("put policy: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		churnCandidates(t, env.srv, stop)
	}()

	// Two request-shaped readers over the real signed stack, so each request
	// carries an authenticated caller: WhoAmI assembles its tier and cap view
	// through runtime/cap/census, and the candidates view walks the candidate
	// set. Both read the reloadable sections on every request.
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pollReloadedViews(t, env, stop)
		}()
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// churnCandidates alternates the stored candidate set and ticks until stop, so
// the refresh path really swaps the config under the readers.
func churnCandidates(t *testing.T, s *Server, stop <-chan struct{}) {
	t.Helper()
	for i := 0; ; i++ {
		if stopped(stop) {
			return
		}
		body := reloadCandidateJSON
		if i%2 == 1 {
			body = `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`
		}
		if _, err := config.Open(s.DB()).Put(config.Candidates, []byte(body)); err != nil {
			t.Errorf("put candidates: %v", err)
			return
		}
		if err := s.Tick(context.Background()); err != nil {
			t.Errorf("tick: %v", err)
			return
		}
	}
}

// pollReloadedViews reads the two views a client fetches per request until stop.
func pollReloadedViews(t *testing.T, env *testEnv, stop <-chan struct{}) {
	t.Helper()
	for {
		if stopped(stop) {
			return
		}
		for _, path := range []string{"/v1/whoami", "/v1/candidates"} {
			resp, _ := doSigned(t, env.ts, env.kp, "GET", path, nil, "")
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s = %d, want 200", path, resp.StatusCode)
				return
			}
		}
	}
}

// stopped reports whether a poll loop should end.
func stopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// TestReloadIsWireAdditive is the compat guard: hot reload is a server-side
// behaviour change only. The wire version, the feature-token list WhoAmI
// advertises and the WhoAmI shape itself are all pinned here, so a later edit
// that reaches for a new token, a version bump or a field change to advertise
// the reload fails this test instead of silently breaking an old client.
func TestReloadIsWireAdditive(t *testing.T) {
	if remote.Version != 1 {
		t.Errorf("remote.Version = %d, want 1: a hot reload must not move the wire version", remote.Version)
	}

	env := setupTestEnv(t)
	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami = %d, want 200", resp.StatusCode)
	}

	var who remote.WhoAmI
	if err := json.Unmarshal(body, &who); err != nil {
		t.Fatalf("decode whoami: %v", err)
	}
	// The exact token list, in order: an added or dropped token is a wire
	// change, and the reload must not shift what a client negotiates on.
	want := []string{
		remote.FeatureTier, remote.FeatureQueue, remote.FeatureStop,
		remote.FeatureBuilder, remote.FeatureIdempotentSend, remote.FeatureAuthor,
		remote.FeatureRoles, remote.FeatureLabels, remote.FeatureReaders,
		remote.FeatureOrigin, remote.FeatureForce, remote.FeaturePlacement,
		remote.FeatureAccounts, remote.FeatureChainMember, remote.FeatureChain,
		remote.FeatureIsolation, remote.FeatureWorkflow, remote.FeatureCheck,
	}
	if strings.Join(who.Features, ",") != strings.Join(want, ",") {
		t.Errorf("whoami features = %v\nwant %v", who.Features, want)
	}

	// The shape a client decodes: these carry the values an old client reads,
	// and a reload reaches none of them beyond the tier and cap it always shows.
	if who.ID != env.id || who.ServerVersion != remote.Version {
		t.Errorf("whoami identity = %q v%d, want %q v%d", who.ID, who.ServerVersion, env.id, remote.Version)
	}
	if who.Builders == nil || who.Builders.Cap <= 0 {
		t.Errorf("whoami builders view = %+v, want a cap", who.Builders)
	}

	// And a reload moves exactly the values a client already reads, with no
	// new field carrying the fact that a reload happened.
	env.srv.cfg.Reloader = NewConfigRefresher(config.Open(env.srv.DB()), "", time.Now)
	if _, err := config.Open(env.srv.DB()).Put(config.Policy, []byte(reloadPolicyCapJSON)); err != nil {
		t.Fatalf("put policy: %v", err)
	}
	if err := env.srv.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/whoami", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami after a reload = %d, want 200", resp.StatusCode)
	}
	var reloaded remote.WhoAmI
	if err := json.Unmarshal(body, &reloaded); err != nil {
		t.Fatalf("decode whoami after reload: %v", err)
	}
	if reloaded.Builders == nil || reloaded.Builders.Cap != 7 {
		t.Errorf("whoami cap after a reload = %+v, want 7 without any wire change", reloaded.Builders)
	}
	if strings.Join(reloaded.Features, ",") != strings.Join(who.Features, ",") {
		t.Errorf("features after a reload = %v, want the same %v", reloaded.Features, who.Features)
	}
}

// TestNilReloaderKeepsStartupConfig is the no-source case: a server built
// without a reloader keeps serving exactly the config it was constructed with,
// which is what an admin verb and an embedded server do.
func TestNilReloaderKeepsStartupConfig(t *testing.T) {
	s, root := newTestServer(t, 0)
	putConfig(t, s, config.Candidates, reloadCandidateJSON)

	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if rt := s.runtimeAt(filepath.Join(root, "bindings", "owner")); rt.Candidates != nil {
		t.Errorf("candidates after a tick = %v, want nil with no reloader", rt.Candidates)
	}
}

// countingSource wraps a config store as a ConfigSource whose version is set by
// the test and whose load can be made to fail, and counts the loads.
type countingSource struct {
	inner   *config.Store
	version int64
	loads   int
	loadErr error
}

// ImportFiles imports for real from a non-empty dir, and is a no-op for the
// empty dir these tests use, so the config store is read as stored.
func (c *countingSource) ImportFiles(dir string, now time.Time) (config.ImportResult, error) {
	if dir == "" {
		return config.ImportResult{}, nil
	}
	return c.inner.ImportFiles(dir, now)
}

func (c *countingSource) Version() (int64, error) { return c.version, nil }

func (c *countingSource) Load() (config.Loaded, error) {
	c.loads++
	if c.loadErr != nil {
		return config.Loaded{}, c.loadErr
	}
	return c.inner.Load()
}
