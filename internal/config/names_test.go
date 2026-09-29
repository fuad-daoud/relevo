package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnsureCandidateNamesWritesOnce(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	seedNamelessCandidates(t, s, `[
	  {"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"colour":"red"},
	  {"harness":"agy","provider":"google","model":"gemini-3.8-flash-high","roles":["builder"]}
	]`)
	before, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}

	changed, err := s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("EnsureCandidateNames: %v", err)
	}
	if !changed {
		t.Fatal("EnsureCandidateNames = false, want true on the first run")
	}

	revs, err := s.Log(1)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(revs) == 0 {
		t.Fatal("Log returned no revisions")
	}
	if revs[0].Source != "migration" {
		t.Errorf("revision source = %q, want migration", revs[0].Source)
	}
	if revs[0].Message != "candidate names derived" {
		t.Errorf("revision message = %q, want %q", revs[0].Message, "candidate names derived")
	}

	stored, ok, err := s.Body(Candidates)
	if err != nil || !ok {
		t.Fatalf("Body: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(stored), `"colour": "red"`) {
		t.Errorf("stored body lost the unknown key: %s", stored)
	}
	assertCandidateNames(t, stored, []string{"sonnet", "gemini-3.8-flash-high"})

	after, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Error("the version did not change on the write")
	}

	changed, err = s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("second EnsureCandidateNames: %v", err)
	}
	if changed {
		t.Error("second EnsureCandidateNames = true, want false")
	}
	again, err := s.Version()
	if err != nil {
		t.Fatal(err)
	}
	if again != after {
		t.Errorf("the second run bumped the version: %d -> %d", after, again)
	}
}

// assertCandidateNames checks each stored row kept its model and carries
// want[i] as its name, in order.
func assertCandidateNames(t *testing.T, stored []byte, want []string) {
	t.Helper()
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(stored, &rows); err != nil {
		t.Fatalf("decode stored body: %v", err)
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, name := range want {
		var gotName, model string
		if err := json.Unmarshal(rows[i]["name"], &gotName); err != nil {
			t.Fatalf("row %d name: %v", i, err)
		}
		if gotName != name {
			t.Errorf("row %d name = %q, want %q", i, gotName, name)
		}
		if err := json.Unmarshal(rows[i]["model"], &model); err != nil {
			t.Fatalf("row %d model: %v", i, err)
		}
		if model == "" {
			t.Errorf("row %d lost its model", i)
		}
	}
}

func TestEnsureCandidateNamesAbsentSection(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	changed, err := s.EnsureCandidateNames()
	if err != nil {
		t.Fatalf("EnsureCandidateNames: %v", err)
	}
	if changed {
		t.Error("EnsureCandidateNames = true, want false with no candidates section")
	}

	if _, ok, err := s.Body(Candidates); err != nil || ok {
		t.Errorf("a section was written: ok=%v err=%v", ok, err)
	}
}

func TestFillCandidateNames(t *testing.T) {
	t.Parallel()

	nameless := `[
  {
    "harness": "claude",
    "provider": "anthropic",
    "model": "sonnet",
    "roles": [
      "builder"
    ]
  }
]`
	filled, changed, err := fillCandidateNames([]byte(nameless))
	if err != nil {
		t.Fatalf("fillCandidateNames(nameless): %v", err)
	}
	if !changed {
		t.Error("fillCandidateNames(nameless) changed = false, want true")
	}
	if !strings.Contains(string(filled), `"name": "sonnet"`) {
		t.Errorf("fillCandidateNames(nameless) = %s, want name sonnet", filled)
	}

	named := `[
  {
    "harness": "claude",
    "provider": "anthropic",
    "model": "sonnet",
    "name": "custom",
    "roles": [
      "builder"
    ]
  }
]`
	filled, changed, err = fillCandidateNames([]byte(named))
	if err != nil {
		t.Fatalf("fillCandidateNames(named): %v", err)
	}
	if changed {
		t.Error("fillCandidateNames(named) changed = true, want false")
	}
	if string(filled) != named {
		t.Errorf("fillCandidateNames(named) = %s, want unchanged %s", filled, named)
	}

	badJSON := `[{bad json`
	filled, changed, err = fillCandidateNames([]byte(badJSON))
	if err != nil {
		t.Fatalf("fillCandidateNames(badJSON): %v", err)
	}
	if changed {
		t.Error("fillCandidateNames(badJSON) changed = true, want false")
	}
	if string(filled) != badJSON {
		t.Errorf("fillCandidateNames(badJSON) = %s, want unchanged %s", filled, badJSON)
	}
}

func TestPutCandidatesRecordsOneRevision(t *testing.T) {
	t.Parallel()

	s := openStore(t)

	nameless := `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`
	if _, err := s.As("cli", "config set candidates").Put(Candidates, []byte(nameless)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	revs, err := s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("got %d revisions, want 1", len(revs))
	}
	if revs[0].Source != "cli" {
		t.Errorf("rev source = %q, want cli", revs[0].Source)
	}
	if revs[0].Message != "config set candidates" {
		t.Errorf("rev message = %q, want config set candidates", revs[0].Message)
	}

	var changes []Change
	if err := json.Unmarshal(revs[0].Changes, &changes); err != nil {
		t.Fatalf("decode changes: %v", err)
	}
	found := false
	for _, c := range changes {
		if c.Path == "candidates" && c.Op == "add" {
			afterJSON, err := json.Marshal(c.After)
			if err != nil {
				t.Fatalf("marshal after: %v", err)
			}
			if strings.Contains(string(afterJSON), `"name":"sonnet"`) || strings.Contains(string(afterJSON), `"name": "sonnet"`) {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("revision changes do not include candidates add with names: %s", revs[0].Changes)
	}
}
