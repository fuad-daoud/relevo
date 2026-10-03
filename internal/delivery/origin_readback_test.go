package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// The two payloads of one round, exactly as Queue writes them: the origin line
// OriginLine produces for the kind, a blank line, then the body.
//
// They are built from OriginLine rather than written out, so the read-back
// tests go through the production code they are about. A literal here would
// keep passing while OriginLine changed, which is the failure these tests
// exist to catch.
var (
	reportPayloadRound1 = WithOrigin(
		"The runner finished round 1. Report: /x/001-report.md",
		OriginLine("w", 1, store.DirToMasterMind, store.KindReport))
	haltPayloadRound1 = WithOrigin(
		"artifacts over the cap: 2 > 1 MB. Run: relevo status --name w",
		OriginLine("w", 1, store.DirToMasterMind, store.KindHalt))
)

// sessionRow is one row of the fake session: the turn text a read-back would
// LIKE over.
type sessionRow struct{ text string }

// likeSession is a fake sqlite3 that answers a read-back the way the real
// query does -- by substring -- over a session the test controls.
//
// Modelling the LIKE rather than hardcoding "this origin is present" is the
// point: the defect was that a halt row and a report row shared an origin
// line, so a fake that keyed on the exact line would have kept passing while
// the real query kept colliding.
type likeSession struct {
	rows []sessionRow
	// queries records every non-schema query, so a test can assert on the
	// pattern the LIKE was actually built from.
	queries []string
}

func (f *likeSession) Run(_ context.Context, bin string, args ...string) ([]byte, error) {
	last := ""
	if len(args) > 0 {
		last = args[len(args)-1]
	}
	if strings.Contains(last, "sqlite_master") {
		return []byte(`[{"name":"session_inbox"}]`), nil
	}
	f.queries = append(f.queries, last)

	// The query's patterns are every `%...%` pair it interpolates.
	for _, p := range likePatterns(last) {
		for _, row := range f.rows {
			if strings.Contains(row.text, p) {
				return []byte("1"), nil
			}
		}
	}
	return []byte("0"), nil
}

// likePatterns extracts the SQL string literals that sit between LIKE wildcards
// -- i.e. the origin line the query was built with.
func likePatterns(query string) []string {
	var out []string
	rest := query
	for {
		i := strings.Index(rest, "%")
		if i < 0 {
			return out
		}
		rest = rest[i+1:]
		j := strings.Index(rest, "%")
		if j < 0 {
			return out
		}
		out = append(out, strings.ReplaceAll(rest[:j], "''", "'"))
		rest = rest[j+1:]
	}
}

// TestOpencodeReportNotShadowedBySameRoundHalt pins #961's second defect on the
// opencode side: a session that already holds the round's halt must not cause
// the round's report to be confirmed without a POST.
//
// The read-back is a `LIKE '%origin%'` over the turn text. Before the split, a
// halt and a report of one round shared an origin line, so the halt's row
// satisfied the report's query and Deliver answered "already present" -- the
// report never reached the mastermind at all.
func TestOpencodeReportNotShadowedBySameRoundHalt(t *testing.T) {
	t.Parallel()

	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	stateFile := writeOpencodeServiceFile(t, dir, srv.URL, "pw", 1)
	exec := &likeSession{rows: []sessionRow{{text: haltPayloadRound1}}}

	d := &OpencodeDeliverer{
		StateFiles: []string{stateFile},
		DBPath:     filepath.Join(dir, "opencode.db"),
		Exec:       exec,
		Alive:      aliveAlways,
	}

	// The halt was queued a minute ago; the report is the entry being retried
	// now. The halt row is newer, so only the queuedAt bound excludes it, and
	// the bound is not what is being tested here -- the LIKE is.
	queuedAt := time.Now().Add(-time.Minute)

	// The halt itself confirms from the session, which is what the fixture says
	// the session holds.
	out, reason, err := d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), haltPayloadRound1, "", queuedAt)
	if err != nil {
		t.Fatalf("Deliver(halt): %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("Deliver(halt) = (%v, %q), want OutcomeDelivered/\"already present\"", out, reason)
	}
	if posts != 0 {
		t.Fatalf("POSTs = %d, want 0 for the halt", posts)
	}

	// The report of the same round must still be POSTed: it is a different
	// payload and the session does not hold it.
	out, reason, err = d.Deliver(context.Background(), opencodeMasterMind("ses_abc123"), reportPayloadRound1, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver(report): %v", err)
	}
	if out == OutcomeDelivered && reason == "already present" {
		t.Fatalf("Deliver(report) = (%v, %q): the same-round halt shadowed the report", out, reason)
	}
	if posts != 1 {
		t.Errorf("POSTs = %d, want 1: the report must be POSTed", posts)
	}

	// And the query the report's read-back ran names the report's own origin,
	// so the two are distinguished by the string the LIKE is built from rather
	// than by anything downstream.
	reportOrigin := OriginLine("w", 1, store.DirToMasterMind, store.KindReport)
	haltOrigin := OriginLine("w", 1, store.DirToMasterMind, store.KindHalt)
	var sawReport bool
	for _, q := range exec.queries {
		if strings.Contains(q, reportOrigin) {
			sawReport = true
		}
		if strings.Contains(q, haltOrigin) && strings.Contains(q, reportOrigin) {
			t.Errorf("a query mixed the halt and report origins: %q", q)
		}
	}
	if !sawReport {
		t.Error("no read-back query named the report's origin")
	}
}

// TestAgyReportNotShadowedBySameRoundHalt is TestOpencodeReportNotShadowedBySameRoundHalt
// on the agy side, where the read-back is first-line equality rather than a
// LIKE: an inbox holding the round's halt message must not confirm the round's
// report without a send.
func TestAgyReportNotShadowedBySameRoundHalt(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)

	// The session already holds the halt of round 1, read.
	now := time.Now().UTC()
	writeAgyMessage(t, home, "m-halt", agyTestConv, haltPayloadRound1, now, false)
	markAgyRead(t, home, "m-halt")

	// Fresh enough to stay inside the give-up window: this test is about the
	// read-back, and a payload past the window is answered by the fallback
	// gate before any origin is compared.
	queuedAt := now.Add(-time.Second)

	// The halt confirms off the inbox.
	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, haltPayloadRound1, "", queuedAt)
	if err != nil {
		t.Fatalf("Deliver(halt): %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("Deliver(halt) = (%v, %q), want OutcomeDelivered/\"already present\"", out, reason)
	}
	if fake.calls != 0 {
		t.Fatalf("halt sends = %d, want 0", fake.calls)
	}

	// The report of the same round must still be sent.
	out, reason, err = d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, reportPayloadRound1, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver(report): %v", err)
	}
	if out == OutcomeDelivered && reason == "already present" {
		t.Fatalf("Deliver(report) = (%v, %q): the same-round halt shadowed the report", out, reason)
	}
	if fake.calls != 1 {
		t.Errorf("sends = %d, want 1: the report must be sent", fake.calls)
	}
	if args := fake.lastArgs(); len(args) == 0 {
		t.Fatal("the send carried no argv")
	} else if want := "--title=" + OriginLine("w", 1, store.DirToMasterMind, store.KindReport); args[2] != want {
		t.Errorf("send title = %q, want the report's own origin line %q", args[2], want)
	}

	// Confirm never sends, and the halt in the inbox does not stand in for the
	// report there either.
	before := fake.calls
	out, _, err = d.Confirm(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, reportPayloadRound1, queuedAt)
	if err != nil {
		t.Fatalf("Confirm(report): %v", err)
	}
	if out == OutcomeDelivered {
		t.Errorf("Confirm(report) = %v: the same-round halt in the inbox confirmed the report", out)
	}
	if fake.calls != before {
		t.Errorf("Confirm sent %d times, want none", fake.calls-before)
	}
}

// TestPushedKindsDisagreeOnOriginLine bounds the claim the two read-back tests
// make. Every kind a mastermind can be PUSHED is one of these three, and all
// three lines differ -- that is what a read-back has to tell apart, so it is
// the whole set that matters. Kinds outside it (question, stop, switch, chain,
// edge) are confirmed in place or logged rather than pushed, and their sharing
// the generic line is untouched behaviour this plan does not change.
func TestPushedKindsDisagreeOnOriginLine(t *testing.T) {
	t.Parallel()

	report := OriginLine("w", 1, store.DirToMasterMind, store.KindReport)
	halt := OriginLine("w", 1, store.DirToMasterMind, store.KindHalt)
	findings := OriginLine("w", 1, store.DirToMasterMind, store.KindFindings)

	lines := map[string]string{"report": report, "halt": halt, "findings": findings}
	names := []string{"report", "halt", "findings"}
	for i, a := range names {
		for _, b := range names[i+1:] {
			if lines[a] == lines[b] {
				t.Errorf("%s and %s share the origin line %q", a, b, lines[a])
			}
			if strings.Contains(lines[a], lines[b]) || strings.Contains(lines[b], lines[a]) {
				t.Errorf("%s (%q) and %s (%q) are not substring-distinct", a, lines[a], b, lines[b])
			}
		}
	}

	// The to_builder line is a different direction entirely and must not be
	// reachable from a to_mastermind one either.
	builder := OriginLine("w", 1, store.DirToBuilder, store.KindReport)
	for _, k := range names {
		if strings.Contains(lines[k], builder) || strings.Contains(builder, lines[k]) {
			t.Errorf("the to_builder line %q and the %s line %q are not substring-distinct", builder, k, lines[k])
		}
	}
}