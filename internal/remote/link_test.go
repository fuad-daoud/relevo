package remote

import "testing"

func TestLinkIsNilWithoutAnOrigin(t *testing.T) {
	if got := Link(BindingView{}); got != nil {
		t.Fatalf("Link(empty view) = %+v, want nil", got)
	}
	for _, v := range []BindingView{{Installation: "inst"}, {ID: "rec"}, {Installation: "inst", ID: "rec"}} {
		got := Link(v)
		if got == nil || got.Installation != v.Installation || got.ID != v.ID {
			t.Errorf("Link(%+v) = %+v, want the view's installation and id", v, got)
		}
	}
}
