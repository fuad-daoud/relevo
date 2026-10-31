package dash

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// fetchFunc is one refresh: the db half through query.Filter, the in-Go
// half through query.Apply. It is Model.fetchFn's shape, so a test can hand
// the screen literal rows instead of a database.
type fetchFunc func(ctx context.Context, q histq.Query) ([]db.RoundRow, error)

// fetchDB is the default fetch: one db.Query per refresh, then Apply.
func (m Model) fetchDB() fetchFunc {
	return func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		if m.db == nil {
			return nil, relevo.ErrNoDatabase
		}
		rows, err := m.db.Query(q.Filter)
		if err != nil {
			return nil, err
		}
		return q.Apply(rows), nil
	}
}

// startFetch begins one refresh and returns the command that will deliver
// RowsMsg or ErrMsg. One refresh at a time: a second request while one is
// in flight is dropped, not queued.
func (m Model) startFetch() (Model, tea.Cmd) {
	if m.fetching {
		return m, nil
	}
	m.fetching = true
	fn := m.fetchFn
	if fn == nil {
		fn = m.fetchDB()
	}
	q := m.query
	now := m.now
	return m, func() tea.Msg {
		rows, err := fn(context.Background(), q)
		if err != nil {
			return ErrMsg{Err: err}
		}
		return RowsMsg{Rows: rows, At: now()}
	}
}
