package harness

import (
	"bytes"
	"strings"
	"testing"
)

const (
	customClaudePath = ".claude/agents/my-executor.md"
	customClaudeFull = "/home/u/.claude/agents/my-executor.md"
)

func claudeCustomEnv() *fakeInstallEnv {
	env := freshEnv()
	env.lookPaths["claude"] = "/bin/claude"
	return env
}

func TestInstallCustomWritesTheRenderedFileAtTheConventionPath(t *testing.T) {
	env := claudeCustomEnv()
	body := []byte("---\nname: my-executor\n---\nDo the thing.\n")

	results, err := InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: body}})
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v, want one", results)
	}
	res := results[0]
	if res.Kind != "claude" || res.Role != "my-executor" || res.Path != customClaudePath || res.Outcome != OutcomeWrote {
		t.Fatalf("result = %+v, want claude/my-executor wrote at %s", res, customClaudePath)
	}
	if !bytes.Equal(env.files[customClaudeFull], body) {
		t.Errorf("file on disk = %q, want the rendered bytes", env.files[customClaudeFull])
	}
	if got := env.manifest[customClaudePath]; got != docSHA(body) {
		t.Errorf("manifest[%s] = %q, want the rendered sha", customClaudePath, got)
	}
}

func TestInstallCustomKeepsIdenticalAndUpdatesItsOwnOlderCopy(t *testing.T) {
	older := []byte("---\nname: my-executor\n---\nversion one\n")
	newer := []byte("---\nname: my-executor\n---\nversion two\n")

	env := claudeCustomEnv()
	if _, err := InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: older}}); err != nil {
		t.Fatalf("first InstallCustom: %v", err)
	}
	if env.saves != 1 {
		t.Fatalf("manifest saves = %d, want 1: a changed manifest must be saved", env.saves)
	}

	results, err := InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: older}})
	if err != nil {
		t.Fatalf("second InstallCustom: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeKeptIdentical {
		t.Fatalf("results = %+v, want one %v", results, OutcomeKeptIdentical)
	}

	results, err = InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: newer}})
	if err != nil {
		t.Fatalf("third InstallCustom: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeUpdated {
		t.Fatalf("results = %+v, want one %v: the manifest records relevo's own older copy", results, OutcomeUpdated)
	}
	if !bytes.Equal(env.files[customClaudeFull], newer) {
		t.Errorf("file on disk = %q, want the newer bytes", env.files[customClaudeFull])
	}
	if got := env.manifest[customClaudePath]; got != docSHA(newer) {
		t.Errorf("manifest[%s] = %q, want the newer sha", customClaudePath, got)
	}
}

func TestInstallCustomKeepsAnEditUnlessForced(t *testing.T) {
	rendered := []byte("---\nname: my-executor\n---\nrelevo's copy\n")
	edited := []byte("---\nname: my-executor\n---\nthe user's own copy\n")

	env := claudeCustomEnv()
	env.files[customClaudeFull] = edited

	results, err := InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: rendered}})
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeKeptDiffers {
		t.Fatalf("results = %+v, want one %v", results, OutcomeKeptDiffers)
	}
	if !bytes.Equal(env.files[customClaudeFull], edited) {
		t.Errorf("file on disk = %q, want the user's edit untouched", env.files[customClaudeFull])
	}

	forced := claudeCustomEnv()
	forced.files[customClaudeFull] = edited

	results, err = InstallCustom(forced, InstallOptions{Force: true}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: rendered}})
	if err != nil {
		t.Fatalf("InstallCustom(Force): %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeOverwrote {
		t.Fatalf("results = %+v, want one %v", results, OutcomeOverwrote)
	}
	if !bytes.Equal(forced.files[customClaudeFull], rendered) {
		t.Errorf("file on disk = %q, want the rendered bytes", forced.files[customClaudeFull])
	}
}

func TestInstallCustomRefusesAShippedName(t *testing.T) {
	env := claudeCustomEnv()

	results, err := InstallCustom(env, InstallOptions{}, []CustomDoc{{Kind: "claude", Name: "reviewer", Bytes: []byte("mine\n")}})
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeError {
		t.Fatalf("results = %+v, want one %v", results, OutcomeError)
	}
	if !strings.Contains(results[0].Err, "reviewer is a shipped agent") {
		t.Errorf("err = %q, want it to name reviewer as shipped", results[0].Err)
	}
	if len(env.writes) != 0 || env.saves != 0 {
		t.Errorf("writes = %v, saves = %d, want nothing written for a shipped name", env.writes, env.saves)
	}
}

func TestInstallCustomSkipsKindsNotOnPathAndFilters(t *testing.T) {
	docs := []CustomDoc{
		{Kind: "claude", Name: "my-executor", Bytes: []byte("claude my-executor\n")},
		{Kind: "codex", Name: "my-executor", Bytes: []byte("codex my-executor\n")},
		{Kind: "claude", Name: "my-reviewer", Bytes: []byte("claude my-reviewer\n")},
	}

	env := freshEnv()
	results, err := InstallCustom(env, InstallOptions{}, docs)
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none while no kind's binary is on PATH", results)
	}

	env.lookPaths["claude"] = "/bin/claude"
	results, err = InstallCustom(env, InstallOptions{}, docs)
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 2 || results[0].Role != "my-executor" || results[1].Role != "my-reviewer" {
		t.Fatalf("results = %+v, want the two claude docs", results)
	}

	results, err = InstallCustom(env, InstallOptions{Kind: "codex"}, docs)
	if err != nil {
		t.Fatalf("InstallCustom(Kind=codex): %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("results = %+v, want none: codex is not on PATH", results)
	}

	results, err = InstallCustom(env, InstallOptions{Role: "my-reviewer"}, docs)
	if err != nil {
		t.Fatalf("InstallCustom(Role): %v", err)
	}
	if len(results) != 1 || results[0].Role != "my-reviewer" {
		t.Fatalf("results = %+v, want only my-reviewer", results)
	}
}

func TestInstallCustomDryRunWritesNothing(t *testing.T) {
	env := claudeCustomEnv()
	body := []byte("---\nname: my-executor\n---\nDo the thing.\n")

	results, err := InstallCustom(env, InstallOptions{DryRun: true}, []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: body}})
	if err != nil {
		t.Fatalf("InstallCustom: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != OutcomeWouldWrite {
		t.Fatalf("results = %+v, want one %v", results, OutcomeWouldWrite)
	}
	if len(env.writes) != 0 || len(env.dirs) != 0 {
		t.Errorf("writes = %v, dirs = %v, want nothing written", env.writes, env.dirs)
	}
	if env.saves != 0 {
		t.Errorf("manifest saves = %d, want 0 on a dry run", env.saves)
	}
}

func TestCustomFilesStates(t *testing.T) {
	env := claudeCustomEnv()
	body := []byte("---\nname: my-executor\n---\nDo the thing.\n")
	docs := []CustomDoc{{Kind: "claude", Name: "my-executor", Bytes: body}}

	files, err := CustomFiles(env, docs)
	if err != nil {
		t.Fatalf("CustomFiles(missing): %v", err)
	}
	if len(files) != 1 || files[0].Path != customClaudeFull || files[0].State != FileMissing {
		t.Fatalf("files = %+v, want one missing file at %s", files, customClaudeFull)
	}

	env.files[customClaudeFull] = body
	files, err = CustomFiles(env, docs)
	if err != nil {
		t.Fatalf("CustomFiles(up to date): %v", err)
	}
	if len(files) != 1 || files[0].State != FileUpToDate {
		t.Fatalf("files = %+v, want one %v", files, FileUpToDate)
	}

	env.files[customClaudeFull] = []byte("---\nname: my-executor\n---\nmy own copy\n")
	files, err = CustomFiles(env, docs)
	if err != nil {
		t.Fatalf("CustomFiles(edited): %v", err)
	}
	if len(files) != 1 || files[0].State != FileEdited {
		t.Fatalf("files = %+v, want one %v", files, FileEdited)
	}
}
