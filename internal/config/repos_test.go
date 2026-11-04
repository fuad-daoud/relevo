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
		reposTestKey: {Name: "laptop", Gate: "make check", BoardTheme: "midnight", BuilderActor: "relevo-builder", ReviewerActor: "relevo-reviewer", PlannerActor: "relevo-lite-planner", Cleanup: "make clean", CleanupAfter: "both"},
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

// TestParseReposTrimpath pins the fourth key: on and off both store and
// round-trip through EncodeRepos, a value outside the two is refused naming
// the section and the entry it was found on, and an entry whose only field is
// the default `on` is still stored -- the explicit word is the encoding of
// "this repository said so", without which the entry would drop as empty and
// the setting could never be carried across a sync.
func TestParseReposTrimpath(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ value, want string }{{"off", "off"}, {"on", "on"}} {
		body := `{"` + reposTestKey + `":{"trimpath":"` + tc.value + `"}}`
		out, err := ParseRepos([]byte(body))
		if err != nil {
			t.Fatalf("ParseRepos(%s): %v", body, err)
		}
		if got := out[reposTestKey].Trimpath; got != tc.want {
			t.Errorf("trimpath = %q, want %q", got, tc.want)
		}
		encoded, err := EncodeRepos(out)
		if err != nil {
			t.Fatalf("EncodeRepos(%+v): %v", out, err)
		}
		round, err := ParseRepos(encoded)
		if err != nil {
			t.Fatalf("ParseRepos(%s): %v", encoded, err)
		}
		if round[reposTestKey].Trimpath != tc.want {
			t.Errorf("round trip = %q, want %q", round[reposTestKey].Trimpath, tc.want)
		}
	}

	_, err := ParseRepos([]byte(`{"` + reposTestKey + `":{"trimpath":"maybe"}}`))
	if err == nil {
		t.Fatal("ParseRepos(maybe) = nil error, want a refusal")
	}
	for _, want := range []string{string(Repos), reposTestKey, `trimpath is "maybe"`, "on or off"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}

	out, err := ParseRepos([]byte(`{"` + reposTestKey + `":{"trimpath":"on"}}`))
	if err != nil {
		t.Fatalf("ParseRepos(trimpath only): %v", err)
	}
	if _, ok := out[reposTestKey]; !ok {
		t.Errorf("an entry holding only trimpath on dropped: %+v, want it stored (on is stored explicitly for exactly this reason)", out)
	}
}

// TestParseReposCleanup pins the two cleanup keys: a command with
// cleanup_after both round-trips through EncodeRepos, each of round, chain
// and both parses, and a value outside the three is refused naming the want.
// Dropping one's case arm is the mutation this test catches.
func TestParseReposCleanup(t *testing.T) {
	t.Parallel()

	body := `{"` + reposTestKey + `":{"cleanup":"make clean","cleanup_after":"both"}}`
	out, err := ParseRepos([]byte(body))
	if err != nil {
		t.Fatalf("ParseRepos(%s): %v", body, err)
	}
	if got := out[reposTestKey].Cleanup; got != "make clean" {
		t.Errorf("cleanup = %q, want %q", got, "make clean")
	}
	if got := out[reposTestKey].CleanupAfter; got != "both" {
		t.Errorf("cleanup_after = %q, want %q", got, "both")
	}
	encoded, err := EncodeRepos(out)
	if err != nil {
		t.Fatalf("EncodeRepos(%+v): %v", out, err)
	}
	round, err := ParseRepos(encoded)
	if err != nil {
		t.Fatalf("ParseRepos(%s): %v", encoded, err)
	}
	if got := round[reposTestKey]; got.Cleanup != "make clean" || got.CleanupAfter != "both" {
		t.Errorf("round trip = %+v, want cleanup and cleanup_after kept", got)
	}

	for _, after := range []string{"round", "chain", "both"} {
		out, err := ParseRepos([]byte(`{"` + reposTestKey + `":{"cleanup_after":"` + after + `"}}`))
		if err != nil {
			t.Fatalf("ParseRepos(cleanup_after %s): %v", after, err)
		}
		if got := out[reposTestKey].CleanupAfter; got != after {
			t.Errorf("cleanup_after = %q, want %q", got, after)
		}
	}

	_, err = ParseRepos([]byte(`{"` + reposTestKey + `":{"cleanup_after":"weekly"}}`))
	if err == nil {
		t.Fatal("ParseRepos(weekly) = nil error, want a refusal")
	}
	for _, want := range []string{string(Repos), reposTestKey, `cleanup_after is "weekly"`, "round, chain or both"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestParseReposActors pins the three actor keys: each stores and round-trips
// through EncodeRepos like gate does, an entry holding only one is still
// stored, a non-string value is refused naming the section and the entry, and
// the unknown-field refusal names the new vocabulary. Dropping one field's
// case arm is the mutation this test catches.
func TestParseReposActors(t *testing.T) {
	t.Parallel()

	actorOf := func(e RepoEntry, field string) string {
		switch field {
		case "builder_actor":
			return e.BuilderActor
		case "reviewer_actor":
			return e.ReviewerActor
		case "planner_actor":
			return e.PlannerActor
		default:
			t.Fatalf("unknown actor field %q", field)
			return ""
		}
	}

	for _, field := range []string{"builder_actor", "reviewer_actor", "planner_actor"} {
		body := `{"` + reposTestKey + `":{"` + field + `":"relevo-custom"}}`
		out, err := ParseRepos([]byte(body))
		if err != nil {
			t.Fatalf("ParseRepos(%s): %v", body, err)
		}
		if got := actorOf(out[reposTestKey], field); got != "relevo-custom" {
			t.Errorf("%s = %q, want %q", field, got, "relevo-custom")
		}
		encoded, err := EncodeRepos(out)
		if err != nil {
			t.Fatalf("EncodeRepos(%+v): %v", out, err)
		}
		round, err := ParseRepos(encoded)
		if err != nil {
			t.Fatalf("ParseRepos(%s): %v", encoded, err)
		}
		if got := actorOf(round[reposTestKey], field); got != "relevo-custom" {
			t.Errorf("%s round trip = %q, want %q", field, got, "relevo-custom")
		}

		_, err = ParseRepos([]byte(`{"` + reposTestKey + `":{"` + field + `":1}}`))
		if err == nil {
			t.Fatalf("ParseRepos(%s as a number) = nil error, want a refusal", field)
		}
		for _, want := range []string{string(Repos), reposTestKey, field + " is not text"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s refusal = %q, want it to name %q", field, err, want)
			}
		}

		empty, err := ParseRepos([]byte(`{"` + reposTestKey + `":{"` + field + `":""}}`))
		if err != nil {
			t.Fatalf("ParseRepos(%s empty): %v", field, err)
		}
		if len(empty) != 0 {
			t.Errorf("ParseRepos(%s empty) = %+v, want no entry (an empty field stores as absent)", field, empty)
		}
	}

	_, err := ParseRepos([]byte(`{"` + reposTestKey + `":{"typo":"x"}}`))
	if err == nil {
		t.Fatal("ParseRepos(typo) = nil error, want a refusal")
	}
	for _, want := range []string{"builder_actor", "reviewer_actor", "planner_actor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("unknown-field refusal = %q, want it to name %q", err, want)
		}
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
