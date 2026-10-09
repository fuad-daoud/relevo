package relevo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// detailFixture is one binding whose row carries all three detail figures, and
// the counters that say whether they were built: a working headless builder
// with a log to tail, an open round with a baseline to diff against and a
// usage reader to peek.
//
// The name is unique per test because liveStat caches by name + baseline tree
// for the life of the process: a shared name would hand the second test the
// first one's diff out of the cache instead of out of the fake git.
type detailFixture struct {
	rt   Runtime
	git  *fakeGit
	use  *fakeUsage
	name string
}

// newDetailFixture seeds a sent headless binding that can produce Live,
// LiveUsage and a tail all at once.
func newDetailFixture(t *testing.T, name string) detailFixture {
	t.Helper()
	fr := newFakeRunner()
	rt, b := sentHeadless(t, fr)
	fg := &fakeGit{worktreeStat: git.Stat{FilesChanged: 3, Insertions: 7, Deletions: 2}}
	fu := &fakeUsage{peekSamples: []usage.Sample{{Tokens: usage.Tokens{In: 11, Out: 5}}}}
	rt.Git, rt.Usage = fg, fu

	// The round's baseline is what liveStat diffs the working tree against, and
	// the builder log is what the tail reads.
	b.RoundBaselineTree = "baseline-" + name
	b.CWD = "/repo"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(b.Builder.LogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.Builder.LogPath, []byte("l1\nl2\nl3\nl4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return detailFixture{rt: rt, git: fg, use: fu, name: b.Name}
}

// row returns the binding's one row from a report built with opts.
func (f detailFixture) row(t *testing.T, opts ...StatusOption) view.BindingStatus {
	t.Helper()
	rep, err := Status(context.Background(), f.rt, opts...)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(rep.Bindings) != 1 {
		t.Fatalf("bindings = %d, want 1", len(rep.Bindings))
	}
	return rep.Bindings[0]
}

// withoutDetailFields is row with the three gated figures cleared, so it can be
// compared against a row built with them off: everything else must be equal.
func withoutDetailFields(row view.BindingStatus) view.BindingStatus {
	row.Live, row.LiveUsage = nil, nil
	if row.Headless != nil {
		h := *row.Headless
		h.Tail = nil
		row.Headless = &h
	}
	return row
}

// TestStatusWithoutDetailCarriesNoLiveFigures: the fleet read leaves all three
// detail figures nil and every other field exactly as the same store read
// returns them with the figures on.
func TestStatusWithoutDetailCarriesNoLiveFigures(t *testing.T) {
	t.Parallel()

	f := newDetailFixture(t, "detail-off")

	off := f.row(t, Detail(false))
	if off.Live != nil || off.LiveUsage != nil {
		t.Fatalf("Live = %+v, LiveUsage = %+v, want both nil", off.Live, off.LiveUsage)
	}
	if off.Headless == nil {
		t.Fatal("Headless info must still be there; only its tail is gated")
	}
	if off.Headless.Tail != nil {
		t.Errorf("Tail = %q, want nil", off.Headless.Tail)
	}

	on := f.row(t)
	if on.Live == nil || on.LiveUsage == nil || on.Headless == nil || len(on.Headless.Tail) == 0 {
		t.Fatalf("with detail on the row must carry all three: Live %+v LiveUsage %+v Headless %+v",
			on.Live, on.LiveUsage, on.Headless)
	}
	if want := []string{"l2", "l3", "l4"}; !reflect.DeepEqual(on.Headless.Tail, want) {
		t.Errorf("Tail = %q, want %q", on.Headless.Tail, want)
	}

	if got, want := withoutDetailFields(on), off; !reflect.DeepEqual(got, want) {
		t.Errorf("every other field differs:\n without the figures %+v\n detail off    %+v", got, want)
	}
}

// TestStatusWithDetailIsUnchangedByTheOption: two reads of one store, byte for
// byte the same document.
func TestStatusWithDetailIsUnchangedByTheOption(t *testing.T) {
	t.Parallel()

	f := newDetailFixture(t, "detail-same")

	on, err := Status(context.Background(), f.rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	again, err := Status(context.Background(), f.rt)
	if err != nil {
		t.Fatalf("Status again: %v", err)
	}
	want, err := json.Marshal(on)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(again)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("the option changed the report:\n%s\n%s", want, got)
	}
}

// TestStatusDetailFalseCallsNoGitAndNoPeek: the gate is on the call, not on the
// value -- no git fork and no usage read at all, so the row's cost is gone
// rather than its figure.
func TestStatusDetailFalseCallsNoGitAndNoPeek(t *testing.T) {
	t.Parallel()

	f := newDetailFixture(t, "detail-nocalls")
	f.row(t, Detail(false))

	if f.git.calls != 0 || f.git.worktreeStatCalls != 0 {
		t.Errorf("git calls = %d (worktree stat %d), want 0", f.git.calls, f.git.worktreeStatCalls)
	}
	if len(f.use.peeks) != 0 {
		t.Errorf("usage peeks = %d, want 0", len(f.use.peeks))
	}
	if len(f.use.sources) != 0 {
		t.Errorf("usage reads = %d, want 0", len(f.use.sources))
	}

	// The same runtime with detail on does both, so the counters are real.
	f.row(t)
	if f.git.worktreeStatCalls == 0 {
		t.Error("with detail on the row must still read the live diff")
	}
	if len(f.use.peeks) == 0 {
		t.Error("with detail on the row must still peek the live usage")
	}
}

// TestStatusRowCarriesTheDetailFigures: the single-row entry point the detail
// pane fetches through builds the full row, tail and all.
func TestStatusRowCarriesTheDetailFigures(t *testing.T) {
	t.Parallel()

	f := newDetailFixture(t, "detail-one")

	row, err := StatusRow(context.Background(), f.rt, f.name)
	if err != nil {
		t.Fatalf("StatusRow: %v", err)
	}
	if row.Name != f.name {
		t.Errorf("Name = %q, want %q", row.Name, f.name)
	}
	if row.Live == nil || row.LiveUsage == nil {
		t.Fatalf("Live %+v LiveUsage %+v, want both present", row.Live, row.LiveUsage)
	}
	if row.Headless == nil || len(row.Headless.Tail) == 0 {
		t.Fatalf("Headless %+v, want a tail", row.Headless)
	}

	if _, err := StatusRow(context.Background(), f.rt, "no-such-binding"); err == nil {
		t.Error("an unknown binding must be an error, not an empty row")
	}
}
