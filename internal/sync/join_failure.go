package sync

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// KeyJoinFailure is the record a failed enable leaves beside the join marker,
// so a join that dies in the daemon still reads as failed rather than running.
const KeyJoinFailure = "sync.join_failure"

// JoinFailure is what a failed enable records: when it failed and the fixed
// text the refusal carried, never a token, passphrase or secret.
type JoinFailure struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// WriteJoinFailure records one enable's failure beside the join marker.
func WriteJoinFailure(local db.KV, at time.Time, message string) error {
	body, err := json.Marshal(JoinFailure{At: at.UTC(), Message: message})
	if err != nil {
		return fmt.Errorf("sync: join failure: %w", err)
	}
	if err := local.KVPut(KeyJoinFailure, body); err != nil {
		return fmt.Errorf("sync: join failure: %w", err)
	}
	return nil
}

// ReadJoinFailure returns the failure a failed enable recorded. One that will
// not parse is a failure rather than absent, so a stranded join never reads as
// a machine at rest.
func ReadJoinFailure(local db.KV) (JoinFailure, bool, error) {
	body, ok, err := local.KVGet(KeyJoinFailure)
	if err != nil {
		return JoinFailure{}, false, fmt.Errorf("sync: join failure: %w", err)
	}
	if !ok {
		return JoinFailure{}, false, nil
	}
	var failure JoinFailure
	if err := json.Unmarshal(body, &failure); err != nil {
		return JoinFailure{}, false, fmt.Errorf("sync: join failure: %w", err)
	}
	return failure, true, nil
}

// ClearJoinFailure removes the failure record, run wherever the join marker's
// life ends so off never reads as failed.
func ClearJoinFailure(local db.KV) error {
	if err := local.KVDelete(KeyJoinFailure); err != nil {
		return fmt.Errorf("sync: join failure: %w", err)
	}
	return nil
}

// NoteDaemonRestart records the enable a restart stranded: a set marker on a
// machine that is off with no failure means the run that wrote the marker is
// gone with this process's restart. It reports whether it wrote.
func NoteDaemonRestart(local Local, now time.Time) (bool, error) {
	_, marked, err := ReadJoin(local)
	if err != nil {
		return false, err
	}
	if !marked {
		return false, nil
	}
	on, err := Enabled(local)
	if err != nil {
		return false, err
	}
	if on {
		return false, nil
	}
	_, failed, err := ReadJoinFailure(local)
	if err != nil {
		return false, err
	}
	if failed {
		return false, nil
	}
	if err := WriteJoinFailure(local, now, "sync: the enable did not finish before the daemon restarted"); err != nil {
		return false, err
	}
	return true, nil
}
