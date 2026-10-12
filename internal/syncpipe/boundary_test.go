package syncpipe

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/synclog"
	"github.com/fuad-daoud/relevo/internal/syncworker"
)

func TestABoundaryRefusalReadsAsErrBoundary(t *testing.T) {
	err := refusalError(syncworker.Response{Code: syncworker.CodeBoundary, Error: "wanted 2, got 3"})
	if !errors.Is(err, synclog.ErrBoundary) {
		t.Errorf("refusal = %v, want synclog.ErrBoundary", err)
	}
}

func TestMarkStateCarriesTheBoundaryTags(t *testing.T) {
	got := markStateFromWire(syncworker.MarkState{
		SignedFrom: map[string]int{"m1": 2},
		SignedTags: map[string][]byte{"m1": {1, 2}},
	})
	if got.SignedFrom["m1"] != 2 || len(got.SignedTags["m1"]) != 2 {
		t.Errorf("state = %+v, want the boundary and its tag", got)
	}
}
