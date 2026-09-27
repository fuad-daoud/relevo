package ui

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// ConfigLog is every stored revision, newest first. The rows carry no
// snapshot: the list is a header list.
func (a *mastermindActions) ConfigLog() ([]db.RevisionRow, error) {
	if a.runtime().Config == nil {
		return nil, errors.New("no config store")
	}
	return a.runtime().Config.Log(0)
}

// ConfigChanges is one revision's changes in human words.
func (a *mastermindActions) ConfigChanges(rev int64) ([]relevo.ChangeLine, error) {
	if a.runtime().Config == nil {
		return nil, errors.New("no config store")
	}
	return relevo.RevisionChanges(a.runtime().Config, rev)
}

// RollbackPreview is what rolling back to rev would change, and a refusal when
// the result would not pass the cockpit's own checks.
func (a *mastermindActions) RollbackPreview(rev int64) ([]relevo.ChangeLine, error) {
	if a.runtime().Config == nil {
		return nil, errors.New("no config store")
	}
	return relevo.RollbackPreview(a.runtime().Config, rev)
}

// Rollback writes the config back to rev's snapshot and reloads this adapter's
// runtime from the store, the way ApplyConfig reloads it after an edit.
// A write error is a failure; a failed reload after a successful write is not,
// because the write already happened, so the text says so. ErrNoChange is not
// an error to show red: it is the answer "already equals".
func (a *mastermindActions) Rollback(ctx context.Context, rev int64) Result {
	if a.runtime().Config == nil {
		return Result{Err: errors.New("no config store")}
	}
	row, err := a.runtime().Config.As("rollback", "").Rollback(rev)
	if err != nil {
		if errors.Is(err, config.ErrNoChange) {
			return Result{Text: fmt.Sprintf("config already equals #%d", rev)}
		}
		return Result{Err: err, Refresh: true}
	}
	if err := a.live.Refresh(); err != nil {
		return Result{Text: "saved; reload failed: " + err.Error(), Refresh: true}
	}
	return Result{Text: fmt.Sprintf("rolled back to #%d as #%d", rev, row.Rev), Refresh: true}
}
