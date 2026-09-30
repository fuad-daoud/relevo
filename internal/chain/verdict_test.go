package chain

import (
	"strings"
	"testing"
)

func block(keys ...string) []byte {
	return []byte("prose\n\n```relevo\n" + strings.Join(keys, "\n") + "\n```\n")
}

// fenced renders one relevo block without block's prose prefix, so a test can
// stack more than one block in a body.
func fenced(keys ...string) string {
	return "```relevo\n" + strings.Join(keys, "\n") + "\n```\n"
}

// statusBlock is relevo's own status tail, the shape a reader prompt asks a
// member to append after its verdict or findings block.
func statusBlock() string {
	return fenced(
		"status: done",
		`halted_at: ""`,
		"changed_paths: []",
		"commands_run: []",
		"not_done: []",
	)
}

func TestParseVerdictReadsPassAndChanges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body []byte
		want Verdict
	}{
		{"pass", []byte("```relevo\nverdict: pass\n```\n"), VerdictPass},
		{"changes", []byte("```relevo\nverdict: changes\n```\n"), VerdictChanges},
		{"quoted pass", []byte("```relevo\nverdict: \"pass\"\n```\n"), VerdictPass},
		{"after prose", []byte("a report\n\n```relevo\nverdict: pass\n```\n"), VerdictPass},
		{"with a comment", []byte("```relevo\nverdict: changes   # needs work\n```\n"), VerdictChanges},
	} {
		if got := ParseVerdict(tc.body); got != tc.want {
			t.Fatalf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestParseVerdictReadsAVerdictBeforeTheStatusBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		verdict string
		want    Verdict
	}{
		{"pass", VerdictPass},
		{"changes", VerdictChanges},
	} {
		body := []byte(fenced("verdict: "+tc.verdict) + "\n" + statusBlock())
		if got := ParseVerdict(body); got != tc.want {
			t.Fatalf("verdict %q before a status block: want %q, got %q", tc.verdict, tc.want, got)
		}
	}
}

func TestParseVerdictLastVerdictBlockWins(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body []byte
		want Verdict
	}{
		{
			name: "pass then changes",
			body: []byte(fenced("verdict: pass") + "prose between\n\n" + fenced("verdict: changes")),
			want: VerdictChanges,
		},
		{
			name: "changes then pass",
			body: []byte(fenced("verdict: changes") + "prose between\n\n" + fenced("verdict: pass")),
			want: VerdictPass,
		},
		{
			name: "verdict, status, verdict",
			body: []byte(fenced("verdict: changes") + statusBlock() + fenced("verdict: pass")),
			want: VerdictPass,
		},
	}
	for _, tc := range cases {
		if got := ParseVerdict(tc.body); got != tc.want {
			t.Fatalf("%s: want %q, got %q", tc.name, tc.want, got)
		}
	}
}

func TestParseVerdictRejectsMissingBlock(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{
		nil,
		[]byte("no block here\n"),
		[]byte("```relevo\nverdict: pass\n"),
		[]byte("```relevo\nverdict: pass\n```\ntrailing prose\n"),
	} {
		if got := ParseVerdict(body); got != "" {
			t.Fatalf("ParseVerdict(%q): want no verdict, got %q", body, got)
		}
	}
}

func TestParseVerdictRejectsMissingKey(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{block("status: done"), []byte("```relevo\n\n```\n"), []byte(statusBlock())} {
		if got := ParseVerdict(body); got != "" {
			t.Fatalf("ParseVerdict(%q): want no verdict, got %q", body, got)
		}
	}
}

func TestParseVerdictRejectsUnknownValue(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{
		block("verdict: maybe"),
		block("verdict: PASS"),
		block("verdict: changes please"),
		[]byte(fenced("verdict: pass") + fenced("verdict: maybe")),
	} {
		if got := ParseVerdict(body); got != "" {
			t.Fatalf("ParseVerdict(%q): want no verdict, got %q", body, got)
		}
	}
}

func TestParseFindingsReadsACount(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		body []byte
		want int
	}{
		{block("findings: 0"), 0},
		{block("findings: 3"), 3},
		{block("findings: \"4\""), 4},
	} {
		got, ok := ParseFindings(tc.body)
		if !ok || got != tc.want {
			t.Fatalf("ParseFindings(%q): want (%d, true), got (%d, %v)", tc.body, tc.want, got, ok)
		}
	}
}

func TestParseFindingsReadsACountBeforeAStatusBlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		count string
		want  int
	}{
		{"1", 1},
		{"0", 0},
	} {
		body := []byte(fenced("findings: "+tc.count) + "\n" + statusBlock())
		got, ok := ParseFindings(body)
		if !ok || got != tc.want {
			t.Fatalf("findings: %s before a status block: want (%d, true), got (%d, %v)", tc.count, tc.want, got, ok)
		}
	}
}

func TestParseFindingsLastFindingsBlockWins(t *testing.T) {
	t.Parallel()
	body := []byte(fenced("findings: 3") + "prose between\n\n" + fenced("findings: 0"))
	got, ok := ParseFindings(body)
	if !ok || got != 0 {
		t.Fatalf("findings: 3 then findings: 0: want (0, true), got (%d, %v)", got, ok)
	}
}

func TestParseFindingsRejectsMissingKey(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{
		[]byte("no block\n"),
		block("verdict: pass"),
		[]byte("```relevo\n\n```\n"),
	} {
		if got, ok := ParseFindings(body); ok {
			t.Fatalf("ParseFindings(%q): want false, got (%d, true)", body, got)
		}
	}
}

func TestParseFindingsRejectsNonNumeric(t *testing.T) {
	t.Parallel()
	for _, body := range [][]byte{
		block("findings: many"),
		block("findings: 1.5"),
		block("findings: -1"),
		block("findings:"),
	} {
		if got, ok := ParseFindings(body); ok {
			t.Fatalf("ParseFindings(%q): want false, got (%d, true)", body, got)
		}
	}
}
