package db

import (
	"context"
	"fmt"
)

// OpenRoundsOnFinishedBindings counts rounds still open on bindings that are
// done or archived, and how many bindings carry them.
func (d *DB) OpenRoundsOnFinishedBindings() (rounds, bindings int, err error) {
	if err := d.sqlDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*), COUNT(DISTINCT round.binding_id)
			FROM round JOIN binding ON binding.id = round.binding_id
			WHERE round.outcome = 'open'
				AND (binding.final_state = 'done' OR binding.archived_at IS NOT NULL)`).Scan(&rounds, &bindings); err != nil {
		return 0, 0, fmt.Errorf("db: open rounds on finished bindings: %w", mapBusy(err))
	}
	return rounds, bindings, nil
}
