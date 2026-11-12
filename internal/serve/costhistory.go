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
// to the newest 20. The sample carries its own binding and round, so a check
// settled after the close folds into it. A repeated call for the same
// binding's round is a no-op without a write, so a retried close records the
// round once. The write is atomic, through the shared writer. IO errors
// propagate; the hook decides they are warn-only.
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
	s.Binding = binding
	s.Round = round
	cf.Samples = append(cf.Samples, s)
	if len(cf.Samples) > costHistoryMaxSamples {
		cf.Samples = cf.Samples[len(cf.Samples)-costHistoryMaxSamples:]
	}
	cf.LastBinding = binding
	cf.LastRound = round
	return true, writeCostFile(path, cf)
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

// burstBaseOf answers one rusage's seat terms through the burst rule: the
// burst is the measured one, or the OOM peak when the kill count says the
// scope was killed -- every scoped round carries the counter, including a
// zero one. A measurement with neither records nothing. A missing base reads
// as zero and the seat lookup ignores it.
func burstBaseOf(r *store.Rusage) (base, burst int64, ok bool) {
	if r == nil {
		return 0, 0, false
	}
	if r.BaseBytes != nil {
		base = *r.BaseBytes
	}
	switch {
	case r.BurstBytes != nil:
		return base, *r.BurstBytes, true
	case r.EvOOMKill != nil && *r.EvOOMKill > 0 && r.PeakMemBytes > 0:
		return base, r.PeakMemBytes, true
	default:
		return base, 0, false
	}
}

// sampleForEntry answers a closed round's footprint from its report entry,
// folding the round gate's trailer in. The gate starts on the done marker
// without waiting for the runner to exit, so its peak can land while the
// runner still holds its resting memory: the burst is the higher of the
// round's own peak and the round's base plus the gate's peak. The gate's
// base is its busy level, never the round's resting one, so the sample's
// base stays the round's -- a round with no base of its own takes the
// gate's. A round with no known burst on either side records nothing.
func sampleForEntry(e store.LogEntry) (costSample, bool) {
	roundBase, roundBurst, roundOK := burstBaseOf(e.Rusage)
	var gate *store.Rusage
	if e.Gate != nil {
		gate = e.Gate.Rusage
	}
	gateBase, gateBurst, gateOK := burstBaseOf(gate)
	if !roundOK && !gateOK {
		return costSample{}, false
	}
	base := gateBase
	if e.Rusage != nil && e.Rusage.BaseBytes != nil {
		base = roundBase
	}
	burst := roundBurst
	if gateOK && roundBase+gateBurst > burst {
		burst = roundBase + gateBurst
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
// MemoryHigh a served round runs under. A class with too little history gets
// none, because a guessed ceiling below the round's real peak only throttles
// it, and the stall the throttle causes is the round's alone. A learned class
// gets half again its p90 burst, so the ceiling catches a runaway round, not
// an ordinary one above the p90. ok is false whenever there is no positive
// ceiling to stamp.
func roundBurstFor(root string) func(store.Binding) (int64, bool) {
	return func(b store.Binding) (int64, bool) {
		samples := costSamplesFor(root, b)
		if len(samples) < seatMinSamples {
			return 0, false
		}
		bursts := make([]int64, len(samples))
		for i, s := range samples {
			bursts[i] = s.Burst
		}
		burst := nearestRank(bursts, 90)
		high := burst + burst/2
		return high, high > 0
	}
}

// costSamplesFor reads a served binding's repo-role cost history. A binding
// with no serve facts, no root, or an unreadable file has none.
func costSamplesFor(root string, b store.Binding) []costSample {
	if root == "" || b.Serve == nil {
		return nil
	}
	cf, err := loadCostFile(costFilePath(root, b.Serve.RepoID, b.WriterRole()))
	if err != nil {
		slog.Warn("cost history unreadable, using defaults", "binding", b.Name, "err", err)
		return nil
	}
	return cf.Samples
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
	return lookupSeat(costSamplesFor(root, b), planner, reader)
}
