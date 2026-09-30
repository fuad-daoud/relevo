package consult

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// relevoBlock wraps body in the fenced block a report or a findings file ends
// with, so the tests below read as the artefact rather than the fence.
func relevoBlock(body string) string {
	return "findings in prose\n\n```relevo\n" + body + "\n```\n"
}

// TestVerifyQuestionNamesEveryFile pins what the reviewer is handed: the plan,
// the report, the diff command and the gate log paths, "none" for a round with
// no gate, and the block template it must answer with. Mutation check: drop any
// one argument from the format string and this fails.
func TestVerifyQuestionNamesEveryFile(t *testing.T) {
	t.Parallel()

	q := verifyQuestion("webshop", 1,
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"git diff tree-a tree-b",
		"/state/webshop/001-gate.log")

	for _, want := range []string{
		"Verify round 1 of binding \"webshop\"",
		"/state/webshop/001-plan.md",
		"/state/webshop/001-report.md",
		"git diff tree-a tree-b",
		"/state/webshop/001-gate.log",
		"verdict: accepted | rejected",
		"reasons: [\"...\"]",
		"```relevo",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("question does not name %q:\n%s", want, q)
		}
	}

	if q := verifyQuestion("webshop", 1, "p", "r", "d", ""); !strings.Contains(q, "Gate:   none") {
		t.Errorf("empty gate log must read \"Gate:   none\", got:\n%s", q)
	}
}

func TestVerifyDiffCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		baseline string
		closed   string
		want     string
	}{
		{"tree-a", "tree-b", "git diff tree-a tree-b"},
		{"", "tree-b", "none"},
		{"tree-a", "", "none"},
	}
	for _, tc := range cases {
		got := VerifyDiffCommand(tc.baseline, tc.closed)
		if got != tc.want {
			t.Errorf("VerifyDiffCommand(%q, %q) = %q, want %q", tc.baseline, tc.closed, got, tc.want)
		}
	}
}

// TestParseVerdict is the table for the block parser: the two verdicts, both
// reason forms, and the unstructured answers that must not be read as a
// judgement.
func TestParseVerdict(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		findings    string
		wantVerdict string
		wantReasons []string
	}{
		{
			name:        "accepted",
			findings:    relevoBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
		{
			name:        "rejected with a JSON reasons array",
			findings:    relevoBlock("verdict: rejected\nreasons: [\"a\", \"b\"]"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "rejected with a YAML-ish reason list",
			findings:    relevoBlock("verdict: rejected\nreasons:\n- a\n- b"),
			wantVerdict: "rejected",
			wantReasons: []string{"a", "b"},
		},
		{
			name:        "no block at all",
			findings:    "just prose, no verdict block",
			wantVerdict: "unstructured",
		},
		{
			name:        "a verdict that is neither word",
			findings:    relevoBlock("verdict: maybe"),
			wantVerdict: "unstructured",
		},
		{
			name: "the last block wins",
			findings: relevoBlock("verdict: rejected\nreasons: [\"stale\"]") +
				"\nmore prose\n\n" + relevoBlock("verdict: accepted"),
			wantVerdict: "accepted",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			verdict, reasons := parseVerdict([]byte(c.findings))
			if verdict != c.wantVerdict {
				t.Errorf("verdict = %q, want %q", verdict, c.wantVerdict)
			}
			if !reflect.DeepEqual(reasons, c.wantReasons) {
				t.Errorf("reasons = %v, want %v", reasons, c.wantReasons)
			}
		})
	}

	if v, _ := parseVerdict(nil); v != "unstructured" {
		t.Errorf("parseVerdict(nil) = %q, want unstructured", v)
	}
}

// TestStageVerifyQuestionRecordsARowAndNoFile pins the staging policy: a verify
// question is row-only, whatever its size. The question is oversized on purpose
// -- the function must be size-blind -- and the ask name is reserved, so the
// staged question is readable from the row and there is no file on disk.
// Mutation check: write the question with os.WriteFile again and this fails:
// the file exists, and ReadFile misses because no row holds the name.
func TestStageVerifyQuestionRecordsARowAndNoFile(t *testing.T) {
	t.Parallel()

	s := store.New(t.TempDir())
	if err := s.Save(store.Binding{Name: "webshop", CWD: "/repo"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	question := strings.Repeat("x", InlineAskMax+1)
	askPath := s.AskPath("webshop", 1, "7f2a3c1d")
	if err := s.WithLock(func(tx *store.Tx) error {
		return stageVerifyQuestion(tx, "webshop", 1, askPath, question)
	}); err != nil {
		t.Fatalf("stageVerifyQuestion: %v", err)
	}

	if _, err := os.Stat(askPath); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("os.Stat(%s) err = %v, want the file not to exist", askPath, err)
	}
	body, err := s.ReadFile(askPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", askPath, err)
	}
	if string(body) != question {
		t.Errorf("ReadFile(%s) = %d bytes, want the staged question", askPath, len(body))
	}
}
