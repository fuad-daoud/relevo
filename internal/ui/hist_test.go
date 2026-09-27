package ui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/view"
)

// histFixtureDir is the ingest package's own golden fixture -- three
// rounds (reported, halted, exited) -- reused read-only here exactly as
// internal/relevo's Show tests reuse it.
const histFixtureDir = "../ingest/testdata/binding-three-rounds"

var histFixtureBindFiles = map[string]bool{
	"bind-legacy.json":   true,
	"bind-round4.json":   true,
	"bind-cwd-gone.json": true,
}

// seedArchivedHistBinding archives the golden fixture into a record and
// ingests it as an archive source, so the binding it produces carries
// ArchivedAt and is never in a live report.
func seedArchivedHistBinding(t *testing.T) (relevo.Runtime, relevo.HistoryBinding) {
	t.Helper()
	root := t.TempDir()
	s := store.New(root)
	if err := os.MkdirAll(s.Dir("fixture"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	entries, err := os.ReadDir(histFixtureDir)
	if err != nil {
		t.Fatalf("ReadDir fixture: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || histFixtureBindFiles[e.Name()] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(histFixtureDir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(s.Dir("fixture"), e.Name()), data, 0o644); err != nil {
			t.Fatalf("write %s: %v", e.Name(), err)
		}
	}
	if _, err := s.Archive("fixture"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := s.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if _, err := ingest.Ingest(context.Background(), ingest.ArchivedSource(s, archived[0].RecordID), d, ingest.Deps{}); err != nil {
		t.Fatalf("archive Ingest: %v", err)
	}
	seedRoundMirror(t, d, histFixtureDir)

	rt := relevo.Runtime{Store: store.New(t.TempDir()), DB: d}

	rows, err := relevo.Bindings(context.Background(), rt, "")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len(Bindings) = %d, want 1", len(rows))
	}
	return rt, rows[0]
}

// TestPointAtArchivedRowLoadsPlanFromDB pins the hist branch (#183, §5.8):
// pointing at a hist row sets live false, round the newest (every round
// closed), and the plan tab's fetch reads the database, not a file.
func TestPointAtArchivedRowLoadsPlanFromDB(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	v, cmd := newHistRoundView(testEnv(mastermindSource{rt}, view.Report{}, 140, 40), h, 0)
	rv := v.(roundView)

	if rv.pane.detail.live {
		t.Error("live = true, want false for a hist row")
	}
	if rv.pane.detail.name != "fixture" {
		t.Errorf("name = %q, want fixture", rv.pane.detail.name)
	}
	if rv.pane.detail.rounds != 3 || rv.pane.detail.round != 3 {
		t.Errorf("round=%d rounds=%d, want round=3 rounds=3", rv.pane.detail.round, rv.pane.detail.rounds)
	}
	if rv.pane.detail.archivedAt.IsZero() {
		t.Error("archivedAt must be set for an archived hist row")
	}
	if cmd == nil {
		t.Fatal("expected a fetch command for the active (plan) tab")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", cmd())
	}
	if tMsg.t != tabPrompt {
		t.Errorf("t = %v, want tabPrompt (the default active tab)", tMsg.t)
	}
	if tMsg.content.err != nil {
		t.Fatalf("unexpected error: %v", tMsg.content.err)
	}
	if tMsg.content.body == "" {
		t.Error("expected the round 3 plan's body, got empty")
	}
}

// TestArchivedTerminalTabShowsTranscriptRows pins fetchShow's terminal
// routing and the "never tail-following" rule for a hist row's terminal.
func TestArchivedTerminalTabShowsTranscriptRows(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)

	rv := newTestHistRound(t, rt, h, 0)
	if rv.pane.detail.follow {
		t.Error("follow = true, want false for a hist row (never tail-following)")
	}

	rv = roundKey(rv, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	rv.pane.tabInFlight = false
	if rv.pane.detail.round != 2 {
		t.Fatalf("round = %d, want 2", rv.pane.detail.round)
	}

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}}, testEnv(mastermindSource{rt}, view.Report{}, 140, 40))
	rv = next.(roundView)
	if cmd == nil {
		t.Fatal("expected a fetch command for the terminal tab")
	}
	rv = roundMsg(rv, cmd().(tabMsg))

	if rv.pane.detail.follow {
		t.Error("follow must stay false after loading the terminal tab")
	}
	if !rv.pane.detail.cache[tabTerminal].transcript {
		t.Error("a hist row's terminal tab must render as transcript content")
	}
	if rv.pane.detail.cache[tabTerminal].body == "" {
		t.Error("expected rendered transcript rows, got empty body")
	}
}

// TestArchivedMissingDiffIsEmptyProse pins §6: a missing artifact renders
// as tabContent.empty prose, not as an error.
func TestArchivedMissingDiffIsEmptyProse(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}}, testEnv(mastermindSource{rt}, view.Report{}, 140, 40))
	_ = next
	if cmd == nil {
		t.Fatal("expected a fetch command for the diff tab")
	}
	msg := cmd().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("unexpected error: %v", msg.content.err)
	}
	if want := "no diff for round 3"; msg.content.empty != want {
		t.Errorf("empty = %q, want %q", msg.content.empty, want)
	}
}

// TestDetailHeaderArchived pins detailHeader's exact rendering for an
// archived binding.
func TestDetailHeaderArchived(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)

	got := rv.pane.detailHeader()
	if !strings.HasPrefix(got, "fixture · round 3 of 3 · archived 2026-") {
		t.Errorf("detailHeader() = %q, want a %q prefix", got, "fixture · round 3 of 3 · archived 2026-")
	}
}

// TestArchivedStepRoundRefetches pins round stepping for a hist row (#183).
func TestArchivedStepRoundRefetches(t *testing.T) {
	rt, h := seedArchivedHistBinding(t)
	rv := newTestHistRound(t, rt, h, 0)

	for tb := tab(0); tb < tabCount; tb++ {
		rv.pane.detail.cache[tb] = tabContent{loaded: true, body: "stale"}
	}
	rv.pane.tabInFlight = false

	next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}}, testEnv(mastermindSource{rt}, view.Report{}, 140, 40))
	rv = next.(roundView)
	if rv.pane.detail.round != 2 {
		t.Fatalf("round = %d, want 2", rv.pane.detail.round)
	}
	for tb := tab(0); tb < tabCount; tb++ {
		if rv.pane.detail.cache[tb].loaded {
			t.Errorf("tab %v cache still loaded after stepping", tb)
		}
	}
	if cmd == nil {
		t.Fatal("expected a refetch command for the active tab")
	}
	tMsg, ok := cmd().(tabMsg)
	if !ok {
		t.Fatalf("expected tabMsg, got %T", cmd())
	}
	if tMsg.round != 2 {
		t.Errorf("fetched round = %d, want 2", tMsg.round)
	}
}

// mirrorLines returns body's complete lines the way the retired round
// transcript read yielded them (internal/ingest readAppendOnly): everything
// up to the final newline, split on '\n'. A file with no trailing newline
// has no complete line and yields none.
func mirrorLines(body []byte) [][]byte {
	end := bytes.LastIndexByte(body, '\n')
	if end < 0 {
		return nil
	}
	return bytes.Split(body[:end], []byte{'\n'})
}

// mirrorTranscriptRecords builds round n's builder transcript rows exactly as
// ingest built them before D3b: the builder stream (NNN-builder.jsonl) when
// the fixture has one, else the builder log (NNN-builder.log) (D3b plan §3
// fence item 3).
func mirrorTranscriptRecords(t *testing.T, dir string, n int, builderKind string) []db.TranscriptRecord {
	t.Helper()

	streamName := fmt.Sprintf("%03d-builder.jsonl", n)
	stream, err := os.ReadFile(filepath.Join(dir, streamName))
	if err == nil {
		lines := mirrorLines(stream)
		recs := make([]db.TranscriptRecord, 0, len(lines))
		for i, line := range lines {
			rec := db.TranscriptRecord{Seq: i, RecordJSON: string(line)}
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 && trimmed[0] == '{' {
				rec.Rendered = strings.Join(transcript.Render(builderKind, line), "\n")
			}
			recs = append(recs, rec)
		}
		return recs
	}
	if !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", streamName, err)
	}

	logName := fmt.Sprintf("%03d-builder.log", n)
	logBody, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", logName, err)
	}
	lines := mirrorLines(logBody)
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{Seq: i, Rendered: string(line)})
	}
	return recs
}

// seedRoundMirror inserts, directly, the round mirror rows ingest wrote
// before D3b and no longer writes: the plain round-file artifacts (plan,
// report, diff, drift) and each round's builder transcript, read from the
// fixture's own files exactly as ingest built them (D3b plan §3 fence items
// 1 and 3). relevo.Show's database fallback (showDB) still reads those rows
// for a binding with no live files and no archived record in the runtime's
// store, so seedArchivedHistBinding seeds them by hand after ingest.Ingest.
func seedRoundMirror(t *testing.T, d *db.DB, dir string) {
	t.Helper()

	var bind struct {
		Name    string `json:"name"`
		Builder struct {
			Kind string `json:"kind"`
		} `json:"builder"`
	}
	bindJSON, err := os.ReadFile(filepath.Join(dir, "bind.json"))
	if err != nil {
		t.Fatalf("read bind.json: %v", err)
	}
	if err := json.Unmarshal(bindJSON, &bind); err != nil {
		t.Fatalf("parse bind.json: %v", err)
	}

	row, found, err := d.Binding(bind.Name)
	if err != nil || !found {
		t.Fatalf("Binding(%s): found=%v err=%v", bind.Name, found, err)
	}
	rounds, err := d.Rounds(row.ID)
	if err != nil {
		t.Fatalf("Rounds: %v", err)
	}

	type artifactSeed struct {
		roundID, kind, text, sha string
		size                     int64
	}
	type transcriptSeed struct {
		roundID string
		recs    []db.TranscriptRecord
	}

	roundFiles := []struct{ base, kind string }{
		{"%03d-plan.md", db.ArtifactPrompt},
		{"%03d-report.md", db.ArtifactReport},
		{"%03d-diff.patch", db.ArtifactDiff},
		{"%03d-drift.patch", db.ArtifactDrift},
	}

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	var artifacts []artifactSeed
	var transcripts []transcriptSeed
	for _, r := range rounds {
		for _, a := range roundFiles {
			body, rerr := os.ReadFile(filepath.Join(dir, fmt.Sprintf(a.base, r.Number)))
			if rerr != nil {
				if os.IsNotExist(rerr) {
					continue
				}
				t.Fatalf("read %s: %v", a.base, rerr)
			}
			sum := sha256.Sum256(body)
			artifacts = append(artifacts, artifactSeed{
				roundID: r.ID,
				kind:    a.kind,
				text:    string(body),
				size:    int64(len(body)),
				sha:     fmt.Sprintf("%x", sum),
			})
		}
		transcripts = append(transcripts, transcriptSeed{
			roundID: r.ID,
			recs:    mirrorTranscriptRecords(t, dir, r.Number, bind.Builder.Kind),
		})
	}

	if err := d.Tx(func(tx *db.Tx) error {
		for _, a := range artifacts {
			if err := tx.UpsertArtifact(db.Artifact{
				RoundID:    a.roundID,
				Kind:       a.kind,
				Text:       a.text,
				Bytes:      a.size,
				SHA256:     a.sha,
				CapturedAt: now,
			}); err != nil {
				return err
			}
		}
		for _, ts := range transcripts {
			if len(ts.recs) == 0 {
				continue
			}
			if _, err := tx.AppendTranscript(db.OwnerRound, ts.roundID, ts.recs); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed round mirror: %v", err)
	}
}
