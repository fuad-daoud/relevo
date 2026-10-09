package relevo

// The view tests for #374 §3.1-§3.3: relevo config's pick and candidates blocks, the
// post-bind consult-name note and the legacy-field warnings read the roles
// registry when a roles.json is loaded, and reproduce today's bytes when it is
// not. The legacy behaviour is pinned by the older tests, which stay unedited.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// rolesViewsCandidatesJSON is claude a on both roles, claude b and opencode m
// on builder. In file mode the rows, not these roles, decide who serves what.
const rolesViewsCandidatesJSON = `[
  {"harness":"claude","provider":"test","model":"a","roles":["builder","reviewer"]},
  {"harness":"claude","provider":"test","model":"b","roles":["builder"]},
  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
]`

// rolesViewsSection returns the part of got between the start line and the
// next line named end, so a test can assert on one role's block alone.
func rolesViewsSection(t *testing.T, got, start, end string) string {
	t.Helper()
	i := strings.Index(got, start)
	if i < 0 {
		t.Fatalf("no %q in:\n%s", start, got)
	}
	rest := got[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("no %q after %q in:\n%s", end, start, got)
	}
	return rest[:j]
}

// TestRolesViewsFormatPolicyForFileMode pins §3.1's FormatPolicyFor: the header
// names roles.json, the rows are the row's candidate list in order with an
// `order` tag and the pick marked, a role with nothing listed says so, and the
// legacy "no policy configured" line is never printed.
//
// Mutation check: make FormatPolicyFor print FormatPolicy's "  (no order set)"
// header instead of "  (config actors)" and this test fails on its first line.
func TestRolesViewsFormatPolicyForFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesViewsCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{"claude/test/b", "claude/test/a"}},
		"reviewer": {},
	})

	got := FormatPolicyFor(reg, nil, set, policy.Policy{}, nil, nil, availability.History{}, baseTime, time.UTC)

	want := "builder  (config actors)\n" +
		"  1  b  order     <- would pick\n" +
		"  2  a  order\n" +
		"librarian  (config actors)\n" +
		"  no candidate listed in config actors librarian.candidates\n" +
		"reviewer  (config actors)\n" +
		"  no candidate listed in config actors reviewer.candidates\n" +
		"researcher  (config actors)\n" +
		"  no candidate listed in config actors researcher.candidates\n"
	if got != want {
		t.Errorf("FormatPolicyFor =\n%q\nwant:\n%q", got, want)
	}

	if !strings.Contains(got, "builder  (config actors)") {
		t.Errorf("output must carry the file-mode header, got:\n%s", got)
	}
	pick := strings.Index(got, "1  b  order     <- would pick")
	second := strings.Index(got, "2  a  order")
	if pick < 0 || second < 0 || pick > second {
		t.Errorf("the row's first candidate must be row 1 with the pick, then the second:\n%s", got)
	}
	if !strings.Contains(got, "no candidate listed in config actors reviewer.candidates") {
		t.Errorf("a role with nothing listed must say so, got:\n%s", got)
	}
	if strings.Contains(got, "no policy configured") {
		t.Errorf("file mode must not print the legacy no-policy line:\n%s", got)
	}
}

// TestRolesViewsFormatPolicyForLegacyMatches pins §3.1's legacy half: with a
// legacy registry the file-mode entry point is FormatPolicy byte for byte.
func TestRolesViewsFormatPolicyForLegacyMatches(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	pol := orderOf("builder", testClaudeRef, testAgyRef)
	gates := []availability.Gate{{Token: testClaudeRef, Kind: availability.RateLimited, Until: baseTime.Add(time.Hour)}}

	legacy, _ := roles.Build(nil, set, pol)
	want := FormatPolicy(set, pol, gates, availability.History{}, baseTime, time.UTC)
	got := FormatPolicyFor(legacy, nil, set, pol, gates, nil, availability.History{}, baseTime, time.UTC)
	if got != want {
		t.Errorf("FormatPolicyFor(legacy) =\n%q\nwant FormatPolicy's:\n%q", got, want)
	}
}

// TestRolesViewsPerRoleGateFiltering pins §3.1's per-role gate filtering in the
// legacy renderer: a roles-missing gate scoped to reviewer never shows on a
// builder row, and does show on reviewer's.
//
// Mutation check: render the rows from every gate instead of gatesForRole and
// the builder half fails.
func TestRolesViewsPerRoleGateFiltering(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	gates := []availability.Gate{{
		Token: testClaudeRef, Kind: availability.RolesMissing, Role: "reviewer",
		Note: "agents missing for reviewer",
	}}

	got := FormatPolicy(set, policy.Policy{}, gates, availability.History{}, baseTime, time.UTC)

	builder := rolesViewsSection(t, got, "builder  (no order set)", "reviewer  (no order set)")
	if strings.Contains(builder, "agents missing") {
		t.Errorf("a reviewer-scoped gate must not show on builder rows:\n%s", builder)
	}
	reviewer := rolesViewsSection(t, got, "reviewer  (no order set)", "researcher  (no order set)")
	if !strings.Contains(reviewer, "agents missing") {
		t.Errorf("a reviewer-scoped gate must show on reviewer rows:\n%s", reviewer)
	}
}

// TestRolesViewsPolicyWarningsForFileMode pins §3.1's PolicyWarningsFor: a
// listed token that is not configured, and a listed token whose kind has no
// definition for the role, each give their exact text -- and nothing else,
// since in file mode the list is the assignment.
func TestRolesViewsPolicyWarningsForFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"]}
	]`)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"scout": {Shape: ptr("reader"), Candidates: []string{"claude/test/m", "claude/test/ghost"}},
	})

	got := PolicyWarningsFor(reg, set, policy.Policy{})
	want := []PolicyWarning{
		{
			Role: "scout", Index: 0, Token: "claude/test/m",
			Text: `config actors scout.candidates[0] "m": scout has no definition for claude`,
		},
		{
			Role: "scout", Index: 1, Token: "claude/test/ghost",
			Text: `config actors scout.candidates[1] "claude/test/ghost" is not a configured candidate`,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PolicyWarningsFor = %+v, want %+v", got, want)
	}
}

// TestRolesViewsRoleRefusalsForFileMode pins §3.1's RoleRefusalsFor: with every
// listed candidate gated for builder only, builder has a refusal and reviewer
// -- same candidates, same gates -- has none.
func TestRolesViewsRoleRefusalsForFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef, testOpencodeRef}},
		"reviewer": {Candidates: []string{testClaudeRef, testOpencodeRef}},
	})
	gates := []availability.Gate{
		{Token: testClaudeRef, Kind: availability.RateLimited, Role: "builder", Until: baseTime.Add(time.Hour)},
		{Token: testOpencodeRef, Kind: availability.RateLimited, Role: "builder", Until: baseTime.Add(time.Hour)},
	}

	got := RoleRefusalsFor(reg, set, policy.Policy{}, gates)
	if len(got) != 1 {
		t.Fatalf("RoleRefusalsFor = %+v, want exactly the builder refusal", got)
	}
	if got[0].Role != "builder" {
		t.Errorf("refusal role = %q, want builder (the gate is builder-scoped)", got[0].Role)
	}
	if !strings.Contains(got[0].Text, "every candidate serving builder is gated") {
		t.Errorf("refusal text = %q, want the all-gated wording", got[0].Text)
	}
}

// TestRolesViewsFormatCandidatesLatencyForFileMode pins §3.2: the roles column
// is the registry's answer, "(no role)" when no role lists the candidate, and
// the tier segment is gone -- the role owns the tier in file mode.
func TestRolesViewsFormatCandidatesLatencyForFileMode(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"},
	  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]}
	]`)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder":  {Candidates: []string{testClaudeRef}},
		"reviewer": {Candidates: []string{testClaudeRef}},
	})

	got := view.FormatCandidatesLatencyFor(reg, set, nil, nil)
	// claude's entry takes "m" first, so opencode's becomes "opencode-m".
	want := "m" + strings.Repeat(" ", 11) + "claude/test/m  " + "  builder, reviewer\n" +
		"opencode-m" + strings.Repeat(" ", 2) + "opencode/test/m" + "  (no actor)\n"
	if got != want {
		t.Errorf("view.FormatCandidatesLatencyFor =\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "tier:") {
		t.Errorf("file mode must not print the candidate's tier:\n%s", got)
	}

	legacy, _ := roles.Build(nil, set, policy.Policy{})
	if a, b := view.FormatCandidatesLatencyFor(legacy, set, nil, nil), view.FormatCandidatesLatency(set, nil, nil); a != b {
		t.Errorf("view.FormatCandidatesLatencyFor(legacy) = %q, want view.FormatCandidatesLatency's %q", a, b)
	}
}

// TestRolesViewsLegacyRoleFieldWarnings pins §3.1's LegacyRoleFieldWarnings:
// file mode names each legacy field still set, in order, and legacy mode says
// nothing -- without roles.json those fields are the source, not stale copies.
func TestRolesViewsLegacyRoleFieldWarnings(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, `[
	  {"harness":"claude","provider":"test","model":"m","roles":["builder"],"tier":"yolo"}
	]`)
	pol := policy.Policy{
		Order: map[string][]string{"builder": {testClaudeRef}},
		Tier:  map[string]string{"builder": "edit"},
	}
	reg := rolesFileRegistry(t, set, pol, map[string]roles.Row{
		"builder": {Candidates: []string{testClaudeRef}},
	})

	got := LegacyRoleFieldWarnings(reg, set, pol)
	want := []string{
		"candidates: claude/test/m: roles is ignored; config roles assigns candidates to roles",
		"candidates: claude/test/m: tier is ignored; set the role's tier in config roles",
		"policy: order is ignored; config roles <role>.candidates orders them",
		"policy: tier is ignored; set the role's tier in config roles",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LegacyRoleFieldWarnings =\n%q\nwant:\n%q", got, want)
	}

	legacy, _ := roles.Build(nil, set, pol)
	if got := LegacyRoleFieldWarnings(legacy, set, pol); got != nil {
		t.Errorf("LegacyRoleFieldWarnings(legacy) = %q, want nil", got)
	}
}

// TestRolesViewsMergedGateTexts pins §3.3: a candidate gated for two roles
// prints one merged roles-missing part naming both, in sorted order, beside the
// role-free gate rendered as it always was.
//
// Mutation check: render one part per gate instead of grouping by kind and
// until, and the merged roles-missing part is gone.
func TestRolesViewsMergedGateTexts(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	until := baseTime.Add(time.Hour)
	gates := []availability.Gate{
		{Token: testClaudeRef, Kind: availability.RolesMissing, Role: "reviewer"},
		{Token: testClaudeRef, Kind: availability.RolesMissing, Role: "builder"},
		{Token: testClaudeRef, Kind: availability.RateLimited, Until: until},
	}

	got := view.FormatCandidates(set, gates)

	want := "   unavailable: agents missing (builder, reviewer) until cleared; " +
		availability.GateKindText(availability.RateLimited) + " " + availability.GateUntilText(until)
	if !strings.Contains(got, want) {
		t.Errorf("view.FormatCandidates =\n%q\nwant it to contain:\n%q", got, want)
	}
	if n := strings.Count(got, "agents missing"); n != 1 {
		t.Errorf("agents missing appears %d times, want the merged part once:\n%s", n, got)
	}
}

// TestRolesViewsResolveRoleFileModeNothingServes pins §3.4: in file mode a role
// whose row lists nothing gives the roles.json wording, and the error still
// matches ErrRoleNotServed.
func TestRolesViewsResolveRoleFileModeNothingServes(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {Candidates: []string{}},
	})

	_, err := resolveRole(reg, set, nil, "", "builder")
	if err == nil {
		t.Fatal("resolveRole with an empty config actors builder.candidates = nil, want an error")
	}
	if !strings.Contains(err.Error(), "config actors builder.candidates") {
		t.Errorf("err = %q, want it to name config actors builder.candidates", err)
	}
	if !errors.Is(err, ErrRoleNotServed) {
		t.Errorf("err = %q, want errors.Is(err, ErrRoleNotServed)", err)
	}
}

// statusRowForTest saves b and reads it back through statusRow, the function
// `relevo status` builds its rows with.
func statusRowForTest(t *testing.T, rt Runtime, b store.Binding) view.BindingStatus {
	t.Helper()
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("save binding: %v", err)
	}
	row, err := statusRow(context.Background(), rt, b, statusConfig{detail: true})
	if err != nil {
		t.Fatalf("statusRow: %v", err)
	}
	return row
}

// customBuilderBinding is an ACTIVE binding whose builder kind is claude.
func customBuilderBinding(name string) store.Binding {
	return store.Binding{
		Name:       name,
		CWD:        "/repo",
		MasterMind: store.Endpoint{PaneID: "w2:p3"},
		Builder:    store.Endpoint{Kind: "claude", AgentName: name},
		Round:      1,
		State:      store.StateActive,
	}
}

// TestRolesViewsStatusNamesCustomBuilderDefinition pins #374 §2.2: a binding
// whose kind has a custom definition carries it on the row and in its JSON.
func TestRolesViewsStatusNamesCustomBuilderDefinition(t *testing.T) {
	rt := newRuntime(t)
	rt.Registry = rolesFileRegistry(t, rt.Candidates, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Candidates:  []string{testClaudeRef},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-executor"}},
		},
	})

	row := statusRowForTest(t, rt, customBuilderBinding("custom-builder"))

	if row.BuilderDefinition != "my-executor" || !row.BuilderDefinitionCustom {
		t.Errorf("BuilderDefinition/Custom = %q/%v, want my-executor/true",
			row.BuilderDefinition, row.BuilderDefinitionCustom)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	if !strings.Contains(string(raw), `"agent_definition":"my-executor"`) {
		t.Errorf("JSON = %s, want it to contain %q", raw, `"agent_definition":"my-executor"`)
	}
}

// TestRolesViewsStatusOmitsShippedBuilderDefinition pins #374 §2.2's other
// half: a shipped definition -- here the legacy registry's -- leaves the
// fields out, so today's JSON is unchanged.
func TestRolesViewsStatusOmitsShippedBuilderDefinition(t *testing.T) {
	rt := newRuntime(t) // no roles.json: the legacy derivation

	row := statusRowForTest(t, rt, customBuilderBinding("shipped-builder"))

	if row.BuilderDefinition != "" || row.BuilderDefinitionCustom {
		t.Errorf("BuilderDefinition/Custom = %q/%v, want empty/false",
			row.BuilderDefinition, row.BuilderDefinitionCustom)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	if strings.Contains(string(raw), "agent_definition") {
		t.Errorf("JSON = %s, want no agent_definition key", raw)
	}
}
