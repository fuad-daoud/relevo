package relevo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive the config document through the real sync pipeline on the
// in-memory log: nothing here binds the document by hand, so a construction
// site that forgets to bind it fails.

var cfgSyncKey = []byte("0123456789abcdef0123456789abcdef")

const (
	cfgCandidate = `[{"name":"m","harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`
	cfgToken     = "FIXTURE-JOIN-TOKEN-0123456789abcdef0123456789abcdef"
)

type cfgMachine struct {
	st     *store.Store
	mdb    *db.DB
	local  relevosync.Local
	cfg    *config.Store
	runner *relevosync.Runner
	log    *synclog.MemTransport
}

// newCfgMachine is one machine over a real split pair. A nil key is a locked
// machine, and enabled is whether sync is already on.
func newCfgMachine(t *testing.T, log *synclog.MemTransport, key []byte, enabled bool) *cfgMachine {
	t.Helper()
	st := store.New(t.TempDir())
	mdb, err := st.DB()
	if err != nil {
		t.Fatalf("store db: %v", err)
	}
	local, err := relevosync.LocalHandle(mdb)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	if enabled {
		if err := relevosync.MarkEnabled(local, true, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("MarkEnabled: %v", err)
		}
	}
	if key != nil {
		if err := relevosync.SetKey(local, key, time.Unix(0, 0).UTC()); err != nil {
			t.Fatalf("SetKey: %v", err)
		}
	}
	return &cfgMachine{
		st: st, mdb: mdb, local: local, cfg: config.Open(mdb), log: log,
		runner: &relevosync.Runner{Client: log.OnLog(mdb.Origin()), Local: local, Timeout: time.Second},
	}
}

func (m *cfgMachine) attempt(t *testing.T) {
	t.Helper()
	if res := m.runner.SyncOnce(context.Background(), m.mdb); res.Err != nil {
		t.Fatalf("SyncOnce: %v", res.Err)
	}
}

func (m *cfgMachine) set(t *testing.T, sec config.Section, body string) {
	t.Helper()
	if _, err := m.cfg.As("config set", "").Put(sec, []byte(body)); err != nil {
		t.Fatalf("config set %s: %v", sec, err)
	}
}

func (m *cfgMachine) body(t *testing.T, sec config.Section) string {
	t.Helper()
	body, _, err := m.cfg.Body(sec)
	if err != nil {
		t.Fatalf("Body(%s): %v", sec, err)
	}
	return string(body)
}

func (m *cfgMachine) label(t *testing.T) string {
	t.Helper()
	list, err := m.mdb.InstallationList()
	if err != nil {
		t.Fatalf("InstallationList: %v", err)
	}
	for _, inst := range list {
		if inst.ID == m.mdb.Origin() && inst.Label != "" {
			return inst.Label
		}
	}
	return m.mdb.Origin()
}

func (m *cfgMachine) sources(t *testing.T) []string {
	t.Helper()
	rows, err := m.cfg.Log(0)
	if err != nil {
		t.Fatalf("config log: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Source)
	}
	return out
}

func (m *cfgMachine) hasSource(t *testing.T, want string) bool {
	t.Helper()
	for _, s := range m.sources(t) {
		if s == want {
			return true
		}
	}
	return false
}

func TestSteadyAttemptCarriesAConfigEditToAPeer(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.set(t, config.Candidates, cfgCandidate)
	a.attempt(t)
	b.attempt(t)

	loaded, err := b.cfg.Load()
	if err != nil {
		t.Fatalf("Load on B: %v", err)
	}
	if loaded.Candidates == nil || loaded.Candidates.Len() != 1 {
		t.Errorf("B's candidates = %v, want A's candidate", loaded.Candidates)
	}
	if want := "sync:" + a.label(t); !b.hasSource(t, want) {
		t.Errorf("B's revision sources %v lack %q", b.sources(t), want)
	}
}

func TestPushAndPullVerbsCarryAConfigEdit(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	a.set(t, config.Candidates, cfgCandidate)
	if _, err := exportOnce(a.mdb, log.OnLog(a.mdb.Origin())); err != nil {
		t.Fatalf("push from A: %v", err)
	}
	if _, err := importOnce(b.mdb, log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("pull on B: %v", err)
	}
	if got := b.body(t, config.Candidates); !strings.Contains(got, "sonnet") {
		t.Errorf("B's candidates after a pull = %s, want A's candidate", got)
	}
}

func TestSteadyAttemptKeepsMachineFieldsHome(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	// A claude account is invalid without a config_dir, so B holds its own
	// before A's edit arrives.
	b.set(t, config.Candidates, cfgCandidate)
	b.set(t, config.Accounts, cfgAccountOn("/only-on-b", `"anthropic"`))
	time.Sleep(20 * time.Millisecond)
	a.set(t, config.Candidates, cfgCandidate)
	a.set(t, config.Accounts, cfgAccountOn("/only-on-a", `"anthropic","extra"`))
	a.attempt(t)
	b.attempt(t)

	got := b.body(t, config.Accounts)
	if !strings.Contains(got, `"extra"`) {
		t.Errorf("B's accounts = %s, want A's edit of the account", got)
	}
	if strings.Contains(got, "/only-on-a") || !strings.Contains(got, "/only-on-b") {
		t.Errorf("B's accounts = %s, want B's own config_dir and not A's", got)
	}
}

func TestLockedPeerParksConfigUntilUnlock(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, nil, true)

	a.set(t, config.Candidates, cfgCandidate)
	a.attempt(t)
	b.attempt(t)

	if got := b.body(t, config.Candidates); strings.Contains(got, "sonnet") {
		t.Fatalf("locked B applied %s, want it parked", got)
	}
	if parked, err := b.mdb.PendingDocs(); err != nil || len(parked) == 0 {
		t.Fatalf("pending = %v, %v; want A's entry parked", parked, err)
	}

	if err := relevosync.SetKey(b.local, cfgSyncKey, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if _, err := relevosync.DrainParkedDocs(b.mdb, b.local, log.OnLog(b.mdb.Origin())); err != nil {
		t.Fatalf("DrainParkedDocs: %v", err)
	}
	if got := b.body(t, config.Candidates); !strings.Contains(got, "sonnet") {
		t.Errorf("B's candidates after the unlock = %s, want A's candidate", got)
	}
}

func TestJoinBringsTheSharedConfig(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, false)

	a.set(t, config.Candidates, cfgCandidate)
	a.attempt(t)

	if _, _, err := db.CompressHistoryOnce(b.mdb, t.TempDir(), time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("CompressHistoryOnce: %v", err)
	}
	if err := relevosync.SetR2(b.local, verbFixtureR2, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("SetR2: %v", err)
	}
	enabler := &relevosync.Enabler{
		Request: relevosync.EnableRequest{
			Local: b.local, Shared: b.mdb, URL: "libsql://join.invalid", Token: []byte(cfgToken),
		},
		Transport: log.OnLog(b.mdb.Origin()),
		Now:       func() time.Time { return time.Unix(0, 0).UTC() },
	}
	if _, err := enabler.Enable(); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if got := b.body(t, config.Candidates); !strings.Contains(got, "sonnet") {
		t.Errorf("B's candidates after the join = %s, want A's candidate", got)
	}
}

func TestConfigEditMarksTheOutboxPending(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	m := newCfgMachine(t, log, cfgSyncKey, true)
	d := NewDaemon(Runtime{Store: m.st, Sync: m.runner}, time.Second)
	m.attempt(t)
	if d.outboxPending() {
		t.Fatal("outbox pending after the first export, want nothing waiting")
	}

	m.set(t, config.Candidates, cfgCandidate)
	if !d.outboxPending() {
		t.Error("outbox not pending after a config write")
	}
	m.attempt(t)
	if d.outboxPending() {
		t.Error("outbox still pending after the export")
	}
}

func TestLockedMachineDoesNotCountConfigSignalsAsPending(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	m := newCfgMachine(t, log, nil, true)
	d := NewDaemon(Runtime{Store: m.st, Sync: m.runner}, time.Second)

	m.set(t, config.Candidates, cfgCandidate)
	if d.outboxPending() {
		t.Error("a locked machine reports a signal it can never export as pending")
	}
}

// A served daemon drives the runner it shares with the verbs, and its runtime
// seam holds no machine-local file; the key is read where the exporter reads it.
func TestServedDaemonCountsConfigSignalsWithTheSharedRunnersKey(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	m := newCfgMachine(t, log, cfgSyncKey, true)
	d := NewDaemon(Runtime{Store: m.st, Sync: &relevosync.Runner{}}, time.Second)
	d.SetSyncVerbs(&VerbRunner{Runner: m.runner})
	m.attempt(t)

	m.set(t, config.Candidates, cfgCandidate)
	if !d.outboxPending() {
		t.Error("outbox not pending after a config write on a served daemon")
	}
}

// TestAppliedConfigItemReloadsTheRunningDaemon pins that an item the importer
// applies moves the config version the daemon's watcher compares, so the
// running daemon reloads it with no restart.
func TestAppliedConfigItemReloadsTheRunningDaemon(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	watcher := NewConfigWatcher(b.cfg, t.TempDir(), func(string) string { return "" })
	rt := watcher.Refresh(Runtime{})
	if rt.Candidates != nil && rt.Candidates.Len() != 0 {
		t.Fatalf("B starts with candidates %v", rt.Candidates)
	}

	a.set(t, config.Candidates, cfgCandidate)
	a.attempt(t)
	b.attempt(t)

	rt = watcher.Refresh(rt)
	if rt.Candidates == nil || rt.Candidates.Len() != 1 {
		t.Errorf("the daemon's candidates after the sync = %v, want the applied one", rt.Candidates)
	}
}

func cfgAccountOn(configDir, groups string) string {
	return `[{"name":"work","harness":"claude","groups":[` + groups + `],"config_dir":"` + configDir + `"}]`
}
