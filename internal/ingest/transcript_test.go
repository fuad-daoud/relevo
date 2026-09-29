package ingest

import (
	"testing"
	"time"
)

func TestTranscriptFromStream(t *testing.T) {
	lines := [][]byte{
		[]byte(`{"type":"text","part":{"text":"Adding the auth package."}}`),
		[]byte(`{"type":"tool_use","part":{"tool":"read","state":{"status":"completed","input":{"path":"auth.go"},"output":"package auth\n"}}}`),
		[]byte(`{"type":"tool_use","part":{"tool":"write","state":{"status":"completed","input":{"path":"auth.go"},"output":""}}}`),
		[]byte(`{"type":"text","part":{"text":"Done."}}`),
		[]byte(`{"type":"error","error":{"message":"transient retry"}}`),
		[]byte(`not valid json at all {{{`),
	}

	recs, skipped := streamTranscriptRecords("opencode", lines, 0)

	if len(recs) != 6 {
		t.Fatalf("len(recs) = %d, want 6", len(recs))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}
	for i, r := range recs[:5] {
		if r.Seq != i {
			t.Errorf("recs[%d].Seq = %d, want %d", i, r.Seq, i)
		}
		if r.Rendered == "" {
			t.Errorf("recs[%d].Rendered is empty, want the renderer's output", i)
		}
		if r.RecordJSON != string(lines[i]) {
			t.Errorf("recs[%d].RecordJSON = %q, want the verbatim line", i, r.RecordJSON)
		}
	}
	garbage := recs[5]
	if garbage.Rendered != "" {
		t.Errorf("garbage line Rendered = %q, want empty", garbage.Rendered)
	}
	if garbage.RecordJSON != string(lines[5]) {
		t.Errorf("garbage line RecordJSON = %q, want the verbatim line kept", garbage.RecordJSON)
	}
	if garbage.Seq != 5 {
		t.Errorf("garbage line Seq = %d, want 5", garbage.Seq)
	}
}

func TestTranscriptFromLogOnly(t *testing.T) {
	lines := [][]byte{
		[]byte("round 2 builder log line 1"),
		[]byte("round 2 builder log line 2"),
		[]byte("round 2 builder log line 3"),
	}

	recs := logOnlyTranscriptRecords(lines, 0)

	if len(recs) != 3 {
		t.Fatalf("len(recs) = %d, want 3", len(recs))
	}
	for i, r := range recs {
		if r.RecordJSON != "" {
			t.Errorf("recs[%d].RecordJSON = %q, want empty (no jsonl behind a log-only round)", i, r.RecordJSON)
		}
		if r.Rendered != string(lines[i]) {
			t.Errorf("recs[%d].Rendered = %q, want %q", i, r.Rendered, lines[i])
		}
		if r.Seq != i {
			t.Errorf("recs[%d].Seq = %d, want %d", i, r.Seq, i)
		}
	}
}

// TestTranscriptFromStreamClaudePair: one batch is one pass, so a claude call
// and its result in the same batch carry the span between their two events.
func TestTranscriptFromStreamClaudePair(t *testing.T) {
	prev := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prev })

	lines := [][]byte{
		[]byte(`{"type":"assistant","timestamp":"2026-09-26T20:16:21Z","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"ls"}}]}}`),
		[]byte(`{"type":"user","timestamp":"2026-09-26T20:16:25.2Z","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":false,"content":"ok"}]}}`),
	}

	recs, skipped := streamTranscriptRecords("claude", lines, 0)

	if skipped != 0 {
		t.Fatalf("skipped = %d, want 0", skipped)
	}
	if want := "20:16:21 ● Bash ls"; recs[0].Rendered != want {
		t.Errorf("call Rendered = %q, want %q", recs[0].Rendered, want)
	}
	if want := "20:16:25 +4.2s   ⎿ ok: ok"; recs[1].Rendered != want {
		t.Errorf("result Rendered = %q, want %q", recs[1].Rendered, want)
	}
}
