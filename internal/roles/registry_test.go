package roles

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

// setFromJSON writes body to a temporary candidates.json and loads it.
func setFromJSON(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load(%s): %v", body, err)
	}
	return set
}

// emptySet is a candidate set with nothing in it: a machine with no
// candidates.json.
func emptySet(t *testing.T) *candidate.Set {
	t.Helper()
	return setFromJSON(t, `[]`)
}

// setFromFile loads a candidates fixture.
func setFromFile(t *testing.T, path string) *candidate.Set {
	t.Helper()
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load(%s): %v", path, err)
	}
	return set
}

// policyFromFile loads a policy fixture.
func policyFromFile(t *testing.T, path string) policy.Policy {
	t.Helper()
	pol, err := policy.Load(path)
	if err != nil {
		t.Fatalf("policy.Load(%s): %v", path, err)
	}
	return pol
}

// TestBuildNoOverridePin pins that neither derivation changes a built-in role
// when nothing overrides it: Spec is byte-for-byte harness.RoleByName, on every
// kind.
func TestBuildNoOverridePin(t *testing.T) {
	tests := []struct {
		name string
		f    *File
	}{
		{"legacy", nil},
		{"empty file", &File{Rows: map[string]Row{}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := Build(tt.f, emptySet(t), policy.Policy{})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			wantSource := SourceLegacy
			if tt.f != nil {
				wantSource = SourceFile
			}
			if reg.Source() != wantSource {
				t.Errorf("Source() = %q, want %q", reg.Source(), wantSource)
			}
			if got := reg.Names(); !reflect.DeepEqual(got, harness.RoleNames()) {
				t.Errorf("Names() = %v, want %v", got, harness.RoleNames())
			}

			for _, name := range harness.RoleNames() {
				want, ok := harness.RoleByName(name)
				if !ok {
					t.Fatalf("harness.RoleByName(%q) not found", name)
				}
				for _, h := range harness.All() {
					got, err := reg.Spec(name, h.Kind)
					if err != nil {
						t.Fatalf("Spec(%q, %q): %v", name, h.Kind, err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("Spec(%q, %q) = %+v, want %+v", name, h.Kind, got, want)
					}
				}
			}
		})
	}
}

// TestBuildNilSet pins #374 §4.1: Build tolerates a nil set, treating it as a
// machine with no candidates.json -- legacy Build succeeds, a built-in role's
// spec is byte-for-byte harness.RoleByName, and no candidate is ranked.
func TestBuildNilSet(t *testing.T) {
	reg, err := Build(nil, nil, policy.Policy{})
	if err != nil {
		t.Fatalf("Build(nil, nil, policy.Policy{}): %v", err)
	}
	if reg.Source() != SourceLegacy {
		t.Errorf("Source() = %q, want %q", reg.Source(), SourceLegacy)
	}

	want, ok := harness.RoleByName("builder")
	if !ok {
		t.Fatal("harness.RoleByName(\"builder\") not found")
	}
	got, err := reg.Spec("builder", "claude")
	if err != nil {
		t.Fatalf("Spec(builder, claude): %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Spec(builder, claude) = %+v, want %+v", got, want)
	}

	builder, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if len(builder.Ranked) != 0 {
		t.Errorf("builder.Ranked = %v, want empty", builder.Ranked)
	}
}

// TestLegacyDerivationMachineConfig pins the legacy derivation against this
// machine's own config: the registry must rank and tier exactly as today's
// rankedList and resolveTier do.
func TestLegacyDerivationMachineConfig(t *testing.T) {
	set := setFromFile(t, "testdata/legacy-candidates.json")
	pol := policyFromFile(t, "testdata/legacy-policy.json")

	reg, err := Build(nil, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if reg.Source() != SourceLegacy {
		t.Errorf("Source() = %q, want %q", reg.Source(), SourceLegacy)
	}

	builderOrder := []string{
		"opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
		"codex/openai/gpt-5.6-terra:high",
		"agy/antigravity/claude-sonnet-4-6",
		"agy/google/gemini-3.8-flash-high",
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/glm-5.3-flash",
	}
	wantBuilderRanked := make([]Ranked, 0, len(builderOrder))
	for i, tok := range builderOrder {
		wantBuilderRanked = append(wantBuilderRanked, Ranked{Token: tok, Position: i + 1})
	}

	builder, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if !reflect.DeepEqual(builder.Candidates, builderOrder) {
		t.Errorf("builder.Candidates = %v, want %v", builder.Candidates, builderOrder)
	}
	if !reflect.DeepEqual(builder.Ranked, wantBuilderRanked) {
		t.Errorf("builder.Ranked = %v, want %v", builder.Ranked, wantBuilderRanked)
	}
	if !builder.Ordered {
		t.Error("builder.Ordered = false, want true")
	}

	reviewer, ok := reg.Role("reviewer")
	if !ok {
		t.Fatal("Role(\"reviewer\") not found")
	}
	wantReviewerRanked := []Ranked{
		{Token: "claude/anthropic/sonnet", Position: 1},
		{Token: "codex/openai/gpt-5.6-terra:high", Position: 2},
	}
	if !reflect.DeepEqual(reviewer.Ranked, wantReviewerRanked) {
		t.Errorf("reviewer.Ranked = %v, want %v", reviewer.Ranked, wantReviewerRanked)
	}

	researcher, ok := reg.Role("researcher")
	if !ok {
		t.Fatal("Role(\"researcher\") not found")
	}
	wantResearcherRanked := []Ranked{{Token: "claude/anthropic/haiku", Position: 0}}
	if !reflect.DeepEqual(researcher.Ranked, wantResearcherRanked) {
		t.Errorf("researcher.Ranked = %v, want %v", researcher.Ranked, wantResearcherRanked)
	}
	if researcher.Ordered {
		t.Error("researcher.Ordered = true, want false")
	}

	sonnet, err := set.Lookup(candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"})
	if err != nil {
		t.Fatalf("set.Lookup(sonnet): %v", err)
	}
	if tier, ok := reg.TierFor("builder", sonnet); !ok || tier != harness.TierYolo {
		t.Errorf("TierFor(builder, sonnet) = (%q, %v), want (yolo, true)", tier, ok)
	}

	haiku, err := set.Lookup(candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "haiku"})
	if err != nil {
		t.Fatalf("set.Lookup(haiku): %v", err)
	}
	if tier, ok := reg.TierFor("researcher", haiku); ok || tier != "" {
		t.Errorf("TierFor(researcher, haiku) = (%q, %v), want (\"\", false)", tier, ok)
	}
}

// TestLegacyRankingEdge pins the two rules a mutation would break: an order
// entry relevo cannot use still consumes its position, and the unlisted
// candidates follow in ref order with position 0.
func TestLegacyRankingEdge(t *testing.T) {
	set := setFromJSON(t, `[
	  {"harness":"claude","provider":"anthropic","model":"a","roles":["builder"]},
	  {"harness":"claude","provider":"anthropic","model":"b","roles":["builder"]},
	  {"harness":"claude","provider":"anthropic","model":"c","roles":["builder"]},
	  {"harness":"claude","provider":"anthropic","model":"d","roles":["reviewer"]}
	]`)
	pol := policy.Policy{Order: map[string][]string{
		"builder": {
			"claude/anthropic/missing", // parses, is not configured: skipped
			"claude/anthropic/b",       // serving: position 2
			"claude/anthropic/d",       // configured, does not serve builder: skipped
		},
	}}

	reg, err := Build(nil, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	want := []Ranked{
		{Token: "claude/anthropic/b", Position: 2},
		{Token: "claude/anthropic/a", Position: 0},
		{Token: "claude/anthropic/c", Position: 0},
	}
	role, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if !reflect.DeepEqual(role.Ranked, want) {
		t.Errorf("Ranked = %v, want %v", role.Ranked, want)
	}
}

// TestLegacyTierChain pins the legacy tier chain: the candidate's own tier
// when it has one, else policy's tier for the role, else nothing.
func TestLegacyTierChain(t *testing.T) {
	set := setFromJSON(t, `[{"harness":"claude","provider":"anthropic","model":"a","roles":["builder"]}]`)
	pol := policy.Policy{Tier: map[string]string{"builder": "edit"}}

	reg, err := Build(nil, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	noTier := candidate.Candidate{Harness: "claude", Provider: "anthropic", Model: "a"}
	if tier, ok := reg.TierFor("builder", noTier); !ok || tier != harness.TierEdit {
		t.Errorf("TierFor(builder, no tier) = (%q, %v), want (edit, true)", tier, ok)
	}

	withTier := noTier
	withTier.Tier = "read"
	if tier, ok := reg.TierFor("builder", withTier); !ok || tier != harness.TierRead {
		t.Errorf("TierFor(builder, tier read) = (%q, %v), want (read, true)", tier, ok)
	}
}

// TestFileMergeDefinitions pins the field-by-field merge: a kind the row
// overrides takes the file's agent, every other kind keeps the shipped one.
func TestFileMergeDefinitions(t *testing.T) {
	f := fileFromJSON(t, `{"builder": {"definitions": {"claude": {"agent": "my-executor", "requires": ["my-scout"]}}}}`)
	reg, err := Build(f, emptySet(t), policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantClaude := harness.RoleSpec{
		Name:        "builder",
		Shape:       harness.ShapeBuilder,
		Definition:  "my-executor",
		Definitions: []string{"my-executor", "my-scout"},
	}
	gotClaude, err := reg.Spec("builder", "claude")
	if err != nil {
		t.Fatalf("Spec(builder, claude): %v", err)
	}
	if !reflect.DeepEqual(gotClaude, wantClaude) {
		t.Errorf("Spec(builder, claude) = %+v, want %+v", gotClaude, wantClaude)
	}

	wantOpencode := harness.RoleSpec{
		Name:        "builder",
		Shape:       harness.ShapeBuilder,
		Definition:  "plan-executor",
		Definitions: []string{"plan-executor", "researcher"},
	}
	gotOpencode, err := reg.Spec("builder", "opencode")
	if err != nil {
		t.Fatalf("Spec(builder, opencode): %v", err)
	}
	if !reflect.DeepEqual(gotOpencode, wantOpencode) {
		t.Errorf("Spec(builder, opencode) = %+v, want %+v", gotOpencode, wantOpencode)
	}

	builder, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if d := builder.Definitions["claude"]; !d.Custom {
		t.Errorf("claude definition Custom = false, want true (my-executor is not shipped)")
	}
	if d := builder.Definitions["opencode"]; d.Custom {
		t.Errorf("opencode definition Custom = true, want false (plan-executor is shipped)")
	}
}

// TestFileShippedNameCollision pins that naming a shipped agent is allowed:
// it is that file, so Custom stays false.
func TestFileShippedNameCollision(t *testing.T) {
	f := fileFromJSON(t, `{"reviewer": {"definitions": {"claude": {"agent": "reviewer"}}}}`)
	reg, err := Build(f, emptySet(t), policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	reviewer, ok := reg.Role("reviewer")
	if !ok {
		t.Fatal("Role(\"reviewer\") not found")
	}
	if d := reviewer.Definitions["claude"]; d.Custom {
		t.Errorf("claude definition Custom = true, want false (reviewer is shipped)")
	}
	if spec, err := reg.Spec("reviewer", "claude"); err != nil || spec.Definition != "reviewer" {
		t.Errorf("Spec(reviewer, claude) = (%+v, %v), want definition reviewer", spec, err)
	}
}

// TestFileCandidates pins file-mode ranking: the row's list is the only
// source, an unconfigured token is skipped, and the position is the token's
// 1-based index in that list.
func TestFileCandidates(t *testing.T) {
	f := fileFromJSON(t, `{"builder": {"candidates": [
	  "codex/openai/gpt-5.6-terra:high",
	  "agy/nope/nope",
	  "claude/anthropic/sonnet"
	]}}`)
	set := setFromFile(t, "testdata/legacy-candidates.json")
	pol := policyFromFile(t, "testdata/legacy-policy.json")

	reg, err := Build(f, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantRanked := []Ranked{
		{Token: "codex/openai/gpt-5.6-terra:high", Position: 1},
		{Token: "claude/anthropic/sonnet", Position: 3},
	}
	builder, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if !reflect.DeepEqual(builder.Ranked, wantRanked) {
		t.Errorf("builder.Ranked = %v, want %v", builder.Ranked, wantRanked)
	}
	if !builder.Ordered {
		t.Error("builder.Ordered = false, want true in file mode")
	}

	if !reg.Serves("builder", candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"}) {
		t.Error("Serves(builder, claude/anthropic/sonnet) = false, want true")
	}
	if reg.Serves("builder", candidate.Ref{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high"}) {
		t.Error("Serves(builder, agy/google/gemini-3.8-flash-high) = true, want false: it is not in the row's list")
	}

	reviewer, ok := reg.Role("reviewer")
	if !ok {
		t.Fatal("Role(\"reviewer\") not found")
	}
	if len(reviewer.Ranked) != 0 {
		t.Errorf("reviewer.Ranked = %v, want empty: the file assigns no candidates to it", reviewer.Ranked)
	}
}

// TestNewReaderRole pins a role the file adds: it is named last, its
// definition exists only for the kinds the file gives it, and a candidate on
// another kind is not served.
func TestNewReaderRole(t *testing.T) {
	f := fileFromJSON(t, `{"security-reviewer": {
	  "shape": "reader",
	  "definitions": {"claude": {"agent": "sec-review"}},
	  "candidates": ["claude/anthropic/sonnet", "codex/openai/gpt-5.6-terra:high"]
	}}`)
	set := setFromFile(t, "testdata/legacy-candidates.json")
	pol := policyFromFile(t, "testdata/legacy-policy.json")

	reg, err := Build(f, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	names := reg.Names()
	if len(names) == 0 || names[len(names)-1] != "security-reviewer" {
		t.Fatalf("Names() = %v, want it to end with security-reviewer", names)
	}

	role, ok := reg.Role("security-reviewer")
	if !ok {
		t.Fatal("Role(\"security-reviewer\") not found")
	}
	if role.Shape != harness.ShapeConsult || role.Builtin {
		t.Errorf("role = %+v, want a non-builtin reader", role)
	}

	if _, err := reg.Spec("security-reviewer", "codex"); err == nil {
		t.Error("Spec(security-reviewer, codex) = nil error, want ErrNoDefinition")
	} else if !errors.Is(err, ErrNoDefinition) {
		t.Errorf("Spec(security-reviewer, codex) err = %v, want ErrNoDefinition", err)
	}
	if reg.Serves("security-reviewer", candidate.Ref{Harness: "codex", Provider: "openai", Model: "gpt-5.6-terra:high"}) {
		t.Error("Serves(security-reviewer, codex ref) = true, want false: it has no codex definition")
	}
	if !reg.Serves("security-reviewer", candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"}) {
		t.Error("Serves(security-reviewer, claude ref) = false, want true")
	}
}

// TestFileTier pins the file tier: it is carried and, above max_tier, refused.
func TestFileTier(t *testing.T) {
	reg, err := Build(fileFromJSON(t, `{"builder": {"tier": "edit"}}`), emptySet(t), policy.Policy{MaxTier: "yolo"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tier, ok := reg.TierFor("builder", candidate.Candidate{}); !ok || tier != harness.TierEdit {
		t.Errorf("TierFor(builder, ...) = (%q, %v), want (edit, true)", tier, ok)
	}

	_, err = Build(fileFromJSON(t, `{"builder": {"tier": "yolo"}}`), emptySet(t), policy.Policy{MaxTier: "edit"})
	if err == nil {
		t.Fatal("Build(tier yolo above max_tier edit) = nil error, want ErrBadRoles")
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("Build err = %v, want ErrBadRoles", err)
	}
	if !strings.Contains(err.Error(), "exceeds max_tier") {
		t.Errorf("Build err = %q, want it to name max_tier", err.Error())
	}
}

// TestBuildNewWriterRow pins #382 plan §4: a new writer row is a writer in the
// built registry, its gate defaults to true (the built-in builder's rule),
// and its definition is what Spec resolves for the kind the row gives.
func TestBuildNewWriterRow(t *testing.T) {
	f := fileFromJSON(t, `{"ui-builder": {
	  "shape": "writer",
	  "definitions": {"claude": {"agent": "my-ui"}},
	  "candidates": ["claude/anthropic/sonnet"]
	}}`)
	set := setFromFile(t, "testdata/legacy-candidates.json")
	pol := policyFromFile(t, "testdata/legacy-policy.json")

	reg, err := Build(f, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	role, ok := reg.Role("ui-builder")
	if !ok {
		t.Fatal("Role(\"ui-builder\") not found")
	}
	if role.Shape != harness.ShapeBuilder {
		t.Errorf("Shape = %q, want %q", role.Shape, harness.ShapeBuilder)
	}
	if !role.Check {
		t.Error("Check = false, want true: a new writer checks by default")
	}
	if role.Builtin {
		t.Error("Builtin = true, want false")
	}

	spec, err := reg.Spec("ui-builder", "claude")
	if err != nil {
		t.Fatalf("Spec(ui-builder, claude): %v", err)
	}
	if spec.Definition != "my-ui" {
		t.Errorf("Definition = %q, want my-ui", spec.Definition)
	}
}

// TestBuildNewWriterGateFalse pins #382 plan §4: "gate": false opts a new
// writer out of the default gate.
func TestBuildNewWriterGateFalse(t *testing.T) {
	f := fileFromJSON(t, `{"ui-builder": {"shape": "writer", "gate": false}}`)
	reg, err := Build(f, emptySet(t), policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	role, ok := reg.Role("ui-builder")
	if !ok {
		t.Fatal("Role(\"ui-builder\") not found")
	}
	if role.Check {
		t.Error("Check = true, want false: the row opted out")
	}
}

// TestValidateNewReaderGateStillRefused pins that #382 §4 lifts the writer
// refusal only: a new reader role still has no gate.
func TestValidateNewReaderGateStillRefused(t *testing.T) {
	_, err := Load(writeRoles(t, `{"security-reviewer": {"shape": "reader", "gate": true}}`))
	if err == nil {
		t.Fatal("Load(reader with gate true) = nil, want an error")
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("err = %v, want ErrBadRoles", err)
	}
	if !strings.Contains(err.Error(), "a reader role has no gate") {
		t.Errorf("err = %q, want the reader-gate text", err.Error())
	}
}

// TestRoleCopies pins that Role hands out a copy: mutating what a caller gets
// cannot change what the registry resolves.
func TestRoleCopies(t *testing.T) {
	f := fileFromJSON(t, `{"builder": {
	  "candidates": ["claude/anthropic/sonnet"],
	  "definitions": {"claude": {"agent": "my-executor", "requires": ["my-scout"]}}
	}}`)
	set := setFromJSON(t, `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]}]`)
	reg, err := Build(f, set, policy.Policy{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	first, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	first.Candidates = append(first.Candidates, "extra/candidate/token")
	first.Ranked = append(first.Ranked, Ranked{Token: "extra/candidate/token", Position: 9})
	first.Definitions["claude"] = Definition{Agent: "mutated"}
	delete(first.Definitions, "opencode")

	second, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if len(second.Candidates) != 1 {
		t.Errorf("second Candidates = %v, want the one token the file lists", second.Candidates)
	}
	if len(second.Ranked) != 1 {
		t.Errorf("second Ranked = %v, want the one ranked token", second.Ranked)
	}
	if d := second.Definitions["claude"]; d.Agent != "my-executor" || !d.Custom {
		t.Errorf("second claude definition = %+v, want my-executor (custom)", d)
	}
	if _, ok := second.Definitions["opencode"]; !ok {
		t.Error("second Definitions lost opencode: the map was shared with a caller")
	}
}

// TestFileCandidatesByName pins A1 §4.2 for roles.json: an entry may be a
// candidate name, buildFile ranks the canonical token, and Serves is true for
// it.
func TestFileCandidatesByName(t *testing.T) {
	f := fileFromJSON(t, `{"builder": {"candidates": ["sonnet", "gpt-5.6-terra"]}}`)
	set := setFromFile(t, "testdata/legacy-candidates.json")
	pol := policyFromFile(t, "testdata/legacy-policy.json")

	reg, err := Build(f, set, pol)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	wantRanked := []Ranked{
		{Token: "claude/anthropic/sonnet", Position: 1},
		{Token: "codex/openai/gpt-5.6-terra:high", Position: 2},
	}
	builder, ok := reg.Role("builder")
	if !ok {
		t.Fatal("Role(\"builder\") not found")
	}
	if !reflect.DeepEqual(builder.Ranked, wantRanked) {
		t.Errorf("builder.Ranked = %v, want %v", builder.Ranked, wantRanked)
	}
	if !reg.Serves("builder", candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "sonnet"}) {
		t.Error("Serves(builder, claude/anthropic/sonnet) = false, want true: the file names it by name")
	}
	if reg.Serves("builder", candidate.Ref{Harness: "claude", Provider: "anthropic", Model: "haiku"}) {
		t.Error("Serves(builder, claude/anthropic/haiku) = true, want false: it is not in the row's list")
	}
}

// TestCandidateEntriesAcceptNames pins A1 §4.2's validation rule for a role's
// candidates: an entry is a candidate name or a harness/provider/model token.
func TestCandidateEntriesAcceptNames(t *testing.T) {
	for _, body := range []string{
		`{"builder": {"candidates": ["sonnet"]}}`,
		`{"builder": {"candidates": ["a"]}}`,
		`{"builder": {"candidates": ["claude/anthropic/sonnet"]}}`,
	} {
		if _, err := Load(writeRoles(t, body)); err != nil {
			t.Errorf("Load(%s) = %v, want no error", body, err)
		}
	}

	_, err := Load(writeRoles(t, `{"builder": {"candidates": ["A/b"]}}`))
	if err == nil {
		t.Fatal("Load accepted A/b, want the bad-roles error")
	}
	want := `builder.candidates[0]: "A/b": want a candidate name or harness/provider/model`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Load(A/b) err = %q, want it containing %q", err.Error(), want)
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("Load(A/b) err = %v, want ErrBadRoles", err)
	}
}
