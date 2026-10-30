package text_test

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/text"
)

func TestArtifactWordsFollowTheShape(t *testing.T) {
	t.Parallel()

	if got := text.OutputWord(true); got != "output" {
		t.Errorf("OutputWord(reader) = %q, want output", got)
	}
	if got := text.OutputWord(false); got != "report" {
		t.Errorf("OutputWord(writer) = %q, want report", got)
	}
	if got := text.WithoutArtifact(true); got != "without an output" {
		t.Errorf("WithoutArtifact(reader) = %q", got)
	}
	if got := text.WithoutArtifact(false); got != "without a report" {
		t.Errorf("WithoutArtifact(writer) = %q", got)
	}
}

func TestBriefKeepsTheFirstLine(t *testing.T) {
	t.Parallel()

	if got := text.Brief(nil); got != "" {
		t.Errorf("Brief(nil) = %q, want empty", got)
	}
	if got := text.Brief(errors.New("  fatal: bad ref \nhint: run git fetch\n")); got != "fatal: bad ref" {
		t.Errorf("Brief = %q, want the trimmed first line", got)
	}
}
