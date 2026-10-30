package serviceunit

import (
	"bytes"
	"testing"

	"github.com/fuad-daoud/relevo/internal/shipped"
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
	got := DecideUnitAction(PlistName, CanonicalPlist(rendered, bin, home), true, tpl, shipped.UnitBefore)
	if got != ActionCurrent {
		t.Errorf("a freshly rendered plist = %q, want %q", got, ActionCurrent)
	}
}

func TestServiceRenderReplacesPlaceholders(t *testing.T) {
	const bin, path = "/home/u/.local/bin/relevo",
		"/home/u/.local/bin:/home/u/go/bin:/usr/local/bin:/usr/bin:/bin:/home/u/npm-global/bin:/home/u/.bun/bin"
	rendered := RenderService(ShippedService(), bin, path)
	if bytes.Contains(rendered, []byte("@BIN@")) || bytes.Contains(rendered, []byte("@PATH@")) {
		t.Fatal("RenderService left a placeholder")
	}
	want := "ExecStart=" + bin + " daemon --interval 2s"
	if !bytes.Contains(rendered, []byte(want)) {
		t.Errorf("RenderService did not put %q in ExecStart", bin)
	}
	if !bytes.Contains(rendered, []byte("Environment=PATH="+path)) {
		t.Error("RenderService did not put the assembled PATH in Environment=PATH=")
	}
}

func TestServiceCanonicalRoundTrip(t *testing.T) {
	const bin, path = "/home/u/.local/bin/relevo", "/home/u/.local/bin:/usr/local/bin:/bin:/home/u/.bun/bin"
	rendered := RenderService(ShippedService(), bin, path)
	if got := CanonicalService(rendered, bin, path); !bytes.Equal(got, ShippedService()) {
		t.Error("CanonicalService(RenderService(t)) != t")
	}
	got := DecideUnitAction(ServiceName, CanonicalService(rendered, bin, path), true, ShippedService(), shipped.UnitBefore)
	if got != ActionCurrent {
		t.Errorf("a freshly rendered service = %q, want %q", got, ActionCurrent)
	}
}

// TestServiceCanonicalSurvivesHarnessDrift pins that a unit rendered before a
// harness moved still reads as current: the PATH line reverts whole when the
// value assembled now differs from the one written.
func TestServiceCanonicalSurvivesHarnessDrift(t *testing.T) {
	const bin = "/home/u/.local/bin/relevo"
	rendered := RenderService(ShippedService(), bin, "/usr/bin:/bin:/home/u/.bun/bin")
	got := CanonicalService(rendered, bin, "/usr/bin:/bin:/home/u/.npm-global/bin")
	if !bytes.Equal(got, ShippedService()) {
		t.Fatalf("drifted unit did not canonicalise to the template:\n%s", got)
	}
	if bytes.Equal(CanonicalService(rendered, "/opt/other/relevo", "/usr/bin"), ShippedService()) {
		t.Fatal("a unit naming another binary canonicalised to the template")
	}
}

// oldServiceUnit is relevo.service as pre-template releases shipped it: a
// fixed PATH list and %h in ExecStart, written to disk byte for byte. Its
// sha256 is e884d43199e3d68927e1418f05c522378b8ecc1583f16112a713ac083428c210
// in the shipped index.
const oldServiceUnit = `[Unit]
Description=relevo - MasterMind/builder handoff
After=default.target
# On 2026-09-23 the systemd default start limit (5 starts in 10 s) left the
# daemon down until ` + "`systemctl --user reset-failed`" + `: Restart=on-failure had
# exhausted the limit. RestartSec=5 already paces restarts, so the limit is
# disabled rather than tuned (#370 §4.9).
StartLimitIntervalSec=0

[Service]
Type=simple
# The daemon execs the harness CLIs by PATH lookup, and a user unit inherits
# no login shell PATH, so it has to be named here.
Environment=PATH=%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin
ExecStart=%h/.local/bin/relevo daemon --interval 2s
Restart=on-failure
RestartSec=5
# No MemoryMax: builders, gates and consults run in their own relevo-*.scope
# units and survive a restart of this one (verified 2026-09-23, #370). A
# builder is in this cgroup only when scopes are unavailable (` + "`relevo doctor`" + `
# -> ` + "`restart`" + `), which is why there is still no memory cap here: a cap sized
# for the daemon (once 128M) OOM-killed a single ` + "`claude -p`" + `, and the default
# OOMPolicy=stop then took the daemon down with it (#120).
# An OOM-killed builder is an exited builder: the daemon sees it on the
# next tick and switches or halts as policy says. The daemon itself
# holds no long-lived state; it rebuilds from ~/.local/state/relevo and a
# live builder probe on every start.
OOMPolicy=continue
# The daemon owns the database; under memory pressure systemd-oomd should take
# a round scope or another app first.
ManagedOOMPreference=avoid

[Install]
WantedBy=default.target
`

// A pre-template unit's bytes are the raw bytes the index holds, so the unit
// an older release wrote stays stale instead of reading as a user edit.
func TestOldServiceUnitIsStillStale(t *testing.T) {
	got := DecideUnitAction(ServiceName, []byte(oldServiceUnit), true, ShippedService(), shipped.UnitBefore)
	if got != ActionStale {
		t.Errorf("a pre-template unit = %q, want %q", got, ActionStale)
	}
	// Read-time canonicalisation must leave it byte for byte, even here: the
	// old unit's PATH value can equal a machine's assembled value exactly, so
	// the substitution rides on the binary path, which the old unit never
	// carries.
	bin, path := "/home/u/.local/bin/relevo", "%h/.local/bin:%h/go/bin:/usr/local/bin:/usr/bin:/bin"
	if canon := CanonicalService([]byte(oldServiceUnit), bin, path); !bytes.Equal(canon, []byte(oldServiceUnit)) {
		t.Error("CanonicalService rewrote a pre-template unit")
	}
}

func TestHandEditedServiceUnitIsEdited(t *testing.T) {
	edited := []byte("[Service]\nExecStart=/opt/relevo daemon --interval 2s\n")
	got := DecideUnitAction(ServiceName, edited, true, ShippedService(), shipped.UnitBefore)
	if got != ActionEdited {
		t.Errorf("an unshipped unit = %q, want %q", got, ActionEdited)
	}
}

func TestShippedServiceIsCurrentAgainstItself(t *testing.T) {
	cur := ShippedService()
	if got := DecideUnitAction(ServiceName, cur, true, cur, shipped.UnitBefore); got != ActionCurrent {
		t.Errorf("got %q, want current", got)
	}
}
