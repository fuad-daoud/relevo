package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// fakeSource is the ConfigSource test seam: it returns whatever version,
// loaded config and errors the test sets, and counts its calls.
type fakeSource struct {
	version   int64
	loaded    config.Loaded
	loadErr   error
	importErr error
	verErr    error

	importCalls   int
	loadCalls     int
	lastImportDir string
}

func (f *fakeSource) ImportFiles(dir string, _ time.Time) (config.ImportResult, error) {
	f.importCalls++
	f.lastImportDir = dir
	return config.ImportResult{}, f.importErr
}

func (f *fakeSource) Version() (int64, error) { return f.version, f.verErr }

func (f *fakeSource) Load() (config.Loaded, error) {
	f.loadCalls++
	if f.loadErr != nil {
		return config.Loaded{}, f.loadErr
	}
	return f.loaded, nil
}

// loaded builds a Loaded whose registry is the roles.Build derivation of its
// parts, the way config.Store.Load does.
func loaded(t *testing.T, pol policy.Policy, rf *roles.File, warnings []string) config.Loaded {
	t.Helper()
	set := &candidate.Set{}
	reg, err := roles.Build(rf, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return config.Loaded{
		Candidates: set,
		Policy:     pol,
		RolesFile:  rf,
		Registry:   reg,
		Warnings:   warnings,
	}
}

func TestRefreshLoadsOnFirstCall(t *testing.T) {
	t.Parallel()

	pol := policy.Policy{Order: map[string][]string{"builder": {"agy"}}}
	src := &fakeSource{version: 1, loaded: loaded(t, pol, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	cls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return cls
	}

	out := w.Refresh(Runtime{})

	if src.importCalls != 1 {
		t.Errorf("ImportFiles calls = %d, want 1", src.importCalls)
	}
	if src.lastImportDir != "/cfg/relevo" {
		t.Errorf("ImportFiles dir = %q, want /cfg/relevo", src.lastImportDir)
	}
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1", src.loadCalls)
	}
	if resolveCalls != 1 {
		t.Errorf("resolve calls = %d, want 1", resolveCalls)
	}
	if out.Candidates != src.loaded.Candidates {
		t.Errorf("rt.Candidates was not replaced")
	}
	if len(out.Policy.Order["builder"]) != 1 || out.Policy.Order["builder"][0] != "agy" {
		t.Errorf("rt.Policy was not replaced")
	}
	if out.Classify != cls {
		t.Errorf("rt.Classify was not replaced from resolve seam")
	}
}

func TestRefreshSkipsWhenVersionUnchanged(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first call failed to load")
	}

	out2 := w.Refresh(out1)
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Errorf("second call loaded despite an unchanged version; loadCalls=%d resolveCalls=%d", src.loadCalls, resolveCalls)
	}
	if out2.Candidates != out1.Candidates {
		t.Errorf("Candidates pointer not equal")
	}
	// The import runs on every tick: a file dropped in is picked up even when
	// the version has not changed yet.
	if src.importCalls != 2 {
		t.Errorf("ImportFiles calls = %d, want 2", src.importCalls)
	}
}

func TestRefreshReloadsOnVersionChange(t *testing.T) {
	t.Parallel()

	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	resolveCalls := 0
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		resolveCalls++
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if src.loadCalls != 1 || resolveCalls != 1 {
		t.Fatalf("first load failed")
	}

	// A new config version and a new policy.
	src.version = 2
	src.loaded = loaded(t, policy.Policy{Order: map[string][]string{"builder": {"claude"}}}, nil, nil)

	out2 := w.Refresh(out1)
	if src.loadCalls != 2 {
		t.Errorf("Load calls = %d, want 2", src.loadCalls)
	}
	if resolveCalls != 2 {
		t.Errorf("resolve calls = %d, want 2", resolveCalls)
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "claude" {
		t.Errorf("new Policy.Order not visible, got %v", out2.Policy.Order["builder"])
	}
}

func TestRefreshKeepsLastGoodOnBadLoad(t *testing.T) {
	t.Parallel()

	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	goodCls := &classify.Fake{}
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return goodCls
	}
	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	t0 := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	rt := Runtime{Now: func() time.Time { return t0 }}
	out1 := w.Refresh(rt)
	if src.loadCalls != 1 {
		t.Fatalf("first load failed")
	}

	// A new version whose load fails.
	src.version = 2
	src.loadErr = policy.ErrBadPolicy
	out2 := w.Refresh(out1)

	if out2.Candidates != src.loaded.Candidates {
		t.Errorf("Candidates changed on a bad load")
	}
	if len(out2.Policy.Order["builder"]) != 1 || out2.Policy.Order["builder"][0] != "good" {
		t.Errorf("Policy changed on a bad load")
	}
	if out2.Classify != goodCls {
		t.Errorf("Classify changed on a bad load")
	}
	if w.version != 1 {
		t.Errorf("version advanced to %d despite the error, want it to stay at 1", w.version)
	}

	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}

	// A third call with the same bad version tries the load again.
	_ = w.Refresh(out2)
	if src.loadCalls != 3 {
		t.Errorf("Load calls = %d, want 3", src.loadCalls)
	}
	if len(warnMsgs) != 1 {
		t.Errorf("warn called %d times, want 1", len(warnMsgs))
	}
}

func TestRefreshKeepsLastGoodOnImportError(t *testing.T) {
	t.Parallel()

	good := loaded(t, policy.Policy{Order: map[string][]string{"builder": {"good"}}}, nil, nil)
	src := &fakeSource{version: 1, loaded: good}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	out1 := w.Refresh(Runtime{})
	if out1.Candidates != good.Candidates {
		t.Fatalf("first load did not replace Candidates")
	}

	// The next tick's import fails: the old copy is kept and warned about.
	src.importErr = errors.New("policy.json: bad policy")
	out2 := w.Refresh(out1)
	if out2.Candidates != good.Candidates {
		t.Errorf("Candidates changed on an import error")
	}
	if len(warnMsgs) != 1 {
		t.Fatalf("warn called %d times, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "keeping the copy loaded at") {
		t.Errorf("warn text %q does not contain %q", warnMsgs[0], "keeping the copy loaded at")
	}
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1: an import error must not load", src.loadCalls)
	}
}

func TestRefreshWarnsOncePerDistinctError(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	rt := Runtime{}

	// Three ticks with the same error -> one warn.
	src.loadErr = errors.New("error one")
	w.Refresh(rt)
	w.Refresh(rt)
	w.Refresh(rt)
	if len(warnMsgs) != 1 {
		t.Fatalf("warn calls = %d, want 1 after 3 identical errors", len(warnMsgs))
	}

	// A different error text -> a second warn.
	src.loadErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after the error changed", len(warnMsgs))
	}

	// A good load at a new version.
	src.loadErr = nil
	src.version = 2
	src.loaded = loaded(t, policy.Policy{}, nil, nil)
	w.Refresh(rt)
	if len(warnMsgs) != 2 {
		t.Fatalf("warn calls = %d, want 2 after a successful load", len(warnMsgs))
	}

	// The same error again -> a third warn.
	src.version = 3
	src.loadErr = errors.New("error two")
	w.Refresh(rt)
	if len(warnMsgs) != 3 {
		t.Fatalf("warn calls = %d, want 3 after a repeated error following a good load", len(warnMsgs))
	}
}

// TestRefreshEmptyConfigIsNotAnError pins the "absent section = missing file"
// invariant: a Loaded with every section empty replaces the runtime quietly.
func TestRefreshEmptyConfigIsNotAnError(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	warnCalls := 0
	w.warn = func(msg string, args ...any) { warnCalls++ }

	out := w.Refresh(Runtime{})
	if src.loadCalls != 1 {
		t.Errorf("Load calls = %d, want 1", src.loadCalls)
	}
	if warnCalls != 0 {
		t.Errorf("warn calls = %d, want 0", warnCalls)
	}
	if out.Candidates == nil {
		t.Errorf("Candidates = nil, want the empty set")
	}
}

func TestRefreshStartupFailureSaysStartup(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loadErr: errors.New("parse error")}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)

	var warnMsgs []string
	w.warn = func(msg string, args ...any) { warnMsgs = append(warnMsgs, msg) }

	_ = w.Refresh(Runtime{})

	if len(warnMsgs) != 1 {
		t.Fatalf("warn calls = %d, want 1", len(warnMsgs))
	}
	if !strings.Contains(warnMsgs[0], "startup") {
		t.Errorf("warn text %q does not contain 'startup'", warnMsgs[0])
	}
}

// TestRefreshCarriesAndLogsConfigWarnings pins #372 §4.4: the watcher carries
// the config warnings on the Runtime, logs each distinct text once, and does
// not repeat an unchanged warning on a later reload.
func TestRefreshCarriesAndLogsConfigWarnings(t *testing.T) {
	t.Parallel()

	src := &fakeSource{
		version: 1,
		loaded: loaded(t, policy.Policy{}, nil,
			[]string{"candidates.json: cand warning", "policy.json: policy warning"}),
	}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	var warns []string
	w.warn = func(msg string, args ...any) { warns = append(warns, msg) }

	out := w.Refresh(Runtime{})
	want := []string{"candidates.json: cand warning", "policy.json: policy warning"}
	if len(out.ConfigWarnings) != 2 || out.ConfigWarnings[0] != want[0] || out.ConfigWarnings[1] != want[1] {
		t.Fatalf("ConfigWarnings = %v, want %v", out.ConfigWarnings, want)
	}
	if len(warns) != 2 {
		t.Fatalf("warn calls = %v, want both warnings logged", warns)
	}

	// A reload that produces the same warnings must not log them again.
	src.version = 2
	out2 := w.Refresh(out)
	if len(out2.ConfigWarnings) != 2 {
		t.Fatalf("ConfigWarnings after reload = %v, want 2", out2.ConfigWarnings)
	}
	if len(warns) != 2 {
		t.Errorf("warn calls after an unchanged reload = %v, want still 2", warns)
	}
}

// TestRefreshReloadsRegistry pins §5.1: a new version's roles section replaces
// the runtime's registry.
func TestRefreshReloadsRegistry(t *testing.T) {
	t.Parallel()

	first := &roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/test/m"}},
	}}
	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, first, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	out1 := w.Refresh(Runtime{})
	if out1.Registry == nil || out1.Registry.Source() != roles.SourceFile {
		t.Fatalf("Registry = %+v, want a roles.json registry", out1.Registry)
	}

	second := &roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"agy/test/m"}},
	}}
	src.version = 2
	src.loaded = loaded(t, policy.Policy{}, second, nil)

	out2 := w.Refresh(out1)
	if out2.Registry == out1.Registry {
		t.Error("Registry was not replaced after a roles change")
	}
	builder, ok := out2.Registry.Role("builder")
	if !ok {
		t.Fatal(`Role("builder") not found`)
	}
	if len(builder.Candidates) != 1 || builder.Candidates[0] != "agy/test/m" {
		t.Errorf("builder.Candidates = %v, want the reloaded row", builder.Candidates)
	}
}

// TestRefreshLoadsLegacyRegistryWhenNoRolesSection pins §5.1: a Loaded with no
// roles file still has a registry, and it is the legacy derivation.
func TestRefreshLoadsLegacyRegistryWhenNoRolesSection(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}

	out := w.Refresh(Runtime{})
	if out.Registry == nil {
		t.Fatal("Registry = nil, want the legacy derivation")
	}
	if out.Registry.Source() != roles.SourceLegacy {
		t.Errorf("Source() = %q, want %q", out.Registry.Source(), roles.SourceLegacy)
	}
}

// TestRefreshInstallsRemoteClientOnServersChange pins that a reloaded servers
// section reaches rt.Remote through the construction seam.
func TestRefreshInstallsRemoteClientOnServersChange(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	src.loaded.Servers = remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}
	src.loaded.ClientKey = []byte("key")

	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	var handed []remote.Servers
	w.remoteFor = func(servers remote.Servers, key []byte) (RemoteClient, error) {
		handed = append(handed, servers)
		return &fakeRemote{}, nil
	}

	out1 := w.Refresh(Runtime{})
	if len(handed) != 1 {
		t.Fatalf("remoteFor calls = %d, want 1", len(handed))
	}
	if !sameServers(handed[0], src.loaded.Servers) {
		t.Errorf("remoteFor servers = %v, want %v", handed[0], src.loaded.Servers)
	}
	if out1.Remote == nil {
		t.Fatal("Remote = nil after the first refresh, want an installed client")
	}

	// A new version with a second server: the seam runs again with the new map
	// and installs a different client.
	src.version = 2
	src.loaded.Servers = remote.Servers{
		"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"},
		"api": {URL: "https://api:7777", Fingerprint: "sha256:11"},
	}
	out2 := w.Refresh(out1)
	if len(handed) != 2 {
		t.Fatalf("remoteFor calls = %d, want 2 after a servers change", len(handed))
	}
	if !sameServers(handed[1], src.loaded.Servers) {
		t.Errorf("remoteFor servers = %v, want %v", handed[1], src.loaded.Servers)
	}
	if out2.Remote == out1.Remote {
		t.Error("Remote was not replaced after a servers change")
	}
}

// TestRefreshKeepsTheClientWhenServersAreUnchanged pins the comparison: an
// unchanged section and key cost no rebuild.
func TestRefreshKeepsTheClientWhenServersAreUnchanged(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	src.loaded.Servers = remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}

	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	calls := 0
	w.remoteFor = func(servers remote.Servers, key []byte) (RemoteClient, error) {
		calls++
		return &fakeRemote{}, nil
	}

	out1 := w.Refresh(Runtime{})
	if calls != 1 {
		t.Fatalf("remoteFor calls = %d, want 1", calls)
	}

	src.version = 2
	out2 := w.Refresh(out1)
	if calls != 1 {
		t.Errorf("remoteFor calls = %d, want still 1 with unchanged servers", calls)
	}
	if out2.Remote != out1.Remote {
		t.Error("Remote was replaced despite unchanged servers and key")
	}
}

// TestRefreshKeepsTheInstalledClientWhenTheBuildFails pins the failure arm: a
// build error keeps the client already installed and warns once.
func TestRefreshKeepsTheInstalledClientWhenTheBuildFails(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	src.loaded.Servers = remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}

	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	w.resolve = func(cfg *policy.Classify, key string, getenv func(string) string) classify.Classifier {
		return &classify.Fake{}
	}
	first := &fakeRemote{}
	calls := 0
	w.remoteFor = func(servers remote.Servers, key []byte) (RemoteClient, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return nil, errors.New("bad key")
	}
	var warns []string
	w.warn = func(msg string, args ...any) { warns = append(warns, msg) }

	out1 := w.Refresh(Runtime{})
	if out1.Remote != first {
		t.Fatalf("Remote = %v, want the first client", out1.Remote)
	}

	src.version = 2
	src.loaded.Servers = remote.Servers{
		"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"},
		"api": {URL: "https://api:7777", Fingerprint: "sha256:11"},
	}
	out2 := w.Refresh(out1)
	if out2.Remote != first {
		t.Error("Remote changed after a failed build")
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "keeping the installed one") {
		t.Errorf("warns = %v, want exactly one naming the installed client", warns)
	}
}

// TestRefreshBuildsTheClientForAStoredKey pins that the production default
// seam, set by the constructor, installs a client for a stored key.
func TestRefreshBuildsTheClientForAStoredKey(t *testing.T) {
	t.Parallel()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("remote.MarshalPrivate: %v", err)
	}

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	src.loaded.Servers = remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}
	src.loaded.ClientKey = pem

	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	out := w.Refresh(Runtime{})
	if out.Remote == nil {
		t.Fatal("Remote = nil, want the constructor's default seam to build a client")
	}
}

// TestRefreshWithoutAKeyWarnsAndCarriesNoClient pins that a keyless config
// never leaves a stale client behind.
func TestRefreshWithoutAKeyWarnsAndCarriesNoClient(t *testing.T) {
	t.Parallel()

	src := &fakeSource{version: 1, loaded: loaded(t, policy.Policy{}, nil, nil)}
	src.loaded.Servers = remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}

	w := NewConfigWatcher(src, "/cfg/relevo", nil)
	var warns []string
	w.warn = func(msg string, args ...any) { warns = append(warns, msg) }

	out := w.Refresh(Runtime{})
	if out.Remote != nil {
		t.Errorf("Remote = %v, want nil without a key", out.Remote)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "run relevo config server key") {
		t.Errorf("warns = %v, want exactly one naming the fix", warns)
	}
}

// TestDaemonUsesAServerAddedWhileRunning pins the issue's headline one tick
// apart: the config write `relevo config server add` performs is what the
// running daemon's next tick consumes.
func TestDaemonUsesAServerAddedWhileRunning(t *testing.T) {
	t.Parallel()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("remote.MarshalPrivate: %v", err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)
	if err := st.As("cli", "test key").PutSecret(config.SecretClientKey, pem); err != nil {
		t.Fatalf("PutSecret: %v", err)
	}

	rt := Runtime{Store: store.New(t.TempDir()), Config: st}
	w := NewConfigWatcher(st, t.TempDir(), nil)
	daemon := NewDaemon(rt, time.Second).WithRefresh(w.Refresh)

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	if daemon.rt.Remote != nil {
		t.Fatal("Remote != nil before a server is configured")
	}

	body, err := client.EncodeServers(remote.Servers{
		"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"},
	})
	if err != nil {
		t.Fatalf("EncodeServers: %v", err)
	}
	if _, err := st.As("cli", "config server add zen").Put(config.Servers, body); err != nil {
		t.Fatalf("Put servers: %v", err)
	}

	if err := daemon.Tick(context.Background()); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if daemon.rt.Remote == nil {
		t.Fatal("Remote = nil after a server was added while the daemon was running")
	}
}
