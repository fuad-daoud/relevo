package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// testStoreChain is a valid chain row: one plan in progress and one distinct
// member binding name per part.
func testStoreChain(name string) db.ChainRow {
	now := time.Now().UTC().Truncate(time.Millisecond)
	return db.ChainRow{
		ID:            db.NewID(),
		Name:          name,
		Status:        "running",
		Phase:         "build",
		Step:          "building",
		Plan:          1,
		Plans:         1,
		PlanPathsJSON: []byte(`["/plans/001.md"]`),
		SettingsJSON:  []byte(`{"max_corrections":1}`),
		Builder:       name,
		Reviewer:      name + "-rev",
		Planner:       name + "-plan",
		Security:      name + "-sec",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func chainEventFor(c db.ChainRow) db.ChainEventRow {
	return db.ChainEventRow{
		Phase:  "build",
		Step:   "building",
		Member: c.Builder,
		Round:  1,
		Plan:   2,
		Event:  `{"kind":"builder_closed","outcome":"done","gate":"green"}`,
		Action: `{"kind":"send","member":"` + c.Reviewer + `","seed":"reviewer"}`,
	}
}

func seededChainStore(t *testing.T) (*Store, db.ChainRow) {
	t.Helper()
	s := New(t.TempDir())
	c := testStoreChain("x")
	if err := s.WithLock(func(tx *Tx) error { return tx.ChainPut(c) }); err != nil {
		t.Fatalf("ChainPut: %v", err)
	}
	return s, c
}

func memberNames(t *testing.T, d *db.DB) []string {
	t.Helper()
	recs, err := d.RecordList("")
	if err != nil {
		t.Fatalf("RecordList: %v", err)
	}
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Name
	}
	return out
}

// TestCreateChainWritesEveryMemberAndTheChainAtomically pins the all-or-none
// create: a member whose directory cannot be made leaves no chain row and no
// member row.
func TestCreateChainWritesEveryMemberAndTheChainAtomically(t *testing.T) {
	root := t.TempDir()
	s := New(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	// A member's binding directory that is a file refuses prepareSave.
	if err := os.WriteFile(filepath.Join(root, "x-rev"), []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("plant the blocking file: %v", err)
	}

	err := s.WithLock(func(tx *Tx) error {
		return tx.CreateChain(testStoreChain("x"), []Binding{
			newBinding("x", "/repo"),
			newBinding("x-rev", "/repo"),
		})
	})
	if err == nil {
		t.Fatal("CreateChain = nil, want the member's error")
	}

	if _, err := s.Chain("x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Chain after a failed create = %v, want ErrNotFound", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if names := memberNames(t, d); len(names) != 0 {
		t.Errorf("member records = %v, want none", names)
	}
}

// TestCreateChainWritesNoMemberWhenTheChainRowIsRefused pins the one
// transaction from the other side: when the chain row's own write is refused
// IN the transaction, after every member prepared and wrote, no member record
// survives either. The failure is injected after the members' writes -- unlike
// TestCreateChainWritesEveryMemberAndTheChainAtomically, whose message cannot be
// prepared at all -- so it fails when the members and the chain row are written
// in two separate transactions.
func TestCreateChainWritesNoMemberWhenTheChainRowIsRefused(t *testing.T) {
	s := New(t.TempDir())
	// A chain row ChainPut refuses: no plans.
	refused := testStoreChain("x")
	refused.Plans = 0

	err := s.WithLock(func(tx *Tx) error {
		return tx.CreateChain(refused, []Binding{
			newBinding("x", "/repo"),
			newBinding("x-rev", "/repo"),
		})
	})
	if err == nil {
		t.Fatal("CreateChain = nil, want the chain row refused")
	}

	if _, err := s.Chain("x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Chain after the refused row = %v, want ErrNotFound", err)
	}
	d, err := s.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	if names := memberNames(t, d); len(names) != 0 {
		t.Errorf("member records = %v, want none", names)
	}
}

// TestCreateChainSetsTheStoredBindingDefaults pins that each member is saved
// as the ordinary binding it is: format and shape stamped, the builder actor
// defaulted, and the binding's own round kept.
func TestCreateChainSetsTheStoredBindingDefaults(t *testing.T) {
	s := New(t.TempDir())
	builder := newBinding("x", "/repo")
	reviewer := newBinding("x-rev", "/repo")
	reviewer.Role = "reviewer"
	reviewer.Shape = ShapeReader

	if err := s.WithLock(func(tx *Tx) error {
		return tx.CreateChain(testStoreChain("x"), []Binding{builder, reviewer})
	}); err != nil {
		t.Fatalf("CreateChain: %v", err)
	}

	got, err := s.Load("x")
	if err != nil {
		t.Fatalf("Load(x): %v", err)
	}
	if got.Role != "builder" || got.Shape != ShapeWriter {
		t.Errorf("builder role/shape = %q/%q, want builder/writer", got.Role, got.Shape)
	}
	if got.Round != 1 {
		t.Errorf("builder round = %d, want 1", got.Round)
	}
	if got.Format == 0 {
		t.Error("builder format is unset, want the stored format")
	}

	rev, err := s.Load("x-rev")
	if err != nil {
		t.Fatalf("Load(x-rev): %v", err)
	}
	if rev.Role != "reviewer" || rev.Shape != ShapeReader {
		t.Errorf("reviewer role/shape = %q/%q, want reviewer/reader", rev.Role, rev.Shape)
	}
	if rev.Round != 1 {
		t.Errorf("reviewer round = %d, want 1", rev.Round)
	}
}

func TestStoreChainRoundTrip(t *testing.T) {
	s, c := seededChainStore(t)

	got, err := s.Chain("x")
	if err != nil {
		t.Fatalf("Chain(x): %v", err)
	}
	if got.ID != c.ID || got.Step != c.Step || got.Plans != c.Plans {
		t.Errorf("Chain(x) = %+v, want id %s step %q plans %d", got, c.ID, c.Step, c.Plans)
	}

	byMember, err := s.ChainByMember(c.Reviewer)
	if err != nil {
		t.Fatalf("ChainByMember(%s): %v", c.Reviewer, err)
	}
	if byMember.ID != c.ID {
		t.Errorf("ChainByMember id = %s, want %s", byMember.ID, c.ID)
	}

	all, err := s.Chains()
	if err != nil {
		t.Fatalf("Chains: %v", err)
	}
	if len(all) != 1 || all[0].Name != "x" {
		t.Fatalf("Chains = %+v, want only x", all)
	}

	if err := s.WithLock(func(tx *Tx) error {
		return tx.ChainEventAppend("x", chainEventFor(c))
	}); err != nil {
		t.Fatalf("ChainEventAppend: %v", err)
	}
	events, err := s.ChainEvents("x")
	if err != nil {
		t.Fatalf("ChainEvents(x): %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].Member != c.Builder || events[0].Plan != 2 {
		t.Fatalf("ChainEvents(x) = %+v, want one row for %q at seq 1 on plan 2", events, c.Builder)
	}

	if _, err := s.Chain("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Chain(missing) = %v, want ErrNotFound", err)
	}
}

// chainMemberCount counts the stored member rows directly, so a refused create
// can be checked for the rows it must not have written.
func chainMemberCount(t *testing.T, s *Store) int {
	t.Helper()
	sqlDB, err := db.OpenRaw(s.DBPath())
	if err != nil {
		t.Fatalf("OpenRaw: %v", err)
	}
	defer func() { _ = sqlDB.Close() }()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM chain_member`).Scan(&n); err != nil {
		t.Fatalf("count chain_member: %v", err)
	}
	return n
}

// TestCreateChainWritesMemberRowsAtomically pins the member rows in the same
// transaction as the chain: a successful create stores one row per member in
// argument order, and a refused create stores none.
func TestCreateChainWritesMemberRowsAtomically(t *testing.T) {
	t.Run("a successful create", func(t *testing.T) {
		s := New(t.TempDir())
		builder := newBinding("x", "/repo")
		reviewer := newBinding("x-rev", "/repo")
		reviewer.Role = "reviewer"
		reviewer.Shape = ShapeReader

		if err := s.WithLock(func(tx *Tx) error {
			return tx.CreateChain(testStoreChain("x"), []Binding{builder, reviewer})
		}); err != nil {
			t.Fatalf("CreateChain: %v", err)
		}

		members, err := s.ChainMembers("x")
		if err != nil {
			t.Fatalf("ChainMembers(x): %v", err)
		}
		if len(members) != 2 {
			t.Fatalf("member rows = %+v, want 2", members)
		}
		if members[0].Binding != "x" || members[0].Actor != "builder" || members[0].Seq != 0 {
			t.Errorf("builder member row = %+v, want x/builder/0", members[0])
		}
		if members[1].Binding != "x-rev" || members[1].Actor != "reviewer" || members[1].Seq != 1 {
			t.Errorf("reviewer member row = %+v, want x-rev/reviewer/1", members[1])
		}
	})

	t.Run("a refused create", func(t *testing.T) {
		s := New(t.TempDir())
		refused := testStoreChain("x")
		refused.Plans = 0

		if err := s.WithLock(func(tx *Tx) error {
			return tx.CreateChain(refused, []Binding{newBinding("x", "/repo")})
		}); err == nil {
			t.Fatal("CreateChain = nil, want the chain row refused")
		}
		if n := chainMemberCount(t, s); n != 0 {
			t.Errorf("chain_member rows = %d, want none", n)
		}
	})
}

// TestStoreChainMembersAndChecksRoundTrip pins the store twins of the member
// and check reads and writes, and their not-found answers.
func TestStoreChainMembersAndChecksRoundTrip(t *testing.T) {
	s, c := seededChainStore(t)

	if err := s.WithLock(func(tx *Tx) error {
		return tx.ChainMembersPut("x", []db.ChainMemberRow{{Binding: "x-rev", Actor: "reviewer", Seq: 0}})
	}); err != nil {
		t.Fatalf("ChainMembersPut: %v", err)
	}
	members, err := s.ChainMembers("x")
	if err != nil {
		t.Fatalf("ChainMembers: %v", err)
	}
	if len(members) != 1 || members[0].Binding != "x-rev" || members[0].ChainID != c.ID || members[0].Actor != "reviewer" {
		t.Fatalf("ChainMembers = %+v, want the written row for %s", members, c.ID)
	}

	var run int
	if err := s.WithLock(func(tx *Tx) error {
		var err error
		run, err = tx.ChainCheckNextRun("x")
		return err
	}); err != nil || run != 1 {
		t.Fatalf("ChainCheckNextRun = (%d, %v), want (1, nil)", run, err)
	}
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ChainCheckPut("x", db.ChainCheckRow{Run: run, Step: "check", Command: "make check", Result: "green"})
	}); err != nil {
		t.Fatalf("ChainCheckPut: %v", err)
	}
	check, err := s.ChainCheck("x", run)
	if err != nil {
		t.Fatalf("ChainCheck: %v", err)
	}
	if check.Run != run || check.Command != "make check" || check.Result != "green" || check.ChainID != c.ID {
		t.Errorf("ChainCheck = %+v, want the written check for %s", check, c.ID)
	}
	if next, err := s.ChainCheckNextRun("x"); err != nil || next != 2 {
		t.Errorf("ChainCheckNextRun after a put = (%d, %v), want (2, nil)", next, err)
	}

	if _, err := s.ChainCheck("x", 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainCheck(missing run) = %v, want ErrNotFound", err)
	}
	if _, err := s.ChainMembers("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainMembers(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.ChainCheck("missing", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainCheck(missing chain) = %v, want ErrNotFound", err)
	}
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ChainCheckPut("missing", db.ChainCheckRow{Run: 1})
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainCheckPut(missing) = %v, want ErrNotFound", err)
	}
	if err := s.WithLock(func(tx *Tx) error {
		return tx.ChainMembersPut("missing", nil)
	}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainMembersPut(missing) = %v, want ErrNotFound", err)
	}
	if _, err := s.ChainCheckNextRun("missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainCheckNextRun(missing) = %v, want ErrNotFound", err)
	}
}

// TestChainSaveWithEventLeavesNoRowOnAFailedEvent pins the one transaction:
// when either half of the save fails, neither the new state nor the trace row
// survives.
func TestChainSaveWithEventLeavesNoRowOnAFailedEvent(t *testing.T) {
	t.Run("an invalid state change", func(t *testing.T) {
		s, c := seededChainStore(t)
		bad := c
		bad.Step = ""
		if err := s.WithLock(func(tx *Tx) error {
			return tx.ChainSaveWithEvent(bad, chainEventFor(c))
		}); err == nil {
			t.Fatal("ChainSaveWithEvent = nil, want the invalid row refused")
		}
		assertChainUnchangedAndNoEvents(t, s, c)
	})

	t.Run("a duplicate primary key", func(t *testing.T) {
		s, c := seededChainStore(t)
		dup := testStoreChain("y")
		dup.ID = c.ID
		if err := s.WithLock(func(tx *Tx) error {
			return tx.ChainSaveWithEvent(dup, chainEventFor(c))
		}); err == nil {
			t.Fatal("ChainSaveWithEvent = nil, want the duplicate id refused")
		}
		if _, err := s.Chain("y"); !errors.Is(err, ErrNotFound) {
			t.Errorf("Chain(y) after the failed save = %v, want ErrNotFound", err)
		}
		assertChainUnchangedAndNoEvents(t, s, c)
	})
}

func assertChainUnchangedAndNoEvents(t *testing.T, s *Store, want db.ChainRow) {
	t.Helper()
	got, err := s.Chain(want.Name)
	if err != nil {
		t.Fatalf("Chain(%s): %v", want.Name, err)
	}
	if got.Status != want.Status || got.Step != want.Step || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("chain = %+v, want the unchanged %+v", got, want)
	}
	events, err := s.ChainEvents(want.Name)
	if err != nil {
		t.Fatalf("ChainEvents(%s): %v", want.Name, err)
	}
	if len(events) != 0 {
		t.Errorf("events = %+v, want none", events)
	}
}

// TestChainByMemberAfterARenameIsGone pins the lookup against the live member
// columns: a member the chain no longer names resolves to nothing.
func TestChainByMemberAfterARenameIsGone(t *testing.T) {
	s, c := seededChainStore(t)

	renamed := c
	renamed.Builder = "y"
	if err := s.WithLock(func(tx *Tx) error { return tx.ChainPut(renamed) }); err != nil {
		t.Fatalf("ChainPut(renamed): %v", err)
	}

	if _, err := s.ChainByMember(c.Builder); !errors.Is(err, ErrNotFound) {
		t.Errorf("ChainByMember(%s) = %v, want ErrNotFound", c.Builder, err)
	}
	got, err := s.ChainByMember("y")
	if err != nil {
		t.Fatalf("ChainByMember(y): %v", err)
	}
	if got.ID != c.ID {
		t.Errorf("ChainByMember(y) id = %s, want %s", got.ID, c.ID)
	}
}

// TestChainInputPathKeysACopyByItsSource pins the copy's name: it is
// <chainDir>/inputs/<source binding>-<NNN>/<source base name>, derived from the
// source path's own binding and round file; a path outside the root, or one the
// store cannot resolve to a binding round file, resolves no copy.
func TestChainInputPathKeysACopyByItsSource(t *testing.T) {
	s := New(t.TempDir())

	cases := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "a flat round file",
			source: s.DiffPath("shop", 4),
			want:   filepath.Join(s.ChainInputDir("x"), "shop-004", "004-diff.patch"),
		},
		{
			name:   "a nested artifact",
			source: s.OutputPath("rev", 3, "reviewer", "report"),
			want:   filepath.Join(s.ChainInputDir("x"), "rev-003", "report.md"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.ChainInputPath("x", tc.source)
			if !ok || got != tc.want {
				t.Errorf("ChainInputPath(%s) = %q (ok %v), want %q", tc.source, got, ok, tc.want)
			}
		})
	}

	for _, bad := range []string{
		filepath.Join(t.TempDir(), "004-diff.patch"),
		s.ChainDir("x"),
		s.ChainPlanPath("x", 1),
	} {
		if got, ok := s.ChainInputPath("x", bad); ok {
			t.Errorf("ChainInputPath(%s) = %q, want false", bad, got)
		}
	}
}

// TestDiskRegularFileRefusesAPlantAtARowOnlyKey pins the disk-record rule: a
// plant at a reserved round-file name is never a disk record, while a plain
// report is; a symlink is never one either.
func TestDiskRegularFileRefusesAPlantAtARowOnlyKey(t *testing.T) {
	s := New(t.TempDir())
	const binding = "shop"

	for _, tc := range reservedKeyCases() {
		path := tc.path(s, binding)
		writePlant(t, path, "the plant's bytes\n")
		if s.DiskRegularFile(path) {
			t.Errorf("DiskRegularFile(%s) = true, want false: a reserved name is row-only", tc.name)
		}
	}

	report := s.ReportPath(binding, 1)
	writePlant(t, report, "the report\n")
	if !s.DiskRegularFile(report) {
		t.Errorf("DiskRegularFile(%s) = false, want true: a plain report is a disk record", report)
	}

	sentinel := filepath.Join(t.TempDir(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("x"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
	link := s.ReportPath(binding, 2)
	if err := os.Symlink(sentinel, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if s.DiskRegularFile(link) {
		t.Errorf("DiskRegularFile(%s) = true, want false: a symlink is never opened", link)
	}
}
