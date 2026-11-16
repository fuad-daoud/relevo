package syncworker

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// The migrate verb's own numbers: what one Migrate call moved and built. Plan
// 9 carries this over the pipe; the verb wrapper adds nothing, so the shape is
// defined here where the numbers are produced.

// migrateSweepPasses bounds the late-row loop: each pass writes what the log
// holds, pulls, and stops when no origin's max exceeds its cover. An old
// binary writing on every pull would otherwise hold the verb forever.
const migrateSweepPasses = 3

// MigrateOrigin is one origin's share of a migration: the rows read, the
// objects those rows now live in, the head the migration recorded, and the
// floor and label the heads row carries.
type MigrateOrigin struct {
	Rows       int    `json:"rows,omitempty"`
	Objects    int    `json:"objects,omitempty"`
	Seq        int    `json:"seq,omitempty"`
	Floor      int    `json:"floor,omitempty"`
	LegacyUpto int    `json:"legacy_upto,omitempty"`
	Label      string `json:"label,omitempty"`
}

// MigrateResult is what one Migrate call did. The byte totals are this call's
// deltas, from the difference of the backend's counters before and after, so
// a long-lived worker's earlier totals never leak in.
type MigrateResult struct {
	AlreadyMigrated bool                     `json:"already_migrated,omitempty"`
	Origins         map[string]MigrateOrigin `json:"origins,omitempty"`
	Rows            int                      `json:"rows,omitempty"`
	ObjectsCreated  int                      `json:"objects_created,omitempty"`
	ObjectsVerified int                      `json:"objects_verified,omitempty"`
	RowsSwept       int                      `json:"rows_swept,omitempty"`
	SharedRows      int                      `json:"shared_rows,omitempty"`
	R2Put           int64                    `json:"r2_put,omitempty"`
	R2Get           int64                    `json:"r2_get,omitempty"`
	TursoSent       int64                    `json:"turso_sent,omitempty"`
	TursoReceived   int64                    `json:"turso_received,omitempty"`
}

// Migrate converts a format-5 remote into format 6: pull, flip to
// 5-migrating, write the segments sweeping late rows, then one replica
// transaction builds heads and shared, sets format 6 and drops log and marks,
// then push. A crashed or late run repeats safely.
func (b *TursoBackend) Migrate() (MigrateResult, error) {
	if err := b.checkMigrateReady(); err != nil {
		return MigrateResult{}, err
	}
	before := b.bytes
	ctx := context.Background()
	if done, err := b.checkMigratable(ctx); err != nil {
		return MigrateResult{}, err
	} else if done {
		return b.alreadyMigratedResult(before), nil
	}
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return MigrateResult{}, fmt.Errorf("syncworker: migrate pull: %w", markTransferRefusal(err))
	}
	if done, err := b.checkMigratable(ctx); err != nil {
		return MigrateResult{}, err
	} else if done {
		return b.alreadyMigratedResult(before), nil
	}
	if err := b.flipMigrating(ctx); err != nil {
		return MigrateResult{}, err
	}
	writes, covered, swept, err := b.sweepSegments(ctx)
	if err != nil {
		return MigrateResult{}, err
	}
	floors, labels, sharedRows, err := b.commitMigration(ctx, covered, writes)
	if err != nil {
		return MigrateResult{}, err
	}
	return b.migrateResult(before, writes, floors, labels, swept, sharedRows), nil
}

// alreadyMigratedResult answers the re-run after the commit's push: the flag
// with this call's byte deltas, so a caller sees what the finishing push cost.
func (b *TursoBackend) alreadyMigratedResult(before Stats) MigrateResult {
	after := b.bytes
	return MigrateResult{
		AlreadyMigrated: true,
		R2Put:           after.R2Put - before.R2Put,
		R2Get:           after.R2Get - before.R2Get,
		TursoSent:       after.TursoSent - before.TursoSent,
		TursoReceived:   after.TursoReceived - before.TursoReceived,
	}
}

// migrateResult folds one call's writes, floors, labels and shared count into
// the answer, with the byte totals as this call's deltas.
func (b *TursoBackend) migrateResult(before Stats, writes map[string]originWrite, floors map[string]int, labels map[string]string, swept, sharedRows int) MigrateResult {
	after := b.bytes
	out := MigrateResult{
		Origins:       make(map[string]MigrateOrigin),
		RowsSwept:     swept,
		SharedRows:    sharedRows,
		R2Put:         after.R2Put - before.R2Put,
		R2Get:         after.R2Get - before.R2Get,
		TursoSent:     after.TursoSent - before.TursoSent,
		TursoReceived: after.TursoReceived - before.TursoReceived,
	}
	for origin, w := range writes {
		if w.rows == 0 {
			continue
		}
		out.Origins[origin] = MigrateOrigin{
			Rows:       w.rows,
			Objects:    w.created + w.verified,
			Seq:        w.last,
			Floor:      floors[origin],
			LegacyUpto: w.last,
			Label:      labels[origin],
		}
		out.Rows += w.rows
		out.ObjectsCreated += w.created
		out.ObjectsVerified += w.verified
	}
	return out
}

// checkMigrateReady refuses a Migrate the backend cannot run: no replica, a
// normal pipe, no bucket to write the segments to, or no mirror list, which
// would leave shared empty and starve the servers. The pull has not run, so
// a refused call costs no traffic.
func (b *TursoBackend) checkMigrateReady() error {
	if err := b.ready(); err != nil {
		return err
	}
	if !b.spec.Migrate {
		return fmt.Errorf("syncworker: migrate without a migrate hello: %w", ErrProtocol)
	}
	if b.blobs == nil {
		return ErrNoR2
	}
	if len(b.spec.MirrorTables) == 0 {
		return fmt.Errorf("syncworker: migrate without the tables to mirror: %w", ErrProtocol)
	}
	return nil
}

// checkMigratable re-reads the tables and format: a finished remote finishes
// its lost push and answers already-migrated, a format-5 or resumable
// migrating shape continues, and anything else is refused with the same
// refusal a migrate hello would give.
func (b *TursoBackend) checkMigratable(ctx context.Context) (bool, error) {
	tables, err := userTables(ctx, b.db)
	if err != nil {
		return false, fmt.Errorf("syncworker: read the remote's tables: %w", err)
	}
	if !isLogTables(tables) && !isOlderTables(tables) && !isMigratingTables(tables) {
		return false, errNotALog(b.spec.URL)
	}
	format, err := readFormat(ctx, b.db)
	if err != nil {
		return false, err
	}
	if format == "6" && isLogTables(tables) {
		if err := b.transfer(ctx, b.driver.Push); err != nil {
			return false, fmt.Errorf("syncworker: push the migrated log: %w", markTransferRefusal(err))
		}
		return true, nil
	}
	if format == "5" && slices.Equal(tables, olderLogTables[formatFive]) {
		return false, nil
	}
	if format == migratingFormat && isMigratingTables(tables) {
		if canResumeMigration(tables) {
			return false, nil
		}
		return false, errMigrationStuck(b.spec.URL, b.spec.Origin)
	}
	if older, ok := olderLogTables[atoiOrZero(format)]; ok && slices.Equal(tables, older) {
		return false, errMigrateTooOld(b.spec.URL, format)
	}
	return false, errNotALog(b.spec.URL)
}

// flipMigrating moves the format row to 5-migrating and pushes it, so an old
// binary refuses at its next open before it can write past the segments. The
// update is conditional, so a resumed run flips nothing and keeps going.
func (b *TursoBackend) flipMigrating(ctx context.Context) error {
	if _, err := b.db.ExecContext(ctx,
		"UPDATE meta SET value = ? WHERE key = ? AND value = ?",
		migratingFormat, metaFormatKey, "5"); err != nil {
		return fmt.Errorf("syncworker: flip the migrating format: %w", err)
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return fmt.Errorf("syncworker: push the migrating format: %w", markTransferRefusal(err))
	}
	return nil
}

// sweepSegments writes every origin's segments, pulling between passes to
// catch rows an old binary wrote after the read. It stops when the drop rule
// holds, counts the rows above cover as swept, and after the bound refuses
// with the blockers named. Nothing was dropped.
func (b *TursoBackend) sweepSegments(ctx context.Context) (map[string]originWrite, map[string]int, int, error) {
	writes := make(map[string]originWrite)
	covered := make(map[string]int)
	swept := 0
	for pass := 0; pass < migrateSweepPasses; pass++ {
		maxes, err := b.logOriginMaxes(ctx)
		if err != nil {
			return nil, nil, 0, err
		}
		if err := b.writeOrigins(ctx, maxes, writes, covered); err != nil {
			return nil, nil, 0, err
		}
		if err := b.transfer(ctx, b.driver.Pull); err != nil {
			return nil, nil, 0, fmt.Errorf("syncworker: migrate pull: %w", markTransferRefusal(err))
		}
		next, err := b.logOriginMaxes(ctx)
		if err != nil {
			return nil, nil, 0, err
		}
		blockers := dropBlockers(next, covered)
		if len(blockers) == 0 {
			return writes, covered, swept, nil
		}
		n, err := b.countAbove(ctx, covered, blockers)
		if err != nil {
			return nil, nil, 0, err
		}
		swept += n
		if pass == migrateSweepPasses-1 {
			return nil, nil, 0, MarkRefusal(CodeMigrateIncomplete,
				fmt.Errorf("syncworker: rows for %s are above the migrated segments: run the migrate again to sweep them",
					strings.Join(blockers, ", ")))
		}
	}
	return writes, covered, swept, nil
}

// writeOrigins writes one pass of segments for every origin the log names, in
// origin order, recording the cover and the write. A re-run re-verifies the
// objects the last run wrote, starting every origin from seq 0.
func (b *TursoBackend) writeOrigins(ctx context.Context, maxes map[string]int, writes map[string]originWrite, covered map[string]int) error {
	origins := make([]string, 0, len(maxes))
	for origin := range maxes {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	for _, origin := range origins {
		w, err := b.writeLegacySegments(ctx, origin, maxes[origin])
		if err != nil {
			return err
		}
		writes[origin] = w
		if w.rows > 0 {
			covered[origin] = w.last
		}
	}
	return nil
}

// countAbove counts the log rows above cover for the blocker origins: the
// late rows the next pass sweeps.
func (b *TursoBackend) countAbove(ctx context.Context, covered map[string]int, blockers []string) (int, error) {
	total := 0
	for _, origin := range blockers {
		var n int
		if err := b.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM log WHERE origin = ? AND seq > ?", origin, covered[origin]).Scan(&n); err != nil {
			return 0, fmt.Errorf("syncworker: count the late rows: %w", err)
		}
		total += n
	}
	return total, nil
}
