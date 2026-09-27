package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/legacy"
	"github.com/fuad-daoud/relevo/internal/store"
)

const fixtureDir = "testdata/binding-three-rounds"

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func copyFixture(t *testing.T) string {
	t.Helper()
	return copyFixtureAs(t, "bind.json")
}

func copyLegacyFixture(t *testing.T) string {
	t.Helper()
	return copyFixtureAs(t, "bind-legacy.json")
}

// copyFixtureAs copies the fixture, minus its bind.json stand-ins, into a fresh
// temp dir with bindFile as bind.json.
func copyFixtureAs(t *testing.T, bindFile string) string {
	t.Helper()
	dst := t.TempDir()
	if _, err := copyFixtureMembers(t, dst, fixtureDir); err != nil {
		t.Fatal(err)
	}
	bindData, err := os.ReadFile(filepath.Join(fixtureDir, bindFile))
	if err != nil {
		t.Fatalf("read %s: %v", bindFile, err)
	}
	if err := os.WriteFile(filepath.Join(dst, "bind.json"), bindData, 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	return dst
}

// copyFixtureMembers copies every fixture member except the bind.json variants
// into dst.
func copyFixtureMembers(t *testing.T, dst, src string) (int, error) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, fmt.Errorf("ReadDir fixture: %w", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || isBindVariant(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644); err != nil {
			return 0, fmt.Errorf("write %s: %w", e.Name(), err)
		}
		n++
	}
	return n, nil
}

// isBindVariant reports whether name is one of the fixture's bind.json stand-ins.
func isBindVariant(name string) bool {
	if name == "bind.json" {
		return true
	}
	return strings.HasPrefix(name, "bind-") && strings.HasSuffix(name, ".json")
}

func mustBinding(t *testing.T, d *db.DB, name string) db.BindingRow {
	t.Helper()
	b, found, err := d.Binding(name)
	if err != nil {
		t.Fatalf("Binding(%s): %v", name, err)
	}
	if !found {
		t.Fatalf("Binding(%s): not found", name)
	}
	return b
}

func mustRounds(t *testing.T, d *db.DB, bindingID string) []db.Round {
	t.Helper()
	rounds, err := d.Rounds(bindingID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}
	sort.Slice(rounds, func(i, j int) bool { return rounds[i].Number < rounds[j].Number })
	return rounds
}

func strEq(a *string, want string) bool { return a != nil && *a == want }

func normalizeRounds(rounds []db.Round) []db.Round {
	out := make([]db.Round, len(rounds))
	copy(out, rounds)
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func assertArtifact(t *testing.T, d *db.DB, roundID, kind string, wantFound bool) {
	t.Helper()
	_, found, err := d.Artifact(roundID, kind)
	if err != nil {
		t.Fatalf("Artifact(%s): %v", kind, err)
	}
	if found != wantFound {
		t.Errorf("Artifact(%s) found = %v, want %v", kind, found, wantFound)
	}
}

// archiveFixtureAs packs the fixture, with bindFile as bind.json, into an
// archived record and returns the store and the record's id.
func archiveFixtureAs(t *testing.T, bindFile string) (*store.Store, string) {
	t.Helper()
	root := t.TempDir()
	s := store.New(root)
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := copyFixtureMembers(t, s.Dir("fixture"), fixtureDir); err != nil {
		t.Fatal(err)
	}
	bindData, err := os.ReadFile(filepath.Join(fixtureDir, bindFile))
	if err != nil {
		t.Fatalf("read %s: %v", bindFile, err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir("fixture"), "bind.json"), bindData, 0o644); err != nil {
		t.Fatalf("write bind.json: %v", err)
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	return s, archived[0].RecordID
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mapGitFacts answers RepoFacts per directory, erroring for any dir it was not
// told about -- standing in for a gc'd worktree.
type mapGitFacts map[string]struct{ originURL, commonDir string }

func (m mapGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	f, ok := m[dir]
	if !ok {
		return "", "", fmt.Errorf("no such repo: %s", dir)
	}
	return f.originURL, f.commonDir, nil
}

type fakeGitFacts struct {
	originURL, commonDir string
}

func (f fakeGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	return f.originURL, f.commonDir, nil
}

// txProbingGitFacts opens a transaction on the same *db.DB Ingest is about to
// write with, from inside RepoFacts.
type txProbingGitFacts struct {
	d      *db.DB
	called bool
	txErr  error
}

func (f *txProbingGitFacts) RepoFacts(ctx context.Context, dir string) (string, string, error) {
	f.called = true
	f.txErr = f.d.Tx(func(*db.Tx) error { return nil })
	return "git@github.com:o/r.git", "/work/fixture/.git", nil
}

// dedupeAt is the created_at every seeded mirror binding and record shares.
var dedupeAt = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

func seedMirrorBinding(t *testing.T, d *db.DB, name string) string {
	t.Helper()
	id, err := d.UpsertBinding(db.Binding{
		Name:         name,
		CWD:          "/tmp/" + name,
		BuilderMode:  "headless",
		CreatedAt:    dedupeAt,
		IngestSource: db.IngestLive,
	})
	if err != nil {
		t.Fatalf("UpsertBinding(%s): %v", name, err)
	}
	return id
}

func seedMirrorRound(t *testing.T, d *db.DB, bindingID string, number int) string {
	t.Helper()
	id, err := d.UpsertRound(db.Round{BindingID: bindingID, Number: number, StartedAt: dedupeAt, Outcome: db.OutcomeOpen})
	if err != nil {
		t.Fatalf("UpsertRound(%d): %v", number, err)
	}
	return id
}

// recordJSON is the binding_record JSON for name whose builder harness kind is
// builderKind.
func recordJSON(t *testing.T, name, builderKind string) string {
	t.Helper()
	data, err := json.Marshal(store.Binding{
		Name:    name,
		CWD:     "/tmp/" + name,
		Builder: store.Endpoint{Kind: builderKind},
	})
	if err != nil {
		t.Fatalf("marshal record JSON: %v", err)
	}
	return string(data)
}

// seedRecordAt inserts a live record named name, created at at.
func seedRecordAt(t *testing.T, d *db.DB, name, builderKind string, at time.Time) string {
	t.Helper()
	id, err := d.RecordPut(db.Record{
		Owner:     "",
		Name:      name,
		State:     "active",
		Round:     1,
		CWD:       "/tmp/" + name,
		JSON:      recordJSON(t, name, builderKind),
		CreatedAt: at,
		UpdatedAt: at,
	})
	if err != nil {
		t.Fatalf("RecordPut(%s): %v", name, err)
	}
	return id
}

func putArtifact(t *testing.T, d *db.DB, roundID, kind, text string) db.Artifact {
	t.Helper()
	a := db.Artifact{
		ID:         db.NewID(),
		RoundID:    roundID,
		Kind:       kind,
		Text:       text,
		Bytes:      int64(len(text)),
		SHA256:     sha256Hex([]byte(text)),
		CapturedAt: time.Now(),
	}
	if err := d.UpsertArtifact(a); err != nil {
		t.Fatalf("UpsertArtifact(%s): %v", kind, err)
	}
	return a
}

func putRoundFile(t *testing.T, d *db.DB, recordID, name string, round int, body string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := d.Tx(func(tx *db.Tx) error {
		return tx.RoundFilePut(recordID, name, round, []byte(body), now, now)
	}); err != nil {
		t.Fatalf("RoundFilePut(%s): %v", name, err)
	}
}

func appendTranscript(t *testing.T, d *db.DB, ownerKind, ownerID string, recs []db.TranscriptRecord) {
	t.Helper()
	if _, err := d.AppendTranscript(ownerKind, ownerID, recs); err != nil {
		t.Fatalf("AppendTranscript(%s, %s): %v", ownerKind, ownerID, err)
	}
}

func mustPlan(t *testing.T, d *db.DB) dedupePlan {
	t.Helper()
	plan, err := DedupeMirror(d, nil)
	if err != nil {
		t.Fatalf("DedupeMirror: %v", err)
	}
	return plan
}

// rehearsalRenames builds the substitutions a rehearsal runs with, from the
// RELEVO_DEDUPE_REHEARSAL_* variables.
func rehearsalRenames(t *testing.T) []legacy.Prefix {
	t.Helper()
	pair := func(fromVar, toVar string) (legacy.Prefix, bool) {
		from, to := os.Getenv(fromVar), os.Getenv(toVar)
		if from == "" || to == "" {
			return legacy.Prefix{}, false
		}
		return legacy.Prefix{Old: from, New: to}, true
	}
	var renames []legacy.Prefix
	if p, ok := pair("RELEVO_DEDUPE_REHEARSAL_STATE_FROM", "RELEVO_DEDUPE_REHEARSAL_STATE_TO"); ok {
		renames = append(renames, p)
	}
	if p, ok := pair("RELEVO_DEDUPE_REHEARSAL_CONFIG_FROM", "RELEVO_DEDUPE_REHEARSAL_CONFIG_TO"); ok {
		renames = append(renames, p)
	}
	return renames
}

func copyRehearsalFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// seedPlannedScene seeds one mapped binding with a report artifact identical to
// its round file, an answer artifact, a two-row round transcript re-derivable from
// the round's stream, and a mastermind transcript under the same owner id.
func seedPlannedScene(t *testing.T, d *db.DB) string {
	t.Helper()
	const name = "webshop"
	bindingID := seedMirrorBinding(t, d, name)
	recordID := seedRecordAt(t, d, name, "claude", dedupeAt)
	round3 := seedMirrorRound(t, d, bindingID, 3)

	body := "003-report.md: the builder's report\n"
	putRoundFile(t, d, recordID, "003-report.md", 3, body)
	putArtifact(t, d, round3, db.ArtifactReport, body)
	putArtifact(t, d, round3, db.ArtifactAnswer, "the answer\n")

	line1 := `{"ts":"2026-09-01T10:00:00Z","type":"assistant","message":{"content":"hello"}}`
	line2 := `{"ts":"2026-09-01T10:00:01Z","type":"user","message":{"content":"go on"}}`
	putRoundFile(t, d, recordID, "003-builder.jsonl", 3, line1+"\n"+line2+"\n")
	recs, _ := streamTranscriptRecords("claude", [][]byte{[]byte(line1), []byte(line2)}, 0)
	appendTranscript(t, d, db.OwnerRound, round3, recs)
	appendTranscript(t, d, db.OwnerMasterMind, round3, []db.TranscriptRecord{
		{Seq: 0, RecordJSON: line1, Rendered: "the mastermind's own line"},
	})

	return round3
}

func dedupeBackupPath(backupDir string, now time.Time) string {
	return filepath.Join(backupDir, "relevo.db.pre-dedupe-"+now.UTC().Format("20060102-150405"))
}

func seedPlannedSceneCounts() DedupeStats {
	return DedupeStats{
		MirrorBindings:          1,
		Unmapped:                0,
		ArtifactsDeleted:        1,
		ArtifactsKept:           1,
		TranscriptRoundsDeleted: 1,
		TranscriptRowsDeleted:   2,
		TranscriptRoundsKept:    0,
	}
}
