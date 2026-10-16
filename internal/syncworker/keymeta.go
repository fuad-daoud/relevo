package syncworker

// The remote's passphrase material: the KDF parameters and the check value, in
// the meta table. Nothing here derives or compares a key -- a machine that
// holds the Turso token can read all of it -- but a machine needs to learn what
// the remote was set up with before it can sign anything a reader would accept.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
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
	metaKeyEpochKey  = "key_epoch"
	keyWrappedPrefix = "key_wrapped."
	signedFromPrefix = "signed_from."
)

// ErrKeyEpoch is a key_epoch row this build refuses to read: the remote is
// untrusted, and a row that is not the canonical decimal of an epoch 2 or later
// is not a row this build can act on. An absent row is not read here at all --
// it is epoch 1, which is the caller's default.
var ErrKeyEpoch = errors.New("syncworker: the remote's key_epoch row is not an epoch this build reads")

// KeyRekeySpec is what one rekey writes: the epoch it is rotating from, the
// material the remote is to hold, and the cut each origin's history is fenced
// at.
type KeyRekeySpec struct {
	// From is the epoch the caller read the remote at. The rekey is a
	// compare-and-set against it, so a remote whose epoch has moved under the
	// caller -- another machine rekeyed first -- refuses and changes nothing.
	From int
	// Key is the material the remote holds afterwards: a fresh salt, the
	// derivation, the new key's check, the epoch it names, and the key it
	// replaced sealed for the machines still holding it.
	Key KeyMeta
	// Cuts is one cut per origin in the log, each at that origin's head as the
	// caller observed it.
	Cuts map[string]EpochCut
}

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
	// One read rather than several: the material is written as one unit, so
	// reading it row by row would let a rekey landing between two of the
	// reads answer with a mixture of two states.
	rows, err := b.db.QueryContext(context.Background(),
		"SELECT key, value FROM meta WHERE key IN (?, ?, ?, ?) OR key LIKE ?",
		metaKDFSaltKey, metaKDFKey, metaKeyCheckKey, metaKeyEpochKey, keyWrappedPrefix+"%")
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
		if key.String == metaKeyEpochKey {
			epoch, err := parseKeyEpoch(value.String)
			if err != nil {
				return KeyMeta{}, err
			}
			out.Epoch = epoch
			continue
		}
		if epoch, ok := strings.CutPrefix(key.String, keyWrappedPrefix); ok {
			epoch, err := parseWrappedEpoch(epoch)
			if err != nil {
				return KeyMeta{}, err
			}
			if out.Wrapped == nil {
				out.Wrapped = make(map[int][]byte)
			}
			out.Wrapped[epoch] = decodeKeyValue(value.String)
			continue
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
	if out.Epoch == 0 {
		out.Epoch = 1
	}
	return out, nil
}

// parseKeyEpoch reads the key_epoch row the remote holds. The row exists only
// once a rekey has written it, so a present row must be the canonical decimal
// of an epoch 2 or later: anything else is a remote this build cannot act on,
// refused rather than read as epoch 1.
func parseKeyEpoch(value string) (int, error) {
	epoch, err := strconv.Atoi(value)
	if err != nil || strconv.Itoa(epoch) != value || epoch < 2 {
		return 0, fmt.Errorf("syncworker: read the key material: %q: %w", value, ErrKeyEpoch)
	}
	return epoch, nil
}

// parseWrappedEpoch reads the epoch a key_wrapped row is for, on the same rule
// the key_epoch row is read under: the name is the remote's claim, and a claim
// this build cannot read is refused rather than skipped.
func parseWrappedEpoch(name string) (int, error) {
	epoch, err := strconv.Atoi(name)
	if err != nil || strconv.Itoa(epoch) != name || epoch < 1 {
		return 0, fmt.Errorf("syncworker: read the key material: %q is not an epoch: %w", name, ErrKeyEpoch)
	}
	return epoch, nil
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

// epochCutPrefix prefixes the meta row holding one origin's cut, named for the
// origin it fences.
const epochCutPrefix = "epoch_cut."

// KeyRekey rotates the log's key to the epoch the spec names: a fresh pull, one
// transaction that verifies the caller's cuts against the log's own heads and
// compare-and-sets the epoch before anything is written, then a push and a
// read-back answer. A refusal anywhere in the transaction leaves the remote as
// it was.
func (b *TursoBackend) KeyRekey(spec KeyRekeySpec) (KeyMeta, error) {
	if err := b.ready(); err != nil {
		return KeyMeta{}, err
	}
	ctx := context.Background()
	// The pull is what makes the reads below this attempt's remote rather than
	// the last one's: the heads the cuts are checked against and the epoch the
	// CAS is set against both have to be what the remote holds now.
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: rekey the key: %w", markTransferRefusal(err))
	}
	if err := b.rekeyTx(ctx, spec); err != nil {
		return KeyMeta{}, err
	}
	if err := b.transfer(ctx, b.driver.Push); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: push the rekey: %w", markTransferRefusal(err))
	}
	if err := b.transfer(ctx, b.driver.Pull); err != nil {
		return KeyMeta{}, fmt.Errorf("syncworker: pull the rekey: %w", markTransferRefusal(err))
	}
	held, err := b.KeyMeta()
	if err != nil {
		return KeyMeta{}, err
	}
	if held.Epoch != spec.Key.Epoch || !bytes.Equal(held.Check, spec.Key.Check) {
		return KeyMeta{}, MarkRefusal(CodeEpochRace, fmt.Errorf(
			"syncworker: the remote is not at epoch %d with this key: another machine rekeyed first", spec.Key.Epoch))
	}
	return held, nil
}

// rekeyTx is the one transaction a rekey writes everything in. The heads are
// read and the epoch is set before any row is written, so a caller whose
// observation is stale writes nothing at all.
func (b *TursoBackend) rekeyTx(ctx context.Context, spec KeyRekeySpec) error {
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("syncworker: rekey the key: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	heads, err := readLogHeads(ctx, tx)
	if err != nil {
		return err
	}
	if err := checkPredictedCuts(heads, spec.Cuts); err != nil {
		return err
	}
	if err := casEpoch(ctx, tx, spec.From, spec.Key.Epoch); err != nil {
		return err
	}
	if err := writeRekeyRows(ctx, tx, spec); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("syncworker: rekey the key: %w", err)
	}
	return nil
}

// writeRekeyRows replaces the key material with the new epoch's, stores the key
// it replaced sealed for the machines still holding it, and writes each origin's
// cut. Every write is a replace, because a rekey rewrites what a previous one
// wrote rather than finding it absent.
func writeRekeyRows(ctx context.Context, tx *sql.Tx, spec KeyRekeySpec) error {
	rows := []struct{ key, value string }{
		{metaKDFSaltKey, encodeKeyValue(spec.Key.Salt)},
		{metaKDFKey, spec.Key.KDF},
		{metaKeyCheckKey, encodeKeyValue(spec.Key.Check)},
		{metaKeyEpochKey, strconv.Itoa(spec.Key.Epoch)},
	}
	for epoch, wrapped := range spec.Key.Wrapped {
		rows = append(rows, struct{ key, value string }{
			keyWrappedPrefix + strconv.Itoa(epoch), encodeKeyValue(wrapped)})
	}
	for origin, cut := range spec.Cuts {
		rows = append(rows, struct{ key, value string }{
			epochCutPrefix + origin,
			fmt.Sprintf("%d %d %s", cut.Epoch, cut.Seq, encodeKeyValue(cut.Tag))})
	}
	for _, row := range rows {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", row.key, row.value); err != nil {
			return fmt.Errorf("syncworker: rekey the key: %w", markDriverRefusal(err))
		}
	}
	return nil
}

// readLogHeads is every origin's highest sequence in the log, the fact a cut is
// predicted from and checked against.
func readLogHeads(ctx context.Context, tx *sql.Tx) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT origin, MAX(seq) FROM log GROUP BY origin")
	if err != nil {
		return nil, fmt.Errorf("syncworker: rekey the key: %w", markDriverRefusal(err))
	}
	defer func() { _ = rows.Close() }()
	heads := make(map[string]int)
	for rows.Next() {
		var origin string
		var seq sql.NullInt64
		if err := rows.Scan(&origin, &seq); err != nil {
			return nil, fmt.Errorf("syncworker: rekey the key: %w", err)
		}
		heads[origin] = int(seq.Int64)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("syncworker: rekey the key: %w", err)
	}
	return heads, nil
}

// checkPredictedCuts refuses a rekey whose cuts are not the log's own heads: an
// origin whose head moved between the caller's read and this transaction, or
// one the caller named that the log does not hold, is an observation that is no
// longer true, and nothing of the rekey may land on it.
func checkPredictedCuts(heads map[string]int, cuts map[string]EpochCut) error {
	for origin, seq := range heads {
		cut, ok := cuts[origin]
		if !ok || cut.Seq != seq {
			return fmt.Errorf("syncworker: rekey the key: %s's head is %d, not the %d the caller predicted",
				origin, seq, cut.Seq)
		}
	}
	for origin := range cuts {
		if _, ok := heads[origin]; !ok {
			return fmt.Errorf("syncworker: rekey the key: %s has no entries in the log", origin)
		}
	}
	return nil
}

// casEpoch moves key_epoch from to to, or refuses. An update that moved no row
// says the remote's epoch is not the one the caller read -- another machine
// rekeyed first, or a caller that read a remote it was never looking at -- and
// the class says which, because no retry with the same material can get past
// it.
func casEpoch(ctx context.Context, tx *sql.Tx, from, to int) error {
	// The row is inserted at its default before the set, so an epoch-1 remote
	// -- one that holds no row at all -- CASes from 1 the same way an older
	// one CASes from its own row.
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)",
		metaKeyEpochKey, "1"); err != nil {
		return fmt.Errorf("syncworker: rekey the key: %w", markDriverRefusal(err))
	}
	res, err := tx.ExecContext(ctx,
		"UPDATE meta SET value = ? WHERE key = ? AND value = ?",
		strconv.Itoa(to), metaKeyEpochKey, strconv.Itoa(from))
	if err != nil {
		return fmt.Errorf("syncworker: rekey the key: %w", markDriverRefusal(err))
	}
	moved, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("syncworker: rekey the key: %w", err)
	}
	if moved == 0 {
		return MarkRefusal(CodeEpochRace, fmt.Errorf(
			"syncworker: the remote is not at epoch %d: another machine rekeyed first", from))
	}
	return nil
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

// readEpochCuts reads each origin's stored cut.
//
// The meta table is written by other machines, so a row that does not parse is
// not skipped either: it comes back as the cut nothing verifies under -- epoch
// 0, sequence 0, no tag -- and the reader holds that origin the way it holds a
// boundary whose tag fails.
func (b *TursoBackend) readEpochCuts(ctx context.Context, out *MarkState) error {
	rows, err := b.db.QueryContext(ctx,
		"SELECT key, value FROM meta WHERE key LIKE ?", epochCutPrefix+"%")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var key, value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if out.EpochCuts == nil {
			out.EpochCuts = make(map[string]EpochCut)
		}
		origin := key.String[len(epochCutPrefix):]
		epoch, seq, tag := parseCut(value.String)
		out.EpochCuts[origin] = EpochCut{Epoch: epoch, Seq: seq, Tag: tag}
	}
	return rows.Err()
}

// parseCut reads "<epoch> <seq> <base64 tag>"; what does not parse is the zero
// cut: epoch 0, sequence 0, no tag.
func parseCut(value string) (int, int, []byte) {
	fields := strings.Fields(value)
	if len(fields) != 3 {
		return 0, 0, nil
	}
	epoch, err := strconv.Atoi(fields[0])
	if err != nil || epoch < 1 {
		return 0, 0, nil
	}
	seq, err := strconv.Atoi(fields[1])
	if err != nil || seq < 0 {
		return 0, 0, nil
	}
	tag, err := base64.StdEncoding.DecodeString(fields[2])
	if err != nil {
		return 0, 0, nil
	}
	return epoch, seq, tag
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

// keyRekey rotates the log's key to the epoch the request names. A request that
// does not hold together -- no key, no epoch it read, a target that is not the
// next epoch after it, or a cut fencing another epoch than the one being
// written -- is refused before the backend is reached: a rekey writes
// everything or nothing, and a spec that does not hold together is nothing.
func (s *server) keyRekey(req Request) Response {
	if req.KeyMeta == nil {
		return refuse(req, fmt.Errorf("%w: key_rekey with no key", ErrProtocol))
	}
	if req.Epoch < 1 {
		return refuse(req, fmt.Errorf("%w: key_rekey with no epoch it read", ErrProtocol))
	}
	if req.KeyMeta.Epoch != req.Epoch+1 {
		return refuse(req, fmt.Errorf("%w: key_rekey to epoch %d from epoch %d", ErrProtocol, req.KeyMeta.Epoch, req.Epoch))
	}
	for origin, cut := range req.Cuts {
		if cut.Epoch != req.KeyMeta.Epoch {
			return refuse(req, fmt.Errorf("%w: a cut for %s names epoch %d, not %d",
				ErrProtocol, origin, cut.Epoch, req.KeyMeta.Epoch))
		}
	}
	key, err := s.backend.KeyRekey(KeyRekeySpec{From: req.Epoch, Key: *req.KeyMeta, Cuts: req.Cuts})
	if err != nil {
		return refuse(req, err)
	}
	return Response{ID: req.ID, OK: true, KeyMeta: &key}
}
