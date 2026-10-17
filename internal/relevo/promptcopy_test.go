package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func findPromptArg(argv []string) string {
	for i, arg := range argv {
		if arg == "-p" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	for _, arg := range argv {
		if strings.Contains(arg, "Read: ") {
			return arg
		}
	}
	return ""
}

func seedAgyHeadless(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Runner = fr
	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testAgyRef, MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind agy: %v", err)
	}
	return rt, b
}

func TestStartRoundPromptCopy(t *testing.T) {
	// 1. startRound on an agy candidate at edit:
	// prompt names copy, original Read: line replaced, copy bytes equal staged original.
	fr := newFakeRunner()
	rt, b := seedAgyHeadless(t, fr)
	b.Round = 1
	b.Tier = "edit"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.EnsureOutDir(b.Name); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}

	origPath := rt.Store.PromptPath(b.Name, b.Round)
	origBytes := []byte("# round 1 instructions\nexecute step 1\n")
	if err := os.WriteFile(origPath, origBytes, 0o644); err != nil {
		t.Fatalf("write original prompt: %v", err)
	}

	launchPromptText := fmt.Sprintf("relevo: round 1\nRead: %s\nReport to: out\n", origPath)
	copyPath := rt.Store.PromptCopyPath(b.Name, b.Round)

	_, err := startRound(context.Background(), rt, nil, b, launchPromptText, false)
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	spec := fr.specs[0]
	pArg := findPromptArg(spec.Argv)
	if !strings.Contains(pArg, "Read: "+copyPath+"\n") {
		t.Errorf("argv prompt %q does not contain Read: %s", pArg, copyPath)
	}
	if strings.Contains(pArg, "Read: "+origPath) {
		t.Errorf("argv prompt %q still contains original Read: %s", pArg, origPath)
	}
	copyData, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy file %s: %v", copyPath, err)
	}
	if !bytes.Equal(copyData, origBytes) {
		t.Errorf("copy content = %q, want %q", copyData, origBytes)
	}

	// 2. Same at read tier:
	fr.specs = nil
	b.Round = 2
	b.Tier = "read"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save round 2: %v", err)
	}
	origPath2 := rt.Store.PromptPath(b.Name, b.Round)
	origBytes2 := []byte("# round 2 read instructions\n")
	if err := os.WriteFile(origPath2, origBytes2, 0o644); err != nil {
		t.Fatalf("write orig 2: %v", err)
	}
	copyPath2 := rt.Store.PromptCopyPath(b.Name, b.Round)
	launchPrompt2 := fmt.Sprintf("relevo: round 2\nRead: %s\nReport to: out\n", origPath2)

	_, err = startRound(context.Background(), rt, nil, b, launchPrompt2, false)
	if err != nil {
		t.Fatalf("startRound read: %v", err)
	}
	if len(fr.specs) != 1 {
		t.Fatalf("specs = %d, want 1", len(fr.specs))
	}
	spec2 := fr.specs[0]
	pArg2 := findPromptArg(spec2.Argv)
	if !strings.Contains(pArg2, "Read: "+copyPath2+"\n") {
		t.Errorf("round 2 argv prompt %q does not contain Read: %s", pArg2, copyPath2)
	}
	if strings.Contains(pArg2, "Read: "+origPath2) {
		t.Errorf("round 2 argv prompt %q still contains original Read: %s", pArg2, origPath2)
	}
	copyData2, err := os.ReadFile(copyPath2)
	if err != nil {
		t.Fatalf("read copy file %s: %v", copyPath2, err)
	}
	if !bytes.Equal(copyData2, origBytes2) {
		t.Errorf("round 2 copy content = %q, want %q", copyData2, origBytes2)
	}

	// 3. agy at yolo and at harness, and claude at edit:
	// argv names the original, and no copy file exists.

	// 3a. agy at yolo
	fr.specs = nil
	b.Round = 3
	b.Tier = "yolo"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save round 3: %v", err)
	}
	origPath3 := rt.Store.PromptPath(b.Name, b.Round)
	if err := os.WriteFile(origPath3, []byte("round 3\n"), 0o644); err != nil {
		t.Fatalf("write orig 3: %v", err)
	}
	copyPath3 := rt.Store.PromptCopyPath(b.Name, b.Round)
	launchPrompt3 := fmt.Sprintf("relevo: round 3\nRead: %s\nReport to: out\n", origPath3)

	_, err = startRound(context.Background(), rt, nil, b, launchPrompt3, true)
	if err != nil {
		t.Fatalf("startRound yolo: %v", err)
	}
	spec3 := fr.specs[0]
	pArg3 := findPromptArg(spec3.Argv)
	if !strings.Contains(pArg3, "Read: "+origPath3) {
		t.Errorf("yolo argv prompt %q should name original %s", pArg3, origPath3)
	}
	if _, err := os.Stat(copyPath3); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("yolo copy file %s must not exist: %v", copyPath3, err)
	}

	// 3b. agy at harness
	fr.specs = nil
	b.Round = 4
	b.Tier = "harness"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save round 4: %v", err)
	}
	origPath4 := rt.Store.PromptPath(b.Name, b.Round)
	if err := os.WriteFile(origPath4, []byte("round 4\n"), 0o644); err != nil {
		t.Fatalf("write orig 4: %v", err)
	}
	copyPath4 := rt.Store.PromptCopyPath(b.Name, b.Round)
	launchPrompt4 := fmt.Sprintf("relevo: round 4\nRead: %s\nReport to: out\n", origPath4)

	_, err = startRound(context.Background(), rt, nil, b, launchPrompt4, false)
	if err != nil {
		t.Fatalf("startRound harness: %v", err)
	}
	spec4 := fr.specs[0]
	pArg4 := findPromptArg(spec4.Argv)
	if !strings.Contains(pArg4, "Read: "+origPath4) {
		t.Errorf("harness tier argv prompt %q should name original %s", pArg4, origPath4)
	}
	if _, err := os.Stat(copyPath4); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("harness tier copy file %s must not exist: %v", copyPath4, err)
	}

	// 3c. claude at edit:
	fr.specs = nil
	bClaude, err := Bind(context.Background(), rt, BindOptions{
		Name: "claude-test", Candidate: testClaudeRef, Tier: "edit", MasterMindID: testMasterMindName, CWD: "/claude-repo",
	})
	if err != nil {
		t.Fatalf("Bind claude: %v", err)
	}
	origPathClaude := rt.Store.PromptPath(bClaude.Name, bClaude.Round)
	if err := os.WriteFile(origPathClaude, []byte("claude prompt\n"), 0o644); err != nil {
		t.Fatalf("write claude orig: %v", err)
	}
	copyPathClaude := rt.Store.PromptCopyPath(bClaude.Name, bClaude.Round)
	launchClaude := fmt.Sprintf("relevo: round 1\nRead: %s\nReport to: out\n", origPathClaude)

	_, err = startRound(context.Background(), rt, nil, bClaude, launchClaude, false)
	if err != nil {
		t.Fatalf("startRound claude edit: %v", err)
	}
	specClaude := fr.specs[0]
	pArgClaude := findPromptArg(specClaude.Argv)
	if !strings.Contains(pArgClaude, "Read: "+origPathClaude) {
		t.Errorf("claude argv prompt %q should name original %s", pArgClaude, origPathClaude)
	}
	if _, err := os.Stat(copyPathClaude); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("claude copy file %s must not exist: %v", copyPathClaude, err)
	}
	wantGrant := "--allowedTools=Read(/" + origPathClaude + ")"
	foundGrant := false
	for _, a := range specClaude.Argv {
		if a == wantGrant {
			foundGrant = true
			break
		}
	}
	if !foundGrant {
		t.Errorf("claude argv %v missing allowedTools grant %s", specClaude.Argv, wantGrant)
	}
}

func TestResumeAndSecondStartRoundRewritesCopy(t *testing.T) {
	// 4. resumeRound for agy at edit, and a second startRound for the same round (relaunch/switch):
	// the copy is rewritten from the original after being overwritten with garbage.
	fr := newFakeRunner()
	rt, b := seedAgyHeadless(t, fr)
	b.Round = 1
	b.Tier = "edit"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.EnsureOutDir(b.Name); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}

	origPath := rt.Store.PromptPath(b.Name, b.Round)
	origBytes := []byte("# original prompt for round 1\n")
	if err := os.WriteFile(origPath, origBytes, 0o644); err != nil {
		t.Fatalf("write original prompt: %v", err)
	}

	launchPromptText := fmt.Sprintf("relevo: round 1\nRead: %s\nReport to: out\n", origPath)
	copyPath := rt.Store.PromptCopyPath(b.Name, b.Round)

	// First start: copy created
	_, err := startRound(context.Background(), rt, nil, b, launchPromptText, false)
	if err != nil {
		t.Fatalf("first startRound: %v", err)
	}

	// Overwrite copy with garbage
	garbage1 := []byte("garbage content 1")
	if err := os.WriteFile(copyPath, garbage1, 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// Second startRound for the same round (relaunch / switch)
	_, err = startRound(context.Background(), rt, nil, b, launchPromptText, false)
	if err != nil {
		t.Fatalf("second startRound: %v", err)
	}
	data, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(data, origBytes) {
		t.Errorf("after second startRound, copy = %q, want original %q", data, origBytes)
	}

	// Overwrite with garbage again
	garbage2 := []byte("garbage content 2")
	if err := os.WriteFile(copyPath, garbage2, 0o644); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// resumeRound for the same round
	_, err = resumeRound(context.Background(), rt, nil, b, "test-session-id", launchPromptText, false)
	if err != nil {
		t.Fatalf("resumeRound: %v", err)
	}
	data2, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy after resume: %v", err)
	}
	if !bytes.Equal(data2, origBytes) {
		t.Errorf("after resumeRound, copy = %q, want original %q", data2, origBytes)
	}
}

func TestStartRoundPlantedSymlinkRefused(t *testing.T) {
	// 5. A planted symlink at the copy path: stage refuses to follow it (the link target is unchanged).
	fr := newFakeRunner()
	rt, b := seedAgyHeadless(t, fr)
	b.Round = 1
	b.Tier = "edit"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := rt.Store.EnsureOutDir(b.Name); err != nil {
		t.Fatalf("EnsureOutDir: %v", err)
	}

	origPath := rt.Store.PromptPath(b.Name, b.Round)
	origBytes := []byte("# authentic instructions\n")
	if err := os.WriteFile(origPath, origBytes, 0o644); err != nil {
		t.Fatalf("write original prompt: %v", err)
	}

	launchPromptText := fmt.Sprintf("relevo: round 1\nRead: %s\nReport to: out\n", origPath)
	copyPath := rt.Store.PromptCopyPath(b.Name, b.Round)

	// Plant a symlink at copyPath pointing to a sensitive outside file
	targetPath := filepath.Join(t.TempDir(), "target.txt")
	targetSecret := []byte("confidential sensitive file")
	if err := os.WriteFile(targetPath, targetSecret, 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(targetPath, copyPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, err := startRound(context.Background(), rt, nil, b, launchPromptText, false)
	if err != nil {
		t.Fatalf("startRound: %v", err)
	}

	// Target file must be unchanged
	targetData, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if !bytes.Equal(targetData, targetSecret) {
		t.Errorf("target file was modified through symlink: got %q, want %q", targetData, targetSecret)
	}

	// Copy path must be replaced with regular file containing origBytes
	copyData, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if !bytes.Equal(copyData, origBytes) {
		t.Errorf("copy content = %q, want %q", copyData, origBytes)
	}
	fi, err := os.Lstat(copyPath)
	if err != nil {
		t.Fatalf("Lstat copy: %v", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		t.Errorf("copy is still a symlink")
	}
}

func TestSendPreflightPromptCopy(t *testing.T) {
	// 6. sendPreflight / dry run for agy at edit:
	// pf.prompt names the copy and no file is written; with opts.Tier read/edit override
	// on a binding whose stored tier is yolo, the decision follows the override.
	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, testTwoReviewerJSON)
	rt.Runner = newFakeRunner()

	b, err := Bind(context.Background(), rt, BindOptions{
		Name: "agy-preflight", Candidate: testAgyRef, Tier: "edit", MasterMindID: testMasterMindName, CWD: "/repo",
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}

	planFile := writePlan(t, "# test plan")
	origPath := rt.Store.PromptPath(b.Name, b.Round)
	copyPath := rt.Store.PromptCopyPath(b.Name, b.Round)

	// Dry run for agy at edit
	pf, err := sendPreflight(context.Background(), rt, b.Name, planFile, SendOptions{})
	if err != nil {
		t.Fatalf("sendPreflight: %v", err)
	}
	if !strings.Contains(pf.prompt, "Read: "+copyPath+"\n") {
		t.Errorf("pf.prompt %q does not contain Read: %s", pf.prompt, copyPath)
	}
	if strings.Contains(pf.prompt, "Read: "+origPath) {
		t.Errorf("pf.prompt %q contains original Read: %s", pf.prompt, origPath)
	}
	// No file is written during preflight
	if _, err := os.Stat(copyPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry-run must not create copy file %s: %v", copyPath, err)
	}

	// Binding whose stored tier is yolo:
	bYolo, err := Bind(context.Background(), rt, BindOptions{
		Name: "agy-yolo", Candidate: testAgyRef, Tier: "yolo", AllowYolo: true, MasterMindID: testMasterMindName, CWD: "/yolo-repo",
	})
	if err != nil {
		t.Fatalf("Bind yolo: %v", err)
	}
	copyPathYolo := rt.Store.PromptCopyPath(bYolo.Name, bYolo.Round)
	origPathYolo := rt.Store.PromptPath(bYolo.Name, bYolo.Round)

	// Without override, stored tier yolo does not want copy:
	pfYoloNoOverride, err := sendPreflight(context.Background(), rt, bYolo.Name, planFile, SendOptions{AllowYolo: true})
	if err != nil {
		t.Fatalf("sendPreflight yolo without override: %v", err)
	}
	if !strings.Contains(pfYoloNoOverride.prompt, "Read: "+origPathYolo) {
		t.Errorf("pfYoloNoOverride should name original: %s", pfYoloNoOverride.prompt)
	}

	// With opts.Tier edit override on binding stored as yolo:
	pfEditOverride, err := sendPreflight(context.Background(), rt, bYolo.Name, planFile, SendOptions{Tier: "edit"})
	if err != nil {
		t.Fatalf("sendPreflight edit override: %v", err)
	}
	if !strings.Contains(pfEditOverride.prompt, "Read: "+copyPathYolo+"\n") {
		t.Errorf("pfEditOverride prompt %q does not name copy %s", pfEditOverride.prompt, copyPathYolo)
	}
	if _, err := os.Stat(copyPathYolo); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry-run with edit override must not create copy file: %v", err)
	}

	// With opts.Tier read override on binding stored as yolo:
	pfReadOverride, err := sendPreflight(context.Background(), rt, bYolo.Name, planFile, SendOptions{Tier: "read"})
	if err != nil {
		t.Fatalf("sendPreflight read override: %v", err)
	}
	if !strings.Contains(pfReadOverride.prompt, "Read: "+copyPathYolo+"\n") {
		t.Errorf("pfReadOverride prompt %q does not name copy %s", pfReadOverride.prompt, copyPathYolo)
	}
	if _, err := os.Stat(copyPathYolo); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dry-run with read override must not create copy file: %v", err)
	}
}
