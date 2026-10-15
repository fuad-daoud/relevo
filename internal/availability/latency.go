package availability

import (
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// LatencyRetainWindow is how long a sample is kept before Prune drops it,
// matching HistoryRetainWindow so both records age out together. It is
// db.HistoryWindow rather than a number written here, for the reason the
// availability window names.
const LatencyRetainWindow = db.HistoryWindow

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

// latencyKey is the retired kv row the latency document lived in. Nothing
// writes or reads it; MigrateHistoryDocuments deletes it once the samples it
// holds have moved into latency_sample, and names it here so the migration and
// the deletion cannot spell it differently. The json tags above are its shape,
// kept for that decode alone.
const latencyKey = "latency"

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
