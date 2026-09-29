package roles

// The tests for #374 §3.1: FromLegacy translates candidates.json and
// policy.json into a roles.json, File.Encode writes it, and Registry.RoleTier
// reads a role's own tier without a candidate.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
)

// strPtr is a *string for the Row fields these tests build in memory.
func strPtr(s string) *string { return &s }

// legacyFixtures loads the round-1 fixtures: the candidates.json and
// policy.json relevo translates.
func legacyFixtures(t *testing.T) (*candidate.Set, policy.Policy) {
	t.Helper()
	return setFromFile(t, filepath.Join("testdata", "legacy-candidates.json")),
		policyFromFile(t, filepath.Join("testdata", "legacy-policy.json"))
}

// rankedTokens returns a role's ranked candidate tokens, in order.
func rankedTokens(role Role) []string {
	tokens := make([]string, 0, len(role.Ranked))
	for _, r := range role.Ranked {
		tokens = append(tokens, r.Token)
	}
	return tokens
}

// TestFromLegacyFixtures pins §3.1 over the round-1 fixtures: the rows hold the
// ranked tokens in order, the tier chain resolves to yolo for builder (policy)
// and reviewer (candidates), researcher has none, and there are no notes.
func TestFromLegacyFixtures(t *testing.T) {
	set, pol := legacyFixtures(t)

	f, notes, err := FromLegacy(set, pol)
	if err != nil {
		t.Fatalf("FromLegacy: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %q, want none", notes)
	}

	wantCandidates := map[string][]string{
		"builder": {
			"opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
			"codex/openai/gpt-5.6-terra:high",
			"agy/antigravity/claude-sonnet-4-6",
			"agy/google/gemini-3.8-flash-high",
			"claude/anthropic/sonnet",
			"opencode/openrouter/z-ai/glm-5.3-flash",
		},
		"reviewer":   {"claude/anthropic/sonnet", "codex/openai/gpt-5.6-terra:high"},
		"researcher": {"claude/anthropic/haiku"},
	}
	wantTier := map[string]*string{
		"builder":    strPtr("yolo"),
		"reviewer":   strPtr("yolo"),
		"researcher": nil,
	}

	if len(f.Rows) != len(harness.RoleNames()) {
		t.Errorf("Rows names %d roles, want the %d built-ins", len(f.Rows), len(harness.RoleNames()))
	}
	for name, want := range wantCandidates {
		row, ok := f.Rows[name]
		if !ok {
			t.Fatalf("no row for %s", name)
		}
		if !reflect.DeepEqual(row.Candidates, want) {
			t.Errorf("%s candidates = %v, want %v", name, row.Candidates, want)
		}
		if !reflect.DeepEqual(row.Tier, wantTier[name]) {
			t.Errorf("%s tier = %v, want %v", name, derefTier(row.Tier), derefTier(wantTier[name]))
		}
		if row.Shape != nil || row.Check != nil || row.Definitions != nil {
			t.Errorf("%s row sets shape/check/definitions; legacy has none of them: %+v", name, row)
		}
	}
}

// derefTier renders a *string tier for an error message.
func derefTier(t *string) string {
	if t == nil {
		return "nil"
	}
	return *t
}

// TestFromLegacyRankedPostcondition pins §3.1's postcondition: for every role
// the legacy registry orders, the file FromLegacy produces ranks exactly the
// same tokens.
//
// Mutation check: append an unlisted candidate to a row instead of the ranked
// list and this test fails for that role.
func TestFromLegacyRankedPostcondition(t *testing.T) {
	set, pol := legacyFixtures(t)

	legacy, err := Build(nil, set, pol)
	if err != nil {
		t.Fatalf("Build(legacy): %v", err)
	}
	f, _, err := FromLegacy(set, pol)
	if err != nil {
		t.Fatalf("FromLegacy: %v", err)
	}
	file, err := Build(f, set, pol)
	if err != nil {
		t.Fatalf("Build(file): %v", err)
	}

	checked := 0
	for _, name := range harness.RoleNames() {
		before, _ := legacy.Role(name)
		if !before.Ordered {
			continue
		}
		after, _ := file.Role(name)
		checked++
		if !reflect.DeepEqual(rankedTokens(after), rankedTokens(before)) {
			t.Errorf("%s file-mode Ranked = %v, want the legacy %v", name, rankedTokens(after), rankedTokens(before))
		}
	}
	if checked == 0 {
		t.Fatal("no ordered role in the legacy registry; the postcondition checked nothing")
	}
}

// TestFromLegacyAmbiguousTier pins §3.1 case 4: candidates of one role carrying
// different tiers, with no policy tier, is ErrAmbiguousTier naming both tokens.
//
// Mutation check: take the first candidate's tier instead of refusing, and this
// test fails on the missing error.
func TestFromLegacyAmbiguousTier(t *testing.T) {
	set := setFromJSON(t, `[
	  {"harness":"claude","provider":"test","model":"a","roles":["builder"],"tier":"yolo"},
	  {"harness":"claude","provider":"test","model":"b","roles":["builder"],"tier":"edit"}
	]`)

	_, _, err := FromLegacy(set, policy.Policy{})
	if !errors.Is(err, ErrAmbiguousTier) {
		t.Fatalf("FromLegacy err = %v, want ErrAmbiguousTier", err)
	}
	for _, tok := range []string{"claude/test/a", "claude/test/b"} {
		if !strings.Contains(err.Error(), tok) {
			t.Errorf("message %q must name %s", err, tok)
		}
	}
}

// TestFromLegacyNotes pins §3.1's notes: an unordered role with several
// candidates says relevo refused to choose, and an ordered role with an unlisted
// server says it was appended.
func TestFromLegacyNotes(t *testing.T) {
	t.Run("no order", func(t *testing.T) {
		set := setFromJSON(t, `[
		  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
		  {"harness":"claude","provider":"test","model":"b","roles":["builder"]}
		]`)

		_, notes, err := FromLegacy(set, policy.Policy{})
		if err != nil {
			t.Fatalf("FromLegacy: %v", err)
		}
		if want := "had no order"; !hasNote(notes, want) {
			t.Errorf("notes = %q, want one containing %q", notes, want)
		}
	})

	t.Run("unlisted server", func(t *testing.T) {
		set := setFromJSON(t, `[
		  {"harness":"claude","provider":"test","model":"a","roles":["builder"]},
		  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
		  {"harness":"claude","provider":"test","model":"c","roles":["builder"]}
		]`)
		pol := policy.Policy{Order: map[string][]string{"builder": {"claude/test/a"}}}

		_, notes, err := FromLegacy(set, pol)
		if err != nil {
			t.Fatalf("FromLegacy: %v", err)
		}
		if want := "appended after it"; !hasNote(notes, want) {
			t.Errorf("notes = %q, want one containing %q", notes, want)
		}
	})
}

// hasNote reports whether notes holds a line containing want.
func hasNote(notes []string, want string) bool {
	for _, n := range notes {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

// TestFileEncodeRoundTrip pins §3.1's Encode contract: two-space JSON with a
// trailing newline, an empty candidates list written as [], and a round trip
// through LoadWithWarnings that warns about nothing and yields equal rows.
func TestFileEncodeRoundTrip(t *testing.T) {
	f := &File{Rows: map[string]Row{
		"builder": {
			Shape: strPtr("writer"),
			Check: boolPtr(true),
			Definitions: map[string]DefRow{
				"claude": {Agent: "my-executor", Requires: []string{"my-scout"}},
			},
			Candidates: []string{"claude/test/a"},
			Tier:       strPtr("yolo"),
		},
		"scout": {
			Shape:      strPtr("reader"),
			Candidates: []string{},
		},
	}}

	data, err := f.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("Encode output must end in a newline:\n%s", data)
	}
	if !strings.Contains(string(data), "\n  \"scout\": {") {
		t.Errorf("Encode output must be two-space indented, keyed by role:\n%s", data)
	}
	if !strings.Contains(string(data), `"candidates": []`) {
		t.Errorf("an empty candidates list must be written as [], got:\n%s", data)
	}

	path := filepath.Join(t.TempDir(), "roles.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, warnings, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("LoadWithWarnings(%s): %v", data, err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
	if !reflect.DeepEqual(got, f) {
		t.Errorf("round trip = %+v, want %+v", got, f)
	}
}

// boolPtr is a *bool for the Row fields these tests build in memory.
func boolPtr(b bool) *bool { return &b }

// TestRegistryRoleTier pins §3.2's RoleTier: the file tier in file mode,
// policy's tier for the role in legacy mode, and false when neither sets one.
func TestRegistryRoleTier(t *testing.T) {
	set, pol := legacyFixtures(t)

	legacy, err := Build(nil, set, pol)
	if err != nil {
		t.Fatalf("Build(legacy): %v", err)
	}
	if got, ok := legacy.RoleTier("builder"); !ok || got != harness.TierYolo {
		t.Errorf("legacy RoleTier(builder) = %q, %v; want yolo, true", got, ok)
	}
	if got, ok := legacy.RoleTier("researcher"); ok {
		t.Errorf("legacy RoleTier(researcher) = %q, %v; want false", got, ok)
	}

	file, err := Build(&File{Rows: map[string]Row{"reviewer": {Tier: strPtr("read")}}}, set, pol)
	if err != nil {
		t.Fatalf("Build(file): %v", err)
	}
	if got, ok := file.RoleTier("reviewer"); !ok || got != harness.TierRead {
		t.Errorf("file RoleTier(reviewer) = %q, %v; want read, true", got, ok)
	}
	if got, ok := file.RoleTier("builder"); ok {
		t.Errorf("file RoleTier(builder) = %q, %v; want false (its row sets no tier)", got, ok)
	}
}
