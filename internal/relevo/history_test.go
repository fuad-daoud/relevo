package relevo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

func ptr[T any](v T) *T { return &v }

func openTestHistoryDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestHistoryOptionsFilterHere(t *testing.T) {
	t.Parallel()

	g := &fakeGit{repoFactsOrigin: "git@github.com:o/r.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, _, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "https://github.com/o/r" {
		t.Errorf("Repo = %q, want the normalised origin", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
	if !f.Newest {
		t.Error("Newest = false, want true")
	}
}

func TestHistoryOptionsFilterHereNoRemote(t *testing.T) {
	t.Parallel()

	g := &fakeGit{repoFactsCommonDir: "/work/repo/.git"}
	rt := Runtime{Git: g}
	opts := HistoryOptions{Here: "/work/repo"}

	f, _, err := opts.Filter(context.Background(), rt, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Repo != "/work/repo/.git" {
		t.Errorf("Repo = %q, want the common dir", f.Repo)
	}
	if f.Here != "" {
		t.Errorf("Here = %q, want cleared", f.Here)
	}
}

func TestBindingsNoDatabase(t *testing.T) {
	t.Parallel()

	rt := Runtime{}
	_, err := Bindings(context.Background(), rt, "")
	if !errors.Is(err, ErrNoDatabase) {
		t.Errorf("err = %v, want ErrNoDatabase", err)
	}
}

func TestBindingsHereResolvesRepo(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)

	repoA, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/a"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo a: %v", err)
	}
	repoB, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/b"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo b: %v", err)
	}

	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha-old", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: older, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha-old: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha-new", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: newer, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha-new: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "beta", RepoID: &repoB, CWD: "/work/b", BuilderMode: "pane",
		CreatedAt: newer, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding beta: %v", err)
	}

	g := &fakeGit{repoFactsOrigin: "git@github.com:o/a.git"}
	rt := Runtime{DB: d, Git: g}

	got, err := Bindings(context.Background(), rt, "/work/repo")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2 (only repo a's rows)", len(got))
	}
	if got[0].Name != "alpha-new" || got[1].Name != "alpha-old" {
		t.Errorf("order = [%s, %s], want [alpha-new, alpha-old] (newest first)", got[0].Name, got[1].Name)
	}
}

func TestBindingsHereNotARepoMeansAll(t *testing.T) {
	t.Parallel()

	d := openTestHistoryDB(t)

	repoA, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/a"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo a: %v", err)
	}
	repoB, err := d.UpsertRepo(db.Repo{OriginURL: ptr("https://github.com/o/b"), FirstSeen: time.Now()})
	if err != nil {
		t.Fatalf("UpsertRepo b: %v", err)
	}
	now := time.Now()
	if _, err := d.UpsertBinding(db.Binding{
		Name: "alpha", RepoID: &repoA, CWD: "/work/a", BuilderMode: "pane",
		CreatedAt: now, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding alpha: %v", err)
	}
	if _, err := d.UpsertBinding(db.Binding{
		Name: "beta", RepoID: &repoB, CWD: "/work/b", BuilderMode: "pane",
		CreatedAt: now, IngestSource: db.IngestLive,
	}); err != nil {
		t.Fatalf("UpsertBinding beta: %v", err)
	}

	g := &fakeGit{repoFactsErr: errors.New("not a git repository")}
	rt := Runtime{DB: d, Git: g}

	got, err := Bindings(context.Background(), rt, "/tmp/not-a-repo")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Bindings) = %d, want 2 (a non-repo cwd means no filter)", len(got))
	}
}

func TestHistoryBindingArchivedFacts(t *testing.T) {
	t.Parallel()

	d := seedShowArchiveDB(t)
	rt := Runtime{DB: d}

	got, err := Bindings(context.Background(), rt, "")
	if err != nil {
		t.Fatalf("Bindings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Bindings) = %d, want 1", len(got))
	}
	hb := got[0]
	if hb.Name != "fixture" {
		t.Errorf("Name = %q, want fixture", hb.Name)
	}
	if !hb.Archived {
		t.Error("Archived = false, want true")
	}
	if hb.ArchivedAt.IsZero() {
		t.Error("ArchivedAt is zero, want the tarball's stamp")
	}
}

func TestHistoryOptionsSinceUntil(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Since: "24h", Until: "2026-09-01"}

	f, _, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	wantSince := now.Add(-24 * time.Hour)
	if !f.Since.Equal(wantSince) {
		t.Errorf("Since = %v, want %v", f.Since, wantSince)
	}
	wantUntil := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if !f.Until.Equal(wantUntil) {
		t.Errorf("Until = %v, want %v", f.Until, wantUntil)
	}
}

// TestHistoryOptionsQueryMergesWithFlagNote pins the merge rule: -q is
// parsed first, the explicit flag wins, and one note naming the override
// comes back for the CLI to print to stderr.
func TestHistoryOptionsQueryMergesWithFlagNote(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Query: "harness:agy", Harness: "codex"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Harness != "codex" {
		t.Errorf("Harness = %q, want codex (the flag wins)", f.Harness)
	}
	if !f.Newest {
		t.Error("Newest = false, want true")
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --harness overrides harness:agy from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
}

// TestHistoryOptionsByOverridesQuery pins that --by overrides a by: in -q
// the same way, both in the note and in the Query the caller reads back.
func TestHistoryOptionsByOverridesQuery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Query: "by:day harness:agy", By: "candidate"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Harness != "agy" {
		t.Errorf("Harness = %q, want agy (no flag overrode it)", f.Harness)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisCandidate {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisCandidate)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --by overrides by:day from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
}

// TestHistoryOptionsQueryError pins that a bad -q comes back as histq's own
// ErrQuery, which the CLI maps to exit 2.
func TestHistoryOptionsQueryError(t *testing.T) {
	t.Parallel()

	opts := HistoryOptions{Query: "outcome:nope"}
	_, _, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	var eq histq.ErrQuery
	if !errors.As(err, &eq) {
		t.Fatalf("Filter error = %v, want histq.ErrQuery", err)
	}
	if eq.Token != "outcome:nope" {
		t.Errorf("ErrQuery.Token = %q, want outcome:nope", eq.Token)
	}
}

// TestHistoryOptionsFilterDefaultsAxisNone pins that a Filter with no -q
// query still names an axis: the parsed query starts at AxisNone rather than
// the zero value, so a caller reading ParsedQuery().By never sees "" (which
// cmdHistory read as a regroup axis and turned into "no rounds").
func TestHistoryOptionsFilterDefaultsAxisNone(t *testing.T) {
	t.Parallel()

	opts := HistoryOptions{}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisNone {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisNone)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none", notes)
	}
}

// TestHistoryOptionsByAloneGivesNoNote pins the second symptom: --by with no
// -q query has nothing to conflict with, so it must not print the spurious
// `note: --by overrides by: from -q`.
func TestHistoryOptionsByAloneGivesNoNote(t *testing.T) {
	t.Parallel()

	opts := HistoryOptions{By: "candidate"}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none: --by has no -q by: to override", notes)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisCandidate {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisCandidate)
	}
}

// TestHistoryOptionsByOverridesQueryByNote pins that a real conflict is
// kept: -q named by:binding and --by candidate still notes the override.
func TestHistoryOptionsByOverridesQueryByNote(t *testing.T) {
	t.Parallel()

	opts := HistoryOptions{Query: "by:binding", By: "candidate"}

	_, notes, err := opts.Filter(context.Background(), Runtime{}, time.Now())
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --by overrides by:binding from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
	if got := opts.ParsedQuery().By; got != histq.AxisCandidate {
		t.Errorf("ParsedQuery().By = %q, want %q", got, histq.AxisCandidate)
	}
}

// TestHistoryFilterResolvesName pins A1 §4.2: a --candidate value with no "/"
// resolves to its canonical token, and an unresolved value is left as typed.
func TestHistoryFilterResolvesName(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)

	o := HistoryOptions{Candidate: "claude-m", Names: set}
	f, _, err := o.Filter(context.Background(), Runtime{}, baseTime)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Candidate != testClaudeRef {
		t.Errorf("Candidate = %q, want %q", f.Candidate, testClaudeRef)
	}

	unresolved := HistoryOptions{Candidate: "nope", Names: set}
	f, _, err = unresolved.Filter(context.Background(), Runtime{}, baseTime)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Candidate != "nope" {
		t.Errorf("Candidate = %q, want it left as typed", f.Candidate)
	}
}

var tabNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func TestParseSince(t *testing.T) {
	t.Parallel()

	if got, err := ParseSince("", tabNow); err != nil || !got.IsZero() {
		t.Errorf("empty: %v, %v", got, err)
	}
	if got, _ := ParseSince("24h", tabNow); !got.Equal(tabNow.Add(-24 * time.Hour)) {
		t.Errorf("24h = %v", got)
	}
	if got, _ := ParseSince("7d", tabNow); !got.Equal(tabNow.Add(-7 * 24 * time.Hour)) {
		t.Errorf("7d = %v", got)
	}
	if got, _ := ParseSince("2026-09-01", tabNow); !got.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", got)
	}
	for _, bad := range []string{"7", "7w", "yesterday", "2026-9-1"} {
		if _, err := ParseSince(bad, tabNow); !errors.Is(err, ErrBadSince) {
			t.Errorf("%q: err = %v, want ErrBadSince", bad, err)
		}
	}
}

// TestParseSinceMovedKeepsRelevoWrapper pins that relevo.ParseSince still
// exists after its body moved to internal/histq, that the two agree, and
// that ErrBadSince is the same sentinel histq returns, so a caller's
// errors.Is(err, relevo.ErrBadSince) keeps working.
func TestParseSinceMovedKeepsRelevoWrapper(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"", "24h", "7d", "2026-09-01"} {
		got, err := ParseSince(s, tabNow)
		if err != nil {
			t.Fatalf("relevo.ParseSince(%q): %v", s, err)
		}
		want, err := histq.ParseSince(s, tabNow)
		if err != nil {
			t.Fatalf("histq.ParseSince(%q): %v", s, err)
		}
		if !got.Equal(want) {
			t.Errorf("relevo.ParseSince(%q) = %v, want %v (histq.ParseSince)", s, got, want)
		}
	}

	_, err := ParseSince("yesterday", tabNow)
	if !errors.Is(err, ErrBadSince) {
		t.Errorf("relevo.ParseSince error = %v, want ErrBadSince", err)
	}
	if !errors.Is(err, histq.ErrBadSince) {
		t.Errorf("relevo.ParseSince error = %v, want histq.ErrBadSince", err)
	}
	if ErrBadSince != histq.ErrBadSince {
		t.Error("relevo.ErrBadSince is not histq.ErrBadSince; errors.Is across the move would break")
	}
}

// TestHistoryOptionsTicketFlagOverridesQuery pins #637: --ticket merges with
// the -q query like every other flag, with the override note when -q set a
// different ticket.
func TestHistoryOptionsTicketFlagOverridesQuery(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Query: "ticket:#1", Ticket: "#607"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Ticket != "#607" {
		t.Errorf("Ticket = %q, want #607 (the flag wins)", f.Ticket)
	}
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want exactly one", notes)
	}
	if want := "note: --ticket overrides ticket:#1 from -q"; notes[0] != want {
		t.Errorf("note = %q, want %q", notes[0], want)
	}
}

// TestHistoryOptionsTicketAloneGivesNoNote is the other half: --ticket with no
// -q ticket is set silently, as every other flag is.
func TestHistoryOptionsTicketAloneGivesNoNote(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	opts := HistoryOptions{Ticket: "o/r#607"}

	f, notes, err := opts.Filter(context.Background(), Runtime{}, now)
	if err != nil {
		t.Fatalf("Filter: %v", err)
	}
	if f.Ticket != "o/r#607" {
		t.Errorf("Ticket = %q, want o/r#607", f.Ticket)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none", notes)
	}
}
