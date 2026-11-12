package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/fuad-daoud/relevo/internal/proc"
)

// kSample is one tick's bursting-round count for the K estimate. The JSON
// shape is the file's contract, read back by loadKHistory.
type kSample struct {
	Count int       `json:"count"`
	At    time.Time `json:"at"`
}

// kHistoryMaxAge bounds the K history file: samples older than a day no
// longer describe this host's parallelism, so every append drops them.
const kHistoryMaxAge = 24 * time.Hour

// kSampleWindow is one sample's span: a tick lands every few seconds, so each
// sample keeps the highest count seen in its minute. That bounds the file to
// a day of minutes, and a minute is short enough that two rounds bursting
// together still register.
const kSampleWindow = time.Minute

// kMinSamples is the history depth below which K keeps its cold start: half
// an hour of busy minutes, so a short quiet spell cannot collapse K to one
// before rounds have had time to burst together.
const kMinSamples = 30

// kHistoryPath answers the bursting-count history file. It sits beside the
// cost files but carries its own shape, so a cost trim never touches it.
func kHistoryPath(root string) string {
	return filepath.Join(root, "costs", "k-history.json")
}

// loadKHistory reads the bursting-count history. A missing file is empty
// history, not an error; malformed JSON is, so a corrupt file surfaces
// instead of silently resetting the estimate.
func loadKHistory(root string) ([]kSample, error) {
	raw, err := os.ReadFile(kHistoryPath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var samples []kSample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return nil, err
	}
	return samples, nil
}

// appendKSample records one tick's bursting count, newest-last. A tick inside
// the newest sample's window raises that sample to its count instead of
// adding one, and skips the write when nothing rose, so a busy server
// rewrites the file about once a minute rather than every tick. Samples
// older than a day drop by timestamp, never by count. The write is atomic —
// a temp file in the same directory renamed over the target — so a crash
// leaves the previous history intact. It shares costFileMu because both
// histories live under costs/ and rename into it.
func appendKSample(root string, count int, now time.Time) error {
	costFileMu.Lock()
	defer costFileMu.Unlock()
	samples, err := loadKHistory(root)
	if err != nil {
		return err
	}
	if n := len(samples); n > 0 && now.Sub(samples[n-1].At) >= 0 && now.Sub(samples[n-1].At) < kSampleWindow {
		if count <= samples[n-1].Count {
			return nil
		}
		samples[n-1].Count = count
		return writeKHistory(root, samples)
	}
	kept := make([]kSample, 0, len(samples)+1)
	for _, s := range samples {
		if now.Sub(s.At) <= kHistoryMaxAge {
			kept = append(kept, s)
		}
	}
	kept = append(kept, kSample{Count: count, At: now})
	return writeKHistory(root, kept)
}

// writeKHistory replaces the history file with samples, atomically.
func writeKHistory(root string, kept []kSample) error {
	raw, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return err
	}
	path := kHistoryPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// kCounts projects the history to the counts estimateK reads: one entry per
// sample, so the file stays the estimate's only state.
func kCounts(samples []kSample) []int {
	counts := make([]int, len(samples))
	for i, s := range samples {
		counts[i] = s.Count
	}
	return counts
}

// sampleBursting counts the census's live bursting rounds: a round whose anon
// plus swap exceeds half its burst cost. Swap joins anon because a swapped
// round still holds its seat; page cache never counts. An unreadable cgroup —
// a container round has no scope, a pid may vanish mid-tick — reads as not
// bursting, never as a tick failure: the census already carries the round for
// costs and ceilings.
func (s *Server) sampleBursting(c census) int {
	n := 0
	for _, r := range c.RunningRounds {
		dir, err := liveRoundCgroupDir(r.PID)
		if err != nil {
			continue
		}
		anon, err := proc.MemoryStatAnon(dir)
		if err != nil {
			continue
		}
		swap, err := proc.MemorySwapCurrent(dir)
		if err != nil {
			continue
		}
		if anon+swap > r.Burst/2 {
			n++
		}
	}
	return n
}
