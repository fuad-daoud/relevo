package hooks

import (
	"encoding/json"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/db"
)

const (
	runLogKey = "hooks.log"
	runLogCap = 200
)

// KVLog is a RunLog over the machine database's kv row, capped at runLogCap.
type KVLog struct {
	KV db.DBTxKV
}

var _ RunLog = (*KVLog)(nil)

func NewKVLog(kv db.DBTxKV) *KVLog { return &KVLog{KV: kv} }

// Append adds run, capping the stored array at runLogCap, in one transaction.
func (l *KVLog) Append(run HookRun) error {
	return l.KV.Tx(func(tx db.KVTx) error {
		runs, err := l.read(tx)
		if err != nil {
			return err
		}
		runs = append(runs, run)
		if len(runs) > runLogCap {
			runs = runs[len(runs)-runLogCap:]
		}
		raw, err := json.Marshal(runs)
		if err != nil {
			return fmt.Errorf("hooks: encode run log: %w", err)
		}
		return tx.KVPut(runLogKey, raw)
	})
}

// Runs returns the stored runs, oldest first, adopting a legacy file first.
func (l *KVLog) Runs() ([]HookRun, error) {
	return l.read(l.KV)
}

func (l *KVLog) read(kv db.KVTx) ([]HookRun, error) {
	raw, ok, err := kv.KVGet(runLogKey)
	if err != nil {
		return nil, fmt.Errorf("hooks: read %s: %w", runLogKey, err)
	}
	if !ok {
		return nil, nil
	}
	var runs []HookRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return nil, fmt.Errorf("hooks: decode %s: %w", runLogKey, err)
	}
	return runs, nil
}
