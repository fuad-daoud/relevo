package serve

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/store"
)

// writeCostFile stores cf atomically: a temp file in the same directory
// renamed over the target, so a crash leaves the previous file intact. The
// caller holds costFileMu, so the load-modify-write it belongs to stays one
// critical section.
func writeCostFile(path string, cf costFile) error {
	raw, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
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

// foldCostSample folds a settled check's measurement into its round's
// sample. The check runs after the runner is gone, so its base never joins
// the round's and its peak only raises the burst to the max; a check with no
// known burst folds nothing. The sample is found by its own binding and
// round, so a fold still lands after other bindings closing meanwhile moved
// LastBinding on. A sample the trim dropped, or one written before the key
// fields existed, is left alone with a debug log: the dedupe stays one
// sample per round.
func foldCostSample(root, repoID, role, binding string, round int, r *store.Rusage) (bool, error) {
	_, checkBurst, ok := burstBaseOf(r)
	if !ok {
		return false, nil
	}
	costFileMu.Lock()
	defer costFileMu.Unlock()
	path := costFilePath(root, repoID, role)
	cf, err := loadCostFile(path)
	if err != nil {
		return false, err
	}
	for i := len(cf.Samples) - 1; i >= 0; i-- {
		if cf.Samples[i].Binding != binding || cf.Samples[i].Round != round {
			continue
		}
		if checkBurst <= cf.Samples[i].Burst {
			return false, nil
		}
		cf.Samples[i].Burst = checkBurst
		return true, writeCostFile(path, cf)
	}
	slog.Debug("cost fold: no sample for the checked round", "binding", binding, "round", round)
	return false, nil
}

// foldCheckCost folds one settled check's measurement into its round's cost
// sample, through the key the close recorded: the owner's binding name and
// the closed round. A binding with no serve facts records nothing, so a
// local check never writes. A fold failure only warns: the settle already
// succeeded and must not fail because of it.
func foldCheckCost(root string, b store.Binding, closedRound int, r *store.Rusage) {
	if b.Serve == nil {
		return
	}
	if _, err := foldCostSample(root, b.Serve.RepoID, b.WriterRole(), b.Owner+"/"+b.Name, closedRound, r); err != nil {
		slog.Warn("cost fold failed", "binding", b.Name, "round", closedRound, "err", err)
	}
}
