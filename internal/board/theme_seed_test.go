package board

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/board/theme"
)

// seedTheme is one palette of the embedded seed file, which is the stored set
// a machine that has never been seeded resolves against.
func seedTheme(t *testing.T, name string) *Theme {
	t.Helper()
	th, err := theme.Lookup(name, theme.Seed())
	if err != nil {
		t.Fatalf("Lookup(%s): %v", name, err)
	}
	return &th
}
