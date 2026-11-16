package sync

import "errors"

// ErrMigrateUnconfirmed refuses a migration the operator did not confirm every
// machine is ready for. The flag says every machine already runs this release;
// a machine still on the old release cannot read format 6, and a late row it
// writes can be lost. The sentence is fixed text written in this tree, so a
// reader learns the flag and the hazard rather than a word the remote chose.
var ErrMigrateUnconfirmed = errors.New("sync: refusing to migrate without --confirm-all-upgraded, which says every machine already runs this release: a machine still on the old release cannot read format 6, and a late row it writes can be lost")

// ErrMigrateIncomplete reports a migration that found rows an old binary wrote
// after the read: running the verb again sweeps them. It is not a permanent
// refusal -- a rerun passes, so latching on it would park a machine that only
// needs one more run -- and the sentence names the rerun that ends it.
var ErrMigrateIncomplete = errors.New("sync: rows arrived while migrating; run `relevo db sync migrate` again")

// MigrateGate is the confirm rule for the migrate verb as a pure function, so
// a CLI test pins it without a network: only a confirmed run may migrate.
func MigrateGate(confirmed bool) error {
	if !confirmed {
		return ErrMigrateUnconfirmed
	}
	return nil
}
