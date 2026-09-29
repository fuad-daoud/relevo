package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// seedArtifactRound saves a reader binding with round 1 closed and its
// artifact directory full, for the Show and RoundArtifacts tests.
func seedArtifactRound(t *testing.T, name string) *store.Store {
	t.Helper()

	st := store.New(t.TempDir())
	b := store.Binding{
		Name: name, CWD: "/repo", Round: 2, State: store.StateActive,
		Shape: store.ShapeReader, Role: "reviewer",
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{TS: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 26, 10, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}

	dir := st.ArtifactDir(name, 1, "reviewer")
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("findings.md", "# the findings\n")
	write("site/index.html", "<html>\n")
	return st
}

// TestRoundArtifactsLiveAndSealed: a reader round with findings.md and
// site/index.html is listed with findings.md first, both while on disk and after
// SealRound, and ReadArtifact returns the bytes both ways. ReadArtifact with
// ".." is refused.
func TestRoundArtifactsLiveAndSealed(t *testing.T) {
	t.Parallel()

	st := seedArtifactRound(t, "reader")
	rt := Runtime{Store: st}
	const (
		name  = "reader"
		round = 1
		actor = "reviewer"
	)

	// The file a ".." read would reach, one level above the artifact dir. The
	// listing never holds it, and ReadArtifact must never return it.
	if err := os.WriteFile(filepath.Join(st.Dir(name), "escape.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("write escape: %v", err)
	}

	check := func(t *testing.T, live bool) {
		t.Helper()
		files, err := RoundArtifacts(rt, name, round, actor, "findings.md")
		if err != nil {
			t.Fatalf("RoundArtifacts: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("RoundArtifacts = %+v, want 2 files", files)
		}
		if files[0].Rel != "findings.md" || files[1].Rel != "site/index.html" {
			t.Errorf("Rel order = %q, %q, want findings.md first then site/index.html", files[0].Rel, files[1].Rel)
		}
		if files[0].Size != int64(len("# the findings\n")) {
			t.Errorf("output size = %d, want %d", files[0].Size, len("# the findings\n"))
		}
		if !live && files[0].MTime.IsZero() {
			t.Error("a sealed file has a zero MTime, want the row's stamp")
		}

		got, err := ReadArtifact(rt, name, round, actor, "site/index.html")
		if err != nil || string(got) != "<html>\n" {
			t.Errorf("ReadArtifact(site/index.html) = %q (err %v), want %q", got, err, "<html>\n")
		}
		got, err = ReadArtifact(rt, name, round, actor, "findings.md")
		if err != nil || string(got) != "# the findings\n" {
			t.Errorf("ReadArtifact(findings.md) = %q (err %v), want the findings", got, err)
		}

		// ".." is never a listed Rel, and never reads the escape file.
		if got, err := ReadArtifact(rt, name, round, actor, "../escape.txt"); !errors.Is(err, ErrNoArtifact) {
			t.Errorf("ReadArtifact(..) = %q (err %v), want ErrNoArtifact", got, err)
		} else if bytes.Contains(got, []byte("secret")) {
			t.Errorf("ReadArtifact(..) returned the escape file: %q", got)
		}
	}

	t.Run("live", func(t *testing.T) { check(t, true) })

	sealed := 0
	if err := st.WithLock(func(tx *store.Tx) error {
		var err error
		sealed, err = tx.SealRound(name, round)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	if sealed != 2 {
		t.Fatalf("SealRound sealed %d files, want 2", sealed)
	}
	if _, err := os.Stat(st.ArtifactDir(name, round, actor)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the artifact dir is still on disk after the seal: %v", err)
	}

	t.Run("sealed", func(t *testing.T) { check(t, false) })
}

// TestShowOutputAndArtifacts: the --output and --artifacts sections and
// --artifact <rel>, on a live and a sealed reader round, plus the JSON; a
// writer round answers Missing for --output and --artifacts. --report on a
// reader shows the summary (reportPathFor), not a duplicated path.
func TestShowOutputAndArtifacts(t *testing.T) {
	t.Parallel()

	st := seedArtifactRound(t, "reader")
	rt := Runtime{Store: st}
	ctx := context.Background()
	const (
		name  = "reader"
		round = 1
	)

	// A writer binding with a completed round and no artifact dir.
	writer := store.Binding{Name: "writer", CWD: "/repo", Round: 2, State: store.StateActive}
	if err := st.Save(writer); err != nil {
		t.Fatalf("Save(writer): %v", err)
	}
	for _, e := range []store.LogEntry{
		{TS: time.Date(2026, 9, 26, 11, 0, 0, 0, time.UTC), Round: 1, Direction: store.DirToBuilder, Kind: store.KindPrompt, Confirmed: true},
		{TS: time.Date(2026, 9, 26, 11, 0, 1, 0, time.UTC), Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := st.AppendLog("writer", e); err != nil {
			t.Fatalf("AppendLog(writer): %v", err)
		}
	}

	readerSections := func(t *testing.T) {
		t.Helper()

		res, err := Show(ctx, rt, ShowOptions{Name: name, Round: round, Section: ShowOutput})
		if err != nil {
			t.Fatalf("Show(--output): %v", err)
		}
		if res.Missing || res.Text != "# the findings\n" {
			t.Errorf("--output = %q (missing %v), want the findings", res.Text, res.Missing)
		}

		res, err = Show(ctx, rt, ShowOptions{Name: name, Round: round, Section: ShowArtifacts})
		if err != nil {
			t.Fatalf("Show(--artifacts): %v", err)
		}
		if res.Missing || len(res.Artifacts) != 2 {
			t.Fatalf("--artifacts = %+v (missing %v), want 2 files", res.Artifacts, res.Missing)
		}
		if res.Artifacts[0].Rel != "findings.md" || res.Artifacts[1].Rel != "site/index.html" {
			t.Errorf("--artifacts order = %q, %q, want findings.md first", res.Artifacts[0].Rel, res.Artifacts[1].Rel)
		}

		raw, err := json.Marshal(res)
		if err != nil {
			t.Fatalf("marshal ShowResult: %v", err)
		}
		for _, want := range []string{`"rel":"findings.md"`, `"size":`, `"mtime":"`} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("ShowResult JSON is missing %s:\n%s", want, raw)
			}
		}

		res, err = Show(ctx, rt, ShowOptions{Name: name, Round: round, Section: ShowArtifacts, ArtifactRel: "site/index.html"})
		if err != nil {
			t.Fatalf("Show(--artifact): %v", err)
		}
		if res.Text != "<html>\n" {
			t.Errorf("--artifact = %q, want the raw bytes %q", res.Text, "<html>\n")
		}

		// --report on a reader is its output file (reportPathFor), read
		// through the artifact helper rather than a second path.
		res, err = Show(ctx, rt, ShowOptions{Name: name, Round: round, Section: ShowReport})
		if err != nil {
			t.Fatalf("Show(--report): %v", err)
		}
		if res.Missing || res.Text != "# the findings\n" {
			t.Errorf("--report on a reader = %q (missing %v), want the findings", res.Text, res.Missing)
		}
	}

	t.Run("live", func(t *testing.T) { readerSections(t) })

	if err := st.WithLock(func(tx *store.Tx) error {
		_, err := tx.SealRound(name, round)
		return err
	}); err != nil {
		t.Fatalf("SealRound: %v", err)
	}
	// The listing is read through the live record's sealed rows once the
	// files are gone.
	t.Run("sealed", func(t *testing.T) { readerSections(t) })

	t.Run("writer answers Missing", func(t *testing.T) {
		for _, section := range []ShowSection{ShowOutput, ShowArtifacts} {
			res, err := Show(ctx, rt, ShowOptions{Name: "writer", Round: 1, Section: section})
			if err != nil {
				t.Fatalf("Show(writer --%s): %v", section, err)
			}
			if !res.Missing {
				t.Errorf("writer --%s missing = false, want true (no artifact dir)", section)
			}
		}
	})

	t.Run("unknown rel is ErrNoArtifact", func(t *testing.T) {
		_, err := Show(ctx, rt, ShowOptions{Name: name, Round: round, Section: ShowArtifacts, ArtifactRel: "nope"})
		if !errors.Is(err, ErrNoArtifact) {
			t.Errorf("Show(--artifact nope) err = %v, want ErrNoArtifact", err)
		}
	})
}

// TestArtifactsOverTheCapHoldTheSeal: a cap of 1 MB and a 2 MB artifact. The
// reader round still closes and is marked NEEDS YOU with the reason, the
// daemon tick does not seal it and the files stay on disk, and once the cap is
// raised above the size a later tick seals them.
func TestArtifactsOverTheCapHoldTheSeal(t *testing.T) {
	t.Parallel()

	repo := readerRepo(t)
	rt, b := bindReader(t, repo)

	dir := rt.Store.ArtifactDir("reader-bind", 1, "reviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := bytes.Repeat([]byte("x"), 2<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), big, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}

	oneMB := 1
	rt.Policy.ArtifactMaxMB = &oneMB

	writeReaderStream(t, rt, "reader-bind", 1, readerCloseFinal)
	touch(t, rt.Store.DonePath("reader-bind", 1))
	exitReaderRunner(t, rt, b)

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("State = %q, want %q", got.State, store.StateNeedsYou)
	}
	want := "artifacts over the cap: 2 > 1 MB; raise policy.artifact_max_mb to seal them"
	if got.Halt != want {
		t.Errorf("Halt = %q, want %q", got.Halt, want)
	}
	// The close still wrote the summary and queued the report: nothing is
	// dropped.
	if _, err := os.Stat(filepath.Join(dir, "findings.md")); err != nil {
		t.Errorf("findings.md was not written at the close: %v", err)
	}
	if e := reportEntryFor(t, rt, "reader-bind", 1); e.Path == "" {
		t.Error("no report entry was queued at the close")
	}

	// Make round 1 sealable: it is now two behind, and its stream is drained.
	got.Round = 3
	got.Builder.StreamRound = 0
	if err := rt.Store.Save(got); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick under the cap: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); err != nil {
		t.Errorf("the over-cap artifact was sealed away: %v", err)
	}

	tenMB := 10
	rt.Policy.ArtifactMaxMB = &tenMB
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick with the cap raised: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "big.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the artifact is still on disk after the cap was raised: %v", err)
	}
	sealed, err := rt.Store.ReadFile(filepath.Join(dir, "big.bin"))
	if err != nil || !bytes.Equal(sealed, big) {
		t.Errorf("sealed artifact = %d bytes (err %v), want the %d written", len(sealed), err, len(big))
	}
}

// TestOutputFallsBackToSummaryMd pins the pre-rename read fallback: a round
// whose artifact directory holds only summary.md answers --output with it.
func TestOutputFallsBackToSummaryMd(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := store.Binding{Name: "legacy", CWD: "/repo", Round: 2, State: store.StateActive, Shape: store.ShapeReader, Role: "reviewer"}
	if err := st.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToBuilder, Kind: store.Kind("plan"), Confirmed: true},
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Confirmed: true},
	} {
		if err := st.AppendLog("legacy", e); err != nil {
			t.Fatalf("AppendLog: %v", err)
		}
	}
	dir := st.ArtifactDir("legacy", 1, "reviewer")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.md"), []byte("# the old summary\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := Runtime{Store: st}
	res, err := Show(context.Background(), rt, ShowOptions{Name: "legacy", Round: 1, Section: ShowOutput})
	if err != nil {
		t.Fatalf("Show(--output): %v", err)
	}
	if res.Missing || res.Text != "# the old summary\n" {
		t.Errorf("--output = %q (missing %v), want the pre-rename summary.md", res.Text, res.Missing)
	}
}
