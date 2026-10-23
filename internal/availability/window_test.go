package availability

import (
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

const windowRef = "opencode/alibaba-tp/qwen"

func windowDeps(t *testing.T, windows string, at string) Deps {
	t.Helper()
	body := `[{"name":"qwen","harness":"opencode","provider":"alibaba-tp","model":"qwen","roles":["builder"],"windows":` + windows + `}]`
	set, _, err := candidate.Parse("test", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	d := testDepsWith(t, set)
	now, err := time.Parse(time.RFC3339, at)
	if err != nil {
		t.Fatal(err)
	}
	d.Now = func() time.Time { return now }
	return d
}

func TestWindowGatesOnlyOutsideTheWindow(t *testing.T) {
	only := `{"only":[{"from":"14:00Z","to":"00:00Z"}]}`
	if g := windowGates(windowDeps(t, only, "2026-10-23T10:00:00Z")); len(g) != 1 || g[0].Kind != Scheduled || g[0].Demote || g[0].Token != windowRef {
		t.Errorf("only outside = %+v, want one blocking gate", g)
	}
	if g := windowGates(windowDeps(t, only, "2026-10-23T15:00:00Z")); len(g) != 0 {
		t.Errorf("only inside = %+v, want none", g)
	}
	avoid := `{"avoid":[{"from":"00:30Z","to":"05:00Z"}]}`
	if g := windowGates(windowDeps(t, avoid, "2026-10-23T01:00:00Z")); len(g) != 1 || !strings.HasPrefix(g[0].Note, "inside window") {
		t.Errorf("avoid inside = %+v, want one gate", g)
	}
	if g := windowGates(windowDeps(t, avoid, "2026-10-23T06:00:00Z")); len(g) != 0 {
		t.Errorf("avoid outside = %+v, want none", g)
	}
	demote := `{"mode":"demote","only":[{"from":"14:00Z","to":"00:00Z"}]}`
	if g := windowGates(windowDeps(t, demote, "2026-10-23T10:00:00Z")); len(g) != 1 || !g[0].Demote {
		t.Errorf("demote = %+v, want one Demote gate", g)
	}
}

func TestWindowGateUntilIsTheNextEdge(t *testing.T) {
	d := windowDeps(t, `{"only":[{"from":"14:00Z","to":"00:00Z"}]}`, "2026-10-23T10:00:00Z")
	g := windowGates(d)
	want := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	if len(g) != 1 || !g[0].Until.Equal(want) {
		t.Errorf("gates = %+v, want Until %v", g, want)
	}
}

func TestWindowGateIsNeverInTheLedger(t *testing.T) {
	d := windowDeps(t, `{"only":[{"from":"14:00Z","to":"00:00Z"}]}`, "2026-10-23T10:00:00Z")
	if _, err := Unavailable(d, windowRef, time.Time{}, "limit"); err != nil {
		t.Fatal(err)
	}
	if got := Gates(d); !hasKind(got, Scheduled) || !hasKind(got, RateLimited) {
		t.Fatalf("Gates = %+v, want both kinds", got)
	}
	l, err := LoadLedger(d.Gates)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range l.Entries {
		if e.Kind == Scheduled {
			t.Errorf("ledger holds a scheduled entry: %+v", e)
		}
	}
	if _, removed, err := Available(d, "alibaba-tp", ClearedByServer); err != nil || removed != 1 {
		t.Fatalf("Available removed %d, err %v, want 1", removed, err)
	}
	if got := Gates(d); !hasKind(got, Scheduled) || hasKind(got, RateLimited) {
		t.Errorf("after clear Gates = %+v, want the window gate only", got)
	}
}

func hasKind(gs []Gate, k Kind) bool {
	for _, g := range gs {
		if g.Kind == k {
			return true
		}
	}
	return false
}

func TestGateTextNamesTheWindow(t *testing.T) {
	d := windowDeps(t, `{"only":[{"from":"14:00Z","to":"00:00Z"}]}`, "2026-10-23T10:00:00Z")
	g := windowGates(d)[0]
	if got := GateText(g.Kind, g.Until, g.Note); got != "outside window 14:00Z-00:00Z" {
		t.Errorf("GateText = %q", got)
	}
	if got := g.UntilText(); got != "" {
		t.Errorf("UntilText = %q, want empty", got)
	}
	if got := GateText(RateLimited, time.Time{}, "x"); got != "rate-limited until cleared" {
		t.Errorf("rate-limited GateText = %q", got)
	}
}
