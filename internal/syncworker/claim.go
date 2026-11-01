package syncworker

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// claimPrefix prefixes the meta row holding the claim on one identity.
const claimPrefix = "claim:"

// claimTTL is how long a claim stands against another machine's. It matches
// synclog.ClaimTTL, which this package cannot import.
const claimTTL = 24 * time.Hour

// ClaimIdentity writes the claim token for origin where the remote holds none,
// holds the caller's own, or holds one older than the TTL, then answers with
// the claim the remote holds after a push and a pull. The write alone cannot
// make the claim exclusive: the push is last-writer-wins per row, so the
// read-back narrows the race to one push, and the caller compares the token it
// gets back against its own on every attempt.
func (b *TursoBackend) ClaimIdentity(origin, token string, at time.Time) (string, time.Time, error) {
	if err := b.ready(); err != nil {
		return "", time.Time{}, err
	}
	ctx := context.Background()
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return "", time.Time{}, fmt.Errorf("syncworker: claim an identity: %w", markTransferRefusal(err))
	}
	if token != "" {
		wrote, err := b.writeClaim(ctx, origin, token, at)
		if err != nil {
			return "", time.Time{}, err
		}
		if wrote {
			if err := b.transfer(ctx, b.driver.Push); err != nil {
				return "", time.Time{}, fmt.Errorf("syncworker: push the claim: %w", markTransferRefusal(err))
			}
			if err := b.transfer(ctx, b.driver.Pull); err != nil {
				return "", time.Time{}, fmt.Errorf("syncworker: pull the claim: %w", markTransferRefusal(err))
			}
		}
	}
	return b.readClaim(ctx, origin)
}

// writeClaim stores the claim when the row allows it and says whether it did.
func (b *TursoBackend) writeClaim(ctx context.Context, origin, token string, at time.Time) (bool, error) {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("syncworker: claim an identity: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	held, heldAt, err := queryClaim(ctx, tx, origin)
	if err != nil {
		return false, err
	}
	if held != "" && held != token && at.Sub(heldAt) <= claimTTL {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)",
		claimPrefix+origin, token+" "+at.UTC().Format(time.RFC3339Nano)); err != nil {
		return false, fmt.Errorf("syncworker: claim an identity: %w", markDriverRefusal(err))
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("syncworker: claim an identity: %w", err)
	}
	return true, nil
}

// readClaim reads the claim row outside a transaction.
func (b *TursoBackend) readClaim(ctx context.Context, origin string) (string, time.Time, error) {
	var value sql.NullString
	err := b.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", claimPrefix+origin).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("syncworker: read the claim: %w", markDriverRefusal(err))
	}
	token, at := parseClaim(value.String)
	return token, at, nil
}

// queryClaim reads the claim row inside a transaction.
func queryClaim(ctx context.Context, tx *sql.Tx, origin string) (string, time.Time, error) {
	var value sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", claimPrefix+origin).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("syncworker: claim an identity: %w", markDriverRefusal(err))
	}
	token, at := parseClaim(value.String)
	return token, at, nil
}

// parseClaim reads a claim row. A row that does not parse reads as a claim made
// at the zero time: it holds the identity until the caller's own write replaces
// it, never as an absent one.
func parseClaim(value string) (string, time.Time) {
	token, stamp, _ := strings.Cut(value, " ")
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return token, time.Time{}
	}
	return token, at
}

// claimIdentity serves the verb. An identity to claim is required.
func (s *server) claimIdentity(req Request) Response {
	if req.Origin == "" {
		return refuse(req, fmt.Errorf("%w: claim_identity with no origin", ErrProtocol))
	}
	var at time.Time
	if req.At != nil {
		at = *req.At
	}
	token, claimedAt, err := s.backend.ClaimIdentity(req.Origin, req.ClaimToken, at)
	if err != nil {
		return refuse(req, err)
	}
	resp := Response{ID: req.ID, OK: true, ClaimToken: token}
	if !claimedAt.IsZero() {
		resp.ClaimAt = &claimedAt
	}
	return resp
}
