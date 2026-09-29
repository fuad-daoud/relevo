package relevo

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
)

func TestMatchDenial(t *testing.T) {
	t.Parallel()

	claudeHarness, _ := harness.Lookup("claude")
	agyHarness, _ := harness.Lookup("agy")
	opencodeHarness, _ := harness.Lookup("opencode")

	// for each kind one fixture tail whose last lines include a denial line -> that line returned
	claudeTail := "thinking...\nPermission to use Bash was denied by the user.\n"
	if line, ok := matchDenial(claudeTail, denialPatterns(candidate.Candidate{}, claudeHarness)); !ok || line != "Permission to use Bash was denied by the user." {
		t.Errorf("claude fixture: got (%q, %v), want (\"Permission to use Bash was denied by the user.\", true)", line, ok)
	}

	agyTail := "info: starting tool\nTool call rejected: not permitted in plan mode\n"
	if line, ok := matchDenial(agyTail, denialPatterns(candidate.Candidate{}, agyHarness)); !ok || line != "Tool call rejected: not permitted in plan mode" {
		t.Errorf("agy fixture: got (%q, %v), want (\"Tool call rejected: not permitted in plan mode\", true)", line, ok)
	}

	opencodeTail := "reading files...\nPermission denied: read operation rejected\n"
	if line, ok := matchDenial(opencodeTail, denialPatterns(candidate.Candidate{}, opencodeHarness)); !ok || line != "Permission denied: read operation rejected" {
		t.Errorf("opencode fixture: got (%q, %v), want (\"Permission denied: read operation rejected\", true)", line, ok)
	}

	// a tail with an earlier denial and a later benign line -> the denial line (scan finds the latest matching, not the last line)
	earlierDenialTail := "Permission denied: access refused\nI apologize for that.\nHow can I help further?\n"
	if line, ok := matchDenial(earlierDenialTail, denialPatterns(candidate.Candidate{}, opencodeHarness)); !ok || line != "Permission denied: access refused" {
		t.Errorf("earlier denial: got (%q, %v), want (\"Permission denied: access refused\", true)", line, ok)
	}

	// a benign tail -> ok false
	benignTail := "Everything ran successfully.\nAll tests passed.\n"
	if line, ok := matchDenial(benignTail, denialPatterns(candidate.Candidate{}, claudeHarness)); ok {
		t.Errorf("benign tail matched unexpectedly: %q", line)
	}

	// candidate override replaces defaults
	candOverride := candidate.Candidate{
		DenialPatterns: []string{`(?i)custom permission denial`},
	}
	customTail := "custom permission denial occurred\n"
	if line, ok := matchDenial(customTail, denialPatterns(candOverride, claudeHarness)); !ok || line != "custom permission denial occurred" {
		t.Errorf("candidate override: got (%q, %v), want (\"custom permission denial occurred\", true)", line, ok)
	}
	// claude default should NOT match when candidate overrides
	if _, ok := matchDenial(claudeTail, denialPatterns(candOverride, claudeHarness)); ok {
		t.Errorf("candidate override should have replaced default, but default matched")
	}
}
