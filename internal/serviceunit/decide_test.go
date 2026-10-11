package serviceunit

import (
	"bytes"
	"testing"
)

func TestDecideUnitAction(t *testing.T) {
	current := []byte("current unit\n")
	old := []byte("older shipped unit\n")
	edited := []byte("hand edited unit\n")
	before := func(doc, sha string) bool {
		return doc == ServiceName && sha == SHA256Hex(old)
	}
	cases := []struct {
		name      string
		installed []byte
		present   bool
		want      Action
	}{
		{"no unit installed does nothing", nil, false, ActionAbsent},
		{"shipped bytes are current", current, true, ActionCurrent},
		{"hash in the index but not current is rewritten", old, true, ActionStale},
		{"hash not in the index is left alone", edited, true, ActionEdited},
	}
	for _, c := range cases {
		got := DecideUnitAction(ServiceName, c.installed, c.present, current, before)
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestDecideUnitActionIndexIsPerUnit(t *testing.T) {
	old := []byte("older shipped unit\n")
	before := func(doc, sha string) bool { return doc == ServiceName && sha == SHA256Hex(old) }
	got := DecideUnitAction(PlistName, old, true, []byte("x"), before)
	if got != ActionEdited {
		t.Errorf("another unit's old blob = %q, want %q", got, ActionEdited)
	}
}

func TestPlistCanonicalRoundTrip(t *testing.T) {
	const bin, home = "/home/u/.local/bin/relevo", "/home/u"
	tpl := ShippedPlistTemplate()
	rendered := RenderPlist(tpl, bin, home)
	if bytes.Contains(rendered, []byte("@BIN@")) || bytes.Contains(rendered, []byte("@HOME@")) {
		t.Fatal("RenderPlist left a placeholder")
	}
	if got := CanonicalPlist(rendered, bin, home); !bytes.Equal(got, tpl) {
		t.Error("CanonicalPlist(RenderPlist(t)) != t")
	}
	got := DecideUnitAction(PlistName, CanonicalPlist(rendered, bin, home), true, tpl, ShippedUnitBefore)
	if got != ActionCurrent {
		t.Errorf("a freshly rendered plist = %q, want %q", got, ActionCurrent)
	}
}

func TestShippedServiceIsCurrentAgainstItself(t *testing.T) {
	cur := ShippedService()
	if got := DecideUnitAction(ServiceName, cur, true, cur, ShippedUnitBefore); got != ActionCurrent {
		t.Errorf("got %q, want current", got)
	}
}
