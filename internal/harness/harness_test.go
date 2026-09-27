package harness

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestHarnessRules(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned no entries")
	}

	if !sort.SliceIsSorted(all, func(i, j int) bool {
		return all[i].Kind < all[j].Kind
	}) {
		t.Errorf("All() is not sorted by Kind: %+v", all)
	}

	for _, h := range all {
		got, ok := Lookup(h.Kind)
		if !ok {
			t.Errorf("Lookup(%q) returned ok=false", h.Kind)
			continue
		}
		if !reflect.DeepEqual(got, h) {
			t.Errorf("Lookup(%q) = %+v, want %+v", h.Kind, got, h)
		}
	}
}

func TestLookupUnknown(t *testing.T) {
	_, ok := Lookup("unknown-kind")
	if ok {
		t.Errorf("Lookup(unknown-kind) returned ok=true, want false")
	}
}

func TestRoleTable(t *testing.T) {
	wantNames := []string{"builder", "reviewer", "researcher"}
	if got := RoleNames(); !reflect.DeepEqual(got, wantNames) {
		t.Errorf("RoleNames() = %v, want %v", got, wantNames)
	}

	b, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") returned ok=false")
	}
	if b.Shape != ShapeBuilder {
		t.Errorf("RoleByName(\"builder\").Shape = %v, want %v", b.Shape, ShapeBuilder)
	}
	if b.Definition != "plan-executor" {
		t.Errorf("RoleByName(\"builder\").Definition = %q, want %q", b.Definition, "plan-executor")
	}

	for _, name := range []string{"reviewer", "researcher"} {
		r, ok := RoleByName(name)
		if !ok {
			t.Fatalf("RoleByName(%q) returned ok=false", name)
		}
		if r.Shape != ShapeConsult {
			t.Errorf("RoleByName(%q).Shape = %v, want %v", name, r.Shape, ShapeConsult)
		}
	}

	for _, name := range RoleNames() {
		if _, ok := RoleByName(name); !ok {
			t.Fatalf("RoleByName(%q) returned ok=false", name)
		}
	}

	if _, ok := RoleByName("nope"); ok {
		t.Errorf("RoleByName(\"nope\") returned ok=true, want false")
	}
}

func TestCanServe(t *testing.T) {
	matrix := []struct {
		kind string
		role string
		want bool
	}{
		{"agy", "builder", true},
		{"agy", "reviewer", true},
		{"agy", "researcher", true},
		{"agy", "nope", false},
		{"claude", "builder", true},
		{"claude", "reviewer", true},
		{"claude", "researcher", true},
		{"claude", "nope", false},
		{"opencode", "builder", true},
		{"opencode", "reviewer", true},
		{"opencode", "researcher", true},
		{"opencode", "nope", false},
	}

	for _, tt := range matrix {
		h, ok := Lookup(tt.kind)
		if !ok {
			t.Fatalf("Lookup(%q) not found", tt.kind)
		}
		if got := h.CanServe(tt.role); got != tt.want {
			t.Errorf("Harness(%q).CanServe(%q) = %v, want %v", tt.kind, tt.role, got, tt.want)
		}
	}
}

func TestRoleDefinitionsIncludeDispatchTargets(t *testing.T) {
	want := map[string][]string{
		"builder":    {"plan-executor", "researcher"},
		"reviewer":   {"reviewer"},
		"researcher": {"researcher"},
	}
	for role, defs := range want {
		spec, ok := RoleByName(role)
		if !ok {
			t.Fatalf("RoleByName(%q) not found", role)
		}
		if !reflect.DeepEqual(spec.Definitions, defs) {
			t.Errorf("%s Definitions = %v, want %v", role, spec.Definitions, defs)
		}
		if spec.Definitions[0] != spec.Definition {
			t.Errorf("%s Definitions[0] = %q, want Definition %q", role, spec.Definitions[0], spec.Definition)
		}
	}
}

func TestPlanExecutorDispatchesResearcherOnEveryKind(t *testing.T) {
	for _, h := range All() {
		doc, err := AgentDoc("plan-executor", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", h.Kind, err)
		}
		if h.Kind == "agy" {
			// An idle root agent on agy is an exit relevo treats as a failed
			// builder, so agy's plan-executor names no researcher.
			if strings.Contains(string(doc), "researcher") {
				t.Errorf("agy plan-executor must not mention researcher")
			}
			continue
		}
		if !strings.Contains(string(doc), "researcher") {
			t.Errorf("%s plan-executor does not mention researcher; Definitions for builder is wrong", h.Kind)
		}
	}
}

func TestPlanExecutorReportsGitSurgeryOnEveryKind(t *testing.T) {
	for _, h := range All() {
		doc, err := AgentDoc("plan-executor", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(plan-executor, %s): %v", h.Kind, err)
		}
		if !strings.Contains(string(doc), "Git surgery on your branch:") {
			t.Errorf("%s plan-executor does not require the report to name git surgery on the branch", h.Kind)
		}
	}
}

func TestArchitectShipsOnEveryKindAndIsNotARole(t *testing.T) {
	for _, h := range All() {
		doc, err := AgentDoc("architect", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", h.Kind, err)
		}
		if h.DocExt == "toml" {
			if !strings.Contains(string(doc), "developer_instructions = '''") {
				t.Errorf("%s architect definition lacks the developer_instructions literal", h.Kind)
			}
		} else if !strings.Contains(string(doc), "name: architect") {
			t.Errorf("%s architect definition does not carry name: architect", h.Kind)
		}
		if !strings.Contains(string(doc), "Ordered Implementation Steps") {
			t.Errorf("%s architect definition lacks the plan output structure", h.Kind)
		}
		if h.Kind == "agy" && !strings.Contains(string(doc), "model: inherit") {
			t.Errorf("agy architect must pin model: inherit so the launch line's --model wins")
		}
	}
	if _, ok := RoleByName("architect"); ok {
		t.Error("architect is a shipped definition, not a relevo role")
	}
}

func TestArchitectHandoffIsSharedAcrossKinds(t *testing.T) {
	bodies := map[string]string{}
	for _, h := range All() {
		doc, err := AgentDoc("architect", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", h.Kind, err)
		}
		body := definitionBody(t, h.Kind, string(doc))
		for _, want := range []string{"## Handing off", "relevo send", "RELEVO_PLANNER", "relevo gate"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s architect body lacks %q", h.Kind, want)
			}
		}
		for _, banned := range []string{"Agent tool", "slash command"} {
			if strings.Contains(body, banned) {
				t.Errorf("%s architect body is not harness-neutral: contains %q", h.Kind, banned)
			}
		}
		bodies[h.Kind] = body
	}
	ref := bodies["claude"]
	for kind, body := range bodies {
		if body != ref {
			t.Errorf("%s architect body differs from claude's:\n%s", kind, firstDifferingLine(ref, body))
		}
	}
}

func TestArchitectCarriesLatencyGuidance(t *testing.T) {
	for _, h := range All() {
		doc, err := AgentDoc("architect", h.Kind)
		if err != nil {
			t.Fatalf("AgentDoc(architect, %s): %v", h.Kind, err)
		}
		body := definitionBody(t, h.Kind, string(doc))
		for _, want := range []string{
			"### 8. Working Efficiently",
			"## Writing for the Builder's Round Trips",
			"Fence every deletion",
			"Script the mechanical work",
			"8. Does the plan carry a Working Efficiently section",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s architect body lacks %q", h.Kind, want)
			}
		}
		if i, j := strings.Index(body, "### 8. Working Efficiently"), strings.Index(body, "## Quality Standards"); i < 0 || j < 0 || i > j {
			t.Errorf("%s architect body: Working Efficiently at %d, Quality Standards at %d; want the section before the heading", h.Kind, i, j)
		}
	}
}

func definitionBody(t *testing.T, kind, doc string) string {
	t.Helper()
	if h, ok := Lookup(kind); ok && h.DocExt == "toml" {
		_, after, ok := strings.Cut(doc, "developer_instructions = '''\n")
		if !ok {
			t.Fatalf("%s definition has no developer_instructions literal", kind)
		}
		body, _, ok2 := strings.Cut(after, "'''")
		if !ok2 {
			t.Fatalf("%s definition has no developer_instructions literal", kind)
		}
		return body
	}
	parts := strings.SplitN(doc, "\n---\n", 2)
	if len(parts) != 2 || !strings.HasPrefix(doc, "---\n") {
		t.Fatalf("%s architect definition has no frontmatter fence", kind)
	}
	return parts[1]
}

func firstDifferingLine(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d:\n  claude: %q\n  other:  %q", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("lengths differ: %d vs %d lines", len(al), len(bl))
}

func TestCanServeRequiresEveryDefinition(t *testing.T) {
	h := Harness{Kind: "partial", Roles: []Role{{Name: "plan-executor"}}}
	if h.CanServe("builder") {
		t.Error("a harness shipping plan-executor but not researcher must not serve builder")
	}
	h.Roles = append(h.Roles, Role{Name: "researcher"})
	if !h.CanServe("builder") {
		t.Error("plan-executor + researcher must serve builder")
	}
	if h.CanServe("reviewer") {
		t.Error("no reviewer definition must not serve reviewer")
	}
}

func TestPatternsSetAndCompile(t *testing.T) {
	for _, h := range All() {
		if len(h.LimitPatterns) < 1 {
			t.Errorf("harness %q: len(LimitPatterns) = %d, want >= 1", h.Kind, len(h.LimitPatterns))
		}
		if len(h.DenialPatterns) < 1 {
			t.Errorf("harness %q: len(DenialPatterns) = %d, want >= 1", h.Kind, len(h.DenialPatterns))
		}
		patterns := append(append([]string(nil), h.LimitPatterns...), h.DenialPatterns...)
		for _, pat := range patterns {
			if _, err := regexp.Compile(pat); err != nil {
				t.Errorf("harness %q: pattern %q failed to compile: %v", h.Kind, pat, err)
			}
		}
	}
}

func TestAgyLimitPatternsMatchFixture(t *testing.T) {
	agy, ok := Lookup("agy")
	if !ok {
		t.Fatal("Lookup(\"agy\") not found")
	}
	fixture := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 2h48m52s."
	for _, pat := range agy.LimitPatterns {
		if re, err := regexp.Compile(pat); err == nil && re.MatchString(fixture) {
			return
		}
	}
	t.Errorf("agy LimitPatterns did not match fixture %q", fixture)
}

var launchPrintCases = []struct {
	kind       string
	extra      []string
	wantPrint  []string
	wantPrompt int
}{
	{
		kind: "agy",
		wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
			"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder, "--add-dir", DirPlaceholder},
		wantPrompt: 1,
	},
	{
		kind: "agy", extra: []string{"--dangerously-skip-permissions"},
		wantPrint: []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor",
			"--output-format", "stream-json", "--print-timeout", BudgetPlaceholder, "--add-dir", DirPlaceholder, "--dangerously-skip-permissions"},
		wantPrompt: 1,
	},
	{
		kind:       "claude",
		wantPrint:  []string{"-p", PromptPlaceholder, "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"},
		wantPrompt: 1,
	},
	{
		kind:       "opencode",
		wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--format", "json", "--thinking", "--standalone"},
		wantPrompt: 1,
	},
	{
		kind: "opencode", extra: []string{"--auto"},
		wantPrint:  []string{"run", PromptPlaceholder, "-m", "prov/m/x", "--agent", "plan-executor", "--format", "json", "--thinking", "--standalone", "--auto"},
		wantPrompt: 1,
	},
	{
		kind: "codex",
		wantPrint: []string{"exec", PromptPlaceholder, "-p", "plan-executor", "-m", "m/x", "-c", "model_provider=prov",
			"--json", "-C", DirPlaceholder},
		wantPrompt: 1,
	},
}

func TestLaunchPrintPerKind(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	for _, tt := range launchPrintCases {
		t.Run(tt.kind+" "+strings.Join(tt.extra, " "), func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got, err := h.Launch("prov", "m/x", tt.extra, builder, TierHarness)
			if err != nil {
				t.Fatalf("Launch() error = %v", err)
			}
			if !reflect.DeepEqual(got.Print, tt.wantPrint) {
				t.Errorf("Print = %v, want %v", got.Print, tt.wantPrint)
			}
			if got.PromptAt != tt.wantPrompt {
				t.Errorf("PromptAt = %d, want %d", got.PromptAt, tt.wantPrompt)
			}
			if got.Print[got.PromptAt] != PromptPlaceholder {
				t.Errorf("Print[PromptAt] = %q, want the placeholder", got.Print[got.PromptAt])
			}
		})
	}
	checkLaunchExtras(t, builder)
}

func checkLaunchExtras(t *testing.T, builder RoleSpec) {
	t.Helper()
	// claude refuses stream-json in print mode without --verbose; no
	// candidate's extra_args should have to know.
	c, _ := Lookup("claude")
	claudeLaunch, _ := c.Launch("prov", "m/x", nil, builder, TierHarness)
	if p := claudeLaunch.Print; !containsAdjacent(p, "--output-format", "stream-json") || !contains(p, "--verbose") {
		t.Errorf("claude print form must carry --output-format stream-json and --verbose: %v", p)
	}
	for _, kind := range []string{"agy", "claude"} {
		h, _ := Lookup(kind)
		l, _ := h.Launch("prov", "m/x", nil, builder, TierHarness)
		if p := l.Print; contains(p, "--include-partial-messages") {
			t.Errorf("%s: partial messages are out of scope: %v", kind, p)
		}
	}

	// agy pins its workspace to the round's tree; claude and opencode carry
	// no such flag.
	a, _ := Lookup("agy")
	agyLaunch, _ := a.Launch("prov", "m/x", nil, builder, TierHarness)
	if p := agyLaunch.Print; !containsAdjacent(p, "--add-dir", DirPlaceholder) {
		t.Errorf("agy print form must carry --add-dir <dir>: %v", p)
	}
	for _, kind := range []string{"claude", "opencode"} {
		h, _ := Lookup(kind)
		l, _ := h.Launch("prov", "m/x", nil, builder, TierHarness)
		if p := l.Print; contains(p, "--add-dir") {
			t.Errorf("%s: --add-dir is agy-only: %v", kind, p)
		}
	}

	unknown := Harness{Kind: "unknown"}
	got, _ := unknown.Launch("prov", "m/x", []string{"--z"}, builder, TierHarness)
	if len(got.Print) != 0 || got.PromptAt != -1 {
		t.Errorf("unknown kind: Print = %v PromptAt = %d; want empty and -1", got.Print, got.PromptAt)
	}
}

func TestLaunchClaudeEffort(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	h, ok := Lookup("claude")
	if !ok {
		t.Fatal("Lookup(\"claude\") not found")
	}

	base := func(model string, rest ...string) []string {
		return append([]string{"-p", PromptPlaceholder, "--model", model}, rest...)
	}
	tail := []string{"--agent", builder.Definition, "--output-format", "stream-json", "--verbose"}
	tests := []struct {
		model string
		want  []string
	}{
		{"opus:medium", append(base("opus", "--effort", "medium"), tail...)},
		{"opus:max", append(base("opus", "--effort", "max"), tail...)},
		{"sonnet", append(base("sonnet"), tail...)},
		{"anthropic.claude-x-v1:0", append(base("anthropic.claude-x-v1:0"), tail...)},
		{"opus:turbo", append(base("opus:turbo"), tail...)},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got, err := h.Launch("anthropic", tt.model, nil, builder, TierHarness)
			if err != nil {
				t.Fatalf("Launch(%q) error = %v", tt.model, err)
			}
			if !reflect.DeepEqual(got.Print, tt.want) {
				t.Errorf("Print = %v, want %v", got.Print, tt.want)
			}
			if got.PromptAt != 1 {
				t.Errorf("PromptAt = %d, want 1", got.PromptAt)
			}
		})
	}
}

func TestLaunchOpencodeKeepsVariant(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	h, ok := Lookup("opencode")
	if !ok {
		t.Fatal("Lookup(\"opencode\") not found")
	}

	got, err := h.Launch("cline-pass", "cline-pass/deepseek-v4.1-flash#max", nil, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch error = %v", err)
	}
	if !containsAdjacent(got.Print, "-m", "cline-pass/cline-pass/deepseek-v4.1-flash#max") {
		t.Errorf("Print = %v, want -m cline-pass/cline-pass/deepseek-v4.1-flash#max", got.Print)
	}
	if contains(got.Print, "--effort") {
		t.Errorf("Print carries --effort: %v", got.Print)
	}
	if contains(got.Print, "--variant") {
		t.Errorf("Print carries --variant: %v", got.Print)
	}
}

func TestLaunchCodex(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	h, ok := Lookup("codex")
	if !ok {
		t.Fatal("Lookup(\"codex\") not found")
	}

	wantPrint := []string{"exec", PromptPlaceholder, "-p", "plan-executor", "-m", "gpt-5.6-terra", "-c", "model_provider=openai", "-c", "model_reasoning_effort=high", "--json", "-C", DirPlaceholder}

	got, err := h.Launch("openai", "gpt-5.6-terra:high", nil, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if !reflect.DeepEqual(got.Print, wantPrint) {
		t.Errorf("Print = %v, want %v", got.Print, wantPrint)
	}

	wantPrintEdit := append(append([]string(nil), wantPrint...), "-s", "workspace-write", "-c", StatePlaceholder)
	got, err = h.Launch("openai", "gpt-5.6-terra:high", nil, builder, TierEdit)
	if err != nil {
		t.Fatalf("Launch() TierEdit error = %v", err)
	}
	if !reflect.DeepEqual(got.Print, wantPrintEdit) {
		t.Errorf("TierEdit Print = %v, want %v", got.Print, wantPrintEdit)
	}

	_, err = h.Launch("openai", "gpt-5.6-terra:high", nil, builder, TierRead)
	if !errors.Is(err, ErrTierUnsupported) {
		t.Fatalf("Launch() TierRead error = %v, want %v", err, ErrTierUnsupported)
	}

	got, err = h.Launch("openai", "gpt-5.6-terra:high", []string{"--foo"}, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch() extra error = %v", err)
	}
	if len(got.Print) == 0 || got.Print[len(got.Print)-1] != "--foo" {
		t.Errorf("Print with extra must end in --foo: %v", got.Print)
	}

	printBefore := append([]string(nil), got.Print...)
	rendered := got.PrintArgs("hi", time.Hour, "/w", "/state")
	wantRendered := []string{"exec", "hi", "-p", "plan-executor", "-m", "gpt-5.6-terra", "-c", "model_provider=openai", "-c", "model_reasoning_effort=high", "--json", "-C", "/w", "--foo"}
	if !reflect.DeepEqual(rendered, wantRendered) {
		t.Errorf("PrintArgs = %v, want %v", rendered, wantRendered)
	}
	if !reflect.DeepEqual(got.Print, printBefore) {
		t.Errorf("PrintArgs mutated Print: got %v, want unchanged %v", got.Print, printBefore)
	}
}

func TestLaunchCodexBadModel(t *testing.T) {
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}
	h, ok := Lookup("codex")
	if !ok {
		t.Fatal("Lookup(\"codex\") not found")
	}
	for _, model := range []string{":high", "gpt-5.6-terra:"} {
		_, err := h.Launch("openai", model, nil, builder, TierHarness)
		if !errors.Is(err, ErrBadModel) {
			t.Errorf("Launch(%q) error = %v, want ErrBadModel", model, err)
		}
	}
}

func TestPrintArgsSubstitutesPromptAndBudgetWithoutMutating(t *testing.T) {
	builder, _ := RoleByName("builder")
	h, _ := Lookup("agy")
	extra := []string{"--dangerously-skip-permissions"}
	l, err := h.Launch("prov", "m/x", extra, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	before := append([]string(nil), l.Print...)

	prompt := "Read /state/x/003-plan.md and write /state/x/003-report.md"
	got := l.PrintArgs(prompt, 90*time.Minute, "/w", "/state")
	want := []string{"-p", prompt, "--model", "m/x", "--agent", "plan-executor",
		"--output-format", "stream-json", "--print-timeout", "1h30m0s", "--add-dir", "/w", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PrintArgs = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(l.Print, before) {
		t.Errorf("PrintArgs mutated Print: %v", l.Print)
	}
	if !reflect.DeepEqual(extra, []string{"--dangerously-skip-permissions"}) {
		t.Errorf("extra was modified: %v", extra)
	}
	got[0] = "changed"
	if l.Print[0] != "-p" {
		t.Error("PrintArgs must return a fresh slice, not alias Print")
	}

	// A kind with no budget or dir flag ignores both; the prompt still lands.
	c, _ := Lookup("claude")
	cl, err := c.Launch("prov", "m/x", nil, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	got = cl.PrintArgs("hello", time.Hour, "/w", "/state")
	if !reflect.DeepEqual(got, []string{"-p", "hello", "--model", "m/x", "--agent", "plan-executor", "--output-format", "stream-json", "--verbose"}) {
		t.Errorf("claude PrintArgs = %v", got)
	}
	for _, a := range got {
		if a == BudgetPlaceholder || a == PromptPlaceholder || a == DirPlaceholder || a == StatePlaceholder || a == "/w" || a == "/state" {
			t.Errorf("placeholder survived substitution, or an ignored dir leaked in: %v", got)
		}
	}

	if got := (Launch{Kind: "unknown", PromptAt: -1}).PrintArgs("x", time.Minute, "/w", "/state"); len(got) != 0 {
		t.Errorf("unknown kind PrintArgs = %v, want empty", got)
	}
}

func TestPrintArgsFillsState(t *testing.T) {
	builder, _ := RoleByName("builder")
	codex, ok := Lookup("codex")
	if !ok {
		t.Fatal("Lookup(\"codex\") not found")
	}
	l, err := codex.Launch("openai", "gpt-5.6-terra", nil, builder, TierEdit)
	if err != nil {
		t.Fatalf("codex Launch() TierEdit error: %v", err)
	}

	stateDir := "/home/u/.local/state/relevo/x"
	got := l.PrintArgs("p", 0, "/wt", stateDir)
	for _, a := range got {
		if a == StatePlaceholder {
			t.Errorf("StatePlaceholder survived PrintArgs: %v", got)
		}
	}
	wantRoot := `sandbox_workspace_write.writable_roots=["/home/u/.local/state/relevo/x"]`
	found := false
	for i, a := range got {
		if a == "-s" && i+1 < len(got) && got[i+1] == "workspace-write" {
			for j := i + 2; j < len(got)-1; j++ {
				if got[j] == "-c" && got[j+1] == wantRoot {
					found = true
					break
				}
			}
		}
	}
	if !found {
		t.Errorf("element after -c following workspace-write not found or != %s; got %v", wantRoot, got)
	}

	claude, _ := Lookup("claude")
	cl, err := claude.Launch("prov", "m/x", nil, builder, TierEdit)
	if err != nil {
		t.Fatalf("claude Launch() TierEdit error = %v", err)
	}
	clGot := cl.PrintArgs("p", 0, "/wt", stateDir)
	wantClaude := []string{"-p", "p", "--model", "m/x", "--agent", "plan-executor",
		"--output-format", "stream-json", "--verbose", "--permission-mode", "acceptEdits"}
	if !reflect.DeepEqual(clGot, wantClaude) {
		t.Errorf("claude PrintArgs = %v, want %v", clGot, wantClaude)
	}
}

func TestWritableRootsArgQuotes(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path with space/dir", `sandbox_workspace_write.writable_roots=["/path with space/dir"]`},
		{`/path/"quotes"/dir`, `sandbox_workspace_write.writable_roots=["/path/\"quotes\"/dir"]`},
	}
	for _, tt := range tests {
		got := writableRootsArg(tt.path)
		if got != tt.want {
			t.Errorf("writableRootsArg(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestSplitEffort(t *testing.T) {
	tests := []struct {
		in         string
		wantID     string
		wantEffort string
		wantErr    bool
	}{
		{"gpt-5.6-terra:high", "gpt-5.6-terra", "high", false},
		{"gpt-5.6-terra", "gpt-5.6-terra", "", false},
		{"a:b:c", "a:b", "c", false},
		{":high", "", "", true},
		{"gpt-5.6-terra:", "", "", true},
		{"", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			id, effort, err := SplitEffort(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrBadModel) {
					t.Fatalf("SplitEffort(%q) error = %v, want ErrBadModel", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SplitEffort(%q) unexpected error: %v", tt.in, err)
			}
			if id != tt.wantID || effort != tt.wantEffort {
				t.Errorf("SplitEffort(%q) = (%q, %q), want (%q, %q)", tt.in, id, effort, tt.wantID, tt.wantEffort)
			}
		})
	}
}

var harnessTableExpected = map[string]Harness{
	"agy": {
		Kind:       "agy",
		Binary:     "agy",
		MinVersion: "1.1.6",
		LimitPatterns: []string{
			`(?i)individual quota reached`,
			`(?i)RESOURCE_EXHAUSTED`,
			`(?i)quota exceeded`,
		},
		DenialPatterns: []string{
			`(?i)permission (request )?(denied|rejected)`,
			`(?i)tool (call|use) (was )?rejected`,
			`(?i)not permitted in (plan|accept-edits) mode`,
		},
		Providers: []string{"google", "agy-extra"},
		Roles: []Role{
			{Name: "plan-executor", Path: ".gemini/config/agents/plan-executor.md", Doc: "plan-executor.agy", ExpectModel: "inherit"},
			{Name: "researcher", Path: ".gemini/config/agents/researcher.md", Doc: "researcher.agy", ExpectModel: "inherit"},
			{Name: "reviewer", Path: ".gemini/config/agents/reviewer.md", Doc: "reviewer.agy", ExpectModel: "inherit"},
			{Name: "architect", Path: ".gemini/config/agents/architect.md", Doc: "architect.agy", ExpectModel: "inherit"},
		},
	},
	"claude": {
		Kind:   "claude",
		Binary: "claude",
		LimitPatterns: []string{
			`(?i)you've hit your .*limit`,
			`(?i)usage limit reached`,
			`(?i)rate limit reached`,
			`(?i)limit .*resets`,
		},
		DenialPatterns: []string{
			`(?i)requested permissions to use .* but you haven't granted`,
			`(?i)permission (to use .* was )?denied`,
			`(?i)tool use was rejected`,
		},
		Providers: []string{"anthropic"},
		Roles: []Role{
			{Name: "plan-executor", Path: ".claude/agents/plan-executor.md", Doc: "plan-executor.claude"},
			{Name: "researcher", Path: ".claude/agents/researcher.md", Doc: "researcher.claude"},
			{Name: "reviewer", Path: ".claude/agents/reviewer.md", Doc: "reviewer.claude"},
			{Name: "architect", Path: ".claude/agents/architect.md", Doc: "architect.claude"},
		},
	},
	"opencode": {
		Kind:   "opencode",
		Binary: "opencode",
		LimitPatterns: []string{
			`(?i)rate.?limit(ed)? (reached|exceeded)`,
			`(?i)quota (exceeded|reached)`,
			`(?i)insufficient (credits|quota)`,
			`(?i)RESOURCE_EXHAUSTED`,
			`(?i)error 429`,
			`(?i)requires more credits`,
			`(?i)reached your .* limit`,
		},
		DenialPatterns: []string{
			`(?i)permission.*(denied|rejected)`,
			`(?i)rejected: external_directory`,
		},
		Roles: []Role{
			{Name: "plan-executor", Path: ".config/opencode/agents/plan-executor.md", Doc: "plan-executor.opencode"},
			{Name: "researcher", Path: ".config/opencode/agents/researcher.md", Doc: "researcher.opencode"},
			{Name: "reviewer", Path: ".config/opencode/agents/reviewer.md", Doc: "reviewer.opencode"},
			{Name: "architect", Path: ".config/opencode/agents/architect.md", Doc: "architect.opencode"},
		},
		Files: []ShippedFile{
			{Name: "opencode-plugin/package.json", Path: ".config/opencode/plugins/relevo/package.json", Embed: "opencodeplugin/package.json"},
			{Name: "opencode-plugin/server.ts", Path: ".config/opencode/plugins/relevo/server.ts", Embed: "opencodeplugin/server.ts"},
			{Name: "opencode-plugin/tui.tsx", Path: ".config/opencode/plugins/relevo/tui.tsx", Embed: "opencodeplugin/tui.tsx"},
		},
	},
	"codex": {
		Kind:       "codex",
		Binary:     "codex",
		MinVersion: "0.155.0",
		LimitPatterns: []string{
			`(?i)usage limit`,
			`(?i)rate limit`,
			`(?i)quota`,
			`(?i)"status": 429`,
			`(?i)too many requests`,
		},
		DenialPatterns: []string{
			`(?i)patch rejected: writing outside of the project`,
			`(?i)rejected by user approval settings`,
			`(?i)sandbox.*(denied|blocked|not permitted)`,
			`(?i)permission denied`,
		},
		DocExt: "toml",
		Roles: []Role{
			{Name: "plan-executor", Path: ".codex/plan-executor.config.toml", Doc: "plan-executor.codex"},
			{Name: "researcher", Path: ".codex/researcher.config.toml", Doc: "researcher.codex", ExpectModel: "gpt-5.6-luna"},
			{Name: "reviewer", Path: ".codex/reviewer.config.toml", Doc: "reviewer.codex"},
			{Name: "architect", Path: ".codex/architect.config.toml", Doc: "architect.codex"},
		},
	},
}

func TestTableExactValues(t *testing.T) {
	for kind, want := range harnessTableExpected {
		got, ok := Lookup(kind)
		if !ok {
			t.Errorf("Lookup(%q) not found", kind)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Lookup(%q) = %+v, want %+v", kind, got, want)
		}
	}
}
