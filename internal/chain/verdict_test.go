package chain

import (
	"strings"
	"testing"
)

func block(keys ...string) []byte {
	return []byte("prose\n\n```relevo\n" + strings.Join(keys, "\n") + "\n```\n")
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
	for _, body := range [][]byte{block("status: done"), []byte("```relevo\n\n```\n")} {
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
