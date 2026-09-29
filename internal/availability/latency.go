package availability

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// LatencyRetainWindow is how long a sample is kept before Prune drops it,
// matching HistoryRetainWindow so both records age out together.
const LatencyRetainWindow = 30 * 24 * time.Hour

// Sample records one probe: how long a candidate took to produce its first
// model output, and how long the whole run took. Err is "" on success; a
// sample counts as successful only then.
type Sample struct {
	At      time.Time `json:"at"`
	Token   string    `json:"token"`
	Host    string    `json:"host"`
	TTFTMS  int64     `json:"ttft_ms"`
	TotalMS int64     `json:"total_ms"`
	Err     string    `json:"err,omitempty"`
}

// LatencyHistory holds every sample in the 30-day window, oldest first.
type LatencyHistory struct {
	Samples []Sample `json:"samples"`
}

// Summary is one token's window: how many probes succeeded, how many failed,
// and the p50 time to first output over the successful ones.
type Summary struct {
	N         int
	Errors    int
	TTFTP50MS int64
}

// latencyKey is the kv row the latency document lives in.
const latencyKey = "latency"

// LoadLatency reads the latency history from the kv row "latency". An absent
// row is an empty LatencyHistory and no error: a
// fresh install has probed nothing yet. Invalid JSON is an error, so a torn or
// hand-edited document is reported rather than read as empty.
func LoadLatency(kv db.KV) (LatencyHistory, error) {
	data, ok, err := kv.KVGet(latencyKey)
	if err != nil {
		return LatencyHistory{}, err
	}
	if !ok {
		return LatencyHistory{}, nil
	}

	var h LatencyHistory
	if err := json.Unmarshal(data, &h); err != nil {
		return LatencyHistory{}, fmt.Errorf("decode latency: %w", err)
	}

	return h, nil
}

// SaveLatency writes the whole history document to the kv row "latency".
func SaveLatency(kv db.KV, h LatencyHistory) error {
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal latency: %w", err)
	}
	return kv.KVPut(latencyKey, data)
}

// Prune returns a new LatencyHistory containing every sample no older than
// LatencyRetainWindow, in order. It does not mutate the receiver's slice.
func (h LatencyHistory) Prune(now time.Time) LatencyHistory {
	cutoff := now.Add(-LatencyRetainWindow)
	var kept []Sample
	for _, s := range h.Samples {
		if !s.At.Before(cutoff) {
			kept = append(kept, s)
		}
	}
	return LatencyHistory{Samples: append([]Sample(nil), kept...)}
}

// Append returns a new LatencyHistory with s added to the end. It performs no
// deduplication and does not mutate the receiver's slice.
func (h LatencyHistory) Append(s Sample) LatencyHistory {
	cp := append([]Sample(nil), h.Samples...)
	return LatencyHistory{Samples: append(cp, s)}
}

// Summary summarises token over every sample in h. The caller prunes first,
// so the window is the caller's choice. Errored samples count in Errors and
// are excluded from TTFTP50MS; with no successful sample the p50 is 0.
func (h LatencyHistory) Summary(token string) Summary {
	ttfts := make([]int64, 0, len(h.Samples))
	var sum Summary
	for _, s := range h.Samples {
		if s.Token != token {
			continue
		}
		if s.Err != "" {
			sum.Errors++
			continue
		}
		sum.N++
		ttfts = append(ttfts, s.TTFTMS)
	}

	if len(ttfts) == 0 {
		return sum
	}
	sort.Slice(ttfts, func(i, j int) bool { return ttfts[i] < ttfts[j] })
	sum.TTFTP50MS = ttfts[(len(ttfts)-1)/2] // lower median

	return sum
}
