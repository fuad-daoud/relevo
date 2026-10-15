package config

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/db"
)

// openStoreWithDB returns the handle a seed test reopens the same database
// with, because a seed's rules are about what a second open does. The store
// is bound to a temporary config root, the shape every machine has, so its
// export side is ready for the reads the convergence test takes.
func openStoreWithDB(t *testing.T) (*db.DB, *Store) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	root := t.TempDir()
	return d, Open(d).WithFiles(newMemApplier(root), root)
}

// openSameStore binds a second store over the handle, which is a reopen:
// the marker a first open wrote is all a second one may read.
func openSameStore(t *testing.T, d *db.DB) *Store {
	t.Helper()
	return Open(d)
}

// The themes section: board palettes keyed by name, seeded once per database
// from the release's embedded data, one key per palette when it syncs, and
// writes that are ordinary revisions.

func TestThemesEntryRoundTrips(t *testing.T) {
	t.Parallel()

	palette, err := theme.Parse("ford", []byte(themesVariant))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	body, err := EncodeThemes(map[string]theme.Theme{"ford": palette})
	if err != nil {
		t.Fatalf("EncodeThemes: %v", err)
	}
	out, err := ParseThemes(body)
	if err != nil {
		t.Fatalf("ParseThemes(%s): %v", body, err)
	}
	if out["ford"] != palette {
		t.Errorf("round trip = %+v, want %+v", out["ford"], palette)
	}
}

// TestThemesSectionRefusesABadPalette needs the write path for the field
// rules and the validator itself for the colour rules, both of which the
// Themes case in Validate routes to: a colour the validator refuses is
// refused at write time naming the palette and the field, so a broken palette
// never reaches the database.
func TestThemesSectionRefusesABadPalette(t *testing.T) {
	t.Parallel()

	bad := strings.Replace(themesVariant, `"bg":"#101318"`, `"bg":"mustard"`, 1)

	// Through a write: the refused write stores nothing.
	s := openStore(t)
	_, err := s.As("test", "write themes").Put(Themes, []byte(`{"night":`+bad+`}`))
	if err == nil {
		t.Fatal("a palette with a non-colour bg was stored")
	}
	if !strings.Contains(err.Error(), "bg") || !strings.Contains(err.Error(), "night") {
		t.Errorf("refusal = %q, want it to name the entry and the field", err)
	}
	writeBody, present, err := s.Body(Themes)
	if err != nil {
		t.Fatalf("Body(themes): %v", err)
	}
	if present {
		t.Errorf("the refused write stored %s, want nothing", writeBody)
	}

	// Through the validator, which is what the store's own writes call.
	if _, err := Validate(Themes, []byte(`{"night":`+bad+`}`)); err == nil || !strings.Contains(err.Error(), "bg") {
		t.Errorf("Validate(Themes) = %v, want a refusal naming bg", err)
	}
}

// TestThemesSyncsOneKeyPerTheme pins the per-item shape: the document
// contributes one key per stored palette, and an arriving item lands one
// palette without touching the others.
func TestThemesSyncsOneKeyPerTheme(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	if err := seedOne(t, s, Themes, themesBase); err != nil {
		t.Fatalf("Put(themes): %v", err)
	}

	keys := sortedItemKeys(t, s.Doc())
	var themeKeys []string
	for _, key := range keys {
		if strings.HasPrefix(key, "themes/") {
			themeKeys = append(themeKeys, key)
		}
	}
	want := []string{"themes/blueprint", "themes/cockpit"}
	if !slices.Equal(themeKeys, want) {
		t.Fatalf("Items() themes keys = %v, want exactly the stored keys %v (a whole-section key here would make two machines editing two palettes displace each other)", themeKeys, want)
	}

	entry := json.RawMessage(themesVariant)
	if err := s.Doc().Apply("themes/night", entry); err != nil {
		t.Fatalf("Apply(themes/night): %v", err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Themes) != 3 {
		t.Fatalf("Load held %d palettes, want 3: the applied key must land beside the stored ones", len(loaded.Themes))
	}
	if got := loaded.Themes["night"]; got.Accent != "#6fb9fe" {
		t.Errorf("applied palette accent = %q, want the arriving entry's", got.Accent)
	}
	if got := loaded.Themes["cockpit"]; got.Accent != "#6ea8fe" {
		t.Errorf("stored palette accent = %q, want it untouched by the apply", got.Accent)
	}
}

// TestThemesWriteIsARevisionAndRollsBack proves the section sits inside the
// generic revision machinery: the write records a revision naming the key,
// and rolling back to it removes the section again.
func TestThemesWriteIsARevisionAndRollsBack(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if err := seedOne(t, s, Themes, `{}`); err != nil {
		t.Fatalf("Put(themes): %v", err)
	}
	if err := seedOne(t, s, Themes, themesBase); err != nil {
		t.Fatalf("Put(themes): %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("a themes write left no revision for it")
	}
	// The revision to roll back to is the section's own first write, the one
	// that stored the empty body: rolling back to the palettes' write would
	// be a no-op, because a revision's snapshot is the state after it.
	var rev int64
	for _, row := range rows {
		var changes []Change
		if err := json.Unmarshal(row.Changes, &changes); err != nil {
			t.Fatalf("revision #%d changes: %v", row.Rev, err)
		}
		for _, c := range changes {
			if c.Path == string(Themes) && c.Op == "add" {
				rev = row.Rev
			}
		}
	}
	if rev == 0 {
		t.Fatalf("no revision records the themes section's own write: %+v", rows)
	}

	if _, err := s.Rollback(rev); err != nil {
		t.Fatalf("Rollback(#%d): %v", rev, err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load after rollback: %v", err)
	}
	if len(loaded.Themes) != 0 {
		t.Errorf("themes after rollback = %v, want the palettes gone", loaded.Themes)
	}
}

// TestThemesSectionSurvivesExportImport pins that an export carries the
// section and an import of it lands it on another machine's store whole.
func TestThemesSectionSurvivesExportImport(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if err := seedOne(t, s, Themes, themesBase); err != nil {
		t.Fatalf("Put(themes): %v", err)
	}

	doc, err := s.Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	raw, err := EncodeDoc(doc)
	if err != nil {
		t.Fatalf("EncodeDoc: %v", err)
	}

	var exported map[string]json.RawMessage
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatalf("the exported document does not parse: %v", err)
	}
	body, ok := exported[string(Themes)]
	if !ok {
		t.Fatalf("the exported document carries no themes key: %s", raw)
	}

	peer := openStore(t)
	if _, err := peer.As("import", "config import").PutDoc(Doc{Themes: body}); err != nil {
		t.Fatalf("PutDoc(themes): %v", err)
	}
	loaded, err := peer.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want, err := ParseThemes([]byte(themesBase))
	if err != nil {
		t.Fatalf("ParseThemes: %v", err)
	}
	if !reflect.DeepEqual(loaded.Themes, want) {
		t.Errorf("imported themes = %+v, want %+v", loaded.Themes, want)
	}
}

// TestThemesSeedOnceAndADeletionSticks pins the seed's once-per-database
// rule: seeding writes the palettes into the section, unsetting one stays
// unset across a reopen, and a machine whose marker was cleared by hand (the
// mutation's shape: seeding on every open) resurrects it.
func TestThemesSeedOnceAndADeletionSticks(t *testing.T) {
	t.Parallel()

	d, s := openStoreWithDB(t)
	if wrote, err := s.EnsureThemesSeed(); err != nil || !wrote {
		t.Fatalf("first EnsureThemesSeed = (%t, %v), want (true, nil)", wrote, err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Themes) != len(theme.SeedNames()) {
		t.Fatalf("seeded themes = %+v, want every seed palette", loaded.Themes)
	}

	// `config unset themes.cockpit` updates the section's body without the
	// key, which is exactly this write.
	if err := seedOne(t, s, Themes, `{"blueprint":`+themesBlueprint+`}`); err != nil {
		t.Fatalf("unset cockpit: %v", err)
	}

	// The marker records that the seed ran, so a reopen must not touch it:
	// a palette the user unsets stays unset.
	s2 := openSameStore(t, d)
	if wrote, err := s2.EnsureThemesSeed(); err != nil || wrote {
		t.Fatalf("reopen EnsureThemesSeed = (%t, %v), want (false, nil)", wrote, err)
	}
	loaded, err = s2.Load()
	if err != nil {
		t.Fatalf("Load after reopen: %v", err)
	}
	if _, present := loaded.Themes["cockpit"]; present {
		t.Errorf("cockpit is back after a unset and a reopen, want it unset (the seed must run once per database, not once per open)")
	}
	if _, present := loaded.Themes["blueprint"]; !present {
		t.Errorf("blueprint is gone, want the rest of the section untouched by it")
	}
}

// TestThemesSeedNeverOverwrites pins that a seeded entry never overwrites a
// stored one: a palette a user stored -- or that a peer's sync landed --
// before this database's first seed keeps its colours through the seed, and a
// mutation that writes every seed name regardless would clobber it. The user
// edit here is exactly what `config set themes.cockpit '{...}'` writes.
func TestThemesSeedNeverOverwrites(t *testing.T) {
	t.Parallel()

	d, s := openStoreWithDB(t)
	if err := seedOne(t, s, Themes, `{"cockpit":`+themesVariant+`}`); err != nil {
		t.Fatalf("Put(themes): %v", err)
	}

	wrote, err := s.EnsureThemesSeed()
	if err != nil {
		t.Fatalf("EnsureThemesSeed: %v", err)
	}
	if !wrote {
		t.Error("EnsureThemesSeed wrote nothing though blueprint was absent")
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := loaded.Themes["cockpit"]; got.Accent != "#6fb9fe" {
		t.Errorf("cockpit accent = %q, want the user's entry kept by the seed", got.Accent)
	}
	if _, present := loaded.Themes["blueprint"]; !present {
		t.Error("blueprint is missing, want the seed to add the absent names")
	}

	// And the marker records that the seed ran, so a reopen writes nothing
	// more.
	s2 := openSameStore(t, d)
	if wrote, err := s2.EnsureThemesSeed(); err != nil || wrote {
		t.Fatalf("reopen EnsureThemesSeed = (%t, %v), want (false, nil)", wrote, err)
	}
}

// TestThemesSeedKeepsALocalOverrideAndKeysEveryMachineSame pins what every
// machine seeds: the per-item keys carry the same bytes per name, which is
// what makes two independently seeded machines converge.
func TestThemesSeedKeysEveryMachineSame(t *testing.T) {
	t.Parallel()

	_, a := openStoreWithDB(t)
	_, b := openStoreWithDB(t)
	if _, err := a.EnsureThemesSeed(); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := b.EnsureThemesSeed(); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	aItems, err := a.Doc().Items()
	if err != nil {
		t.Fatalf("Items(A): %v", err)
	}
	bItems, err := b.Doc().Items()
	if err != nil {
		t.Fatalf("Items(B): %v", err)
	}
	aKeys := make([]string, 0)
	for key := range aItems {
		if strings.HasPrefix(key, "themes/") {
			aKeys = append(aKeys, key)
		}
	}
	bKeys := make([]string, 0)
	for key := range bItems {
		if strings.HasPrefix(key, "themes/") {
			bKeys = append(bKeys, key)
		}
	}
	sort.Strings(aKeys)
	sort.Strings(bKeys)
	if !slices.Equal(aKeys, bKeys) {
		t.Fatalf("seeded keys = %v vs %v, want one identical set per machine", aKeys, bKeys)
	}
	for _, key := range aKeys {
		if string(aItems[key]) != string(bItems[key]) {
			t.Errorf("seeded %q bodies differ across machines", key)
		}
	}
}

// TestThemeSeedDoesNotEarnABaseline pins the seed's place beside the
// revisions: it writes revisionlessly, so a machine whose only stored section
// is the seeded `themes` gets no baseline before its first recording write,
// exactly as an empty one does. A user section above the seed earns the
// baseline as always.
func TestThemeSeedDoesNotEarnABaseline(t *testing.T) {
	t.Parallel()

	seedThemes := func() *Store {
		s := openStore(t).WithClock(atTime(revAt))
		if err := s.db.Tx(func(t *db.Tx) error {
			// The shape EnsureThemesSeed writes: raw rows, no revision.
			return t.ConfigPut(string(Themes), []byte(themesBase), revAt)
		}); err != nil {
			t.Fatalf("seed ConfigPut: %v", err)
		}
		return s
	}

	// The seed alone: the first user write takes no baseline.
	s := seedThemes()
	if _, err := s.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":1}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 1 || rows[0].Source != "cli" {
		t.Errorf("Log = %+v, want one cli revision and no baseline (a doc of the seed alone is not user config)", rows)
	}

	// One user section above the seed: the baseline appears as always.
	s2 := seedThemes()
	if err := s2.db.Tx(func(t *db.Tx) error {
		return t.ConfigPut(string(Policy), []byte(`{"max_switches":0}`), revAt)
	}); err != nil {
		t.Fatalf("seed ConfigPut(policy): %v", err)
	}
	if _, err := s2.As("cli", "config set policy").Put(Policy, []byte(`{"max_switches":2}`)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rows, err = s2.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) != 2 || rows[1].Source != "baseline" {
		t.Errorf("Log = %+v, want the cli write and a baseline revision", rows)
	}
}
