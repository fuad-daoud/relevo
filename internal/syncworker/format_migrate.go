package syncworker

import (
	"context"
	"fmt"
	"slices"
	"strconv"
)

// The remote while the migrate verb moves it from format 5 to this build's:
// the format row it carries in between, and which table sets count as that
// move rather than as another database. A normal hello answers the migrate
// refusal on these sets, and a migrate hello opens the ones the verb can
// still finish.

// migratingFormat is the format row a remote carries while the migrate verb
// is moving it. It names no readable schema, so only the migrate verb opens
// it, and only in a shape that verb can finish.
const migratingFormat = "5-migrating"

// formatFive is the only format this release's migrate verb finishes: an
// older one needs a release that still opens it, so it is refused as too old
// instead.
const formatFive = 5

// isMigratingTables reports whether the table set is a migration in progress:
// every table is one the move between formats 5 and 6 passes through, and
// meta is present to carry the format row. The shape is broad on purpose: a
// crashed run can leave the new tables beside the old ones, or a half-applied
// drop behind.
func isMigratingTables(tables []string) bool {
	meta := false
	for _, table := range tables {
		switch table {
		case "heads", "log", "marks", "meta", "shared":
			if table == "meta" {
				meta = true
			}
		default:
			return false
		}
	}
	return meta
}

// canResumeMigration reports whether the migrate verb can still finish on
// this table set: the format-5 tables it reads from are all still there. A
// set past the drop holds no source left to migrate.
func canResumeMigration(tables []string) bool {
	return slices.Contains(tables, "log") &&
		slices.Contains(tables, "marks") &&
		slices.Contains(tables, "meta")
}

// ensureLogForMigrate opens an unmigrated remote for the migrate verb, and
// nothing else: the verb reads the format-5 rows itself, so it opens before
// any pull, and it never creates a log a normal hello would then mistake for
// migrated. Anything the verb cannot finish is refused rather than opened.
func (b *TursoBackend) ensureLogForMigrate(ctx context.Context, url string) error {
	tables, err := userTables(ctx, b.db)
	if err != nil {
		return fmt.Errorf("syncworker: read the remote's tables: %w", err)
	}
	if len(tables) == 0 {
		return errNotALog(url)
	}
	// A remote without a meta table carries no format row to read, so the
	// tables are classified before the row is read: reading first would fail
	// on the missing table instead of refusing with the remote's name.
	if !isLogTables(tables) && !isOlderTables(tables) && !isMigratingTables(tables) {
		return errNotALog(url)
	}
	format, err := readFormat(ctx, b.db)
	if err != nil {
		return err
	}
	if format == strconv.Itoa(logFormatVersion) && isLogTables(tables) {
		return nil
	}
	if format == strconv.Itoa(formatFive) && slices.Equal(tables, olderLogTables[formatFive]) {
		return nil
	}
	if format == migratingFormat && isMigratingTables(tables) {
		if canResumeMigration(tables) {
			return nil
		}
		return errMigrationStuck(url, b.spec.Origin)
	}
	if older, ok := olderLogTables[atoiOrZero(format)]; ok && slices.Equal(tables, older) {
		return errMigrateTooOld(url, format)
	}
	return errNotALog(url)
}

// errMigrationStuck is the refusal a remote gets when it is mid-migration in
// a shape this release cannot resume: the drop ran past the tables the verb
// reads from. The bucket's segment objects are named as intact because they
// are the copy a human recovers from, and a human must look before retrying.
func errMigrationStuck(url, origin string) error {
	return MarkRefusal(CodeRemote,
		fmt.Errorf("sync: refusing remote %s: it is mid-migration in a shape this release cannot resume; the bucket's %s/log/ objects are intact, so have a human look before retrying",
			url, origin))
}

// errMigrateTooOld is the refusal a remote at a format older than 5 gets from
// a migrate hello. It names the format because the fix is a release that
// still migrates that one, not this one.
func errMigrateTooOld(url, format string) error {
	return MarkRefusal(CodeRemote,
		fmt.Errorf("sync: refusing remote %s at format %s: this release migrates format 5 only", url, format))
}
