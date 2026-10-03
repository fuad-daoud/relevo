package serve

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

type aliveRunner struct{}

func (aliveRunner) Start(context.Context, spawn.ProcSpec) (spawn.ProcHandle, error) {
	return spawn.ProcHandle{}, nil
}

func (aliveRunner) Alive(context.Context, spawn.ProcHandle) (bool, error) { return true, nil }

func (aliveRunner) ExitCode(context.Context, spawn.ProcHandle, string) (int, bool) {
	return 0, false
}

func (aliveRunner) Kill(context.Context, spawn.ProcHandle, string) error { return nil }

func (aliveRunner) Rusage(context.Context, spawn.ProcHandle, string) (spawn.ProcRusage, bool) {
	return spawn.ProcRusage{}, false
}

func newAdminServer(t *testing.T, now time.Time, opts ...func(*Config)) *Server {
	t.Helper()
	cfg := Config{DB: testServeDB(t), Root: t.TempDir(), Now: func() time.Time { return now }, Audiences: []string{testAudience}}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func enrol(t *testing.T, s *Server, label string) remote.ClientID {
	t.Helper()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add(label, remote.MarshalPublic(kp.Public, label), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return id
}

// saveOwnerBinding saves one active claude binding for id, after mutate.
func saveOwnerBinding(t *testing.T, s *Server, id remote.ClientID, name string, mutate ...func(*store.Binding)) store.Binding {
	t.Helper()
	rt := ownerRuntime(t, s, id)
	b := store.Binding{
		Name: name, Owner: string(id), CWD: rt.Store.WorktreePath(name),
		State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
	}
	for _, m := range mutate {
		m(&b)
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding %s: %v", name, err)
	}
	return b
}

func ownerRuntime(t *testing.T, s *Server, id remote.ClientID) relevo.Runtime {
	t.Helper()
	rt, err := s.runtime(id)
	if err != nil {
		t.Fatalf("runtime(%s): %v", id, err)
	}
	return rt
}

// gateCandidateSet writes a one-row candidates.json under root and loads it, so
// the server can project its ledger onto a candidate.
func gateCandidateSet(t *testing.T, root string) *candidate.Set {
	t.Helper()
	path := filepath.Join(root, "candidates.json")
	if err := os.WriteFile(path, []byte(`[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	return set
}

func TestOwnerRuntimeMalformedID(t *testing.T) {
	s := newAdminServer(t, time.Now())
	if _, err := s.OwnerRuntime(remote.ClientID("nope")); err == nil {
		t.Fatal("OwnerRuntime(\"nope\") error = nil, want a malformed-id error")
	}
}

func TestAdminStatusAllOwners(t *testing.T) {
	s := newAdminServer(t, time.Now())
	idA := enrol(t, s, "alice")
	idB := enrol(t, s, "bob")
	saveOwnerBinding(t, s, idA, "app-a")
	saveOwnerBinding(t, s, idB, "app-b")

	statuses, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("got %d owner statuses, want 2", len(statuses))
	}
	if statuses[0].Label != "alice" || statuses[1].Label != "bob" {
		t.Errorf("owners not sorted by label: got [%s, %s], want [alice, bob]", statuses[0].Label, statuses[1].Label)
	}
	if len(statuses[0].Report.Bindings) != 1 || statuses[0].Report.Bindings[0].Name != "app-a" {
		t.Errorf("alice bindings = %+v, want [app-a]", statuses[0].Report.Bindings)
	}
	if len(statuses[1].Report.Bindings) != 1 || statuses[1].Report.Bindings[0].Name != "app-b" {
		t.Errorf("bob bindings = %+v, want [app-b]", statuses[1].Report.Bindings)
	}
}

func TestAdminStatusReportsHeadlessLivenessThroughRunner(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	d := testServeDB(t)
	newServer := func(r spawn.Runner) *Server {
		t.Helper()
		s, err := New(Config{DB: d, Root: root, Now: func() time.Time { return now }, Runner: r, Audiences: []string{testAudience}})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return s
	}

	s := newServer(aliveRunner{})
	id := enrol(t, s, "alice")
	rt := ownerRuntime(t, s, id)
	if err := rt.Store.Save(store.Binding{
		Name:  "app",
		Owner: string(id),
		CWD:   rt.Store.WorktreePath("app"),
		State: store.StateActive,
		Round: 1,
		Builder: store.Endpoint{
			Kind: "claude", Mode: store.ModeHeadless, PID: 4242, LogPath: filepath.Join(root, "builder.log"),
		},
	}); err != nil {
		t.Fatal(err)
	}

	statuses, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus with runner: %v", err)
	}
	if got := statuses[0].Report.Bindings[0].BuilderStatus; got != "working" {
		t.Errorf("BuilderStatus with runner = %q, want working", got)
	}

	statuses, _, err = AdminStatus(context.Background(), newServer(nil))
	if err != nil {
		t.Fatalf("AdminStatus without runner: %v", err)
	}
	if got := statuses[0].Report.Bindings[0].BuilderStatus; got != "unknown" {
		t.Errorf("BuilderStatus without runner = %q, want unknown", got)
	}
}

func TestAdminStatusBuildersHeader(t *testing.T) {
	clock := time.Now()
	env := setupTestEnv(t, func(cfg *Config) {
		cfg.MaxBuilders = 1
		cfg.Now = func() time.Time { return clock }
	})
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")

	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	owners, builders, err := AdminStatus(context.Background(), env.srv)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if builders.Running != 1 || builders.Queued != 1 || builders.Cap != 1 {
		t.Errorf("Builders = %+v, want {Running:1 Queued:1 Cap:1 ...}", builders)
	}
	if len(owners) < 2 || len(owners[1].Report.Bindings) < 1 {
		t.Fatalf("owners = %+v, want at least 2 owners with bindings", owners)
	}
	if got := owners[1].Report.Bindings[0].BuilderStatus; !strings.Contains(got, "queued") {
		t.Errorf("queued row builder status = %q, want queued", got)
	}
}

// seedAbandonedBinding saves an active owned binding last seen at lastSeen.
func seedAbandonedBinding(t *testing.T, s *Server, id remote.ClientID, name string, lastSeen time.Time) {
	t.Helper()
	saveOwnerBinding(t, s, id, name, func(b *store.Binding) {
		b.Serve = &store.ServeFacts{LastSeen: lastSeen}
	})
}

func TestGCAbandonedArchivesOnlyIdleOld(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")

	seedAbandonedBinding(t, s, id, "old-idle", now.Add(-48*time.Hour))
	seedAbandonedBinding(t, s, id, "old-running", now.Add(-48*time.Hour))
	seedAbandonedBinding(t, s, id, "new-idle", now.Add(-1*time.Hour))

	// Make old-running's round 1 running: a plan with no report.
	rt := ownerRuntime(t, s, id)
	if err := rt.Store.AppendLog("old-running", store.LogEntry{
		Round:     1,
		Kind:      store.KindPrompt,
		Direction: store.DirToBuilder,
		TS:        now.Add(-48 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	olderThan := 24 * time.Hour

	dryResults, err := GCAbandoned(ctx, s, olderThan, now, true)
	if err != nil {
		t.Fatalf("GCAbandoned dry run: %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Name != "old-idle" || dryResults[0].Archive {
		t.Fatalf("dry run results = %+v, want one unarchived old-idle", dryResults)
	}
	if _, err := rt.Store.Load("old-idle"); err != nil {
		t.Fatalf("old-idle was removed during dry run: %v", err)
	}

	results, err := GCAbandoned(ctx, s, olderThan, now, false)
	if err != nil {
		t.Fatalf("GCAbandoned actual: %v", err)
	}
	if len(results) != 1 || results[0].Name != "old-idle" || !results[0].Archive {
		t.Fatalf("actual results = %+v, want one archived old-idle", results)
	}
	if _, err := rt.Store.Load("old-idle"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old-idle load err = %v, want ErrNotFound", err)
	}
	if _, err := rt.Store.Load("old-running"); err != nil {
		t.Errorf("old-running was archived but should have been kept: %v", err)
	}
	if _, err := rt.Store.Load("new-idle"); err != nil {
		t.Errorf("new-idle was archived but should have been kept: %v", err)
	}
}

func TestAdminUnbindByLabelAndId(t *testing.T) {
	s := newAdminServer(t, time.Now())
	id := enrol(t, s, "alice")
	saveOwnerBinding(t, s, id, "by-label")
	saveOwnerBinding(t, s, id, "by-id")
	rt := ownerRuntime(t, s, id)
	ctx := context.Background()

	if _, err := AdminUnbind(ctx, s, "alice", "by-label", false); err != nil {
		t.Fatalf("AdminUnbind by label: %v", err)
	}
	if _, err := rt.Store.Load("by-label"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("by-label load err = %v, want ErrNotFound", err)
	}

	if _, err := AdminUnbind(ctx, s, string(id), "by-id", false); err != nil {
		t.Fatalf("AdminUnbind by id: %v", err)
	}
	if _, err := rt.Store.Load("by-id"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("by-id load err = %v, want ErrNotFound", err)
	}
}

// TestAdminOwnerRuntime covers the server-side read helper: two enrolled owners,
// each with one binding holding one report entry, plus the refusal cases -- a
// shared label, and an owner with no bindings directory at all.
func TestAdminOwnerRuntime(t *testing.T) {
	s := newAdminServer(t, time.Now())
	idA := enrol(t, s, "alice")
	idB := enrol(t, s, "bob")
	idDup1 := enrol(t, s, "dup")
	idDup2 := enrol(t, s, "dup")
	idCarol := enrol(t, s, "carol")

	for _, o := range []struct {
		id   remote.ClientID
		name string
	}{{idA, "api"}, {idB, "web"}} {
		saveOwnerBinding(t, s, o.id, o.name)
		rt := ownerRuntime(t, s, o.id)
		if err := rt.Store.AppendLog(o.name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	byLabel, label, err := AdminOwnerRuntime(s, "alice")
	if err != nil {
		t.Fatalf("AdminOwnerRuntime(alice): %v", err)
	}
	if label != "alice" {
		t.Errorf("label = %q, want alice", label)
	}
	byID, labelByID, err := AdminOwnerRuntime(s, string(idA))
	if err != nil {
		t.Fatalf("AdminOwnerRuntime(id): %v", err)
	}
	if labelByID != "alice" {
		t.Errorf("label by id = %q, want alice", labelByID)
	}
	if byLabel.Store.Dir("api") != byID.Store.Dir("api") {
		t.Errorf("by label resolved to %q, by id to %q", byLabel.Store.Dir("api"), byID.Store.Dir("api"))
	}

	if _, _, err := AdminOwnerRuntime(s, "dup"); err == nil ||
		!strings.Contains(err.Error(), "ambiguous") ||
		!strings.Contains(err.Error(), string(idDup1)) || !strings.Contains(err.Error(), string(idDup2)) {
		t.Errorf("ambiguous label err = %v, want one naming %s and %s", err, idDup1, idDup2)
	}

	if _, _, err := AdminOwnerRuntime(s, "nobody"); !errors.Is(err, ErrNoSuchClient) {
		t.Errorf("unknown owner err = %v, want ErrNoSuchClient", err)
	}

	// An enrolled owner with no bindings directory: ErrNotFound, and the
	// directory must still not exist afterwards.
	carolRoot, err := s.ownerRoot(idCarol)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := AdminOwnerRuntime(s, "carol"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("owner with no bindings dir err = %v, want store.ErrNotFound", err)
	}
	if _, statErr := os.Stat(carolRoot); !os.IsNotExist(statErr) {
		t.Errorf("AdminOwnerRuntime created %s (stat err %v)", carolRoot, statErr)
	}
}

func TestAdminUnbindRefusesRunningUnlessForce(t *testing.T) {
	now := time.Now()
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	saveOwnerBinding(t, s, id, "running")
	rt := ownerRuntime(t, s, id)
	if err := rt.Store.AppendLog("running", store.LogEntry{
		Round:     1,
		Kind:      store.KindPrompt,
		Direction: store.DirToBuilder,
		TS:        now,
	}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if _, err := AdminUnbind(ctx, s, "alice", "running", false); err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("AdminUnbind without force: got err %v, want a refusal mentioning 'running'", err)
	}
	if _, err := rt.Store.Load("running"); err != nil {
		t.Errorf("running binding was removed despite the refusal: %v", err)
	}

	if _, err := AdminUnbind(ctx, s, "alice", "running", true); err != nil {
		t.Fatalf("AdminUnbind with force: %v", err)
	}
	if _, err := rt.Store.Load("running"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("running binding still present after forced unbind: %v", err)
	}
}

func TestAdminUnbindAmbiguousLabel(t *testing.T) {
	s := newAdminServer(t, time.Now())
	enrol(t, s, "dup")
	enrol(t, s, "dup")

	ctx := context.Background()
	if _, err := AdminUnbind(ctx, s, "dup", "whatever", false); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("AdminUnbind with ambiguous label: got err %v, want an 'ambiguous' error", err)
	}
}

func TestRenderClients(t *testing.T) {
	enrolled := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name    string
		clients []Client
		want    string
	}{
		{
			name: "empty input",
			want: "no clients\n",
		},
		{
			name: "one enrolled client",
			clients: []Client{
				{ID: remote.ClientID("SHA256:abcdefgh"), Label: "alice", EnrolledAt: enrolled},
			},
			want: "SHA256:abcdefgh  alice  enrolled 2026-01-15\n",
		},
		{
			name: "one revoked client",
			clients: []Client{
				{ID: remote.ClientID("SHA256:abcdefgh"), Label: "alice", EnrolledAt: enrolled, RevokedAt: revoked},
			},
			want: "SHA256:abcdefgh  alice  enrolled 2026-01-15  revoked 2026-03-20\n",
		},
		{
			name: "two clients order preserved",
			clients: []Client{
				{ID: remote.ClientID("SHA256:aaaaaaaa"), Label: "bob", EnrolledAt: enrolled},
				{ID: remote.ClientID("SHA256:bbbbbbbb"), Label: "alice", EnrolledAt: enrolled, RevokedAt: revoked},
			},
			want: "SHA256:aaaaaaaa  bob  enrolled 2026-01-15\nSHA256:bbbbbbbb  alice  enrolled 2026-01-15  revoked 2026-03-20\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RenderClients(tc.clients); got != tc.want {
				t.Errorf("RenderClients() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAdminGatesAvailableUnavailable: AdminGates on an empty ledger is empty,
// AdminUnavailable records a gate RenderGates names, and AdminAvailable lifts
// it. The lock store the ledger mutates through must not make an uninitialised
// root look initialised.
func TestAdminGatesAvailableUnavailable(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	s := newAdminServer(t, now, func(c *Config) {
		c.Root = root
		c.Candidates = gateCandidateSet(t, root)
	})

	initialisedBefore, err := Initialised(root, s.DB())
	if err != nil {
		t.Fatalf("Initialised: %v", err)
	}

	if gates := AdminGates(s); len(gates) != 0 {
		t.Fatalf("AdminGates = %v, want none on an empty ledger", gates)
	}
	if out := RenderGates(AdminGates(s), now); out != "no gates\n" {
		t.Errorf("RenderGates(empty) = %q, want %q", out, "no gates\n")
	}

	provider, err := AdminUnavailable(s, "claude/t/m", time.Time{}, "quota")
	if err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	if provider != "t" {
		t.Errorf("AdminUnavailable provider = %q, want t", provider)
	}

	gates := AdminGates(s)
	if len(gates) != 1 {
		t.Fatalf("AdminGates = %v, want one gate", gates)
	}
	if gates[0].Kind != availability.RateLimited {
		t.Errorf("gate kind = %q, want %q", gates[0].Kind, availability.RateLimited)
	}
	out := RenderGates(gates, now)
	if !strings.Contains(out, "m  rate-limited") {
		t.Errorf("RenderGates = %q, want it naming the candidate's short name m", out)
	}
	if strings.Contains(out, "claude/t/m") {
		t.Errorf("RenderGates = %q, want the short name, not the token", out)
	}
	if !strings.Contains(out, "quota") {
		t.Errorf("RenderGates = %q, want it naming quota", out)
	}

	provider, removed, err := AdminAvailable(s, "t")
	if err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	}
	if provider != "t" || removed != 1 {
		t.Errorf("AdminAvailable = %q, %d, want t, 1", provider, removed)
	}
	if gates := AdminGates(s); len(gates) != 0 {
		t.Errorf("AdminGates = %v, want none after AdminAvailable", gates)
	}

	initialisedAfter, err := Initialised(root, s.DB())
	if err != nil {
		t.Fatalf("Initialised: %v", err)
	}
	if initialisedAfter != initialisedBefore {
		t.Errorf("Initialised(root) = %v after the admin calls, want %v: the lock store must not fake an init", initialisedAfter, initialisedBefore)
	}
}

// TestAdminAvailableRecordsServerClear: the server host's own clear is recorded
// as the server's, not a mastermind's.
func TestAdminAvailableRecordsServerClear(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	s := newAdminServer(t, now, func(c *Config) {
		c.Root = root
		c.Candidates = gateCandidateSet(t, root)
	})

	if _, err := AdminUnavailable(s, "claude/t/m", time.Time{}, "quota"); err != nil {
		t.Fatalf("AdminUnavailable: %v", err)
	}
	if _, removed, err := AdminAvailable(s, "t"); err != nil {
		t.Fatalf("AdminAvailable: %v", err)
	} else if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	h, err := availability.LoadHistory(availability.Deps{Gates: db.PrefixKV{KV: s.DB(), Prefix: "serve."}, Now: time.Now})
	if err != nil {
		t.Fatalf("history.LoadHistory: %v", err)
	}
	if len(h.Events) == 0 {
		t.Fatal("the availability row has no events, want a Cleared event")
	}
	ev := h.Events[len(h.Events)-1]
	if ev.Kind != availability.Cleared {
		t.Errorf("kind = %q, want %q", ev.Kind, availability.Cleared)
	}
	if ev.Source != availability.ClearedByServer {
		t.Errorf("source = %q, want %q", ev.Source, availability.ClearedByServer)
	}
	if ev.Provider != "t" {
		t.Errorf("provider = %q, want t", ev.Provider)
	}
}

// TestFlatStatusStampsOwnersAndDedupsGates: Owner/OwnerLabel on every row,
// owners by label, Key() distinct across two owners sharing a binding name, and
// the server-wide ledger gate appearing once, not once per owner.
func TestFlatStatusStampsOwnersAndDedupsGates(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	s := newAdminServer(t, now, func(c *Config) {
		c.Root = root
		c.Candidates = gateCandidateSet(t, root)
	})
	idA := enrol(t, s, "alice")
	idB := enrol(t, s, "bob")
	saveOwnerBinding(t, s, idA, "persist")
	saveOwnerBinding(t, s, idB, "persist")

	rtA := ownerRuntime(t, s, idA)
	if _, err := availability.Unavailable(relevo.AvailabilityDeps(rtA), "claude/t/m", now.Add(time.Hour), "quota"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	out, err := FlatStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("FlatStatus: %v", err)
	}

	if len(out.Bindings) != 2 {
		t.Fatalf("got %d rows, want 2", len(out.Bindings))
	}
	if out.Bindings[0].Owner != string(idA) || out.Bindings[0].OwnerLabel != "alice" || out.Bindings[0].Name != "persist" {
		t.Errorf("row 0 = %+v", out.Bindings[0])
	}
	if out.Bindings[1].Owner != string(idB) || out.Bindings[1].OwnerLabel != "bob" || out.Bindings[1].Name != "persist" {
		t.Errorf("row 1 = %+v", out.Bindings[1])
	}
	if out.Bindings[0].Key() == out.Bindings[1].Key() {
		t.Errorf("Key() must separate two owners' same-named bindings: %q", out.Bindings[0].Key())
	}
	if len(out.Gated) != 1 {
		t.Errorf("len(out.Gated) = %d, want 1 (one ledger gate, not one per owner)", len(out.Gated))
	}
	if out.DoneHidden != 0 {
		t.Errorf("DoneHidden = %d, want 0", out.DoneHidden)
	}
}

// TestStatusDocumentLastContact: last_contact is the max LastSeen over every
// owner; an owner that has never seen a request marshals last_seen as null; and
// the owners array keeps the label order AdminStatus hands over.
func TestStatusDocumentLastContact(t *testing.T) {
	t1 := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)

	owners := []OwnerStatus{
		{Owner: remote.ClientID("SHA256:alice"), Label: "alice"},
		{Owner: remote.ClientID("SHA256:bob"), Label: "bob", LastSeen: t1},
		{Owner: remote.ClientID("SHA256:carol"), Label: "carol", LastSeen: t2},
	}

	doc := StatusDocument(owners, remote.BuildersView{Running: 1, Cap: 2})
	if doc.LastContact == nil || !doc.LastContact.Equal(t2) {
		t.Fatalf("LastContact = %v, want %v", doc.LastContact, t2)
	}
	if len(doc.Owners) != 3 {
		t.Fatalf("got %d owners, want 3", len(doc.Owners))
	}
	for i, want := range []string{"alice", "bob", "carol"} {
		if doc.Owners[i].Label != want {
			t.Errorf("owners[%d].Label = %q, want %q: owners stay label-sorted", i, doc.Owners[i].Label, want)
		}
	}
	if doc.Owners[0].Owner != "SHA256:alice" {
		t.Errorf("owners[0].Owner = %q, want SHA256:alice", doc.Owners[0].Owner)
	}
	if doc.Owners[0].LastSeen != nil {
		t.Errorf("owners[0].LastSeen = %v, want nil for the owner with no contact", doc.Owners[0].LastSeen)
	}
	if doc.Owners[1].LastSeen == nil || !doc.Owners[1].LastSeen.Equal(t1) {
		t.Errorf("owners[1].LastSeen = %v, want %v", doc.Owners[1].LastSeen, t1)
	}
	if doc.Owners[2].LastSeen == nil || !doc.Owners[2].LastSeen.Equal(t2) {
		t.Errorf("owners[2].LastSeen = %v, want %v", doc.Owners[2].LastSeen, t2)
	}

	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"last_seen":null`) {
		t.Errorf("marshalled document has no \"last_seen\":null for the zero owner:\n%s", blob)
	}
	if !strings.Contains(string(blob), `"last_contact":"2026-09-20T12:00:00Z"`) {
		t.Errorf("marshalled last_contact is not the max, as RFC 3339 UTC:\n%s", blob)
	}
}

// TestStatusDocumentEmpty: no owners still prints the three top-level keys, with
// last_contact null and owners an empty array -- [] and never null.
func TestStatusDocumentEmpty(t *testing.T) {
	blob, err := json.Marshal(StatusDocument(nil, remote.BuildersView{}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"runners":{"running":0,"queued":0,"cap":0,"scopes":false},"last_contact":null,"owners":[]}`
	if string(blob) != want {
		t.Errorf("StatusDocument(nil) JSON = %s, want %s", blob, want)
	}
}

// seedGoneRowOwner enrols one owner holding a real binding and a chain whose
// name is not a binding of its own: the chain's row stands in for its member's
// row, so the owner's report names a row the store cannot Load.
func seedGoneRowOwner(t *testing.T, s *Server, label, chainName, member string) remote.ClientID {
	t.Helper()
	id := enrol(t, s, label)
	rt := ownerRuntime(t, s, id)
	if err := rt.Store.Save(store.Binding{
		Name: member, Owner: string(id), CWD: rt.Store.WorktreePath(member),
		State: store.StateActive, Round: 1, Builder: store.Endpoint{Kind: "claude"},
	}); err != nil {
		t.Fatalf("save member binding %s: %v", member, err)
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.ChainPut(db.ChainRow{
			ID: chainName, Owner: string(id), Name: chainName,
			Status: string(chain.StatusRunning), Phase: string(chain.PhaseBuild),
			Step: string(chain.StepBuilding), Plan: 1, Plans: 1, Builder: member,
		})
	})
	if err != nil {
		t.Fatalf("ChainPut %s: %v", chainName, err)
	}
	return id
}

// TestAdminStatusKeepsARowTheStoreLacks pins that a row the store no longer
// holds keeps its row, marked gone and unenriched, and that the verb still
// answers with every other owner and binding.
func TestAdminStatusKeepsARowTheStoreLacks(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	seedGoneRowOwner(t, s, "alice", "board-23", "board-23-web")
	idB := enrol(t, s, "bob")
	seen := now.Add(-2 * time.Hour)
	saveOwnerBinding(t, s, idB, "app-b", func(b *store.Binding) {
		b.Serve = &store.ServeFacts{LastSeen: seen}
	})

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v, want the gone row to leave the verb answering", err)
	}
	if len(owners) != 2 || owners[0].Label != "alice" || owners[1].Label != "bob" {
		t.Fatalf("owners = %+v, want alice then bob", owners)
	}
	if len(owners[0].Report.Bindings) != 1 {
		t.Fatalf("alice bindings = %+v, want the gone row kept", owners[0].Report.Bindings)
	}
	gone := owners[0].Report.Bindings[0]
	if gone.Name != "board-23" {
		t.Errorf("alice's row name = %q, want board-23", gone.Name)
	}
	if gone.Detail != goneRowNote {
		t.Errorf("gone row detail = %q, want %q", gone.Detail, goneRowNote)
	}
	if !owners[0].LastSeen.IsZero() {
		t.Errorf("alice LastSeen = %v, want the zero time: a gone row reports no contact", owners[0].LastSeen)
	}
	if got := owners[1].Report.Bindings[0].Name; got != "app-b" {
		t.Errorf("bob's row = %q, want app-b", got)
	}
	if !owners[1].LastSeen.Equal(seen) {
		t.Errorf("bob LastSeen = %v, want %v: the other owners still answer", owners[1].LastSeen, seen)
	}

	blob, err := json.Marshal(StatusDocument(owners, remote.BuildersView{}))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"detail":"`+goneRowNote+`"`) {
		t.Errorf("the status document does not carry the gone note:\n%s", blob)
	}

	flat, err := FlatStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("FlatStatus: %v", err)
	}
	out := view.RenderStatus(flat)
	if !strings.Contains(out, goneRowNote) {
		t.Errorf("RenderStatus = %q, want the gone note on the human row", out)
	}
}

// TestAdminStatusFailsOnAStoreErrorThatIsNotNotFound pins that the gone arm is
// narrow: a store failure that is not the missing-binding predicate still takes
// the verb down, so a real store fault cannot pass for a stale reference.
func TestAdminStatusFailsOnAStoreErrorThatIsNotNotFound(t *testing.T) {
	s := newAdminServer(t, time.Now())
	seedGoneRowOwner(t, s, "alice", "Board-23", "board-23-web")

	owners, _, err := AdminStatus(context.Background(), s)
	if err == nil {
		t.Fatalf("AdminStatus = %+v, nil; want a store failure that is not not-found to fail the verb", owners)
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Errorf("AdminStatus err = %v, want the error the store raised, not a missing binding", err)
	}
	if !strings.Contains(err.Error(), "Board-23") {
		t.Errorf("AdminStatus err = %v, want it naming the row that could not be read", err)
	}
}

// TestAdminStatusLastSeen: an owner's LastSeen is the newest Serve.LastSeen over
// its bindings, with no RoundStartedAt fallback.
func TestAdminStatusLastSeen(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	idA := enrol(t, s, "alice")
	seen := now.Add(-3 * time.Hour)
	saveOwnerBinding(t, s, idA, "app-a", func(b *store.Binding) {
		b.Serve = &store.ServeFacts{LastSeen: seen}
	})
	idB := enrol(t, s, "bob")
	saveOwnerBinding(t, s, idB, "app-b", func(b *store.Binding) {
		b.RoundStartedAt = now.Add(-9 * time.Hour)
	})

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if len(owners) != 2 || owners[0].Label != "alice" || owners[1].Label != "bob" {
		t.Fatalf("owners = %+v, want alice then bob", owners)
	}
	if !owners[0].LastSeen.Equal(seen) {
		t.Errorf("alice LastSeen = %v, want the binding's Serve.LastSeen %v", owners[0].LastSeen, seen)
	}
	if !owners[1].LastSeen.IsZero() {
		t.Errorf("bob LastSeen = %v, want the zero time: RoundStartedAt is no contact", owners[1].LastSeen)
	}

	doc := StatusDocument(owners, remote.BuildersView{})
	if doc.LastContact == nil || !doc.LastContact.Equal(seen) {
		t.Errorf("LastContact = %v, want %v", doc.LastContact, seen)
	}
}

// TestServeStatusJSONKeysBurstReads pins the literal JSON keys in the
// `relevo serve status --json` document that an external program parses: the
// burst provider in fuad-daoud/servers reads them over ssh to confirm a node
// is idle before tearing it down. The keys are named literally here rather
// than left to the golden file, because `-update` regenerates that golden
// silently, so a rename would pass CI unnoticed.
func TestServeStatusJSONKeysBurstReads(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	id := enrol(t, s, "alice")
	saveOwnerBinding(t, s, id, "app-a", func(b *store.Binding) {
		b.Round = 2
		b.BuilderCandidate = "claude/test/m"
		b.Serve = &store.ServeFacts{LastSeen: now}
	})

	owners, _, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	doc := StatusDocument(owners, remote.BuildersView{Running: 1, Cap: 2})

	blob, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(blob, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	requireKeys(t, m, "runners", "last_contact")
	requireKeys(t, m["runners"], "running", "queued", "cap")

	ownersArr, ok := m["owners"].([]any)
	if !ok || len(ownersArr) == 0 {
		t.Fatalf("owners is not a non-empty JSON array: %T %v", m["owners"], m["owners"])
	}
	owner0, ok := ownersArr[0].(map[string]any)
	if !ok {
		t.Fatalf("owners[0] is not a JSON object: %T", ownersArr[0])
	}
	report, ok := owner0["report"].(map[string]any)
	if !ok {
		t.Fatalf("owners[0].report is not a JSON object: %T", owner0["report"])
	}
	bindings, ok := report["bindings"].([]any)
	if !ok || len(bindings) == 0 {
		t.Fatalf("owners[0].report.bindings is not a non-empty JSON array: %T %v", report["bindings"], report["bindings"])
	}
	b0, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("bindings[0] is not a JSON object: %T", bindings[0])
	}
	requireKeys(t, b0, "name", "round", "state", "candidate", "runner_status")

	if b0["name"] != "app-a" {
		t.Errorf("bindings[0] name = %v, want app-a: the walk must read the seeded binding, not a zero object", b0["name"])
	}
	if b0["round"] != float64(2) {
		t.Errorf("bindings[0] round = %v, want 2 (float64 after Unmarshal)", b0["round"])
	}
}

// TestStatusDocumentCarriesIsolation pins slice A's status fact: the builder
// view `relevo serve status` prints carries the server's isolation mode, none
// when the server configured nothing.
func TestStatusDocumentCarriesIsolation(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := newAdminServer(t, now)
	_, builders, err := AdminStatus(context.Background(), s)
	if err != nil {
		t.Fatalf("AdminStatus: %v", err)
	}
	if builders.Isolation != "none" {
		t.Fatalf("builders.Isolation = %q, want none", builders.Isolation)
	}
	if builders.Image != "" {
		t.Fatalf("builders.Image = %q, want empty in none mode", builders.Image)
	}
}

// requireKeys fails unless obj is a JSON object holding every key. Each
// missing key names the external reader that depends on it.
func requireKeys(t *testing.T, obj any, keys ...string) {
	t.Helper()
	m, ok := obj.(map[string]any)
	if !ok {
		t.Fatalf("value is not a JSON object, so its keys %v cannot be read: %T", keys, obj)
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing JSON key %q: the burst provider in fuad-daoud/servers reads %q; renaming it needs a burst release that accepts the new name first", k, k)
		}
	}
}

// TestRenderGatesSanitizesNote pins that a gate's note is sanitised before it
// is printed, so a reason stored before the fix cannot draw a control sequence.
func TestRenderGatesSanitizesNote(t *testing.T) {
	gates := []availability.Gate{{
		Token: "claude/t/m",
		Name:  "m",
		Kind:  availability.RateLimited,
		Note:  "quota \x1b[2J",
	}}
	out := RenderGates(gates, time.Now())
	if strings.ContainsRune(out, '\x1b') {
		t.Errorf("RenderGates = %q, want the control byte replaced", out)
	}
	if !strings.Contains(out, "\uFFFD") {
		t.Errorf("RenderGates = %q, want a replacement rune", out)
	}
}
