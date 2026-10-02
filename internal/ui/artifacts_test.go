package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// seedReaderArtifacts saves a reader binding for name whose round is closed
// (the binding sits on the round after it) and writes the round's artifact
// directory, every file stamped at at so a golden is deterministic. It
// returns the binding's round baseline head the context row names.
func seedReaderArtifacts(t *testing.T, st *store.Store, name, actor string, round int, files map[string]string, at time.Time) {
	t.Helper()

	b := newTestBinding(name)
	b.Shape = store.ShapeReader
	b.Role = actor
	b.Round = round + 1
	b.RoundBaselineHead = "043cf35abcdef123"
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	dir := st.ArtifactDir(name, round, actor)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatalf("chtimes %s: %v", rel, err)
		}
	}
}

// readerRoundRow is the status row a reader round fixture shows: a closed
// round 1 with the reviewer's report nine minutes old, so its card reads
// "artifact 9m" and its round reads sealed.
func readerRoundRow(name, actor string) view.BindingStatus {
	return view.BindingStatus{
		Name: name, Round: 1, PlanRound: 1, Display: "ACTIVE",
		Role:             actor,
		Shape:            store.ShapeReader,
		BuilderCandidate: "opencode/gpt-5.6-terra", BuilderName: "gpt-5.6-terra",
		BuilderKind: "opencode", BuilderStatus: "idle",
		MasterMindName: "architect-5",
		RoundEnd:       railNow.Add(-9 * time.Minute),
		LastPayload: &view.LastEvent{
			TS: railNow.Add(-9 * time.Minute), Round: 1,
			Direction: store.DirToMasterMind, Kind: store.KindReport,
		},
		LastUsage: &usage.Usage{
			Harness: "opencode", Provider: "cline-pass", Model: "gpt-5.6-terra",
			DurationMS: 9 * 60_000,
			Tokens:     usage.Tokens{In: 212_000, CacheRead: 1_900_000, Out: 18_000},
			Cost:       usage.Cost{USD: 0.42, Basis: usage.Measured},
			Samples:    1,
		},
	}
}

// TestReaderRoundTabs: a reader round shows plan, artifacts, log and
// transcript; a writer round keeps today's five tabs.
func TestReaderRoundTabs(t *testing.T) {
	st := store.New(t.TempDir())
	seedReaderArtifacts(t, st, "review-568", "reviewer", 1, map[string]string{"summary.md": "# hi\n"}, railNow)
	rv := newTestRound(t, relevo.Runtime{Store: st},
		view.Report{Bindings: []view.BindingStatus{readerRoundRow("review-568", "reviewer")}}, "review-568", 0)

	if !rv.pane.reader {
		t.Fatal("the seeded reader binding did not read as a reader round")
	}
	plain := stripANSI(rv.pane.tabsRow())
	for _, want := range []string{"prompt", "artifacts", "log", "transcript"} {
		if !strings.Contains(plain, want) {
			t.Errorf("reader tabs missing %q: %q", want, plain)
		}
	}
	for _, dont := range []string{"report", "diff"} {
		if strings.Contains(plain, dont) {
			t.Errorf("reader tabs carry the writer tab %q: %q", dont, plain)
		}
	}

	wst := store.New(t.TempDir())
	if err := wst.Save(newTestBinding("writer-1")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	wv := newTestRound(t, relevo.Runtime{Store: wst},
		view.Report{Bindings: []view.BindingStatus{{Name: "writer-1", Round: 1, Display: "ACTIVE"}}}, "writer-1", 0)
	if wv.pane.reader {
		t.Fatal("a writer binding read as a reader round")
	}
	wplain := stripANSI(wv.pane.tabsRow())
	for _, want := range []string{"prompt", "report", "transcript", "diff", "log"} {
		if !strings.Contains(wplain, want) {
			t.Errorf("writer tabs missing %q: %q", want, wplain)
		}
	}
	if strings.Contains(wplain, "artifacts") {
		t.Errorf("writer tabs carry the artifacts tab: %q", wplain)
	}
}

// loadArtifactsAt points a reader round's artifacts tab at rel by fetching
// the tab's content the way fetchFor does, and lands the reply in the pane.
func loadArtifactsAt(t *testing.T, rv roundView, rel string) roundView {
	t.Helper()

	src, name, round := rv.pane.src, rv.pane.detail.name, rv.pane.detail.round
	msg := fetchArtifacts(context.Background(), src, name, round, 0)().(tabMsg)
	sel := 0
	for i, f := range msg.content.artifacts {
		if f.Rel == rel {
			sel = i
		}
	}
	msg = fetchArtifacts(context.Background(), src, name, round, sel)().(tabMsg)
	rv.pane.tabInFlight = false
	rv.pane = rv.pane.onTab(msg)
	return rv
}

// TestArtifactsEnterOpensByKind: enter on a .md, .html, .svg and .txt row calls
// the fake open action with pager, browser, browser and editor, each on that
// row's file.
func TestArtifactsEnterOpensByKind(t *testing.T) {
	st := store.New(t.TempDir())
	seedReaderArtifacts(t, st, "review-568", "reviewer", 1, map[string]string{
		"summary.md": "# hi\n",
		"page.html":  "<h1>hi</h1>\n",
		"icon.svg":   "<svg></svg>\n",
		"notes.txt":  "hello\n",
	}, railNow)

	fa := &fakeActions{}
	env := testEnv(mastermindSource{relevo.Runtime{Store: st}},
		view.Report{Bindings: []view.BindingStatus{readerRoundRow("review-568", "reviewer")}}, 140, 40)
	env.Actions = fa

	v, _ := newRoundView(env, "review-568", 0)
	rv := v.(roundView)
	rv.pane, _ = rv.pane.switchTab(tabArtifacts)
	dir := st.ArtifactDir("review-568", 1, "reviewer")

	for _, tc := range []struct{ rel, kind string }{
		{"summary.md", "pager"},
		{"page.html", "browser"},
		{"icon.svg", "browser"},
		{"notes.txt", "editor"},
	} {
		rv = loadArtifactsAt(t, rv, tc.rel)
		next, cmd := rv.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
		rv = next.(roundView)
		if cmd == nil {
			t.Fatalf("%s: enter returned no command", tc.rel)
		}
		if len(fa.opened) == 0 {
			t.Fatalf("%s: enter did not call OpenArtifact", tc.rel)
		}
		got := fa.opened[len(fa.opened)-1]
		want := filepath.Join(dir, filepath.FromSlash(tc.rel))
		if got.kind != tc.kind || got.path != want {
			t.Errorf("%s: OpenArtifact = (%q, %q), want (%q, %q)", tc.rel, got.path, got.kind, want, tc.kind)
		}
	}
}

// TestArtifactsTabReadsSealedFiles: after SealRound the artifacts tab still
// lists the round's files and renders the selected one from its sealed rows.
func TestArtifactsTabReadsSealedFiles(t *testing.T) {
	st := store.New(t.TempDir())
	seedReaderArtifacts(t, st, "review-568", "reviewer", 1, map[string]string{
		"summary.md": "# the summary\n\nNothing wrong.\n",
		"notes.txt":  "hello\n",
	}, railNow)

	if err := st.WithLock(func(tx *store.Tx) error {
		_, err := tx.SealRound("review-568", 1)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if _, err := os.Stat(st.ArtifactDir("review-568", 1, "reviewer")); !os.IsNotExist(err) {
		t.Fatalf("the artifact dir is still on disk after the seal: %v", err)
	}

	msg := fetchArtifacts(context.Background(),
		mastermindSource{relevo.Runtime{Store: st}}, "review-568", 1, 0)().(tabMsg)
	if msg.content.err != nil {
		t.Fatalf("fetchArtifacts after the seal: %v", msg.content.err)
	}
	if len(msg.content.artifacts) != 2 {
		t.Fatalf("sealed artifacts = %d, want 2", len(msg.content.artifacts))
	}
	plain := stripANSI(artifactsBody(msg.content))
	for _, want := range []string{"summary.md", "notes.txt", "the reviewer's final message", "Nothing wrong."} {
		if !strings.Contains(plain, want) {
			t.Errorf("sealed artifacts tab missing %q:\n%s", want, plain)
		}
	}
}

// writerIdleRow is the writer status row writerIdleRoundModel shows: a closed
// round 1 whose report is nine minutes old, so its card reads "reported 9m".
func writerIdleRow(name string) view.BindingStatus {
	return view.BindingStatus{
		Name: name, Round: 1, PlanRound: 1, Display: "ACTIVE",
		BuilderStatus: "idle", BuilderName: "gpt-5.6-terra",
		MasterMindName: "architect-5",
		RoundEnd:       railNow.Add(-9 * time.Minute),
		LastPayload: &view.LastEvent{
			TS: railNow.Add(-9 * time.Minute), Round: 1,
			Direction: store.DirToMasterMind, Kind: store.KindReport,
		},
	}
}

// writerIdleRoundModel pushes a writer round whose state and time read exactly
// like the reader golden's: idle, nine minutes after a report. It is the
// writer half of TestReaderHeaderSharesTheWriterLayout.
func writerIdleRoundModel(t *testing.T, width, height int) Model {
	t.Helper()
	const name = "writer-idle"
	st := store.New(t.TempDir())
	if err := st.Save(newTestBinding(name)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	row := writerIdleRow(name)
	m := goldenActionModelWithStore(t, width, height, &fakeActions{},
		view.Report{Bindings: []view.BindingStatus{row}}, st)
	m = pointer(t, m, name)
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return drain(t, res.(Model), cmd)
}

// TestReaderHeaderSharesTheWriterLayout: a reader round draws line 1 through
// the writer path, so its state-and-time cell is the one a writer round with
// the same state draws (round 5b). A reader rendered through its own header
// function, or through the round-1 card, fails here.
func TestReaderHeaderSharesTheWriterLayout(t *testing.T) {
	reader := roundReaderArtifactsModel(t, 132, 34)
	writer := writerIdleRoundModel(t, 132, 34)

	readerLine := stripANSI(strings.Split(reader.View(), "\n")[2])
	writerLine := stripANSI(strings.Split(writer.View(), "\n")[2])

	readerCell := stripANSI(roundStateCell(readerRoundRow("review-568", "reviewer"), railNow))
	writerCell := stripANSI(roundStateCell(writerIdleRow("writer-idle"), railNow))
	if !strings.HasPrefix(readerLine, readerCell) {
		t.Errorf("reader line 3 does not start with its state-and-time cell\ncell: %q\ngot:  %q", readerCell, readerLine)
	}
	if !strings.HasPrefix(writerLine, writerCell) {
		t.Errorf("writer line 3 does not start with its state-and-time cell\ncell: %q\ngot:  %q", writerCell, writerLine)
	}
	// Only the row's own word differs (artifact for the reader, report for the
	// writer): the state-and-time cell's layout is still the shared one.
	if got := strings.Replace(readerCell, "artifact 9m", "reported 9m", 1); got != writerCell {
		t.Errorf("reader cell = %q, writer cell = %q, want the shared layout", got, writerCell)
	}
}

// TestArtifactCaptionMarksTheOutputAndSummaryMd pins the caption rule: the
// actor's output file and the pre-rename summary.md are the actor's final
// message; any other rel is only its name.
func TestArtifactCaptionMarksTheOutputAndSummaryMd(t *testing.T) {
	cases := []struct {
		rel, output, actor, want string
	}{
		{"findings.md", "findings.md", "reviewer", "findings.md · the reviewer's final message"},
		{"summary.md", "findings.md", "reviewer", "summary.md · the reviewer's final message"},
		{"site/index.html", "findings.md", "reviewer", "site/index.html"},
	}
	for _, tc := range cases {
		c := tabContent{artifactRel: tc.rel, artifactOutput: tc.output, artifactActor: tc.actor}
		if got := artifactCaption(c); got != tc.want {
			t.Errorf("artifactCaption(%s) = %q, want %q", tc.rel, got, tc.want)
		}
	}
}
