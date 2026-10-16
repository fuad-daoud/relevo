package sync

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// KeyReplayRefused keys the marker recording that the one-time repair pass
// for refused batches has run on this machine.
const KeyReplayRefused = "sync.replay.refused"

const replayRefusedGeneration = "1"

// RewindImport sets an origin's mark down so entries behind it can be replayed.
func RewindImport(shared *db.DB, origin string, seq int) error {
	if seq < 0 {
		seq = 0
	}
	return shared.RewindImportMark(origin, seq)
}

// originRewinds computes the rewind target sequence for each remote origin from
// cumulative dropped trouble reports.
//
// DrainOutbox prepends the pending parents it pulls on top of the 256-entry
// window. Rewind each origin to max(0, Nmin - 4*DefaultBatchSize); overshooting
// is harmless because the replay is idempotent and runs through to the head.
//
// The dropped list is capped at maxDroppedReports (50) and keeps the newest,
// so the oldest drop can be gone. When an origin's dropped lines fill the cap,
// rewind that origin to 0 and let the existing floor/snapshot path take over.
func originRewinds(localOrigin string, dropped []string) map[string]int {
	if len(dropped) == 0 {
		return nil
	}
	minSeqs := make(map[string]int)
	counts := make(map[string]int)
	for _, line := range dropped {
		drop, ok := synclog.ParseDrop(line)
		if !ok || drop.Origin == "" || drop.Origin == localOrigin {
			continue
		}
		counts[drop.Origin]++
		if currMin, seen := minSeqs[drop.Origin]; !seen || drop.Seq < currMin {
			minSeqs[drop.Origin] = drop.Seq
		}
	}
	rewinds := make(map[string]int, len(minSeqs))
	for origin, nMin := range minSeqs {
		if counts[origin] >= maxDroppedReports {
			rewinds[origin] = 0
			continue
		}
		rewindTo := nMin - 4*synclog.DefaultBatchSize
		if rewindTo < 0 {
			rewindTo = 0
		}
		rewinds[origin] = rewindTo
	}
	return rewinds
}

// ReplayRefusedPass runs once at the head of steady import to repair origins
// that suffered batch drops under earlier binaries. It parses cumulative dropped
// trouble reports, rewinds each remote origin's mark to before its first dropped
// batch, and writes the KeyReplayRefused marker so it runs only once per machine.
//
// When a transport is provided, the rewind target is resolved to a batch
// boundary: the mark is placed at the start of the first batch past the target
// (first entry's Batch - 1). If the log holds nothing past the target, the mark
// is left alone. If the target is below the origin's compaction floor, the mark
// is placed below the floor and the snapshot path takes over.
func ReplayRefusedPass(shared *db.DB, kv db.KV, t synclog.LogTransport) error {
	if kv == nil || shared == nil {
		return nil
	}
	var gen string
	if err := marker(kv, KeyReplayRefused, &gen); err != nil {
		return err
	}
	if gen != "" {
		return nil
	}

	var tr Trouble
	if err := marker(kv, KeyTrouble, &tr); err != nil {
		return err
	}
	rewinds := originRewinds(shared.Origin(), tr.Dropped)
	if len(rewinds) == 0 {
		return markReplayRefused(kv)
	}

	var heads map[string]int
	if t != nil {
		state, err := t.ReadMarks()
		if err != nil {
			return fmt.Errorf("sync: replay read marks: %w", err)
		}
		heads = state.Heads
	}

	for origin, target := range rewinds {
		rewindTo := target
		if t != nil {
			head, ok := heads[origin]
			if !ok || head <= target {
				continue
			}
			entries, err := t.PullRange(origin, target, head)
			if err != nil {
				return fmt.Errorf("sync: replay pull range %s: %w", origin, err)
			}
			if len(entries) == 0 {
				continue
			}
			boundary := entries[0].Batch - 1
			if boundary <= target && boundary >= 0 {
				rewindTo = boundary
			}
		}
		if err := RewindImport(shared, origin, rewindTo); err != nil {
			return fmt.Errorf("sync: replay rewind %s to %d: %w", origin, rewindTo, err)
		}
	}

	return markReplayRefused(kv)
}

func markReplayRefused(kv db.KV) error {
	body, err := json.Marshal(replayRefusedGeneration)
	if err != nil {
		return fmt.Errorf("sync: encode replay marker: %w", err)
	}
	if err := kv.KVPut(KeyReplayRefused, body); err != nil {
		return fmt.Errorf("sync: write replay marker: %w", err)
	}
	return nil
}
