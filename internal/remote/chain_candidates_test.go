package remote

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestCreateChainRequestCarriesCandidates pins the candidate pins on the wire:
// a legacy settings create keys them by part and a workflow create by actor,
// and both survive the JSON round trip the multipart create performs.
func TestCreateChainRequestCarriesCandidates(t *testing.T) {
	t.Parallel()

	for _, pins := range []map[string]string{
		{"builder": "opencode/test/m", "reviewer": "claude/test/m"},
		{"builder": "opencode/test/m", "lite-planner": "claude/test/m"},
	} {
		req := CreateChainRequest{Name: "shop", RepoID: strings.Repeat("a", 64), BaseCommit: "abc", Candidates: pins}
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back CreateChainRequest
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(back.Candidates, pins) {
			t.Errorf("candidates = %v, want %v", back.Candidates, pins)
		}
	}
}

// TestCreateChainRequestOmitsEmptyCandidates pins the backward-compatible
// default: a create without pins carries no candidates key, so an older
// server reads the request exactly as before the field existed.
func TestCreateChainRequestOmitsEmptyCandidates(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(CreateChainRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"candidates"`) {
		t.Errorf("wire body = %s, want no candidates key without pins", raw)
	}
}

// TestChainResumeRequestCarriesCandidates pins the resume pins on the wire:
// part-keyed pins survive the JSON round trip the resume POST performs.
func TestChainResumeRequestCarriesCandidates(t *testing.T) {
	t.Parallel()

	pins := map[string]string{"reviewer": "claude/test/alt"}
	req := ChainResumeRequest{Candidates: pins}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back ChainResumeRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(back.Candidates, pins) {
		t.Errorf("candidates = %v, want %v", back.Candidates, pins)
	}
}

// TestChainResumeRequestOmitsEmptyCandidates pins the backward-compatible
// default: a resume without pins carries no candidates key, so an older
// server reads the request exactly as before the field existed.
func TestChainResumeRequestOmitsEmptyCandidates(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(ChainResumeRequest{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"candidates"`) {
		t.Errorf("wire body = %s, want no candidates key without pins", raw)
	}
}
