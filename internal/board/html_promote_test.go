package board

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// promotePair returns a live board with a source annotations file and a repo
// board destination, and the two annotations paths.
func promotePair(t *testing.T, withAnnotations bool) (src, dst, srcAnn, dstAnn string) {
	t.Helper()
	dir := t.TempDir()
	src = htmlBoardFor(t, filepath.Join(dir, "live"), "api")
	dst = filepath.Join(dir, "repo", "docs", "boards", "api", htmlBoardFile)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if withAnnotations {
		srcAnn = writeAnnotations(t, src,
			`[{"id":"a1","selector":"#hero","x":0.5,"y":0.5,"text":"hi","by":"human","at":"2026-10-01T00:00:00Z"}]`)
	}
	return src, dst, srcAnn, AnnotationsPath(dst)
}

func writeTargetAnnotations(t *testing.T, dstAnn, body string) {
	t.Helper()
	if err := os.WriteFile(dstAnn, []byte(body), 0o600); err != nil {
		t.Fatalf("write target annotations: %v", err)
	}
}

// TestPromoteCopiesAnnotations pins D7: promoting a board that has notes brings
// the notes with it, byte for byte.
func TestPromoteCopiesAnnotations(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, true)
	copied, err := PromoteWithAnnotations(src, dst, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if !copied {
		t.Error("Promote reported no annotations copied, want copied")
	}
	got, err := os.ReadFile(dstAnn)
	if err != nil {
		t.Fatalf("read promoted annotations: %v", err)
	}
	want, err := os.ReadFile(AnnotationsPath(src))
	if err != nil {
		t.Fatalf("read source annotations: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("promoted annotations = %q, want the source's %q", got, want)
	}
}

// TestPromoteWithoutAnnotationsWritesNone pins that a board with no notes
// promotes no annotations file, and does not leave one behind either.
func TestPromoteWithoutAnnotationsWritesNone(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, false)
	copied, err := PromoteWithAnnotations(src, dst, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if copied {
		t.Error("Promote reported annotations copied for a board with none")
	}
	if _, statErr := os.Stat(dstAnn); !os.IsNotExist(statErr) {
		t.Errorf("Promote created an annotations file: stat error = %v, want not-exist", statErr)
	}
	// And the board itself did land.
	if _, statErr := os.Stat(dst); statErr != nil {
		t.Errorf("Promote did not write the board: %v", statErr)
	}
}

// TestPromoteRefusesExistingAnnotationsWithoutForce pins the check-before-write
// rule: an existing target annotations file without --force refuses, and neither
// file is written. A promotion that half-succeeded would leave a board whose
// notes are the previous board's.
func TestPromoteRefusesExistingAnnotationsWithoutForce(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, true)
	writeTargetAnnotations(t, dstAnn,
		`[{"id":"z9","selector":"","x":0,"y":0,"text":"old","by":"human","at":"2026-10-01T00:00:00Z"}]`)

	_, err := PromoteWithAnnotations(src, dst, false)
	if err == nil || !strings.Contains(err.Error(), ErrInvalid.Error()) {
		t.Fatalf("Promote error = %v, want an invalid refusal", err)
	}
	if !strings.Contains(err.Error(), dstAnn) || !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal %q does not name the annotations path and the flag", err)
	}
	// The board itself was absent, so the refusal came from the annotations
	// check -- which is the point: the annotations target is checked even when
	// the board's own target is free.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a refused promotion wrote the board: stat error = %v", statErr)
	}
	ann, _ := os.ReadFile(dstAnn)
	if !strings.Contains(string(ann), "old") || strings.Contains(string(ann), "hi") {
		t.Errorf("the target annotations were written anyway: %q", ann)
	}
}

// TestPromoteRefusesExistingAnnotationsWhenBothTargetsExist is the case where
// both targets are taken: the board's own refusal comes first, and still nothing
// is written.
func TestPromoteRefusesExistingAnnotationsWhenBothTargetsExist(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, true)
	writeTargetAnnotations(t, dstAnn,
		`[{"id":"z9","selector":"","x":0,"y":0,"text":"old","by":"human","at":"2026-10-01T00:00:00Z"}]`)
	if err := os.WriteFile(dst, []byte("<html>old board</html>"), 0o600); err != nil {
		t.Fatalf("write target board: %v", err)
	}

	_, err := PromoteWithAnnotations(src, dst, false)
	if err == nil {
		t.Fatal("Promote accepted two existing targets without --force")
	}
	board, _ := os.ReadFile(dst)
	if string(board) != "<html>old board</html>" {
		t.Errorf("the target board was written anyway: %q", board)
	}
	ann, _ := os.ReadFile(dstAnn)
	if !strings.Contains(string(ann), "old") || strings.Contains(string(ann), "hi") {
		t.Errorf("the target annotations were written anyway: %q", ann)
	}
}

// TestPromoteForceReplacesAnnotations pins that --force does what it says on the
// notes as well as the board.
func TestPromoteForceReplacesAnnotations(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, true)
	writeTargetAnnotations(t, dstAnn,
		`[{"id":"z9","selector":"","x":0,"y":0,"text":"old","by":"human","at":"2026-10-01T00:00:00Z"}]`)

	copied, err := PromoteWithAnnotations(src, dst, true)
	if err != nil {
		t.Fatalf("Promote --force: %v", err)
	}
	if !copied {
		t.Error("--force reported no annotations copied")
	}
	got, _ := os.ReadFile(dstAnn)
	if strings.Contains(string(got), "old") || !strings.Contains(string(got), "hi") {
		t.Errorf("promoted annotations = %q, want the source's entry", got)
	}
}

// TestPromoteForceRemovesStaleAnnotations pins D7's new sub-decision: with
// --force and no source annotations, a stale target annotations file is removed,
// so a promoted board never inherits another board's pins.
func TestPromoteForceRemovesStaleAnnotations(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, false)
	writeTargetAnnotations(t, dstAnn,
		`[{"id":"z9","selector":"","x":0,"y":0,"text":"stale","by":"human","at":"2026-10-01T00:00:00Z"}]`)

	copied, err := PromoteWithAnnotations(src, dst, true)
	if err != nil {
		t.Fatalf("Promote --force: %v", err)
	}
	if copied {
		t.Error("--force reported annotations copied for a source with none")
	}
	if _, statErr := os.Stat(dstAnn); !os.IsNotExist(statErr) {
		got, _ := os.ReadFile(dstAnn)
		t.Errorf("the stale annotations survived: stat error = %v, contents %q", statErr, got)
	}
}

// TestPromoteRefusesSymlinkedAnnotationsSource pins that a symlinked source
// annotations file is refused rather than copied: promoting a link would give the
// repo board notes that live somewhere else entirely.
func TestPromoteRefusesSymlinkedAnnotationsSource(t *testing.T) {
	src, dst, _, _ := promotePair(t, false)
	elsewhere := filepath.Join(t.TempDir(), "real.json")
	body := `[{"id":"a1","selector":"#hero","x":0.5,"y":0.5,"text":"hi","by":"human","at":"2026-10-01T00:00:00Z"}]`
	if err := os.WriteFile(elsewhere, []byte(body), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(elsewhere, AnnotationsPath(src)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := PromoteWithAnnotations(src, dst, false); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Promote error = %v, want a symlink refusal", err)
	}
	// Nothing landed. The board's own target is absent, so the refusal can only
	// have come from the annotations check -- which is the point: both targets
	// are checked before either write.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a refused promotion wrote the board: stat error = %v", statErr)
	}
}

// TestPromoteKeepsPriorAnnotationsBytes pins that promoting copies bytes rather
// than re-marshalling: the promoted file is the source's file.
func TestPromoteKeepsPriorAnnotationsBytes(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, false)
	srcAnn := AnnotationsPath(src)
	prior := "[\n" +
		`{"id":"a1","selector":"#hero","x":0.5,"y":0.5,"text":"hi","by":"human","at":"2026-10-01T00:00:00Z","tone":"loud"}` +
		"\n]\n"
	if err := os.WriteFile(srcAnn, []byte(prior), 0o600); err != nil {
		t.Fatalf("write source annotations: %v", err)
	}
	if _, err := PromoteWithAnnotations(src, dst, false); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	got, err := os.ReadFile(dstAnn)
	if err != nil {
		t.Fatalf("read promoted: %v", err)
	}
	if string(got) != prior {
		t.Errorf("promoted annotations = %q, want the source's bytes %q", got, prior)
	}
}

// TestPromoteRefusesStaleTargetAnnotationsEvenWhenBoardIsNew is the ordering
// case: board.html is absent at the target and the annotations file is present.
// The annotations check still runs first, so nothing is written.
func TestPromoteRefusesStaleTargetAnnotationsEvenWhenBoardIsNew(t *testing.T) {
	src, dst, _, dstAnn := promotePair(t, true)
	writeTargetAnnotations(t, dstAnn,
		`[{"id":"z9","selector":"","x":0,"y":0,"text":"stale","by":"human","at":"2026-10-01T00:00:00Z"}]`)

	if _, err := PromoteWithAnnotations(src, dst, false); err == nil {
		t.Fatal("Promote accepted an existing target annotations file without --force")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("a refused promotion still wrote the board: stat error = %v", statErr)
	}
}

// TestPromoteAnnotationsAreReadOnlyOnTheSource pins that promoting never edits
// the live board: the source board and its annotations are read, never written.
func TestPromoteAnnotationsAreReadOnlyOnTheSource(t *testing.T) {
	src, dst, srcAnn, _ := promotePair(t, true)
	boardBefore, _ := os.ReadFile(src)
	annBefore, _ := os.ReadFile(srcAnn)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	_ = at

	if _, err := PromoteWithAnnotations(src, dst, true); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if got, _ := os.ReadFile(src); string(got) != string(boardBefore) {
		t.Errorf("the source board changed:\n%q\n%q", boardBefore, got)
	}
	if got, _ := os.ReadFile(srcAnn); string(got) != string(annBefore) {
		t.Errorf("the source annotations changed:\n%q\n%q", annBefore, got)
	}
}
