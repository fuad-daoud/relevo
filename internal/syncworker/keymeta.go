package syncworker

// The remote's passphrase material: the KDF parameters and the check value, in
// the meta table. Nothing here derives or compares a key -- a machine that
// holds the Turso token can read all of it -- but a machine needs to learn what
// the remote was set up with before it can sign anything a reader would accept.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

// The meta rows the material is kept in. The salt and the check are base64
// because meta holds text: a salt is raw bytes, and half of them are not
// representable in a TEXT column without the encoding being lossy or the column
// refusing them.
const (
	metaKDFSaltKey   = "kdf_salt"
	metaKDFKey       = "kdf"
	metaKeyCheckKey  = "key_check"
	signedFromPrefix = "signed_from."
)

// KeyMeta reads the passphrase material the remote holds.
//
// The salt is what decides whether there is a key at all: a remote has none
// until something writes one, and a remote that answers a salt has an answer
// for the other two. A row that is missing is not an error, because a fresh
// remote has none of them.
func (b *TursoBackend) KeyMeta() (KeyMeta, error) {
	if err := b.ready(); err != nil {
		return KeyMeta{}, err
	}
	// One read rather than three: the material is written as one unit, so
	// reading it row by row would let a key_init landing between two of the
	// reads answer with a mixture of two states.
	rows, err := b.db.QueryContext(context.Background(),
		"SELECT key, value FROM meta WHERE key IN (?, ?, ?)",
		metaKDFSaltKey, metaKDFKey, metaKeyCheckKey)
	if err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: read the key material: %w", markDriverRefusal(err))
	}
	defer func() { _ = rows.Close() }()
	var out KeyMeta
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return KeyMeta{}, fmt.Errorf("syncworker: read the key material: %w", err)
		}
		if key.String == metaKDFSaltKey {
			// Decoded rather than taken as text: the row holds the salt's base64,
			// and a caller comparing its own salt against this one has to be
			// comparing the bytes, not their encoding.
			out.Salt = decodeKeyValue(value.String)
			out.Present = true
			continue
		}
		if key.String == metaKDFKey {
			out.KDF = value.String
			continue
		}
		out.Check = decodeKeyValue(value.String)
	}
	if err := rows.Err(); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: read the key material: %w", err)
	}
	return out, nil
}

// decodeKeyValue reads a stored base64 value. A row that is not base64 is read
// as the bytes it holds rather than dropped: the remote is untrusted, so a
// malformed value must not make the row invisible to a caller that is deciding
// whether the remote's key is the one it holds.
func decodeKeyValue(value string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return []byte(value)
	}
	return decoded
}

// KeyInit writes the material where the remote holds none and answers with what
// it holds afterwards.
//
// The write is an insert-if-absent rather than an overwrite, and the answer is
// a read rather than an echo of the request. Together those are what make two
// machines racing the first one both end up with the winner's salt: neither
// clobbers the other's row, and both learn the same salt from the answer, so
// each can compare its own check against it and refuse the log rather than sign
// under a key no other machine shares.
func (b *TursoBackend) KeyInit(key KeyMeta) (KeyMeta, error) {
	if err := b.ready(); err != nil {
		return KeyMeta{}, err
	}
	ctx := context.Background()
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: init the key: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, row := range []struct{ key, value string }{
		{metaKDFSaltKey, encodeKeyValue(key.Salt)},
		{metaKDFKey, key.KDF},
		{metaKeyCheckKey, encodeKeyValue(key.Check)},
	} {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)",
			row.key, row.value); err != nil {
			return KeyMeta{}, fmt.Errorf("syncworker: init the key: %w", markDriverRefusal(err))
		}
	}
	if err := tx.Commit(); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: init the key: %w", err)
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: push the key: %w", markTransferRefusal(err))
	}
	return b.KeyMeta()
}

// encodeKeyValue is a byte value as the meta table holds it.
func encodeKeyValue(value []byte) string {
	return base64.StdEncoding.EncodeToString(value)
}

// writeSignedFrom records where this origin started signing, once, in the
// append's own transaction so the row and the entries it describes cannot
// disagree. The value is the sequence and a tag binding it to the key; a reader
// refuses a row whose tag does not verify, so the worker takes both from the
// exporter and only checks that the sequence is the one the batch gets.
//
// A batch with a signed entry for an origin that has no row is refused unless it
// names that sequence, with nothing written: a row naming a later sequence
// would bless the unsigned entries before it.
func writeSignedFrom(ctx context.Context, tx *sql.Tx, origin string, written []Entry, boundary Boundary) error {
	first := -1
	for n, e := range written {
		if len(e.MAC) > 0 {
			first = n
			break
		}
	}
	if first < 0 {
		return nil
	}
	var have int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM meta WHERE key = ?", signedFromPrefix+origin).Scan(&have); err != nil {
		return fmt.Errorf("syncworker: record where signing starts: %w", err)
	}
	if have > 0 {
		return nil
	}
	if boundary.Seq != written[first].Seq || len(boundary.Tag) != sha256.Size {
		return MarkRefusal(CodeBoundary, fmt.Errorf("%w: wanted %d, got %d", ErrBoundary, written[first].Seq, boundary.Seq))
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)",
		signedFromPrefix+origin, fmt.Sprintf("%d %s", boundary.Seq, encodeKeyValue(boundary.Tag))); err != nil {
		return fmt.Errorf("syncworker: record where signing starts: %w", err)
	}
	return nil
}

// readSignedFrom reads each origin's stored boundary.
//
// The meta table is written by other machines, so a row that does not parse is
// not skipped: it comes back with sequence 0 or no tag, and the reader refuses
// that origin the way it refuses a boundary whose tag fails.
func (b *TursoBackend) readSignedFrom(ctx context.Context, out *MarkState) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT key, value FROM meta WHERE key LIKE ?", signedFromPrefix+"%")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		seq, tag := parseBoundary(value.String)
		if out.SignedFrom == nil {
			out.SignedFrom = make(map[string]int)
			out.SignedTags = make(map[string][]byte)
		}
		origin := key.String[len(signedFromPrefix):]
		out.SignedFrom[origin] = seq
		out.SignedTags[origin] = tag
	}
	return rows.Err()
}

// parseBoundary reads "<seq> <base64 tag>"; what does not parse is 0 and nil.
func parseBoundary(value string) (int, []byte) {
	fields := strings.Split(value, " ")
	seq, err := strconv.Atoi(fields[0])
	if err != nil || seq < 0 {
		return 0, nil
	}
	if len(fields) != 2 {
		return seq, nil
	}
	tag, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return seq, nil
	}
	return seq, tag
}

// readMetaPositions reads the meta rows whose key begins with prefix into a map
// keyed by the rest of it.
//
// The meta table is written by other machines, so a row that is not a
// non-negative number is skipped rather than failing the read: one unreadable
// row must not stop a reader from reading every other one, which is the same
// rule a malformed mark is read under.
func (b *TursoBackend) readMetaPositions(ctx context.Context, prefix string, into *map[string]int) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT key, value FROM meta WHERE key LIKE ?", prefix+"%")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		position, err := strconv.Atoi(value.String)
		if err != nil || position < 0 {
			continue
		}
		if *into == nil {
			*into = make(map[string]int)
		}
		(*into)[key.String[len(prefix):]] = position
	}
	return rows.Err()
}

// keyMeta answers with the passphrase material the remote holds.
func (s *server) keyMeta(req Request) Response {
	key, err := s.backend.KeyMeta()
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, KeyMeta: &key}
}

// keyInit writes the material where the remote holds none and answers with what
// it holds afterwards. A request naming none is a refusal rather than a write
// of empty rows: a key_init with no salt would insert an empty one and make
// the remote look like it holds a key.
func (s *server) keyInit(req Request) Response {
	if req.KeyMeta == nil {
		return refuse(req, fmt.Errorf("%w: key_init with no key", ErrProtocol))
	}
	key, err := s.backend.KeyInit(*req.KeyMeta)
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, KeyMeta: &key}
}
