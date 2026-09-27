package delivery

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

func TestOriginLine(t *testing.T) {
	t.Parallel()

	t.Run("to runner byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToBuilder, store.KindPlan)
		want := `relevo: round 2 · to runner "b1" · from the MasterMind (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind report byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 2, store.DirToMasterMind, store.KindReport)
		want := `relevo: round 2 · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind question byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 3, store.DirToMasterMind, store.KindQuestion)
		want := `relevo: round 3 · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("to mastermind findings byte-exact", func(t *testing.T) {
		got := OriginLine("b1", 1, store.DirToMasterMind, store.KindFindings)
		want := `relevo: consult · to MasterMind · about runner "b1" (not the human)`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestWithOrigin(t *testing.T) {
	t.Parallel()

	origin := `relevo: round 1 · to MasterMind · about runner "b1" (not the human)`

	t.Run("plain payload inserts exactly one blank line", func(t *testing.T) {
		payload := "The runner finished round 1\nReport: /path/to/report"
		got := WithOrigin(payload, origin)
		want := origin + "\n\n" + payload
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("already prefixed returns unchanged", func(t *testing.T) {
		prefixed := origin + "\n\nThe runner finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})

	t.Run("already prefixed with leading whitespace returns unchanged", func(t *testing.T) {
		prefixed := "  " + origin + "\n\nThe runner finished round 1"
		got := WithOrigin(prefixed, origin)
		if got != prefixed {
			t.Fatalf("got %q, want %q", got, prefixed)
		}
	})
}
