package ingest

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// tsKeys is the order in which a stream record's timestamp field is tried.
var tsKeys = []string{"ts", "timestamp", "time"}

// streamTranscriptRecords turns raw builder-stream lines into TranscriptRecord
// rows starting at startSeq. The batch owns one transcript renderer, so a
// claude call and its result in the same batch keep the span between their two
// events. A line that is not a JSON object is kept verbatim in RecordJSON with
// Rendered empty and counted in skipped; the renderer never fails and would
// otherwise render it as itself.
func streamTranscriptRecords(kind string, lines [][]byte, startSeq int) (recs []db.TranscriptRecord, skipped int) {
	recs = make([]db.TranscriptRecord, 0, len(lines))
	r := transcript.NewRenderer()
	for i, line := range lines {
		trimmed := bytes.TrimSpace(line)

		var obj map[string]any
		isJSONObject := len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, &obj) == nil && obj != nil

		var rendered string
		var ts *time.Time
		if isJSONObject {
			rendered = strings.Join(r.Render(kind, line), "\n")
			ts = tsFromRecord(obj)
		} else {
			skipped++
		}

		recs = append(recs, db.TranscriptRecord{
			Seq:        startSeq + i,
			TS:         ts,
			RecordJSON: string(line),
			Rendered:   rendered,
		})
	}
	return recs, skipped
}

func tsFromRecord(obj map[string]any) *time.Time {
	for _, k := range tsKeys {
		s, ok := obj[k].(string)
		if !ok || s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return &t
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return &t
		}
	}
	return nil
}

// logOnlyTranscriptRecords turns NNN-builder.log lines (a pane round or an old
// archive, with no stream) into TranscriptRecord rows: already-rendered text.
func logOnlyTranscriptRecords(lines [][]byte, startSeq int) []db.TranscriptRecord {
	recs := make([]db.TranscriptRecord, 0, len(lines))
	for i, line := range lines {
		recs = append(recs, db.TranscriptRecord{
			Seq:      startSeq + i,
			Rendered: string(line),
		})
	}
	return recs
}
