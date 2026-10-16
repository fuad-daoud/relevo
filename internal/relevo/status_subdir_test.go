package relevo

import (
	"context"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// TestStatusRowNamesTheRoundDirectory pins the row's cwd: the directory the
// round's processes run in, not the bare tree. Filling the row from b.CWD
// would name the tree root for a bind started below it.
func TestStatusRowNamesTheRoundDirectory(t *testing.T) {
	rt := newRuntime(t)
	b := store.Binding{
		Name: "webshop", CWD: "/trees/api", Subdir: "src/api",
		Round: 1, State: store.StateActive,
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	res, err := Status(context.Background(), rt)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(res.Bindings) != 1 {
		t.Fatalf("rows = %d, want the one binding", len(res.Bindings))
	}
	if got, want := res.Bindings[0].CWD, "/trees/api/src/api"; got != want || !strings.Contains(got, "/src/api") {
		t.Errorf("row cwd = %q, want the round's directory %q", got, want)
	}
}
