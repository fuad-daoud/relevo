package harness

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	researcherClaudePath = ".claude/agents/researcher.md"
	researcherClaudeFull = "/home/u/.claude/agents/researcher.md"
)

// TestInstallAccountHomeWritesTheAccountSpelling pins the account-home install:
// a claude account's definitions land under its own home at the
// account-relative spelling (agents/<name>.md), the second run keeps them, and
// a kind with no per-process home (opencode) produces no rows.
func TestInstallAccountHomeWritesTheAccountSpelling(t *testing.T) {
	env := freshEnv()
	home := "/homes/work"

	results, err := InstallAccountHome(env, InstallOptions{Kind: "claude"}, "claude", home)
	if err != nil {
		t.Fatalf("InstallAccountHome: %v", err)
	}
	h, _ := Lookup("claude")
	if len(results) != len(h.Roles) {
		t.Fatalf("results = %d, want %d", len(results), len(h.Roles))
	}
	for _, r := range results {
		want := filepath.Join(home, "agents", r.Role+".md")
		if r.Outcome != OutcomeWrote || r.Path != want {
			t.Errorf("result %+v, want wrote at %q", r, want)
		}
		doc, err := AgentDoc(r.Role, "claude")
		if err != nil {
			t.Fatalf("AgentDoc(%s): %v", r.Role, err)
		}
		if !bytes.Equal(env.files[want], doc) {
			t.Errorf("file %s is not the shipped %s definition", want, r.Role)
		}
	}
	if !contains(env.dirs, filepath.Join(home, "agents")) {
		t.Errorf("dirs = %v, want the account agents directory", env.dirs)
	}

	again, err := InstallAccountHome(env, InstallOptions{Kind: "claude"}, "claude", home)
	if err != nil {
		t.Fatalf("InstallAccountHome (second run): %v", err)
	}
	for _, r := range again {
		if r.Outcome != OutcomeKeptIdentical {
			t.Errorf("second run %+v, want kept (identical)", r)
		}
	}

	oc, err := InstallAccountHome(env, InstallOptions{Kind: "opencode"}, "opencode", "/homes/oc")
	if err != nil || oc != nil {
		t.Errorf("InstallAccountHome(opencode) = %v, %v; want no rows and no error", oc, err)
	}
}

func TestInstallFreshHomeWritesEveryRoleOfPathKinds(t *testing.T) {
	env := freshEnv()
	env.lookPaths["agy"] = "/bin/agy"
	env.lookPaths["claude"] = "/bin/claude"

	results, err := Install(env, InstallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hAgy, _ := Lookup("agy")
	hClaude, _ := Lookup("claude")
	if want := len(hAgy.Roles) + len(hClaude.Roles); len(results) != want {
		t.Fatalf("expected %d results, got %d", want, len(results))
	}

	idx := checkWroteKind(t, env, results, 0, "agy", hAgy.Roles)
	checkWroteKind(t, env, results, idx, "claude", hClaude.Roles)

	for _, res := range results {
		if res.Kind == "opencode" {
			t.Errorf("expected no opencode results, found: %+v", res)
		}
	}
	for _, d := range []string{"/home/u/.gemini/config/agents", "/home/u/.claude/agents"} {
		if !contains(env.dirs, d) {
			t.Errorf("expected dirs to contain %s, got %v", d, env.dirs)
		}
	}
}

func checkWroteKind(t *testing.T, env *fakeInstallEnv, results []InstallResult, idx int, kind string, roles []Role) int {
	t.Helper()
	for _, r := range roles {
		res := results[idx]
		if res.Kind != kind || res.Role != r.Name {
			t.Errorf("result %d: want %s/%s, got %s/%s", idx, kind, r.Name, res.Kind, res.Role)
		}
		if res.Outcome != OutcomeWrote {
			t.Errorf("result %d: want OutcomeWrote, got %v", idx, res.Outcome)
		}
		expectedDoc, err := AgentDoc(r.Name, kind)
		if err != nil {
			t.Fatalf("unexpected AgentDoc error: %v", err)
		}
		fullPath := "/home/u/" + res.Path
		if !bytes.Equal(env.files[fullPath], expectedDoc) {
			t.Errorf("result %d (%s): file content mismatch with AgentDoc", idx, fullPath)
		}
		idx++
	}
	return idx
}

func TestInstallDryRunTouchesNothing(t *testing.T) {
	claudePE, _ := AgentDoc("plan-executor", "claude")
	writeBoth := func(env *fakeInstallEnv) {
		env.files["/home/u/.claude/agents/plan-executor.md"] = claudePE
		env.files["/home/u/.claude/agents/researcher.md"] = []byte("---\nmodel: haiku\n---\nmine\n")
	}

	env := freshEnv()
	writeBoth(env)
	results, err := Install(env, InstallOptions{Kind: "claude", DryRun: true, Force: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	expectedOutcomes := []InstallOutcome{
		OutcomeKeptIdentical,
		OutcomeWouldOverwrite,
		OutcomeWouldWrite,
		OutcomeWouldWrite,
		OutcomeWouldWrite,
	}
	for i, want := range expectedOutcomes {
		if results[i].Outcome != want {
			t.Errorf("result %d (%s): want outcome %v, got %v", i, results[i].Role, want, results[i].Outcome)
		}
	}
	if len(env.dirs) != 0 {
		t.Errorf("expected len(dirs) == 0, got %d", len(env.dirs))
	}
	if len(env.writes) != 0 {
		t.Errorf("expected len(writes) == 0, got %d", len(env.writes))
	}

	env2 := freshEnv()
	writeBoth(env2)
	results2, err := Install(env2, InstallOptions{Kind: "claude", DryRun: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results2) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results2))
	}
	if results2[1].Outcome != OutcomeKeptDiffers {
		t.Errorf("expected researcher OutcomeKeptDiffers without Force, got %v", results2[1].Outcome)
	}
}

func TestInstallNamedKindIgnoresPath(t *testing.T) {
	env := freshEnv() // empty lookPaths
	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for i, res := range results {
		if res.Outcome != OutcomeWrote {
			t.Errorf("result %d: want OutcomeWrote, got %v", i, res.Outcome)
		}
	}
}

func TestInstallNoKindNoBinariesIsEmpty(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestInstallUnknownKind(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "nope"})
	if !errors.Is(err, ErrUnknownKind) {
		t.Fatalf("expected ErrUnknownKind, got %v", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Errorf("expected error to contain 'nope', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestInstallUnknownRole(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "claude", Role: "nope"})
	if !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("expected ErrUnknownRole, got %v", err)
	}
	if !strings.Contains(err.Error(), "nope") || !strings.Contains(err.Error(), "plan-executor") {
		t.Errorf("expected error to contain 'nope' and 'plan-executor', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}

	env2 := freshEnv()
	results2, err2 := Install(env2, InstallOptions{Role: "nope"})
	if !errors.Is(err2, ErrUnknownRole) {
		t.Fatalf("expected ErrUnknownRole, got %v", err2)
	}
	if results2 != nil {
		t.Errorf("expected nil results, got %v", results2)
	}
}

func TestInstallWriteFailureIsPerFile(t *testing.T) {
	env := freshEnv()
	env.writeErrFor["/home/u/.gemini/config/agents/researcher.md"] = errors.New("read-only")

	results, err := Install(env, InstallOptions{Kind: "agy"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results))
	}
	for _, res := range results {
		if res.Role == "researcher" {
			if res.Outcome != OutcomeError {
				t.Errorf("researcher: want OutcomeError, got %v", res.Outcome)
			}
			if res.Err != "read-only" {
				t.Errorf("researcher: want Err 'read-only', got %q", res.Err)
			}
		} else if res.Outcome != OutcomeWrote {
			t.Errorf("%s: want OutcomeWrote, got %v", res.Role, res.Outcome)
		}
	}
}

func TestInstallHomeErrorAborts(t *testing.T) {
	env := freshEnv()
	env.homeErr = errors.New("no home")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "no home") {
		t.Errorf("expected error to contain 'no home', got %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %v", results)
	}
}

func TestInstallUnreadableCountsAsDiffers(t *testing.T) {
	env := freshEnv()
	env.readErr = errors.New("permission denied")

	results, err := Install(env, InstallOptions{Kind: "claude", Role: "reviewer"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Outcome != OutcomeKeptDiffers {
		t.Errorf("expected OutcomeKeptDiffers, got %v", results[0].Outcome)
	}

	env2 := freshEnv()
	env2.readErr = errors.New("permission denied")
	results2, err2 := Install(env2, InstallOptions{Kind: "claude", Role: "reviewer", Force: true})
	if err2 != nil {
		t.Fatalf("unexpected error: %v", err2)
	}
	if len(results2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results2))
	}
	if results2[0].Outcome != OutcomeOverwrote {
		t.Errorf("expected OutcomeOverwrote, got %v", results2[0].Outcome)
	}
}

// olderArchitectDoc is an older shipped architect.claude.md whose sha256 is
// recorded in agents/shipped.sha256, so an install that predates the role
// manifest recognises it as relevo's own older release.
const olderArchitectDoc = `---
name: architect
description: >-
  Use this agent when you need to design the architecture for a new feature or
  system, including interface definitions, data structures, component contracts,
  file paths, and high-level pseudocode. This agent should be invoked before any
  implementation work begins. It is the planner half of a relay handoff: it
  produces the ordered implementation plan a builder executes, and never writes
  implementation code itself.
---

You are a seasoned Software Architect with 20 years of experience designing large-scale distributed systems. Your expertise spans domain-driven design, microservices architecture, interface design, and system decomposition. You think in terms of contracts, boundaries, and abstractions. You are meticulous about separation of concerns and believe that well-designed interfaces are the foundation of maintainable systems.

## Core Mission

Design the complete architecture for features or systems by producing interface definitions, data structures, component contracts, file paths, and high-level logic in pseudocode. You define WHAT gets built and HOW components connect — never HOW they are implemented internally.

## Strict Boundaries

**You WILL:**
- Define interfaces, type signatures, and method contracts
- Design data structures with field-level specifications
- Specify file paths and module organization
- Write high-level pseudocode describing component interactions and logic flow
- Define error types and error handling contracts
- Specify dependency injection requirements and constructor signatures
- Define event/message schemas if applicable
- Break the plan into strictly ordered, actionable implementation steps

**You WILL NOT:**
- Write actual implementation code in any programming language
- Include function bodies, algorithms, or concrete logic
- Make technology-specific implementation choices (e.g., specific libraries)
- Write database queries, ORM configurations, or SQL
- Implement business logic beyond pseudocode flow descriptions
- Write tests or test cases

## Output Structure

Every architectural plan must follow this exact structure:

### 1. System Overview
A concise paragraph describing the feature/system, its purpose, and how it fits into the broader application context.

### 2. File Structure
A tree representation of all new files and directories to be created, with brief annotations explaining each file's responsibility.

### 3. Data Structures & Type Definitions
For each data structure, provide:
- The type/interface name
- Every field with its type and a description of its purpose
- Validation constraints (required/optional, min/max, format requirements)
- Relationships to other data structures

### 4. Interface Definitions & Component Contracts
For each interface/contract, provide:
- The interface name and its single responsibility
- Every method signature including parameter types and return types
- Error types that each method may produce
- Preconditions and postconditions for each method
- Dependencies required by the implementing component

### 5. High-Level Pseudocode
Describe the logical flow of the system using structured pseudocode. Focus on:
- Orchestration between components
- Decision points and branching logic
- Data transformation pipelines
- Error handling flows
- State transitions

### 6. Error Handling Strategy
Define:
- Error categories and their hierarchy
- Which errors are recoverable vs. non-recoverable
- Error propagation contracts between layers
- Logging and observability requirements

### 7. Ordered Implementation Steps
Break the plan into strictly ordered, actionable steps. Each step must:
- Have a clear, single deliverable
- List dependencies on previous steps
- Reference the specific interfaces/data structures being implemented
- Be small enough to be completed in a single focused session
- Include verification criteria (how to know the step is done correctly)

## Quality Standards

- Every public interface must have a clearly stated single responsibility
- Data structures must be complete — no placeholder fields or TODO types
- All error states must be explicitly modeled
- File paths must follow the project's existing conventions (check CLAUDE.md or project structure for guidance)
- Pseudocode must be detailed enough that a developer could implement from it without ambiguity
- Steps must be ordered such that dependencies are always built before dependents

## Self-Verification Checklist

Before finalizing any architectural plan, verify:
1. Are all interfaces defined with complete method signatures?
2. Are all data structures fully specified with types and constraints?
3. Does the file structure cover every artifact mentioned?
4. Are error states explicitly enumerated?
5. Is the implementation order correct (dependencies first)?
6. Have I avoided writing any implementation code?
7. Could a developer unfamiliar with the system implement this plan?

If any answer is "no," revise before presenting the plan.
`

func TestInstallRecognizesOlderShippedBlob(t *testing.T) {
	const full = "/home/u/.claude/agents/architect.md"
	if !ShippedBefore("architect.claude.md", docSHA([]byte(olderArchitectDoc))) {
		t.Fatal("the fixture sha is not in agents/shipped.sha256; regenerate the fixture")
	}
	shipped, err := AgentDoc("architect", "claude")
	if err != nil {
		t.Fatalf("AgentDoc: %v", err)
	}

	env := freshEnv()
	env.files[full] = []byte(olderArchitectDoc)
	results, err := Install(env, InstallOptions{Kind: "claude", Role: "architect"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeUpdated {
		t.Fatalf("results = %+v, want one OutcomeUpdated", results)
	}
	if !bytes.Equal(env.files[full], shipped) {
		t.Error("the older shipped copy was not refreshed with the shipped definition")
	}
	if got := env.manifest[".claude/agents/architect.md"]; got != docSHA(shipped) {
		t.Errorf("manifest sha = %q, want the shipped sha", got)
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}

	dry := freshEnv()
	dry.files[full] = []byte(olderArchitectDoc)
	dryResults, err := Install(dry, InstallOptions{Kind: "claude", Role: "architect", DryRun: true})
	if err != nil {
		t.Fatalf("Install(DryRun): %v", err)
	}
	if len(dryResults) != 1 || dryResults[0].Outcome != OutcomeWouldUpdate {
		t.Fatalf("DryRun results = %+v, want one OutcomeWouldUpdate", dryResults)
	}
	if len(dry.writes) != 0 {
		t.Errorf("DryRun wrote %v", dry.writes)
	}
}

type manifestRow struct {
	name         string
	existing     []byte // nil = the file is absent
	manifest     map[string]string
	force        bool
	dryRun       bool
	wantOutcome  InstallOutcome
	wantManifest map[string]string
	wantWrote    bool
}

var (
	researcherClaudeShipped = mustAgentDoc("researcher", "claude")
	manifestOlder           = []byte("---\nmodel: haiku\n---\nan older relevo copy\n")
	manifestEdited          = []byte("---\nmodel: sonnet\n---\nthe user's own copy\n")
	manifestIdentical       = append(append([]byte(nil), researcherClaudeShipped...), []byte("\n\n")...)
)

func mustAgentDoc(name, kind string) []byte {
	b, err := AgentDoc(name, kind)
	if err != nil {
		panic(err)
	}
	return b
}

// manifestDecisionRows is the install decision table: a missing file, an
// identical file (with and without Force), a copy relevo last wrote, and a user
// edit, with the manifest each one records.
var manifestDecisionRows = []manifestRow{
	{
		name:         "missing file is written and recorded",
		wantOutcome:  OutcomeWrote,
		wantManifest: map[string]string{researcherClaudePath: docSHA(researcherClaudeShipped)},
		wantWrote:    true,
	},
	{
		name:         "missing file under DryRun only reports",
		dryRun:       true,
		wantOutcome:  OutcomeWouldWrite,
		wantManifest: map[string]string{},
	},
	{
		name:         "identical records the bytes on disk",
		existing:     manifestIdentical,
		wantOutcome:  OutcomeKeptIdentical,
		wantManifest: map[string]string{researcherClaudePath: docSHA(manifestIdentical)},
	},
	{
		name:         "identical under Force is still kept",
		existing:     manifestIdentical,
		force:        true,
		wantOutcome:  OutcomeKeptIdentical,
		wantManifest: map[string]string{researcherClaudePath: docSHA(manifestIdentical)},
	},
	{
		name:         "differing but unchanged since relevo wrote it is updated",
		existing:     manifestOlder,
		manifest:     map[string]string{researcherClaudePath: docSHA(manifestOlder)},
		wantOutcome:  OutcomeUpdated,
		wantManifest: map[string]string{researcherClaudePath: docSHA(researcherClaudeShipped)},
		wantWrote:    true,
	},
	{
		name:         "differing but unchanged since relevo wrote it under DryRun only reports",
		existing:     manifestOlder,
		manifest:     map[string]string{researcherClaudePath: docSHA(manifestOlder)},
		dryRun:       true,
		wantOutcome:  OutcomeWouldUpdate,
		wantManifest: map[string]string{researcherClaudePath: docSHA(manifestOlder)},
	},
	{
		name:         "edited by the user is kept",
		existing:     manifestEdited,
		manifest:     map[string]string{researcherClaudePath: docSHA(manifestOlder)},
		wantOutcome:  OutcomeKeptDiffers,
		wantManifest: map[string]string{researcherClaudePath: docSHA(manifestOlder)},
	},
	{
		name:         "a path not in the manifest is kept",
		existing:     manifestEdited,
		wantOutcome:  OutcomeKeptDiffers,
		wantManifest: map[string]string{},
	},
	{
		name:         "edited by the user with Force is overwritten and recorded",
		existing:     manifestEdited,
		manifest:     map[string]string{researcherClaudePath: docSHA(manifestOlder)},
		force:        true,
		wantOutcome:  OutcomeOverwrote,
		wantManifest: map[string]string{researcherClaudePath: docSHA(researcherClaudeShipped)},
		wantWrote:    true,
	},
	{
		name:         "Force overwrites a file with no manifest entry",
		existing:     manifestEdited,
		force:        true,
		wantOutcome:  OutcomeOverwrote,
		wantManifest: map[string]string{researcherClaudePath: docSHA(researcherClaudeShipped)},
		wantWrote:    true,
	},
	{
		name:         "edited by the user with Force under DryRun only reports",
		existing:     manifestEdited,
		manifest:     map[string]string{researcherClaudePath: docSHA(manifestOlder)},
		force:        true,
		dryRun:       true,
		wantOutcome:  OutcomeWouldOverwrite,
		wantManifest: map[string]string{researcherClaudePath: docSHA(manifestOlder)},
	},
}

func TestInstallManifestDecisions(t *testing.T) {
	for _, tc := range manifestDecisionRows {
		t.Run(tc.name, func(t *testing.T) {
			checkManifestRow(t, tc, researcherClaudeShipped)
		})
	}
}

func checkManifestRow(t *testing.T, tc manifestRow, shipped []byte) {
	t.Helper()
	env := freshEnv()
	if tc.existing != nil {
		env.files[researcherClaudeFull] = tc.existing
	}
	env.manifest = tc.manifest

	results, err := Install(env, InstallOptions{
		Kind:   "claude",
		Role:   "researcher",
		Force:  tc.force,
		DryRun: tc.dryRun,
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	if got := results[0].Outcome; got != tc.wantOutcome {
		t.Errorf("outcome = %v, want %v", got, tc.wantOutcome)
	}
	if !reflect.DeepEqual(env.manifest, tc.wantManifest) {
		t.Errorf("manifest = %v, want %v", env.manifest, tc.wantManifest)
	}
	if tc.wantWrote {
		if !bytes.Equal(env.files[researcherClaudeFull], shipped) {
			t.Errorf("file on disk = %q, want the shipped definition", env.files[researcherClaudeFull])
		}
	} else if len(env.writes) != 0 {
		t.Errorf("wrote %v, want no write", env.writes)
	}
	if tc.dryRun && env.saves != 0 {
		t.Errorf("DryRun saved the manifest %d times, want 0", env.saves)
	}
}

func TestInstallSavesManifestOnce(t *testing.T) {
	env := freshEnv()

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5", len(results))
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}
	if len(env.manifest) != 5 {
		t.Errorf("manifest = %v, want one entry per written definition", env.manifest)
	}
	for _, r := range results {
		shipped, err := AgentDoc(r.Role, "claude")
		if err != nil {
			t.Fatalf("AgentDoc(%s): %v", r.Role, err)
		}
		if got := env.manifest[r.Path]; got != docSHA(shipped) {
			t.Errorf("manifest[%s] = %q, want the shipped sha", r.Path, got)
		}
	}
}

func TestInstallReportsManifestLoadErrorOnce(t *testing.T) {
	env := freshEnv()
	env.manifestErr = errors.New("decode role manifest: unexpected end of JSON input")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("Install = nil error, want the manifest error reported")
	}
	if !strings.Contains(err.Error(), "decode role manifest") {
		t.Errorf("Install error = %v, want it to name the manifest", err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5: a corrupt manifest must not block the install", len(results))
	}
	for _, r := range results {
		if r.Outcome != OutcomeWrote {
			t.Errorf("%s/%s outcome = %v, want %v", r.Kind, r.Role, r.Outcome, OutcomeWrote)
		}
	}
}

func TestInstallReportsManifestSaveError(t *testing.T) {
	env := freshEnv()
	env.saveErr = errors.New("read-only file system")

	results, err := Install(env, InstallOptions{Kind: "claude"})
	if err == nil {
		t.Fatal("Install = nil error, want the manifest save error reported")
	}
	if !strings.Contains(err.Error(), "read-only file system") {
		t.Errorf("Install error = %v, want the save error", err)
	}
	if len(results) != 5 {
		t.Fatalf("results = %d, want 5", len(results))
	}
}

func TestReadWriteManifestRoundTrip(t *testing.T) {
	kv := testKV(t)

	empty, err := ReadManifest(kv)
	if err != nil {
		t.Fatalf("ReadManifest(missing): %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ReadManifest(missing) = %v, want an empty map", empty)
	}

	want := map[string]string{
		".claude/agents/plan-executor.md": "0123456789abcdef",
		".claude/agents/researcher.md":    "fedcba9876543210",
	}
	if err := WriteManifest(kv, want); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	got, err := ReadManifest(kv)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadManifest = %v, want %v", got, want)
	}
	if _, ok, err := kv.KVGet("agents-manifest"); err != nil || !ok {
		t.Errorf("KVGet(agents-manifest) = (_, %v, %v), want the row", ok, err)
	}

	// A row that is valid JSON but not a role manifest is an error.
	if err := kv.KVPut(manifestKey, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("KVPut: %v", err)
	}
	if _, err := ReadManifest(kv); err == nil {
		t.Error("ReadManifest(malformed) = nil error, want an error")
	}
}

func TestOSInstallEnvWriteFileReplacesAndLeavesNoTempFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := OSInstallEnv()

	dir := filepath.Join(home, "agents")
	if err := env.MkdirAll(dir); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "researcher.md")

	if err := env.WriteFile(path, []byte("one")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := env.WriteFile(path, []byte("two")); err != nil {
		t.Fatalf("WriteFile (replace): %v", err)
	}

	data, err := env.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "two" {
		t.Errorf("content = %q, want %q", data, "two")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "researcher.md" {
		t.Errorf("directory holds %v, want only researcher.md (no temp file left behind)", entries)
	}
}

func TestDocEqual(t *testing.T) {
	shipped, err := AgentDoc("plan-executor", "claude")
	if err != nil {
		t.Fatalf("AgentDoc error: %v", err)
	}

	tests := []struct {
		name      string
		shipped   []byte
		installed []byte
		want      bool
	}{
		{
			name:      "equal bytes",
			shipped:   shipped,
			installed: shipped,
			want:      true,
		},
		{
			name:      "shipped vs shipped plus trailing whitespace",
			shipped:   shipped,
			installed: append(append([]byte(nil), shipped...), []byte("\n \t\r\n")...),
			want:      true,
		},
		{
			name:      "leading space difference",
			shipped:   shipped,
			installed: append([]byte(" "), shipped...),
			want:      false,
		},
		{
			name:      "changed model line",
			shipped:   []byte("---\nmodel: sonnet\n---\nbody\n"),
			installed: []byte("---\nmodel: haiku\n---\nbody\n"),
			want:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DocEqual(tc.shipped, tc.installed)
			if got != tc.want {
				t.Errorf("DocEqual() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstallResultLine(t *testing.T) {
	tests := []struct {
		res  InstallResult
		want string
	}{
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeWrote},
			want: "wrote  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeKeptDiffers},
			want: "kept (differs; --force to overwrite)  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeUpdated},
			want: "updated (unchanged since relevo wrote it)  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Kind: "claude", Role: "researcher", Path: ".claude/agents/researcher.md", Outcome: OutcomeWouldUpdate},
			want: "would update  ~/.claude/agents/researcher.md",
		},
		{
			res:  InstallResult{Path: ".gemini/config/agents/reviewer.md", Outcome: OutcomeError, Err: "read-only"},
			want: "error  ~/.gemini/config/agents/reviewer.md: read-only",
		},
	}
	for _, tc := range tests {
		got := tc.res.Line()
		if got != tc.want {
			t.Errorf("Line() = %q, want %q", got, tc.want)
		}
	}
}

func TestOSInstallEnvRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	env := OSInstallEnv()

	p, err := env.HomePath("a/b.md")
	if err != nil {
		t.Fatalf("unexpected HomePath error: %v", err)
	}
	expectedHomePath := filepath.Join(home, "a/b.md")
	if p != expectedHomePath {
		t.Errorf("HomePath = %q, want %q", p, expectedHomePath)
	}

	_, err = env.ReadFile(p)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}

	if err := env.MkdirAll(filepath.Dir(p)); err != nil {
		t.Fatalf("MkdirAll error: %v", err)
	}
	if err := env.WriteFile(p, []byte("x")); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}
	data, err := env.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(data) != "x" {
		t.Errorf("ReadFile = %q, want %q", string(data), "x")
	}

	info, err := os.Stat(p)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode perm = %o, want 0644", info.Mode().Perm())
	}

	if _, err := env.LookPath("definitely-not-a-binary-relevo-test"); err == nil {
		t.Error("expected error for non-existent binary, got nil")
	}
}

func pluginRows(results []InstallResult) []InstallResult {
	var out []InstallResult
	for _, r := range results {
		if strings.HasPrefix(r.Role, "opencode-plugin/") {
			out = append(out, r)
		}
	}
	return out
}

func TestInstallPluginFilesOptInProducesNoRowsWithoutFiles(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "opencode"})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := pluginRows(results); len(got) != 0 {
		t.Errorf("plugin rows = %+v, want none while the plugin is not opted in", got)
	}
}

func TestInstallPluginFilesOptInWritesTheShippedFiles(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "opencode", Files: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	rows := pluginRows(results)
	if len(rows) != 3 {
		t.Fatalf("plugin rows = %+v, want 3", rows)
	}
	h, _ := Lookup("opencode")
	for i, f := range h.Files {
		if rows[i].Outcome != OutcomeWrote {
			t.Errorf("%s outcome = %v, want %v", f.Name, rows[i].Outcome, OutcomeWrote)
		}
		if rows[i].Role != f.Name || rows[i].Path != f.Path {
			t.Errorf("row %d = %+v, want role %q path %q", i, rows[i], f.Name, f.Path)
		}
		want, err := ShippedFileBytes("opencode", f.Name)
		if err != nil {
			t.Fatalf("ShippedFileBytes(%s): %v", f.Name, err)
		}
		if got := env.files["/home/u/"+f.Path]; !bytes.Equal(got, want) {
			t.Errorf("%s on disk = %d bytes, want the embedded bytes", f.Path, len(got))
		}
		if got := env.manifest[f.Path]; got != docSHA(want) {
			t.Errorf("manifest[%s] = %q, want the shipped sha", f.Path, got)
		}
	}
	if env.saves != 1 {
		t.Errorf("manifest saves = %d, want 1", env.saves)
	}
}

func TestInstallPluginFilesSecondRunKeepsThem(t *testing.T) {
	env := freshEnv()
	if _, err := Install(env, InstallOptions{Kind: "opencode", Files: true}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	results, err := Install(env, InstallOptions{Kind: "opencode"})
	if err != nil {
		t.Fatalf("second Install: %v", err)
	}
	rows := pluginRows(results)
	if len(rows) != 3 {
		t.Fatalf("plugin rows = %+v, want 3", rows)
	}
	for _, r := range rows {
		if r.Outcome != OutcomeKeptIdentical {
			t.Errorf("%s outcome = %v, want %v", r.Role, r.Outcome, OutcomeKeptIdentical)
		}
	}
}

func TestInstallPluginFilesDryRunWritesNothing(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "opencode", Files: true, DryRun: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	rows := pluginRows(results)
	if len(rows) != 3 {
		t.Fatalf("plugin rows = %+v, want 3", rows)
	}
	for _, r := range rows {
		if r.Outcome != OutcomeWouldWrite {
			t.Errorf("%s outcome = %v, want %v", r.Role, r.Outcome, OutcomeWouldWrite)
		}
	}
	if len(env.writes) != 0 {
		t.Errorf("DryRun wrote %v, want nothing", env.writes)
	}
	if env.saves != 0 {
		t.Errorf("DryRun saved the manifest %d times, want 0", env.saves)
	}
}

func TestInstallPluginFilesRoleSkipsThem(t *testing.T) {
	env := freshEnv()
	results, err := Install(env, InstallOptions{Kind: "opencode", Role: "plan-executor", Files: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(results) != 1 || results[0].Role != "plan-executor" {
		t.Fatalf("results = %+v, want only the plan-executor role", results)
	}
	if got := pluginRows(results); len(got) != 0 {
		t.Errorf("plugin rows = %+v, want none under --role", got)
	}
}

func TestInstallPluginFilesFollowDefinitionRules(t *testing.T) {
	const pkgPath = ".config/opencode/plugins/relevo/package.json"
	const pkgFull = "/home/u/.config/opencode/plugins/relevo/package.json"

	shipped, err := ShippedFileBytes("opencode", "opencode-plugin/package.json")
	if err != nil {
		t.Fatalf("ShippedFileBytes: %v", err)
	}

	t.Run("an unmodified older copy is updated even with Files false", func(t *testing.T) {
		env := freshEnv()
		older := []byte("{\n  \"name\": \"relevo\",\n  \"version\": \"0.0.0-old\"\n}\n")
		env.files[pkgFull] = older
		env.manifest[pkgPath] = docSHA(older)

		results, err := Install(env, InstallOptions{Kind: "opencode"})
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		rows := pluginRows(results)
		if len(rows) != 1 || rows[0].Outcome != OutcomeUpdated {
			t.Fatalf("plugin rows = %+v, want one %v", rows, OutcomeUpdated)
		}
		if !bytes.Equal(env.files[pkgFull], shipped) {
			t.Error("the older plugin copy was not refreshed with the shipped one")
		}
		if got := env.manifest[pkgPath]; got != docSHA(shipped) {
			t.Errorf("manifest[%s] = %q, want the shipped sha", pkgPath, got)
		}
	})

	t.Run("a user edit is kept, and Force overwrites it", func(t *testing.T) {
		edited := []byte("{\n  \"version\": \"mine\"\n}\n")

		env := freshEnv()
		env.files[pkgFull] = edited
		results, err := Install(env, InstallOptions{Kind: "opencode"})
		if err != nil {
			t.Fatalf("Install: %v", err)
		}
		rows := pluginRows(results)
		if len(rows) != 1 || rows[0].Outcome != OutcomeKeptDiffers {
			t.Fatalf("plugin rows = %+v, want one %v", rows, OutcomeKeptDiffers)
		}
		if !bytes.Equal(env.files[pkgFull], edited) {
			t.Error("an edited plugin file must stay as the user wrote it")
		}

		env2 := freshEnv()
		env2.files[pkgFull] = edited
		results2, err := Install(env2, InstallOptions{Kind: "opencode", Force: true})
		if err != nil {
			t.Fatalf("Install(Force): %v", err)
		}
		rows2 := pluginRows(results2)
		if len(rows2) != 1 || rows2[0].Outcome != OutcomeOverwrote {
			t.Fatalf("plugin rows = %+v, want one %v", rows2, OutcomeOverwrote)
		}
		if !bytes.Equal(env2.files[pkgFull], shipped) {
			t.Error("Force did not replace the edited plugin file with the shipped one")
		}
	})
}
