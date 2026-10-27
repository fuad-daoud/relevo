package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// allowanceTestJSON is two builders on different providers, so an allowance
// record on one leaves the other pickable.
const allowanceTestJSON = `[
  {"name":"muse-a","harness":"muse","provider":"meta","model":"a","roles":["builder"]},
  {"name":"other-b","harness":"opencode","provider":"other","model":"b","roles":["builder"]}
]`

const (
	allowanceTestFutureMS = 1757003600000 // baseTime plus one hour, in ms
	allowanceTestPastMS   = 1756996400000 // baseTime minus one hour, in ms
)

func allowanceTestStream(windowUsed, weeklyUsed float64, windowReset, weeklyReset int64) string {
	return `{"payload_type":"run.usage","payload":{"allowance":{"window":{"used_percent":` +
		strconv.FormatFloat(windowUsed, 'f', -1, 64) + `,"resets_at_ms":` + strconv.FormatInt(windowReset, 10) +
		`},"weekly":{"used_percent":` + strconv.FormatFloat(weeklyUsed, 'f', -1, 64) +
		`,"resets_at_ms":` + strconv.FormatInt(weeklyReset, 10) + `}},"models":[]}}` + "\n"
}

// seedAllowanceDoc stores one provider account's last-seen allowance on rt's
// gates handle, the way a muse round close would have recorded it.
func seedAllowanceDoc(t *testing.T, rt Runtime, key string, entry availability.AllowanceEntry) {
	t.Helper()
	doc, err := availability.LoadAllowance(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	doc = doc.Observe(key, availability.AllowanceObservation{
		WindowUsed:       entry.WindowUsed,
		WindowResetsAtMS: entry.WindowResetsAtMS,
		WeeklyUsed:       entry.WeeklyUsed,
		WeeklyResetsAtMS: entry.WeeklyResetsAtMS,
		HasWindow:        true,
		HasWeekly:        true,
	})
	if err := availability.SaveAllowance(rt.Gates, doc); err != nil {
		t.Fatal(err)
	}
}

// closeMuseRound closes one local round of binding b through queueReport's
// local-read branch: usage is nil, so the close reads the round's own stream
// and records its allowance.
func closeMuseRound(t *testing.T, rt Runtime, b store.Binding, stream string, shipped *usage.Usage) {
	t.Helper()
	streamPath := rt.Store.StreamPath(b.Name, b.Round)
	if err := os.MkdirAll(filepath.Dir(streamPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(streamPath, []byte(stream), 0o644); err != nil {
		t.Fatal(err)
	}
	reportPath := rt.Store.ReportPath(b.Name, b.Round)
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reportPath, []byte("report body"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		cur, err := tx.Load(b.Name)
		if err != nil {
			return err
		}
		entries, err := tx.ReadLog(b.Name)
		if err != nil {
			return err
		}
		next, err := queueReport(context.Background(), rt, tx, cur, entries, reportPath,
			"done", "test", nil, shipped, nil, nil, "", false, "", scopeVerdict{})
		if err != nil {
			return err
		}
		return tx.Save(next)
	})
	if err != nil {
		t.Fatalf("queueReport: %v", err)
	}
}

func museRoundBinding(name string, mut func(*store.Binding)) store.Binding {
	return inflightBinding(name, "muse/meta/a", func(b *store.Binding) {
		b.CWD = "/repo"
		b.Builder.Kind = "muse"
		b.BuilderAccount = "m1"
		b.RoundStartedAt = baseTime
		if mut != nil {
			mut(b)
		}
	})
}

func TestRoundCloseRecordsTheMuseAllowance(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	b := museRoundBinding("webshop", nil)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	closeMuseRound(t, rt, b, allowanceTestStream(92, 34, allowanceTestFutureMS, allowanceTestFutureMS), nil)
	doc, err := availability.LoadAllowance(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := doc.Accounts["meta@m1"]
	if !ok {
		t.Fatalf("accounts = %+v, want the round's meta@m1 record", doc.Accounts)
	}
	if got.WindowUsed != 92 || got.WindowResetsAtMS != allowanceTestFutureMS {
		t.Errorf("window = %+v, want the stream's last 92", got)
	}
	if got.WeeklyUsed != 34 || got.WeeklyResetsAtMS != allowanceTestFutureMS {
		t.Errorf("weekly = %+v, want the stream's last 34", got)
	}
}

func TestRoundCloseWithShippedUsageRecordsNoAllowance(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	b := museRoundBinding("webshop", nil)
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	shipped := &usage.Usage{Harness: "muse", Provider: "meta"}
	closeMuseRound(t, rt, b, allowanceTestStream(92, 34, allowanceTestFutureMS, allowanceTestFutureMS), shipped)
	doc, err := availability.LoadAllowance(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Accounts) != 0 {
		t.Errorf("accounts = %+v, want none: a remote round ships no local stream", doc.Accounts)
	}
}

func TestRoundCloseRecordsNoAllowanceForANonMuseBuilder(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	b := museRoundBinding("webshop", func(b *store.Binding) {
		b.BuilderCandidate = "opencode/other/b"
		b.Builder.Kind = "opencode"
	})
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	closeMuseRound(t, rt, b, allowanceTestStream(92, 34, allowanceTestFutureMS, allowanceTestFutureMS), nil)
	doc, err := availability.LoadAllowance(rt.Gates)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Accounts) != 0 {
		t.Errorf("accounts = %+v, want none: only muse streams carry allowance", doc.Accounts)
	}
}

func TestAllowanceExhaustionSkipsTheCandidate(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	seedAllowanceDoc(t, rt, "meta", availability.AllowanceEntry{
		WindowUsed: 92, WindowResetsAtMS: allowanceTestFutureMS,
		WeeklyUsed: 3, WeeklyResetsAtMS: allowanceTestFutureMS,
	})
	pol := orderOf("builder", "muse/meta/a", "opencode/other/b")
	res, err := resolveCandidate(rt.Candidates, pol, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate: %v", err)
	}
	if got := res.Token(); got != "opencode/other/b" {
		t.Errorf("Token() = %q, want the other provider's candidate", got)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Token != "muse/meta/a" {
		t.Fatalf("Skipped = %+v, want the exhausted muse candidate", res.Skipped)
	}
	note := ExplainResolution("builder", res)
	if !strings.Contains(note, "muse allowance exhausted: window 92%") {
		t.Errorf("note = %q, want the allowance reason through the skip text", note)
	}
}

func TestAllowanceBelowThresholdPicksExactlyAsBefore(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	pol := orderOf("builder", "muse/meta/a", "opencode/other/b")
	plain, err := resolveCandidate(rt.Candidates, pol, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate without allowance: %v", err)
	}
	seedAllowanceDoc(t, rt, "meta", availability.AllowanceEntry{
		WindowUsed: 84, WindowResetsAtMS: allowanceTestFutureMS,
		WeeklyUsed: 84, WeeklyResetsAtMS: allowanceTestFutureMS,
	})
	gated, err := resolveCandidate(rt.Candidates, pol, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate below threshold: %v", err)
	}
	if gated.Token() != "muse/meta/a" || len(gated.Skipped) != 0 {
		t.Errorf("pick = %+v, want the muse candidate with no skips", gated)
	}
	if a, b := ExplainResolution("builder", plain), ExplainResolution("builder", gated); a != b {
		t.Errorf("notes differ:\n%s\n%s\nwant byte-identical picks", a, b)
	}
}

func TestAllowanceStaleResetPicksNormally(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	seedAllowanceDoc(t, rt, "meta", availability.AllowanceEntry{
		WindowUsed: 99, WindowResetsAtMS: allowanceTestPastMS,
		WeeklyUsed: 99, WeeklyResetsAtMS: allowanceTestPastMS,
	})
	pol := orderOf("builder", "muse/meta/a", "opencode/other/b")
	res, err := resolveCandidate(rt.Candidates, pol, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate: %v", err)
	}
	if got := res.Token(); got != "muse/meta/a" {
		t.Errorf("Token() = %q, want the muse candidate once its resets are past", got)
	}
}

func TestAllowanceExplicitPickProceedsWithAnAdvisorySkip(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceTestJSON)
	seedAllowanceDoc(t, rt, "meta", availability.AllowanceEntry{
		WindowUsed: 92, WindowResetsAtMS: allowanceTestFutureMS,
		WeeklyUsed: 3, WeeklyResetsAtMS: allowanceTestFutureMS,
	})
	res, err := resolveCandidate(rt.Candidates, policy.Policy{}, availability.Gates(AvailabilityDeps(rt)), "muse-a", "builder")
	if err != nil {
		t.Fatalf("resolveCandidate named: %v", err)
	}
	if res.How != HowExplicit || res.Token() != "muse/meta/a" {
		t.Errorf("pick = %+v, want the named candidate run explicitly", res)
	}
	if len(res.Gates) != 1 || res.Gates[0].Kind != availability.AllowanceExhausted {
		t.Errorf("Gates = %+v, want the bypassed allowance skip recorded as advisory", res.Gates)
	}
}

// allowanceBothMuseJSON is two muse builders on different providers, so one
// allowance record cannot cover both and the all-gated refusal is reachable.
const allowanceBothMuseJSON = `[
  {"name":"muse-a","harness":"muse","provider":"meta","model":"a","roles":["builder"]},
  {"name":"muse-b","harness":"muse","provider":"other","model":"b","roles":["builder"]}
]`

func TestAllowanceRefusalNamesWaitingNotClearing(t *testing.T) {
	t.Parallel()

	rt := newRuntime(t)
	rt.Candidates = candidateSet(t, allowanceBothMuseJSON)
	seedAllowanceDoc(t, rt, "meta", availability.AllowanceEntry{
		WindowUsed: 92, WindowResetsAtMS: allowanceTestFutureMS,
		WeeklyUsed: 3, WeeklyResetsAtMS: allowanceTestFutureMS,
	})
	seedAllowanceDoc(t, rt, "other", availability.AllowanceEntry{
		WindowUsed: 99, WindowResetsAtMS: allowanceTestFutureMS,
		WeeklyUsed: 3, WeeklyResetsAtMS: allowanceTestFutureMS,
	})
	pol := orderOf("builder", "muse/meta/a", "muse/other/b")
	_, err := resolveCandidate(rt.Candidates, pol, availability.Gates(AvailabilityDeps(rt)), "", "builder")
	if err == nil {
		t.Fatal("resolveCandidate err = nil, want every candidate gated")
	}
	msg := err.Error()
	if !strings.Contains(msg, "muse allowance exhausted") || !strings.Contains(msg, "running round to finish") {
		t.Errorf("error %q wants the allowance reason and the wait", msg)
	}
	if strings.Contains(msg, "gate --clear") {
		t.Errorf("error %q offers gate --clear for an allowance gate that resets on its own", msg)
	}
}
