package mastermind

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func testDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func testRegistry(t *testing.T) *DBRegistry {
	t.Helper()
	return testRegistryOn(t, testDB(t))
}

// testRegistryOn is a DBRegistry over an existing database handle, so two
// registries over one handle share a database like two processes do.
func testRegistryOn(t *testing.T, d *db.DB) *DBRegistry {
	t.Helper()
	return &DBRegistry{
		KV:  db.TxKV{DB: d},
		Now: func() time.Time { return testNow },
	}
}

func mustCreate(t *testing.T, reg *DBRegistry, rec Record) Record {
	t.Helper()
	got, err := reg.Create(rec)
	if err != nil {
		t.Fatalf("Create(%s): %v", rec.ID, err)
	}
	return got
}

func record(id, name, kind, session string, host int) Record {
	return Record{
		ID:            id,
		Name:          name,
		HarnessKind:   kind,
		SessionID:     session,
		HostPID:       host,
		HostStartedAt: int64(host) * 10,
		CWD:           "/tmp/relevo-mastermind-test",
		CreatedAt:     testNow,
		SeenAt:        testNow,
	}
}

func TestNewIDShape(t *testing.T) {
	// A pinned reader makes the id exactly predictable.
	if got, err := NewID(bytes.NewReader(make([]byte, 8))); err != nil {
		t.Fatalf("NewID: %v", err)
	} else if got != "mm_aaaaaaaaaaaa" {
		t.Errorf("NewID(zero reader) = %q, want %q", got, "mm_aaaaaaaaaaaa")
	}

	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id, err := NewID(rand.Reader)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if err := ValidID(id); err != nil {
			t.Fatalf("NewID produced %q: %v", id, err)
		}
		if !strings.HasPrefix(id, "mm_") {
			t.Fatalf("NewID produced %q, want the mm_ prefix", id)
		}
		if len(id) != len("mm_")+idChars {
			t.Fatalf("NewID produced %q, want %d characters", id, len("mm_")+idChars)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}

	if _, err := NewID(bytes.NewReader(nil)); err == nil {
		t.Error("NewID with an empty reader: want an error")
	}
}

func TestValidName(t *testing.T) {
	valid := []string{
		"a",
		"architect-1",
		"claude-1",
		"a0",
		"a-",
		"a" + strings.Repeat("b", MaxNameLen-1), // 32 characters: the cap
	}
	for _, name := range valid {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}

	invalid := []string{
		"",
		"A",
		"Architect-1",
		"1a",
		"-a",
		"_a",
		"a_b",
		"a.b",
		"a b",
		"architect_1",
		"a" + strings.Repeat("b", MaxNameLen), // 33 characters
	}
	for _, name := range invalid {
		err := ValidName(name)
		if err == nil {
			t.Errorf("ValidName(%q) = nil, want an error", name)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidName(%q) = %v, want ErrInvalid", name, err)
		}
	}

	if err := ValidID("mm_aaaaaaaaaaaa"); err != nil {
		t.Errorf("ValidID(valid) = %v, want nil", err)
	}
	for _, id := range []string{"mm_AAAAAAAAAAAA", "mm_aaaaaaaaaaa", "mm_aaaaaaaaaaaaa", "aaaaaaaaaaaa", "mm_aaaaaaaaaab1"} {
		if err := ValidID(id); err == nil {
			t.Errorf("ValidID(%q) = nil, want an error", id)
		}
	}
}

// TestNewIDMintsMm pins D4: every new id is `mm_` plus 12 lowercase base32
// characters.
func TestNewIDMintsMm(t *testing.T) {
	id, err := NewID(bytes.NewReader(make([]byte, 8)))
	if err != nil {
		t.Fatalf("NewID: %v", err)
	}
	if id != "mm_aaaaaaaaaaaa" {
		t.Errorf("NewID(zero reader) = %q, want mm_aaaaaaaaaaaa", id)
	}
	if !strings.HasPrefix(id, "mm_") {
		t.Errorf("NewID = %q, want the mm_ prefix", id)
	}
}

// TestValidIDAcceptsLegacyPlAndULID pins every id shape: the `mm_` ids NewID
// mints, the historical `pl_` shape still in a real relevo.db, and a legacy
// ULID.
func TestValidIDAcceptsLegacyPlAndULID(t *testing.T) {
	valid := []string{
		"mm_aaaaaaaaaaaa",
		"mm_zzzzzzzzzzzz",
		"pl_aaaaaaaaaaaa",
		"pl_zzzzzzzzzzzz",
		"01M3252956S27X5G5MPVM77PJ7",
		"7ZZZZZZZZZZZZZZZZZZZZZZZZZ",
	}
	for _, id := range valid {
		if err := ValidID(id); err != nil {
			t.Errorf("ValidID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"01m3252956s27x5g5mpvm77pj7",  // a lowercase ULID is the wrong spelling of the shape
		"01M3252956S27X5G5MPVM77PJ",   // 25 characters
		"01M3252956S27X5G5MPVM77PJ77", // 27 characters
		"01M3252956I27X5G5MPVM77PJ7",  // I is not in the Crockford alphabet
		"01M3252956L27X5G5MPVM77PJ7",  // L
		"01M3252956O27X5G5MPVM77PJ7",  // O
		"01M3252956U27X5G5MPVM77PJ7",  // U
		"pl_aaaaaaaaaaa",              // pl_ plus 11 characters
		"mm_aaaaaaaaaaa",              // mm_ plus 11 characters
	}
	for _, id := range invalid {
		err := ValidID(id)
		if err == nil {
			t.Errorf("ValidID(%q) = nil, want an error", id)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidID(%q) = %v, want ErrInvalid", id, err)
		}
	}
}

// TestEnvLineNamesMasterMind pins D5: the line init --hook appends to
// $CLAUDE_ENV_FILE writes the new env name.
func TestEnvLineNamesMasterMind(t *testing.T) {
	if got, want := EnvLine("mm_aaaaaaaaaaaa"), "export RELEVO_MASTERMIND=mm_aaaaaaaaaaaa\n"; got != want {
		t.Errorf("EnvLine = %q, want %q", got, want)
	}
}

// TestResolveFallsBackToLegacyEnvVar pins the env fallback: the export line
// already in a live session's $CLAUDE_ENV_FILE is state already written, so
// Resolve still reads RELEVO_PLANNER after RELEVO_MASTERMIND.
func TestResolveFallsBackToLegacyEnvVar(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101))

	env := envFunc(withEnv(withEnv(claudeEnv(0, ""), "RELEVO_MASTERMIND", ""), "RELEVO_PLANNER", "pl_aaaaaaaaaaaa"))
	rec, res, err := Resolve(reg, ResolveInput{Env: env, PPID: 999, ProcStart: procStartAt(0)})
	if err != nil {
		t.Fatalf("Resolve with RELEVO_PLANNER: %v", err)
	}
	if rec.ID != "pl_aaaaaaaaaaaa" || res != ResolutionEnv {
		t.Errorf("Resolve = %s (%s) %q, want pl_aaaaaaaaaaaa (env)", rec.ID, rec.Name, res)
	}
}

func TestDefaultNamePicksSmallestFree(t *testing.T) {
	taken := func(names ...string) func(string) bool {
		set := make(map[string]bool, len(names))
		for _, n := range names {
			set[n] = true
		}
		return func(candidate string) bool { return set[candidate] }
	}

	if got := DefaultName("architect", "claude", taken("architect-1")); got != "architect-2" {
		t.Errorf("DefaultName with architect-1 taken = %q, want architect-2", got)
	}
	if got := DefaultName("architect", "claude", nil); got != "architect-1" {
		t.Errorf("DefaultName with nothing taken = %q, want architect-1", got)
	}
	for _, agent := range []string{"", "My Agent", "1agent", "agent_1", strings.Repeat("a", 31)} {
		if got := DefaultName(agent, "claude", nil); got != "claude-1" {
			t.Errorf("DefaultName(%q, claude) = %q, want claude-1", agent, got)
		}
	}
	if got := DefaultName("", "opencode", taken("opencode-1", "opencode-2")); got != "opencode-3" {
		t.Errorf("DefaultName with two taken = %q, want opencode-3", got)
	}
}

// wantHookJSON marshals context through the envelope shape written out
// literally, so the test asserts hook.go's field names rather than trusting
// its own struct tags.
func wantHookJSON(t *testing.T, context string) string {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	env.HookSpecificOutput.HookEventName = "SessionStart"
	env.HookSpecificOutput.AdditionalContext = context
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal want envelope: %v", err)
	}
	return string(raw) + "\n"
}

func TestHookOutputExactJSON(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	sentence := "You are relevo MasterMind architect-1 (pl_aaaaaaaaaaaa). RELEVO_MASTERMIND is set in your shell; pass --mastermind architect-1 only to act as another MasterMind."
	want := wantHookJSON(t, sentence+"\n\n"+Guide())
	if got := string(HookOutput(rec)); got != want {
		t.Errorf("HookOutput:\n got %s\nwant %s", got, want)
	}

	wantNote := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"boom"}}` + "\n"
	if got := string(HookNote("boom")); got != wantNote {
		t.Errorf("HookNote:\n got %s\nwant %s", got, wantNote)
	}

	if got := EnvLine("pl_aaaaaaaaaaaa"); got != "export RELEVO_MASTERMIND=pl_aaaaaaaaaaaa\n" {
		t.Errorf("EnvLine = %q", got)
	}
}

// TestHookOutputNoEnvExactJSON is the unset-$CLAUDE_ENV_FILE golden.
func TestHookOutputNoEnvExactJSON(t *testing.T) {
	rec := Record{ID: "pl_aaaaaaaaaaaa", Name: "architect-1"}

	sentence := "You are relevo MasterMind architect-1 (pl_aaaaaaaaaaaa). RELEVO_MASTERMIND is set in your shell; pass --mastermind architect-1 only to act as another MasterMind."
	want := wantHookJSON(t, sentence+" "+noEnvNote+"\n\n"+Guide())
	if got := string(HookOutputNoEnv(rec)); got != want {
		t.Errorf("HookOutputNoEnv:\n got %s\nwant %s", got, want)
	}

	normal := hookContext(rec)
	noEnv := hookContext(rec) + " " + noEnvNote
	if !strings.HasPrefix(noEnv, normal+" ") || noEnv != normal+" "+noEnvNote {
		t.Errorf("HookOutputNoEnv context = %q, want the normal text plus %q", noEnv, noEnvNote)
	}
}

func TestParseHookInputRequiresSessionAndCWD(t *testing.T) {
	full := `{"hook_event_name":"SessionStart","source":"resume","session_id":"sess-1","transcript_path":"/tmp/t.jsonl","cwd":"/tmp/p","unknown":"ignored"}`
	in, err := ParseHookInput(strings.NewReader(full))
	if err != nil {
		t.Fatalf("ParseHookInput: %v", err)
	}
	if in.SessionID != "sess-1" || in.CWD != "/tmp/p" {
		t.Errorf("ParseHookInput = %+v", in)
	}
	if in.Source != SourceResume {
		t.Errorf("Source = %q, want %q", in.Source, SourceResume)
	}

	for _, payload := range []string{
		`not json`,
		`{"session_id":"sess-1"}`,
		`{"cwd":"/tmp/p"}`,
		`{"session_id":"","cwd":"/tmp/p"}`,
	} {
		if _, err := ParseHookInput(strings.NewReader(payload)); err == nil {
			t.Errorf("ParseHookInput(%s): want an error", payload)
		}
	}

	in, err = ParseHookInput(strings.NewReader(`{"session_id":"s","cwd":"/tmp/p","source":"something-new"}`))
	if err != nil {
		t.Fatalf("ParseHookInput: %v", err)
	}
	if in.Source != SourceStartup {
		t.Errorf("Source = %q, want %q", in.Source, SourceStartup)
	}
}

func TestRecordValidate(t *testing.T) {
	if err := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0).Validate(); err != nil {
		t.Fatalf("valid record: %v", err)
	}

	bad := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0)
	bad.HostStartedAt = 42
	if err := bad.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("host_started_at with host_pid 0: %v, want ErrInvalid", err)
	}

	oc := record("pl_aaaaaaaaaaaa", "oc-1", "opencode", "not-a-session", 0)
	if err := oc.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("opencode session: %v, want ErrInvalid", err)
	}
	oc.SessionID = "ses_Abc123"
	if err := oc.Validate(); err != nil {
		t.Errorf("valid opencode session: %v", err)
	}

	rel := record("pl_aaaaaaaaaaaa", "architect-1", "claude", "sess-1", 0)
	rel.CWD = "relative/path"
	if err := rel.Validate(); !errors.Is(err, ErrInvalid) {
		t.Errorf("relative cwd: %v, want ErrInvalid", err)
	}
}

func TestErrUnknownMasterMindNamesTheRef(t *testing.T) {
	var target ErrUnknownMasterMind
	err := error(ErrUnknownMasterMind{Ref: "beta"})
	if !errors.As(err, &target) {
		t.Fatalf("errors.As failed for %v", err)
	}
	if target.Ref != "beta" {
		t.Errorf("Ref = %q, want beta", target.Ref)
	}
	if !strings.Contains(err.Error(), "beta") {
		t.Errorf("Error() = %q, want it to name beta", err.Error())
	}
}

// TestListReadsEveryRecord pins one mastermind/<id> row per record, sorted by
// name.
func TestListReadsEveryRecord(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "zeta", "claude", "s-b", 0))
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "s-a", 0))

	records, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("List returned %d records, want 2", len(records))
	}
	if records[0].Name != "alpha" || records[1].Name != "zeta" {
		t.Errorf("List order = %q, %q; want alpha, zeta", records[0].Name, records[1].Name)
	}

	raw, ok, err := reg.KV.KVGet(registryKey("pl_aaaaaaaaaaaa"))
	if err != nil || !ok {
		t.Fatalf("KVGet(pl_aaaaaaaaaaaa) = (_, %v, %v), want the row", ok, err)
	}
	var stored Record
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("decode record row: %v", err)
	}
	if stored.Name != "alpha" || stored.SessionID != "s-a" {
		t.Errorf("record row = %+v", stored)
	}
}
