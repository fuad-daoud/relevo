package burst

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const validFingerprint = "sha256:" + "0f1e2d3c4b5a69788796a5b4c3d2e1f0" + "0f1e2d3c4b5a69788796a5b4c3d2e1f0"

// mutateJSON decodes body, runs fn over the top-level object and re-encodes
// it, so a test names one field's value without restating the document.
func mutateJSON(t *testing.T, body string, fn func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("mutateJSON(%s): %v", body, err)
	}
	if fn != nil {
		fn(m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("mutateJSON(%s): %v", body, err)
	}
	return string(out)
}

func fullRequest() ReportRequest {
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	forwarded := time.Date(2026, 11, 2, 7, 58, 0, 0, time.UTC)
	return ReportRequest{
		Version: ReportVersion, At: at, Reason: ReportReasonInterval,
		Head:    ReportHead{Name: "contabo", CPUs: 4, MemGB: 8},
		Local:   ReportLocal{Running: 3, Queued: 0, Cap: 3},
		Waiting: []ReportWaiting{{ID: "r1", Since: time.Date(2026, 11, 2, 7, 59, 0, 0, time.UTC), Candidate: "cand", Harness: "harn"}},
		Nodes: []ReportNode{{
			ID: "b1", Running: 1, Queued: 0, Unacked: 0, ForwardedAt: &forwarded,
			Rounds:  []ReportRound{{ID: "r2", Candidate: "cand2", Started: time.Date(2026, 11, 2, 7, 57, 0, 0, time.UTC)}},
			Problem: ReportProblemUnreachable,
		}},
	}
}

const fullRequestBody = `{"version":2,"at":"2026-11-02T10:00:00+02:00","reason":"interval",` +
	`"head":{"name":"contabo","cpus":4,"mem_gb":8},"local":{"running":3,"queued":0,"cap":3},` +
	`"waiting":[{"id":"r1","since":"2026-11-02T07:59:00Z","candidate":"cand","harness":"harn"}],` +
	`"nodes":[{"id":"b1","running":1,"queued":0,"unacked":0,"forwarded_at":"2026-11-02T07:58:00Z",` +
	`"rounds":[{"id":"r2","candidate":"cand2","started":"2026-11-02T07:57:00Z"}],"problem":"unreachable"}]}`

const minimalRequestBody = `{"version":2,"at":"2026-11-02T10:00:00Z",` +
	`"head":{"name":"contabo","cpus":4,"mem_gb":8},"local":{"running":0,"queued":0,"cap":3},` +
	`"waiting":[],"nodes":[]}`

// emptyNodesBody is what a provider answers when it has nothing to hand the
// head: an empty list, both optional blocks null.
const emptyNodesBody = `{"version":2,"nodes":[],"preparing":null,"denied":null,"report_every":"30s"}`

func TestValidateRequestAcceptsAFullProviderShapedRequest(t *testing.T) {
	got, notes, err := ValidateRequest([]byte(fullRequestBody))
	if err != nil {
		t.Fatalf("ValidateRequest: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %q, want none for a recognised request", notes)
	}
	if want := fullRequest(); !reflect.DeepEqual(got, want) {
		t.Errorf("request = %+v, want %+v", got, want)
	}
}

func TestValidateRequestAcceptsTheMinimalEmptyLists(t *testing.T) {
	got, notes, err := ValidateRequest([]byte(minimalRequestBody))
	if err != nil || len(notes) != 0 {
		t.Fatalf("ValidateRequest = (%v, %q, %v), want no error and no notes", got, notes, err)
	}
	if got.Reason != "" {
		t.Errorf("absent Reason = %q, want \"\"", got.Reason)
	}
	if len(got.Waiting) != 0 || len(got.Nodes) != 0 {
		t.Errorf("empty lists decoded as %d waiting, %d nodes, want 0 and 0", len(got.Waiting), len(got.Nodes))
	}
}

// TestValidateRequestRefusesVersion1 pins that a v1 request document is
// refused naming the version, as the provider refuses it.
func TestValidateRequestRefusesVersion1(t *testing.T) {
	body := strings.Replace(minimalRequestBody, `"version":2`, `"version":1`, 1)
	_, _, err := ValidateRequest([]byte(body))
	if !refusesWith(t, err, "version: must be 2, got 1") {
		t.Fatalf("err = %v, want refusal of version 1", err)
	}
}

func TestValidateRequestRefusesBadFields(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(map[string]any)
		contains string
	}{
		{name: "missing version", mutate: func(m map[string]any) { delete(m, "version") }, contains: "version: required"},
		{name: "missing at", mutate: func(m map[string]any) { delete(m, "at") }, contains: "at: required"},
		{name: "missing head", mutate: func(m map[string]any) { delete(m, "head") }, contains: "head: required"},
		{name: "missing head name", mutate: func(m map[string]any) { m["head"] = map[string]any{"cpus": 4} }, contains: "head.name: required"},
		{name: "empty head name", mutate: func(m map[string]any) { m["head"] = map[string]any{"name": "", "cpus": 4, "mem_gb": 8} }, contains: "head.name: required"},
		{name: "negative head cpus", mutate: func(m map[string]any) { m["head"] = map[string]any{"name": "h", "cpus": -1, "mem_gb": 8} }, contains: "head.cpus: must be >= 0, got -1"},
		{name: "negative head mem_gb", mutate: func(m map[string]any) { m["head"] = map[string]any{"name": "h", "cpus": 4, "mem_gb": -2} }, contains: "head.mem_gb: must be >= 0, got -2"},
		{name: "missing local", mutate: func(m map[string]any) { delete(m, "local") }, contains: "local: required"},
		{name: "negative local running", mutate: func(m map[string]any) { m["local"] = map[string]any{"running": -1, "queued": 0, "cap": 3} }, contains: "local.running: must be >= 0, got -1"},
		{name: "negative local queued", mutate: func(m map[string]any) { m["local"] = map[string]any{"running": 0, "queued": -1, "cap": 3} }, contains: "local.queued: must be >= 0, got -1"},
		{name: "negative local cap", mutate: func(m map[string]any) { m["local"] = map[string]any{"running": 0, "queued": 0, "cap": -3} }, contains: "local.cap: must be >= 0, got -3"},
		{name: "missing waiting", mutate: func(m map[string]any) { delete(m, "waiting") }, contains: "waiting: required"},
		{name: "null waiting", mutate: func(m map[string]any) { m["waiting"] = nil }, contains: "waiting: required"},
		{name: "missing nodes", mutate: func(m map[string]any) { delete(m, "nodes") }, contains: "nodes: required"},
		{name: "waiting without id", mutate: func(m map[string]any) { m["waiting"] = []any{map[string]any{"since": "2026-11-02T09:59:00Z"}} }, contains: "waiting[0].id: required"},
		{name: "waiting with empty id", mutate: func(m map[string]any) {
			m["waiting"] = []any{map[string]any{"id": "", "since": "2026-11-02T09:59:00Z"}}
		}, contains: "waiting[0].id: required"},
		{name: "waiting without since", mutate: func(m map[string]any) { m["waiting"] = []any{map[string]any{"id": "r1"}} }, contains: "waiting[0].since: required"},
		{name: "node without id", mutate: func(m map[string]any) { m["nodes"] = []any{map[string]any{"running": 0}} }, contains: "nodes[0].id: required"},
		{name: "node negative running", mutate: func(m map[string]any) { m["nodes"] = []any{map[string]any{"id": "b1", "running": -1}} }, contains: "nodes[0].running: must be >= 0, got -1"},
		{name: "node negative queued", mutate: func(m map[string]any) { m["nodes"] = []any{map[string]any{"id": "b1", "queued": -1}} }, contains: "nodes[0].queued: must be >= 0, got -1"},
		{name: "node negative unacked", mutate: func(m map[string]any) { m["nodes"] = []any{map[string]any{"id": "b1", "unacked": -1}} }, contains: "nodes[0].unacked: must be >= 0, got -1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := mutateJSON(t, minimalRequestBody, tt.mutate)
			_, _, err := ValidateRequest([]byte(body))
			if !refusesWith(t, err, tt.contains) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tt.contains)
			}
		})
	}
}

func TestValidateRequestRefusesUnreadableJSON(t *testing.T) {
	_, _, err := ValidateRequest([]byte(`{"version":2,`))
	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("err = %v, want one wrapping ErrBadInput", err)
	}
}

// TestValidateRequestKeepsAnUnknownReasonAsEmptyAndNotesIt pins the provider
// normalisation: a reason outside the closed list is kept as "" and returned
// as a note naming the field, never an error.
func TestValidateRequestKeepsAnUnknownReasonAsEmptyAndNotesIt(t *testing.T) {
	body := mutateJSON(t, minimalRequestBody, func(m map[string]any) { m["reason"] = "heartbeat" })
	got, notes, err := ValidateRequest([]byte(body))
	if err != nil {
		t.Fatalf("ValidateRequest: %v", err)
	}
	if got.Reason != "" {
		t.Errorf("Reason = %q, want \"\" for an unrecognised value", got.Reason)
	}
	if want := []string{"reason"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
}

func TestValidateRequestKeepsAnEmptyReasonUnnoted(t *testing.T) {
	body := mutateJSON(t, minimalRequestBody, func(m map[string]any) { m["reason"] = "" })
	got, notes, err := ValidateRequest([]byte(body))
	if err != nil || got.Reason != "" || len(notes) != 0 {
		t.Fatalf("ValidateRequest = (%q, %q, %v), want empty reason, no notes, no error", got.Reason, notes, err)
	}
}

func TestValidateRequestKeepsEveryKnownReason(t *testing.T) {
	for _, reason := range []string{ReportReasonInterval, ReportReasonForwarded, ReportReasonClosed, ReportReasonStartup} {
		t.Run(reason, func(t *testing.T) {
			body := mutateJSON(t, minimalRequestBody, func(m map[string]any) { m["reason"] = reason })
			got, notes, err := ValidateRequest([]byte(body))
			if err != nil || got.Reason != reason || len(notes) != 0 {
				t.Fatalf("ValidateRequest = (%q, %q, %v), want reason %q kept unnoted", got.Reason, notes, err, reason)
			}
		})
	}
}

// TestValidateRequestKeepsAnUnknownProblemAsEmptyAndNotesIt pins the same
// normalisation for a node problem: kept as "", noted by path, never an
// error, while a recognised problem passes through.
func TestValidateRequestKeepsAnUnknownProblemAsEmptyAndNotesIt(t *testing.T) {
	body := mutateJSON(t, minimalRequestBody, func(m map[string]any) {
		m["nodes"] = []any{
			map[string]any{"id": "b1", "problem": "exploded"},
			map[string]any{"id": "b2", "problem": "key"},
			map[string]any{"id": "b3", "problem": ""},
		}
	})
	got, notes, err := ValidateRequest([]byte(body))
	if err != nil {
		t.Fatalf("ValidateRequest: %v", err)
	}
	if got.Nodes[0].Problem != "" {
		t.Errorf("Nodes[0].Problem = %q, want \"\" for an unrecognised value", got.Nodes[0].Problem)
	}
	if got.Nodes[1].Problem != ReportProblemKey {
		t.Errorf("Nodes[1].Problem = %q, want it kept", got.Nodes[1].Problem)
	}
	if len(got.Nodes[2].Rounds) != 0 {
		t.Errorf("Nodes[2].Rounds = %d, want none", len(got.Nodes[2].Rounds))
	}
	if want := []string{"nodes[0].problem"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
}

func TestValidateRequestKeepsEveryKnownProblem(t *testing.T) {
	for _, problem := range []string{ReportProblemUnreachable, ReportProblemFingerprint, ReportProblemKey} {
		t.Run(problem, func(t *testing.T) {
			body := mutateJSON(t, minimalRequestBody, func(m map[string]any) {
				m["nodes"] = []any{map[string]any{"id": "b1", "problem": problem}}
			})
			got, notes, err := ValidateRequest([]byte(body))
			if err != nil || got.Nodes[0].Problem != problem || len(notes) != 0 {
				t.Fatalf("ValidateRequest = (%q, %q, %v), want problem %q kept unnoted", got.Nodes[0].Problem, notes, err, problem)
			}
		})
	}
}

func TestValidateRequestReadsNullForwardedAtAsNil(t *testing.T) {
	body := mutateJSON(t, minimalRequestBody, func(m map[string]any) {
		m["nodes"] = []any{map[string]any{"id": "b1", "forwarded_at": nil}}
	})
	got, _, err := ValidateRequest([]byte(body))
	if err != nil {
		t.Fatalf("ValidateRequest: %v", err)
	}
	if got.Nodes[0].ForwardedAt != nil {
		t.Errorf("ForwardedAt = %v, want nil for JSON null", got.Nodes[0].ForwardedAt)
	}
}

func responseNode(mutate func(map[string]any)) map[string]any {
	n := map[string]any{
		"id": "b1", "name": "b1 cx43@hel1", "url": "https://1.2.3.4:7777",
		"fingerprint": validFingerprint, "cap": 4, "state": "ready",
	}
	if mutate != nil {
		mutate(n)
	}
	return n
}

func responseNodes(t *testing.T, nodes ...map[string]any) string {
	t.Helper()
	list := make([]any, len(nodes))
	for i, n := range nodes {
		list[i] = n
	}
	return mutateJSON(t, emptyNodesBody, func(m map[string]any) { m["nodes"] = list })
}

func TestValidateResponseAcceptsTheProvidersEmptyReply(t *testing.T) {
	got, notes, err := ValidateResponse([]byte(emptyNodesBody))
	if err != nil || len(notes) != 0 {
		t.Fatalf("ValidateResponse = (%+v, %q, %v), want acceptance of what the provider prints with nothing to hand", got, notes, err)
	}
	if len(got.Nodes) != 0 || got.Preparing != nil || got.Denied != nil {
		t.Errorf("reply = %+v, want an empty node list and both optional blocks null", got)
	}
}

func TestValidateResponseAcceptsAProviderShapedReply(t *testing.T) {
	body := mutateJSON(t, responseNodes(t, responseNode(nil), responseNode(func(n map[string]any) { n["id"] = "b2"; n["state"] = "draining" })), func(m map[string]any) {
		m["preparing"] = map[string]any{"slots": 2, "since": "2026-11-02T10:00:00+02:00"}
		m["denied"] = map[string]any{"reason": "budget", "retry_after": "1h"}
	})
	got, notes, err := ValidateResponse([]byte(body))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %q, want none for a recognised reply", notes)
	}
	if len(got.Nodes) != 2 || got.Nodes[0].ID != "b1" || got.Nodes[1].State != "draining" {
		t.Errorf("nodes = %+v, want both nodes with their states", got.Nodes)
	}
	if got.Preparing == nil || got.Preparing.Slots != 2 {
		t.Fatalf("preparing = %+v, want the 2-slot block", got.Preparing)
	}
	if want := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC); !got.Preparing.Since.Equal(want) {
		t.Errorf("preparing.since = %v, want it normalised to %v", got.Preparing.Since, want)
	}
	if got.Denied == nil || got.Denied.Reason != "budget" || got.Denied.RetryAfter != "1h" {
		t.Errorf("denied = %+v, want budget with a 1h retry", got.Denied)
	}
	if got.ReportEvery != "30s" {
		t.Errorf("report_every = %q, want it kept as sent", got.ReportEvery)
	}
}

// TestValidateResponseRefusesVersion1 pins that a reply claiming any contract
// version but 2 is refused whole: the head keeps its last good list rather
// than acting on a reply from a different contract.
func TestValidateResponseRefusesVersion1(t *testing.T) {
	body := strings.Replace(emptyNodesBody, `"version":2`, `"version":1`, 1)
	_, _, err := ValidateResponse([]byte(body))
	if !refusesWith(t, err, "version: must be 2, got 1") {
		t.Fatalf("err = %v, want refusal of version 1", err)
	}
}

func TestValidateResponseRefusesMissingTopLevelFields(t *testing.T) {
	for _, tt := range []struct {
		name     string
		mutate   func(map[string]any)
		contains string
	}{
		{name: "missing version", mutate: func(m map[string]any) { delete(m, "version") }, contains: "version: required"},
		{name: "missing nodes", mutate: func(m map[string]any) { delete(m, "nodes") }, contains: "nodes: required"},
		{name: "null nodes", mutate: func(m map[string]any) { m["nodes"] = nil }, contains: "nodes: required"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ValidateResponse([]byte(mutateJSON(t, emptyNodesBody, tt.mutate)))
			if !refusesWith(t, err, tt.contains) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tt.contains)
			}
		})
	}
}

func TestValidateResponseRefusesUnreadableJSON(t *testing.T) {
	_, _, err := ValidateResponse([]byte(`{"version":2,`))
	if !errors.Is(err, ErrBadInput) {
		t.Fatalf("err = %v, want one wrapping ErrBadInput", err)
	}
}

// TestValidateResponseRefusesIncompleteOrImpossibleNodes pins the required
// fields and the value rules of a response node: only a node the head can
// address, pin, bound seats by, and read the stop sign from is trusted.
func TestValidateResponseRefusesIncompleteOrImpossibleNodes(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(map[string]any)
		contains string
	}{
		{name: "missing id", mutate: func(n map[string]any) { delete(n, "id") }, contains: `nodes[0].id: required`},
		{name: "empty id", mutate: func(n map[string]any) { n["id"] = "" }, contains: `nodes[0].id: required`},
		{name: "missing name", mutate: func(n map[string]any) { delete(n, "name") }, contains: `nodes[0].name: required`},
		{name: "empty url", mutate: func(n map[string]any) { n["url"] = "" }, contains: `nodes[0].url: required`},
		{name: "http url", mutate: func(n map[string]any) { n["url"] = "http://1.2.3.4:7777" }, contains: `nodes[0].url: must be an https URL`},
		{name: "https url without host", mutate: func(n map[string]any) { n["url"] = "https://:7777" }, contains: `nodes[0].url: must be an https URL`},
		{name: "unreadable url", mutate: func(n map[string]any) { n["url"] = "https://[::1" }, contains: `nodes[0].url: must be an https URL`},
		{name: "missing fingerprint", mutate: func(n map[string]any) { delete(n, "fingerprint") }, contains: `nodes[0].fingerprint: required`},
		{name: "fingerprint without prefix", mutate: func(n map[string]any) { n["fingerprint"] = strings.Repeat("0f", 32) }, contains: `nodes[0].fingerprint: must be sha256:<64 hex>`},
		{name: "fingerprint too short", mutate: func(n map[string]any) { n["fingerprint"] = "sha256:" + strings.Repeat("0f", 31) }, contains: `nodes[0].fingerprint: must be sha256:<64 hex>`},
		{name: "fingerprint upper case", mutate: func(n map[string]any) { n["fingerprint"] = "sha256:" + strings.Repeat("0F", 32) }, contains: `nodes[0].fingerprint: must be sha256:<64 hex>`},
		{name: "fingerprint not hex", mutate: func(n map[string]any) { n["fingerprint"] = "sha256:" + strings.Repeat("zz", 32) }, contains: `nodes[0].fingerprint: must be sha256:<64 hex>`},
		{name: "cap zero", mutate: func(n map[string]any) { n["cap"] = 0 }, contains: `nodes[0].cap: must be >= 1, got 0`},
		{name: "cap negative", mutate: func(n map[string]any) { n["cap"] = -2 }, contains: `nodes[0].cap: must be >= 1, got -2`},
		{name: "empty state", mutate: func(n map[string]any) { n["state"] = "" }, contains: `nodes[0].state: unknown ""`},
		{name: "state outside ready and draining", mutate: func(n map[string]any) { n["state"] = "booting" }, contains: `nodes[0].state: unknown "booting"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := ValidateResponse([]byte(responseNodes(t, responseNode(tt.mutate))))
			if !refusesWith(t, err, tt.contains) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tt.contains)
			}
		})
	}
}

// TestValidateResponseRefusesDuplicateNodeIds pins unique ids: two entries of
// one id would make two nodes of one seat list.
func TestValidateResponseRefusesDuplicateNodeIds(t *testing.T) {
	_, _, err := ValidateResponse([]byte(responseNodes(t, responseNode(nil), responseNode(nil))))
	if !refusesWith(t, err, `nodes[1].id: duplicate "b1"`) {
		t.Fatalf("err = %v, want a refusal naming the duplicate id", err)
	}
}

func TestValidateResponseKeepsEveryDeniedReason(t *testing.T) {
	for _, reason := range []string{"budget", "cap", "no_capacity", "failing"} {
		t.Run(reason, func(t *testing.T) {
			got, notes, err := ValidateResponse([]byte(deniedBody(t, reason)))
			if err != nil || got.Denied == nil || got.Denied.Reason != reason || len(notes) != 0 {
				t.Fatalf("ValidateResponse = (%+v, %q, %v), want denied reason %q kept unnoted", got.Denied, notes, err, reason)
			}
		})
	}
}

// TestValidateResponseKeepsAnUnknownDeniedReasonAsEmptyAndNotesIt pins that
// an unrecognised word in the closed denied list is normalised to "" and
// noted, so one bad word cannot throw away the node list that came with it.
func TestValidateResponseKeepsAnUnknownDeniedReasonAsEmptyAndNotesIt(t *testing.T) {
	got, notes, err := ValidateResponse([]byte(deniedBody(t, "mood")))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}
	if got.Denied == nil || got.Denied.Reason != "" {
		t.Fatalf("denied = %+v, want the reason normalised to \"\"", got.Denied)
	}
	if got.Denied.RetryAfter != "1h" {
		t.Errorf("retry_after = %q, want it kept", got.Denied.RetryAfter)
	}
	if want := []string{"denied.reason"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
}

func deniedBody(t *testing.T, reason string) string {
	t.Helper()
	return mutateJSON(t, emptyNodesBody, func(m map[string]any) {
		m["denied"] = map[string]any{"reason": reason, "retry_after": "1h"}
	})
}

// TestValidateResponseNotesAnUnreadableReportEvery pins that a present but
// unparseable report_every is normalised (ParseReportEvery reads it as the
// default) and noted, never a refusal.
func TestValidateResponseNotesAnUnreadableReportEvery(t *testing.T) {
	body := mutateJSON(t, emptyNodesBody, func(m map[string]any) { m["report_every"] = "half a minute" })
	got, notes, err := ValidateResponse([]byte(body))
	if err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}
	if got.ReportEvery != "half a minute" {
		t.Errorf("report_every = %q, want the raw value kept for the log", got.ReportEvery)
	}
	if want := []string{"report_every"}; !reflect.DeepEqual(notes, want) {
		t.Errorf("notes = %q, want %q", notes, want)
	}
	if d := ParseReportEvery(got.ReportEvery); d != DefaultReportEvery {
		t.Errorf("ParseReportEvery(%q) = %v, want the %v default", got.ReportEvery, d, DefaultReportEvery)
	}
}

// TestParseReportEveryClampsAndDefaults pins the cadence contract: a missing
// value means 30 s, and an out-of-range one is clamped into [10 s, 5 min].
func TestParseReportEveryClampsAndDefaults(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Duration
	}{
		{name: "missing means 30 s", in: "", want: 30 * time.Second},
		{name: "unreadable means 30 s", in: "banana", want: 30 * time.Second},
		{name: "below 10 s clamps up", in: "5s", want: 10 * time.Second},
		{name: "zero clamps up", in: "0s", want: 10 * time.Second},
		{name: "negative clamps up", in: "-1m", want: 10 * time.Second},
		{name: "above 5 min clamps down", in: "10m", want: 5 * time.Minute},
		{name: "at the lower bound", in: "10s", want: 10 * time.Second},
		{name: "at the upper bound", in: "5m", want: 5 * time.Minute},
		{name: "in range passes", in: "45s", want: 45 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseReportEvery(tt.in); got != tt.want {
				t.Errorf("ParseReportEvery(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func refusesWith(t *testing.T, err error, contains string) bool {
	t.Helper()
	if err == nil {
		return false
	}
	if !errors.Is(err, ErrBadInput) {
		t.Errorf("err = %v, does not wrap ErrBadInput", err)
		return false
	}
	if !strings.Contains(err.Error(), contains) {
		t.Errorf("err = %q, does not name %q", err, contains)
		return false
	}
	return true
}
