package relevo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// readerRemoteBinding is remoteBinding with a reader actor, so a closed round
// of it is collected through the artifact routes instead of report and diff.
func readerRemoteBinding(server string) store.Binding {
	b := remoteBinding(server)
	b.Role = "reviewer"
	b.Shape = store.ShapeReader
	return b
}

// readerClosedRemote is a remote reader whose round 1 the server reports
// closed: an artifact listing holding the output and a nested file, and a
// download per rel. The log and stream round files answer 404, as they do for
// a reader; callers override roundArtifactsResp to change the listing.
func readerClosedRemote() *fakeRemote {
	return &fakeRemote{
		getBindingResp: remote.BindingView{
			RoundState:    remote.RoundClosed,
			ClosedRound:   1,
			ResultCommit:  "c0ffee",
			Shape:         store.ShapeReader,
			ReportOutcome: "completed",
		},
		roundArtifactsResp: remote.ArtifactList{
			Actor:  "reviewer",
			Output: "findings.md",
			Files: []remote.ArtifactFile{
				{Rel: "findings.md", Size: 13},
				{Rel: "site/index.html", Size: 16},
			},
		},
		roundArtifactFunc: func(_ context.Context, _, _ string, _ int, rel string) (io.ReadCloser, error) {
			switch rel {
			case "findings.md":
				return io.NopCloser(strings.NewReader("the findings\n")), nil
			case "site/index.html":
				return io.NopCloser(strings.NewReader("<html>hi</html>\n")), nil
			}
			return nil, notFound(rel)
		},
		roundFileFunc: func(_ context.Context, _, _ string, _ int, kind string) (io.ReadCloser, error) {
			return nil, notFound(kind)
		},
	}
}

// TestRemoteReaderCatchUpInstallsArtifacts pins the reader catch-up's fetch
// and install: the output and the nested rel land under NNN-reviewer/,
// and no download temp survives.
func TestRemoteReaderCatchUpInstallsArtifacts(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := readerClosedRemote()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	dir := st.ArtifactDir("api", 1, "reviewer")
	for rel, want := range map[string]string{
		"findings.md":     "the findings\n",
		"site/index.html": "<html>hi</html>\n",
	} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", rel, data, want)
		}
	}
	for _, pattern := range []string{filepath.Join(dir, "*.fetch.*"), filepath.Join(dir, "site", "*.fetch.*")} {
		if left, _ := filepath.Glob(pattern); len(left) != 0 {
			t.Errorf("download temps left behind: %v", left)
		}
	}
	if n := countCalls(fr, "RoundArtifact:"); n != 2 {
		t.Errorf("RoundArtifact calls = %d, want 2", n)
	}
}

// TestRemoteReaderCatchUpRecordsTheOutputReport pins seam 3: the report entry
// points at the output path, carries the view's ReportOutcome because the
// server stripped the block, and no diff entry is written.
func TestRemoteReaderCatchUpRecordsTheOutputReport(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := readerClosedRemote()
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var report *store.LogEntry
	for i := range entries {
		if entries[i].Round == 1 && entries[i].Kind == store.KindReport {
			report = &entries[i]
		}
	}
	if report == nil {
		t.Fatalf("no round 1 report entry: %+v", entries)
	}
	if want := st.OutputPath("api", 1, "reviewer", "findings"); report.Path != want {
		t.Errorf("report Path = %q, want the output path %q", report.Path, want)
	}
	if report.Outcome != "completed" {
		t.Errorf("report Outcome = %q, want the view's ReportOutcome", report.Outcome)
	}
	if HasEntry(entries, 1, store.DirToMasterMind, store.KindDiff) {
		t.Errorf("a diff entry was written for a reader round: %+v", entries)
	}
	if n := countCalls(fr, "RoundFile:"); n != 2 {
		t.Errorf("RoundFile calls = %d, want log and stream only", n)
	}
	if n := countCalls(fr, "RoundArtifacts:"); n != 1 {
		t.Errorf("RoundArtifacts calls = %d, want 1", n)
	}
}

// TestRemoteReaderCatchUpRefusesBadRels pins that a rel the client refuses --
// empty, absolute, backslash or a ".." element -- stops the fetch before
// anything is written.
func TestRemoteReaderCatchUpRefusesBadRels(t *testing.T) {
	for _, rel := range []string{"../escape.txt", "/etc/passwd", `a\b.txt`, ""} {
		t.Run(rel, func(t *testing.T) {
			st := store.New(t.TempDir())
			if err := st.Save(readerRemoteBinding("zen")); err != nil {
				t.Fatal(err)
			}
			fr := readerClosedRemote()
			fr.roundArtifactsResp = remote.ArtifactList{
				Actor:  "reviewer",
				Output: "findings.md",
				Files:  []remote.ArtifactFile{{Rel: rel, Size: 1}},
			}
			rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

			if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
				t.Fatalf("Tick: %v", err)
			}

			if _, err := os.Stat(st.ArtifactDir("api", 1, "reviewer")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the artifact directory exists after refused rel %q (stat err = %v)", rel, err)
			}
			if n := countCalls(fr, "RoundArtifact:"); n != 0 {
				t.Errorf("RoundArtifact calls = %d, want 0", n)
			}
			entries, err := st.ReadLog("api")
			if err != nil {
				t.Fatal(err)
			}
			if HasEntry(entries, 1, store.DirToMasterMind, store.KindReport) {
				t.Errorf("a report entry exists for a refused listing: %+v", entries)
			}
			if n := countCalls(fr, "Ack:"); n != 0 {
				t.Errorf("Ack calls = %d, want 0", n)
			}
		})
	}
}

// TestRemoteReaderCatchUpMissingOutputHalts pins the reportless reader close:
// a listing without the output on a round the server did not stop halts with
// the writer's own message.
func TestRemoteReaderCatchUpMissingOutputHalts(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := readerClosedRemote()
	fr.roundArtifactsResp.Files = []remote.ArtifactFile{{Rel: "site/index.html", Size: 16}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "closed round 1 without its findings file") {
		t.Errorf("Halt = %q, want the reader's missing-output text", got.Halt)
	}
}

// TestRemoteReaderCatchUpStoppedRoundCloses pins the stopped reader round: an
// absent output is not a halt, and the stop is recorded on the report entry.
func TestRemoteReaderCatchUpStoppedRoundCloses(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := readerClosedRemote()
	fr.getBindingResp.Stopped = "killed"
	fr.roundArtifactsResp.Files = nil
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State == store.StateNeedsYou {
		t.Fatalf("a stopped reader round halted: %q", got.Halt)
	}
	if got.Round != 2 {
		t.Errorf("Round = %d, want 2", got.Round)
	}
	entries, err := st.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if !HasEntry(entries, 1, store.DirToMasterMind, store.KindReport) {
		t.Fatalf("no round 1 report entry: %+v", entries)
	}
	if !HasEntry(entries, 1, store.DirToMasterMind, store.KindStop) {
		t.Errorf("no stop entry for the stopped round: %+v", entries)
	}
}

// TestRemoteReaderCatchUpOverTheCapHalts pins the artifact cap through the wire: a listing
// over policy.artifact_max_mb writes nothing and asks for a human.
func TestRemoteReaderCatchUpOverTheCapHalts(t *testing.T) {
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := readerClosedRemote()
	fr.roundArtifactsResp = remote.ArtifactList{
		Actor:  "reviewer",
		Output: "findings.md",
		Files:  []remote.ArtifactFile{{Rel: "findings.md", Size: 3 * (1 << 20)}},
	}
	mb := 1
	rt := Runtime{Store: st, Remote: fr, Policy: policy.Policy{ArtifactMaxMB: &mb}, Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.StateNeedsYou {
		t.Fatalf("state = %s, want needs_you", got.State)
	}
	if !strings.Contains(got.Halt, "artifacts over the cap") {
		t.Errorf("Halt = %q, want the artifact cap reason", got.Halt)
	}
	if n := countCalls(fr, "RoundArtifact:"); n != 0 {
		t.Errorf("RoundArtifact calls = %d, want 0: nothing is downloaded over the cap", n)
	}
}

// TestRemoteReaderRoundSealsOnTheNextTick pins that the installed artifacts
// seal like any other round's files: the directory leaves disk and RoundFiles
// still lists the sealed names.
func TestRemoteReaderRoundSealsOnTheNextTick(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	if err := st.Save(readerRemoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{Store: st, Remote: readerClosedRemote(), Now: func() time.Time { return baseTime }}

	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	b, err := st.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	// Round 1 is the latest closed round, so nothing seals it yet; advancing
	// the binding past it is what the next round's close would do.
	b.Round = 3
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}
	if err := NewDaemon(rt, time.Second).Tick(ctx); err != nil {
		t.Fatalf("second Tick: %v", err)
	}

	if _, err := os.Stat(st.OutputPath("api", 1, "reviewer", "findings")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the output is still on disk after the seal (stat err = %v)", err)
	}
	names, err := st.RoundFiles("api")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"001-reviewer/findings.md", "001-reviewer/site/index.html"} {
		if !slices.Contains(names, want) {
			t.Errorf("RoundFiles = %v, want it to hold %s", names, want)
		}
	}
}
