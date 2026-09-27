package relevo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestClosedRoundSealsOnceTheNextRoundCloses is the P3c seal's end-to-end
// contract (§4.3, §4.4): the tick that closes round 1 leaves its files alone,
// and they stay on disk while round 1 is the binding's latest closed round --
// the mastermind and a repair round still read them (D2/A1). Once the round after
// it closes, the following tick moves them into the store's database and
// deletes them -- and every reader that used to open them still returns the
// same content: Show, ReadDiff, Pull's PushText, ingest's StoreSource, a fork
// cut through the round, and gc's tarball.
func TestClosedRoundSealsOnceTheNextRoundCloses(t *testing.T) {
	t.Parallel()

	rt, _ := sentBinding(t)

	report := []byte("round 1's report\n")
	diff := []byte("diff --git a/x b/x\n--- a/x\n+++ b/x\n")
	logText := []byte("builder output\n")
	for path, body := range map[string][]byte{
		rt.Store.ReportPath("webshop", 1):     report,
		rt.Store.DiffPath("webshop", 1):       diff,
		rt.Store.BuilderLogPath("webshop", 1): logText,
	} {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	touch(t, rt.Store.DonePath("webshop", 1))

	// The ingest mirror is what turns the sealed file back into a round
	// artifact, so the test runs the daemon's ingest hook too.
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer d.Close()
	rt.DB = d

	// Tick 1 closes round 1. Its files must survive: the round it closes is
	// still the binding's round, so nothing is sealable yet.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.Round != 2 {
		t.Fatalf("round after the close = %d, want 2", b.Round)
	}
	if _, err := os.Stat(rt.Store.ReportPath("webshop", 1)); err != nil {
		t.Fatalf("round 1's files must survive the tick that closed it: %v", err)
	}

	// Tick 2 keeps round 1 on disk: it is now the binding's latest closed
	// round, and the mastermind and a repair round still read its files (D2/A1).
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	for _, base := range []string{"001-report.md", "001-prompt.md"} {
		if _, err := os.Stat(filepath.Join(rt.Store.Dir("webshop"), base)); err != nil {
			t.Errorf("%s was deleted while it is still the latest closed round (D2): %v", base, err)
		}
	}

	// Advance the binding as though round 2 had also closed: round 1 is two
	// behind now, so nothing keeps it on disk.
	b, err = rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b.Round = 3
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Tick 3 seals it: the files are gone from the directory.
	if err := NewDaemon(rt, time.Second).Tick(context.Background()); err != nil {
		t.Fatalf("third Tick: %v", err)
	}
	for _, base := range []string{"001-prompt.md", "001-report.md", "001-diff.patch", "001-builder.log", "001-done"} {
		if _, err := os.Stat(filepath.Join(rt.Store.Dir("webshop"), base)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still on disk after the seal", base)
		}
	}

	// -- every reader now answers from the database.

	got, err := rt.Store.ReadFile(rt.Store.ReportPath("webshop", 1))
	if err != nil || !bytes.Equal(got, report) {
		t.Errorf("ReadFile(report) = %q (err %v), want %q", got, err, report)
	}

	res, err := Show(context.Background(), rt, ShowOptions{Name: "webshop", Round: 1, Section: ShowReport})
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if res.Missing || res.Text != string(report) {
		t.Errorf("Show(report) = %q (missing %v), want %q", res.Text, res.Missing, report)
	}

	patch, ok, err := capture.ReadDiff(rt.Store, "webshop", 1)
	if err != nil || !ok || !bytes.Equal(patch, diff) {
		t.Errorf("ReadDiff = %q (ok %v, err %v), want %q", patch, ok, err, diff)
	}

	text, found, err := delivery.Pull(context.Background(), rt.Store, "webshop", "wait")
	if err != nil || !found {
		t.Fatalf("pullPending: found=%v err=%v", found, err)
	}
	if !strings.Contains(text, string(report)) {
		t.Errorf("pullPending text does not carry the sealed report: %q", text)
	}

	src := ingest.StoreSource(rt.Store, "webshop")
	rc, size, err := src.Open("001-report.md")
	if err != nil {
		t.Fatalf("StoreSource.Open: %v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil || !bytes.Equal(body, report) {
		t.Errorf("StoreSource.Open(report) = %q (err %v), want %q", body, err, report)
	}
	if size != int64(len(report)) {
		t.Errorf("StoreSource.Open(report) size = %d, want %d", size, len(report))
	}
	names, err := src.List()
	if err != nil || !slices.Contains(names, "001-report.md") {
		t.Errorf("StoreSource.List = %v (err %v), want the sealed 001-report.md", names, err)
	}

	// A fork cut through round 1 gets the sealed files as its own round_file
	// rows. The fork needs a working directory of its own: ForkState's Save
	// refuses a second active binding on webshop's CWD (/repo).
	if err := rt.Store.ForkState("webshop", store.Binding{Name: "forked", CWD: t.TempDir()}, 1); err != nil {
		t.Fatalf("ForkState: %v", err)
	}
	forked, err := rt.Store.ReadFile(rt.Store.ReportPath("forked", 1))
	if err != nil || !bytes.Equal(forked, report) {
		t.Errorf("forked report = %q (err %v), want %q", forked, err, report)
	}

	// The archive keeps the sealed files as rows of the archived record.
	if _, err := rt.Store.Archive("webshop"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	archived, err := rt.Store.ListArchived()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchived = %+v, %v, want exactly one record", archived, err)
	}
	sealed, found, aerr := rt.Store.ArchivedFile(archived[0].RecordID, "001-report.md")
	if aerr != nil || !found || !bytes.Equal(sealed, report) {
		t.Errorf("archived report = %q (%v, ok=%v), want %q", sealed, aerr, found, report)
	}
}

// TestSealPassEmptiesADoneDirOnly pins the last leftover the seal pass clears:
// once a DONE binding's rounds are all sealed and its directory holds nothing
// else, the empty directory goes too. An ACTIVE binding's directory stays, as
// more rounds are coming.
func TestSealPassEmptiesADoneDirOnly(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		state    store.State
		wantGone bool
	}{
		{"done", store.StateDone, true},
		{"active", store.StateActive, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			st := store.New(root)
			b := store.Binding{Name: "webshop", CWD: "/repo", Round: 3, State: tc.state}
			if err := st.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if err := os.WriteFile(st.ReportPath("webshop", 1), []byte("round 1\n"), 0o644); err != nil {
				t.Fatalf("write round file: %v", err)
			}

			if err := st.WithLock(func(tx *store.Tx) error {
				sealRounds(st, tx, b, policy.DefaultArtifactMaxMB*(1<<20))
				return nil
			}); err != nil {
				t.Fatalf("sealRounds: %v", err)
			}

			// The round file is sealed either way.
			if _, err := os.Stat(st.ReportPath("webshop", 1)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the round file is still on disk after the seal: %v", err)
			}

			_, err := os.Stat(st.Dir("webshop"))
			gone := errors.Is(err, os.ErrNotExist)
			if gone != tc.wantGone {
				t.Errorf("binding dir gone = %v, want %v (stat err %v)", gone, tc.wantGone, err)
			}
		})
	}
}
