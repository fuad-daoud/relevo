package relevo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/store"
)

// scanScopeBinding writes an outgoing builder's bytes followed by the current
// process's into the round's stream, and points the binding's cursor at the
// byte where the current process begins. Both halves are raw stream events
// rendered with the agy kind the setup's builder uses.
func scanScopeBinding(t *testing.T, rt Runtime, b store.Binding, outgoing, current string) store.Binding {
	t.Helper()
	streamWrite(t, rt, outgoing+current)
	b.Builder.StreamRound = b.Round
	b.Builder.StreamStart = int64(len(outgoing))
	return b
}

// TestLimitScanIgnoresThePreviousBuildersLines pins that a limit scan reads
// only what the current builder process wrote: a mid-round switch appends to
// the same stream, so the outgoing process's quota text must not be charged to
// the incoming process's provider.
func TestLimitScanIgnoresThePreviousBuildersLines(t *testing.T) {
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	// The stale line is Google's agy quota failure; the current builder is
	// agy/other/m on provider "other", so a match would gate the wrong
	// provider.
	if got := availability.ProviderOf(b.BuilderCandidate); got != "other" {
		t.Fatalf("BuilderCandidate provider = %q, want the other provider", got)
	}
	outgoing := jsonlLine(t, "agy-errors/results.jsonl", 0)
	current := jsonlLine(t, "agy-errors/results.jsonl", 5)
	b = scanScopeBinding(t, rt, b, outgoing, current)

	patterns := availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate)
	if _, ok := availability.MatchLimit(builderTail(rt, b, availability.LimitScanLines), patterns, rt.Now(), rt.Policy.LimitGateDefault()); !ok {
		t.Fatal("the outgoing line does not match the current builder's patterns: the fixture no longer exercises the bug")
	}

	text := limitText(context.Background(), rt, b)
	if _, ok := availability.MatchLimit(text, patterns, rt.Now(), rt.Policy.LimitGateDefault()); ok {
		t.Errorf("limitText = %q, want no limit line: it must not read the outgoing builder's bytes", text)
	}

	if _, _, handled, err := gateHeadless(t, rt, b, currentBuilderTail(rt, b, availability.LimitScanLines), false); err != nil {
		t.Fatalf("gateOnLimit: %v", err)
	} else if handled {
		t.Error("handled = true, want false: the current builder wrote no limit line")
	}
	if rl := rateLimitedEntries(loadLedger(t, rt)); len(rl) != 0 {
		t.Errorf("rate_limited entries = %+v, want none for provider %q", rl, availability.ProviderOf(b.BuilderCandidate))
	}
}

// TestLimitScanStillSeesTheCurrentBuildersLimit pins the other side of the
// same boundary: a limit line the current process itself wrote, after
// StreamStart, is still matched and gated against the current provider.
func TestLimitScanStillSeesTheCurrentBuildersLimit(t *testing.T) {
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	outgoing := jsonlLine(t, "agy-errors/results.jsonl", 5)
	current := jsonlLine(t, "agy-errors/results.jsonl", 1)
	b = scanScopeBinding(t, rt, b, outgoing, current)

	patterns := availability.LimitPatterns(AvailabilityDeps(rt), b.BuilderCandidate)
	m, ok := availability.MatchLimit(limitText(context.Background(), rt, b), patterns, rt.Now(), rt.Policy.LimitGateDefault())
	if !ok {
		t.Fatal("limitText has no limit line, want the current builder's own")
	}
	if !strings.Contains(m.Line, "Individual quota reached") {
		t.Errorf("matched line = %q, want the current process's quota line", m.Line)
	}

	if _, _, handled, err := gateHeadless(t, rt, b, currentBuilderTail(rt, b, availability.LimitScanLines), false); err != nil {
		t.Fatalf("gateOnLimit: %v", err)
	} else if !handled {
		t.Fatal("handled = false, want true: the current builder's own limit line gates it")
	}
	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 || rl[0].Subject != availability.ProviderOf(b.BuilderCandidate) {
		t.Errorf("rate_limited entries = %+v, want one for %q", rl, availability.ProviderOf(b.BuilderCandidate))
	}
}

// TestDenialScanIgnoresThePreviousBuildersLines pins that a permission-denial
// scan is scoped the same way: a denial the outgoing process hit must not
// block the incoming process.
func TestDenialScanIgnoresThePreviousBuildersLines(t *testing.T) {
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	outgoing := `{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_name":"run_command","tool_info":{"error":{"message":"tool use was rejected: Bash command not allowed"}}}}` + "\n"
	current := jsonlLine(t, "agy-errors/results.jsonl", 5)
	b = scanScopeBinding(t, rt, b, outgoing, current)

	h, _ := harness.Lookup(b.Builder.Kind)
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		t.Fatal(err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		t.Fatal(err)
	}
	patterns := denialPatterns(c, h)
	if _, ok := matchDenial(builderTail(rt, b, availability.LimitScanLines), patterns); !ok {
		t.Fatal("the outgoing line does not match the denial patterns: the fixture no longer exercises the bug")
	}
	if line, ok := matchDenial(currentBuilderTail(rt, b, availability.LimitScanLines), patterns); ok {
		t.Errorf("matchDenial = %q, want none: it must not read the outgoing builder's bytes", line)
	}
}

// TestCurrentBuilderTailFromStaleRoundIsWholeStream pins the fallback: a
// cursor left over from another round names no offset in this round's file,
// so the whole stream is in scope again.
func TestCurrentBuilderTailFromStaleRoundIsWholeStream(t *testing.T) {
	fr := newFakeRunner()
	rt, b := gateOnLimitSetup(t, fr)
	outgoing := jsonlLine(t, "agy-errors/results.jsonl", 0)
	current := jsonlLine(t, "agy-errors/results.jsonl", 5)
	b = scanScopeBinding(t, rt, b, outgoing, current)
	b.Builder.StreamRound = b.Round + 1

	got := currentBuilderTail(rt, b, availability.LimitScanLines)
	want := builderTail(rt, b, availability.LimitScanLines)
	if got != want {
		t.Errorf("currentBuilderTail = %q, want builderTail's whole-stream tail %q", got, want)
	}
	if !strings.Contains(got, "RESOURCE_EXHAUSTED") {
		t.Errorf("currentBuilderTail = %q, want the earlier bytes included for a stale round", got)
	}
}

// TestStreamTailReadFallbackHonoursFrom pins the sealed-round path: when the
// file is not on disk the whole body is read through read, and only the bytes
// at from or later are rendered. A from past the end renders nothing.
func TestStreamTailReadFallbackHonoursFrom(t *testing.T) {
	outgoing := jsonlLine(t, "agy-errors/results.jsonl", 0)
	current := jsonlLine(t, "agy-errors/results.jsonl", 1)
	data := []byte(outgoing + current)
	read := func(string) ([]byte, error) { return data, nil }
	path := filepath.Join(t.TempDir(), "absent.jsonl")

	got := streamTail(path, read, nil, "agy", availability.LimitScanLines, int64(len(outgoing)))
	if !strings.Contains(got, "Resets in 1h43m8s") {
		t.Errorf("streamTail = %q, want the current bytes after from", got)
	}
	if strings.Contains(got, "RESOURCE_EXHAUSTED") {
		t.Errorf("streamTail = %q, want it to ignore the bytes before from", got)
	}

	if got := streamTail(path, read, nil, "agy", availability.LimitScanLines, int64(len(data)+5)); got != "" {
		t.Errorf("streamTail(from past the end) = %q, want empty", got)
	}
}
