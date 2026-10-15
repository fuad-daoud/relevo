package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The repos section: one repository's settings keyed by the repository's
// identity, one key per repository when it syncs, and a write that is an
// ordinary revision.

const reposTestKey = "aaaaaaaa"

func TestReposEntryRoundTripsEveryField(t *testing.T) {
	t.Parallel()

	in := map[string]RepoEntry{
		reposTestKey: {Name: "laptop", Gate: "make check", BoardTheme: "midnight"},
	}
	body, err := EncodeRepos(in)
	if err != nil {
		t.Fatalf("EncodeRepos: %v", err)
	}
	out, err := ParseRepos(body)
	if err != nil {
		t.Fatalf("ParseRepos(%s): %v", body, err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip = %+v, want %+v (a lost field means the struct or its tag dropped one)", out, in)
	}
}

func TestReposSectionRefusesABadShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{"not an object", `[{"a":1}]`, "repos:"},
		{"unknown field", `{"` + reposTestKey + `":{"typo":"x"}}`, `unknown field "typo"`},
		{"non-string value", `{"` + reposTestKey + `":{"gate":1}}`, "gate is not text"},
		{"null entry", `{"` + reposTestKey + `":null}`, "entry is null"},
	}
	for _, tc := range cases {
		_, err := ParseRepos([]byte(tc.body))
		if err == nil {
			t.Errorf("%s: ParseRepos(%s) = nil error, want a refusal naming %q", tc.name, tc.body, tc.want)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %q, want it to name %q", tc.name, err, tc.want)
		}
	}
}

func TestReposEmptyEntryIsNotStored(t *testing.T) {
	t.Parallel()

	out, err := ParseRepos([]byte(`{"` + reposTestKey + `":{},"other":{"name":""}}`))
	if err != nil {
		t.Fatalf("ParseRepos: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("ParseRepos = %+v, want no entry at all (an entry whose fields are all empty is not stored)", out)
	}

	body, err := EncodeRepos(map[string]RepoEntry{reposTestKey: {}})
	if err != nil {
		t.Fatalf("EncodeRepos: %v", err)
	}
	if string(body) != "{}" {
		t.Errorf("EncodeRepos = %s, want {} (an empty entry is dropped)", body)
	}
}

// TestReposSyncsOneKeyPerRepo pins the per-item shape: the document contributes
// one key per stored repository, and an arriving item lands one entry without
// touching the others.
func TestReposSyncsOneKeyPerRepo(t *testing.T) {
	t.Parallel()

	s := openRootedStore(t)
	seedDocConfig(t, s)
	if err := seedOne(t, s, Repos, `{"`+reposTestKey+`":{"name":"laptop"},"bbbbbbbb":{"gate":"make check"}}`); err != nil {
		t.Fatalf("Put(repos): %v", err)
	}

	keys := sortedItemKeys(t, s.Doc())
	var reposKeys []string
	for _, key := range keys {
		if strings.HasPrefix(key, "repos/") {
			reposKeys = append(reposKeys, key)
		}
	}
	want := []string{"repos/" + reposTestKey, "repos/bbbbbbbb"}
	sort.Strings(want)
	if !slices.Equal(reposKeys, want) {
		t.Fatalf("Items() repos keys = %v, want exactly the stored keys %v (a whole-section key here would make two machines editing two repositories displace each other)", reposKeys, want)
	}

	if err := s.Doc().Apply("repos/cccccccc", json.RawMessage(`{"gate":"go vet ./..."}`)); err != nil {
		t.Fatalf("Apply(repos/cccccccc): %v", err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(loaded.Repos) != 3 {
		t.Fatalf("Load held %d entries, want 3: the applied key must land beside the stored ones", len(loaded.Repos))
	}
	if got := loaded.Repos["cccccccc"]; got.Gate != "go vet ./..." {
		t.Errorf("applied entry = %+v, want its gate", got)
	}
	if got := loaded.Repos[reposTestKey]; got.Name != "laptop" {
		t.Errorf("stored entry = %+v, want it untouched by the apply", got)
	}
}

// seedOne stores one section body through the store, for tests that need only
// one write.
func seedOne(t *testing.T, s *Store, sec Section, body string) error {
	t.Helper()
	_, err := s.As("test", "write "+string(sec)).Put(sec, []byte(body))
	return err
}

// TestReposWriteIsARevisionAndRollsBack proves the new section sits inside the
// generic revision machinery: the write records a revision naming the key, and
// rolling back to it removes the section again.
func TestReposWriteIsARevisionAndRollsBack(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if err := seedOne(t, s, Repos, `{}`); err != nil {
		t.Fatalf("Put(repos): %v", err)
	}
	if err := seedOne(t, s, Repos, `{"`+reposTestKey+`":{"gate":"make check"}}`); err != nil {
		t.Fatalf("Put(repos): %v", err)
	}

	rows, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("a repos write left no revision for it")
	}
	// The revision to roll back to is the section's own first write, the one
	// that stored the empty body: rolling back to the entry's write would be a
	// no-op, because a revision's snapshot is the state after it.
	var rev int64
	for _, row := range rows {
		var changes []Change
		if err := json.Unmarshal(row.Changes, &changes); err != nil {
			t.Fatalf("revision #%d changes: %v", row.Rev, err)
		}
		for _, c := range changes {
			if c.Path == string(Repos) && c.Op == "add" {
				rev = row.Rev
			}
		}
	}
	if rev == 0 {
		t.Fatalf("no revision records the repos section's own write: %+v", rows)
	}

	if _, err := s.Rollback(rev); err != nil {
		t.Fatalf("Rollback(#%d): %v", rev, err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatalf("Load after rollback: %v", err)
	}
	if len(loaded.Repos) != 0 {
		t.Errorf("repos after rollback = %+v, want the entry gone", loaded.Repos)
	}
}

// TestReposSectionSurvivesExportImport pins that an export carries the section
// and an import of it lands it on another machine's store whole.
func TestReposSectionSurvivesExportImport(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	if err := seedOne(t, s, Repos, `{"`+reposTestKey+`":{"name":"laptop","gate":"make check","board_theme":"midnight"}}`); err != nil {
		t.Fatalf("Put(repos): %v", err)
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
	body, ok := exported[string(Repos)]
	if !ok {
		t.Fatalf("the exported document carries no repos key: %s", raw)
	}

	peer := openStore(t)
	if _, err := peer.As("import", "config import").PutDoc(Doc{Repos: body}); err != nil {
		t.Fatalf("PutDoc(repos): %v", err)
	}
	loaded, err := peer.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]RepoEntry{reposTestKey: {Name: "laptop", Gate: "make check", BoardTheme: "midnight"}}
	if !reflect.DeepEqual(loaded.Repos, want) {
		t.Errorf("imported repos = %+v, want %+v", loaded.Repos, want)
	}
}
