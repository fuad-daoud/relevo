package availability

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/candidate"
)

const (
	capQwen  = "opencode/alibaba-tp/qwen"
	capGLM   = "opencode/alibaba-tp/glm"
	capOther = "opencode/other/m"
)

func concurrencyDeps(t *testing.T, counts map[string]int) Deps {
	t.Helper()
	body := `[
	  {"name":"qwen","harness":"opencode","provider":"alibaba-tp","model":"qwen","roles":["builder"],"max_concurrency":2},
	  {"name":"glm","harness":"opencode","provider":"alibaba-tp","model":"glm","roles":["builder"]},
	  {"name":"extra","harness":"opencode","provider":"other","model":"m","roles":["builder"],"max_concurrency":1}
	]`
	set, _, err := candidate.Parse("test", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	d := testDepsWith(t, set)
	d.InFlight = func(string) map[string]int { return counts }
	return d
}

func TestConcurrencyGateAtTheCap(t *testing.T) {
	if g := concurrencyGates(concurrencyDeps(t, map[string]int{capQwen: 1})); len(g) != 0 {
		t.Errorf("under the cap = %+v, want no gate", g)
	}
	g := concurrencyGates(concurrencyDeps(t, map[string]int{capQwen: 2}))
	if len(g) != 1 || g[0].Token != capQwen || g[0].Kind != ConcurrencyLimit || g[0].Note != "2/2" || !g[0].Until.IsZero() || g[0].Source != "relevo" {
		t.Fatalf("at the cap = %+v, want one gate with note 2/2", g)
	}
	if got := g[0].UntilText(); got != "" {
		t.Errorf("UntilText = %q, want empty", got)
	}
	if got := GateText(g[0].Kind, g[0].Until, g[0].Note); got != "2/2" {
		t.Errorf("GateText = %q", got)
	}
	if got := GateKindText(ConcurrencyLimit); got != "at concurrency limit" {
		t.Errorf("GateKindText = %q", got)
	}
}

func TestConcurrencyNilCountMeansNoGate(t *testing.T) {
	d := concurrencyDeps(t, map[string]int{capQwen: 9})
	d.InFlight = nil
	if g := concurrencyGates(d); len(g) != 0 {
		t.Errorf("nil InFlight = %+v, want none", g)
	}
}

func TestConcurrencySkipDoesNotGateTheProvider(t *testing.T) {
	d := concurrencyDeps(t, map[string]int{capQwen: 2})
	gates := Gates(d)
	var capGate bool
	for _, g := range gates {
		switch g.Token {
		case capQwen:
			capGate = g.Kind == ConcurrencyLimit
			if g.Kind == RateLimited {
				t.Errorf("cap recorded as a rate limit: %+v", g)
			}
		case "alibaba-tp":
			t.Errorf("provider gated: %+v", g)
		default:
			t.Errorf("unexpected gate %+v", g)
		}
	}
	if !capGate {
		t.Fatalf("gates = %+v, want the cap gate on %s", gates, capQwen)
	}
	l, err := LoadLedger(d.Gates)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 0 {
		t.Errorf("ledger = %+v, want no entry", l.Entries)
	}
	for _, g := range gates {
		if g.Token == capGLM {
			t.Errorf("sibling %s gated: %+v", capGLM, g)
		}
	}
}

func TestConcurrencyPoolUseIsSharedAcrossModels(t *testing.T) {
	g := concurrencyGates(concurrencyDeps(t, map[string]int{capQwen: 1, capGLM: 1}))
	if len(g) != 1 || g[0].Token != capQwen || g[0].Note != "2/2" {
		t.Errorf("pool of two models at 1+1 = %+v, want qwen gated 2/2", g)
	}
}

func TestConcurrencyExceptIsPassedToTheCount(t *testing.T) {
	d := concurrencyDeps(t, nil)
	var got string
	d.InFlight = func(except string) map[string]int { got = except; return nil }
	d.InFlightExcept = "me"
	concurrencyGates(d)
	if got != "me" {
		t.Errorf("except = %q, want me", got)
	}
}
