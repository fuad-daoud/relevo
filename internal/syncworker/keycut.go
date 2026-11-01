package syncworker

import (
	"context"
	"database/sql"
	"fmt"
)

// The worker half of the cut extension: an origin's own vouch for the tail it
// appended before it learned of the rekey, forward only and within the log.
// Authenticity is the reader's tag check; the worker's job is the bound.

// PublishEpochCut extends this worker's own origin's cut, forward only and
// within the log. A pull makes the head this attempt's remote rather than the
// replica's last pull: a vouch computed against a stale replica could name a
// sequence the remote never appended. The extension is a raise, never a
// rewrite, so a late or repeated vouch cannot shrink the fence; a vouch that
// raises nothing is answered as a success.
func (b *TursoBackend) PublishEpochCut(cut EpochCut) error {
	if err := b.ready(); err != nil {
		return err
	}
	ctx := context.Background()
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", markTransferRefusal(err))
	}
	if err := b.cutExtension(ctx, cut); err != nil {
		return err
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", markTransferRefusal(err))
	}
	return nil
}

// cutExtension is the one transaction a vouch writes in: the head is read and
// guarded before any row is written.
func (b *TursoBackend) cutExtension(ctx context.Context, cut EpochCut) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var head sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		"SELECT MAX(seq) FROM log WHERE origin = ?", b.spec.Origin).Scan(&head); err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", markDriverRefusal(err))
	}
	if cut.Seq < 0 || cut.Seq > int(head.Int64) {
		return fmt.Errorf("syncworker: cut extension: the log's head is %d, not the %d named", head.Int64, cut.Seq)
	}
	if err := writeCutExtension(ctx, tx, b.spec.Origin, cut); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", err)
	}
	return nil
}

// writeCutExtension stores the vouch, raising only, in the same row shape a
// rekey writes, so a reader reads extensions and rekey cuts under one rule.
func writeCutExtension(ctx context.Context, tx *sql.Tx, origin string, cut EpochCut) error {
	stored, have, err := readStoredCut(ctx, tx, origin)
	if err != nil {
		return err
	}
	if have && (cut.Epoch < stored.Epoch || (cut.Epoch == stored.Epoch && cut.Seq <= stored.Seq)) {
		return nil
	}
	if cut.Epoch < 1 || len(cut.Tag) == 0 {
		return fmt.Errorf("%w: key_cut with epoch %d and a %d-byte tag", ErrProtocol, cut.Epoch, len(cut.Tag))
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)",
		epochCutPrefix+origin,
		fmt.Sprintf("%d %d %s", cut.Epoch, cut.Seq, encodeKeyValue(cut.Tag))); err != nil {
		return fmt.Errorf("syncworker: push the cut extension: %w", markDriverRefusal(err))
	}
	return nil
}

// readStoredCut reads the origin's stored cut row; have is false when it holds
// none.
func readStoredCut(ctx context.Context, tx *sql.Tx, origin string) (EpochCut, bool, error) {
	var value sql.NullString
	if err := tx.QueryRowContext(ctx,
		"SELECT value FROM meta WHERE key = ?", epochCutPrefix+origin).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return EpochCut{}, false, nil
		}
		return EpochCut{}, false, fmt.Errorf("syncworker: push the cut extension: %w", markDriverRefusal(err))
	}
	epoch, seq, tag := parseCut(value.String)
	return EpochCut{Epoch: epoch, Seq: seq, Tag: tag}, epoch > 0, nil
}

// keyCut serves the verb. A request that does not hold together -- no cut, or
// a cut with an epoch or tag nothing could have written -- is refused before
// the backend is reached.
func (s *server) keyCut(req Request) Response {
	if req.Cut == nil {
		return refuse(req, fmt.Errorf("%w: key_cut with no cut", ErrProtocol))
	}
	if req.Cut.Epoch < 1 || len(req.Cut.Tag) == 0 {
		return refuse(req, fmt.Errorf("%w: key_cut with epoch %d, seq %d, no tag worth carrying",
			ErrProtocol, req.Cut.Epoch, req.Cut.Seq))
	}
	if err := s.backend.PublishEpochCut(*req.Cut); err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true}
}
