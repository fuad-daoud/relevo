package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestErrorBodyJSON(t *testing.T) {
	eb := ErrorBody{
		Code:    CodeStale,
		Message: "stale or replayed",
	}

	data, err := json.Marshal(eb)
	if err != nil {
		t.Fatalf("Marshal ErrorBody: %v", err)
	}

	want := `{"error":"stale","message":"stale or replayed"}`
	if string(data) != want {
		t.Fatalf("ErrorBody JSON = %s, want %s", string(data), want)
	}

	// Verify it implements error
	var asErr error = eb
	if asErr.Error() != "stale or replayed" {
		t.Fatalf("ErrorBody.Error() = %q, want %q", asErr.Error(), "stale or replayed")
	}
}

func TestCodeOf(t *testing.T) {
	if got := CodeOf(ErrUnknownClient); got != CodeNotEnrolled {
		t.Fatalf("CodeOf(ErrUnknownClient) = %q, want %q", got, CodeNotEnrolled)
	}
	if got := CodeOf(ErrRevoked); got != CodeRevoked {
		t.Fatalf("CodeOf(ErrRevoked) = %q, want %q", got, CodeRevoked)
	}
	if got := CodeOf(ErrBadSignature); got != CodeBadSignature {
		t.Fatalf("CodeOf(ErrBadSignature) = %q, want %q", got, CodeBadSignature)
	}
	if got := CodeOf(ErrStale); got != CodeStale {
		t.Fatalf("CodeOf(ErrStale) = %q, want %q", got, CodeStale)
	}
	// A missing audience reuses the version code: the 426 path old clients
	// already understand.
	if got := CodeOf(ErrNoAudience); got != CodeVersion {
		t.Fatalf("CodeOf(ErrNoAudience) = %q, want %q", got, CodeVersion)
	}
	if got := CodeOf(ErrWrongAudience); got != CodeWrongAudience {
		t.Fatalf("CodeOf(ErrWrongAudience) = %q, want %q", got, CodeWrongAudience)
	}
	if got := CodeOf(errors.New("x")); got != "" {
		t.Fatalf("CodeOf(errors.New(\"x\")) = %q, want \"\"", got)
	}
}

func TestRepoID(t *testing.T) {
	// Empty -> ErrNoRoot
	if _, err := RepoID(""); !errors.Is(err, ErrNoRoot) {
		t.Fatalf("RepoID(\"\"): got %v, want ErrNoRoot", err)
	}

	knownSHA := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	expectedSum := sha256.Sum256([]byte(knownSHA))
	expectedHex := hex.EncodeToString(expectedSum[:])

	got, err := RepoID(knownSHA)
	if err != nil {
		t.Fatalf("RepoID: %v", err)
	}
	if got != expectedHex {
		t.Fatalf("RepoID(%q) = %q, want %q", knownSHA, got, expectedHex)
	}
}

func TestBindingViewJSONNames(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	bv := BindingView{
		Name:           "my-binding",
		State:          "running",
		Round:          1,
		RoundState:     RoundRunning,
		ClosedRound:    1,
		Halt:           "none",
		ResultCommit:   "1111111111111111111111111111111111111111",
		DirtyCommit:    "2222222222222222222222222222222222222222",
		ReportOutcome:  "done",
		AckedRound:     1,
		Candidate:      "builder-1",
		RoundStartedAt: now,
		RoundCap:       5,
		RoundTimeoutMS: 60000,
	}

	data, err := json.Marshal(bv)
	if err != nil {
		t.Fatalf("Marshal BindingView: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	expectedKeys := []string{
		"name",
		"state",
		"round",
		"round_state",
		"closed_round",
		"halt",
		"result_commit",
		"dirty_commit",
		"report_outcome",
		"acked_round",
		"candidate",
		"round_started_at",
		"round_cap",
		"round_timeout_ms",
	}

	for _, k := range expectedKeys {
		if _, ok := m[k]; !ok {
			t.Errorf("missing expected key %q in JSON: %s", k, string(data))
		}
	}

	// dirty_commit absent when empty
	bvEmptyDirty := bv
	bvEmptyDirty.DirtyCommit = ""
	dataEmpty, err := json.Marshal(bvEmptyDirty)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(dataEmpty), "dirty_commit") {
		t.Errorf("expected dirty_commit absent when empty, got: %s", string(dataEmpty))
	}
}

// TestBindingViewUsageRoundTrip checks that a view carrying the closed
// round's usage round-trips through the same JSON the log entry already
// persists (#216), and that a view without one ships no "usage" key -- so
// a pre-usage server stays indistinguishable from "no usage recorded".
func TestBindingViewUsageRoundTrip(t *testing.T) {
	bv := BindingView{
		Name:        "api",
		State:       "running",
		Round:       2,
		RoundState:  RoundClosed,
		ClosedRound: 1,
		Usage: &usage.Usage{
			Harness: "opencode",
			Model:   "haiku",
			Tokens:  usage.Tokens{In: 1000, Out: 200},
			Cost:    usage.Cost{USD: 0.12, Basis: usage.Measured},
			Note:    "2 models",
		},
	}

	data, err := json.Marshal(bv)
	if err != nil {
		t.Fatalf("Marshal BindingView: %v", err)
	}

	var got BindingView
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal BindingView: %v", err)
	}
	if got.Usage == nil {
		t.Fatalf("Usage lost on the wire: %s", string(data))
	}
	if *got.Usage != *bv.Usage {
		t.Fatalf("Usage round-trip = %+v, want %+v", *got.Usage, *bv.Usage)
	}

	// A view without usage ships no "usage" key.
	bv.Usage = nil
	data, err = json.Marshal(bv)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), `"usage"`) {
		t.Errorf("expected no usage key when Usage is nil, got: %s", string(data))
	}
}
