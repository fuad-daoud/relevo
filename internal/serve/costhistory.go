package serve

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// costHistoryMaxSamples bounds the cost file: lookups read the whole file,
// so it keeps only the newest samples.
const costHistoryMaxSamples = 20

// costFile is one repo-role's cost history. Samples are newest-last, and
// LastBinding with LastRound name the round the file already holds:
// CloseServedRound can retry after a git failure, so the retry must not record
// the round twice. The binding, as owner/name, is part of the key because every
// binding of the same repo and role counts its rounds from 1, and two owners
// may pick the same name.
type costFile struct {
	LastBinding string       `json:"last_binding"`
	LastRound   int          `json:"last_round"`
	Samples     []costSample `json:"samples"`
}

// costFileMu serialises the read-modify-write of cost files: two bindings of
// one repo and role can close in the same moment, and the later rename would
// drop the earlier sample.
var costFileMu sync.Mutex

// sanitiseRole folds a writer role into a filename segment: the role is
// actor input, so only lowercase alphanumerics and dashes survive and every
// other byte becomes a dash.
func sanitiseRole(role string) string {
	out := make([]byte, len(role))
	for i := 0; i < len(role); i++ {
		c := role[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' {
			out[i] = c
		} else {
			out[i] = '-'
		}
	}
	return string(out)
}

// costFilePath answers the history file for one repo and role. The repo id
// is 64 lowercase hex, so it needs no folding; the role does.
func costFilePath(root, repoID, role string) string {
	return filepath.Join(root, "costs", repoID+"--"+sanitiseRole(role)+".json")
}

// loadCostFile reads a history file. A missing file is empty history, not an
// error; malformed JSON is, so a corrupt file surfaces instead of silently
// resetting the dedupe record.
func loadCostFile(path string) (costFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return costFile{}, nil
		}
		return costFile{}, err
	}
	var cf costFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return costFile{}, err
	}
	return cf, nil
}

// appendCostSample records one closed round's sample, newest-last and trimmed
// to the newest 20. A repeated call for the same binding's round is a no-op
// without a write, so a retried close records the round once. The write is atomic — a
// temp file in the same directory renamed over the target — so a crash leaves
// the previous file intact. IO errors propagate; the hook decides they are
// warn-only.
func appendCostSample(root, repoID, role, binding string, round int, s costSample) (bool, error) {
	costFileMu.Lock()
	defer costFileMu.Unlock()
	path := costFilePath(root, repoID, role)
	cf, err := loadCostFile(path)
	if err != nil {
		return false, err
	}
	if cf.LastBinding == binding && cf.LastRound == round {
		return false, nil
	}
	cf.Samples = append(cf.Samples, s)
	if len(cf.Samples) > costHistoryMaxSamples {
		cf.Samples = cf.Samples[len(cf.Samples)-costHistoryMaxSamples:]
	}
	cf.LastBinding = binding
	cf.LastRound = round
	raw, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "tmp-*.json")
	if err != nil {
		return false, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return false, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return false, err
	}
	return true, nil
}

// reportEntryFor answers the report entry that closed round: the last
// matching entry wins, since a round holds one report.
func reportEntryFor(entries []store.LogEntry, round int) (store.LogEntry, bool) {
	var out store.LogEntry
	found := false
	for _, e := range entries {
		if e.Round == round && e.Kind == store.KindReport && e.Direction == store.DirToMasterMind {
			out = e
			found = true
		}
	}
	return out, found
}

// sampleForEntry answers a closed round's footprint from its report entry. A
// round with no known burst records nothing: an OOM-killed round's reached
// peak stands in for its burst, but only when the kill count says it was
// killed — every scoped round carries the counter, including a zero one. A
// missing base reads as unknown and the seat lookup ignores it.
func sampleForEntry(e store.LogEntry) (costSample, bool) {
	r := e.Rusage
	if r == nil {
		return costSample{}, false
	}
	var burst int64
	switch {
	case r.BurstBytes != nil:
		burst = *r.BurstBytes
	case r.EvOOMKill != nil && *r.EvOOMKill > 0 && r.PeakMemBytes > 0:
		burst = r.PeakMemBytes
	default:
		return costSample{}, false
	}
	var base int64
	if r.BaseBytes != nil {
		base = *r.BaseBytes
	}
	return costSample{Base: base, Burst: burst, At: e.TS}, true
}

// recordClosedCost appends the closed round's measured burst to the cost
// history. A round with nothing measured records nothing, and a read or write
// failure only warns: the close already succeeded and must not fail because
// of it. Runtimes without a cost root or a store — the existing close tests'
// included — record nothing.
func recordClosedCost(rt relevo.Runtime, b store.Binding, closed int) {
	if rt.CostRoot == "" || rt.Store == nil {
		return
	}
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return
	}
	entry, ok := reportEntryFor(entries, closed)
	if !ok {
		return
	}
	sample, ok := sampleForEntry(entry)
	if !ok {
		return
	}
	if _, err := appendCostSample(rt.CostRoot, b.Serve.RepoID, b.WriterRole(), b.Owner+"/"+b.Name, closed, sample); err != nil {
		slog.Warn("cost history append failed", "binding", b.Name, "round", closed, "err", err)
	}
}

// roundBurstFor answers the RoundBurst seam for one live snapshot: the
// binding's seat burst, where ok doubles as the positive check MemoryHigh
// needs, since a scope property takes only a positive byte count. seatCostFor
// never returns zero — short history reads as the class defaults — so ok is
// true in practice and false only for a corrupt or negative reading.
func roundBurstFor(root string, reg *roles.Registry) func(store.Binding) (int64, bool) {
	return func(b store.Binding) (int64, bool) {
		_, burst := seatCostFor(root, reg, b)
		return burst, burst > 0
	}
}

// seatCostFor answers a binding's seat from its repo-role history, classified
// the way admission classifies it: a planner round takes planner defaults, any
// other consult-shaped role reviewer defaults. A missing or unreadable history
// reads as the class defaults, never as a zero cost.
func seatCostFor(root string, reg *roles.Registry, b store.Binding) (base, burst int64) {
	planner := relevo.IsPlannerRound(reg, b)
	reader := false
	if reg != nil {
		if r, ok := reg.Role(b.WriterRole()); ok {
			reader = r.Shape == harness.ShapeConsult
		}
	}
	var samples []costSample
	if root != "" && b.Serve != nil {
		cf, err := loadCostFile(costFilePath(root, b.Serve.RepoID, b.WriterRole()))
		if err != nil {
			slog.Warn("cost history unreadable, using defaults", "binding", b.Name, "err", err)
		} else {
			samples = cf.Samples
		}
	}
	return lookupSeat(samples, planner, reader)
}
