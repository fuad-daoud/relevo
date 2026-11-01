package syncworker

// The remote's passphrase material: the KDF parameters and the check value, in
// the meta table. Nothing here derives or compares a key; a machine reads it to
// learn what the remote was set up with before signing anything a reader would
// accept.

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

// The meta rows the material is kept in. Salt and check are base64 because meta
// holds text, which cannot carry raw bytes losslessly.
const (
	metaKDFSaltKey   = "kdf_salt"
	metaKDFKey       = "kdf"
	metaKeyCheckKey  = "key_check"
	metaKeyEpochKey  = "key_epoch"
	keyWrappedPrefix = "key_wrapped."
	signedFromPrefix = "signed_from."
)

// ErrKeyEpoch is a key_epoch row this build refuses to read: a row that is not
// the canonical decimal of an epoch 2 or later, from an untrusted remote, is
// not a row this build can act on. An absent row is epoch 1, the caller's
// default.
var ErrKeyEpoch = errors.New("syncworker: the remote's key_epoch row is not an epoch this build reads")

// KeyRekeySpec is what one rekey writes: the epoch it rotates from, the
// material the remote holds afterwards, and the cut each origin's history is
// fenced at.
type KeyRekeySpec struct {
	// From is the epoch the caller read the remote at. The rekey is a
	// compare-and-set against it, so a remote whose epoch moved under the
	// caller refuses and changes nothing.
	From int
	// Key is the material the remote holds afterwards: fresh salt, derivation,
	// check, epoch, and the replaced key sealed for machines still holding it.
	Key KeyMeta
	// Cuts is one cut per origin in the log, each at that origin's head as the
	// caller observed it.
	Cuts map[string]EpochCut
}

// KeyMeta reads the passphrase material the remote holds. The salt decides
// whether there is a key at all, and a missing row is not an error: a fresh
// remote has none of them.
func (b *TursoBackend) KeyMeta() (KeyMeta, error) {
	if err := b.ready(); err != nil {
		return KeyMeta{}, err
	}
	// One read rather than several: the material is written as one unit, so a
	// rekey landing between two reads would answer with a mixture of states.
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
			// Decoded rather than taken as text: a caller compares its own salt
			// against the bytes, not their encoding.
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

// parseKeyEpoch reads the key_epoch row, present only after a rekey. It must
// be the canonical decimal of an epoch 2 or later; anything else is refused
// rather than read as epoch 1.
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

// decodeKeyValue reads a stored base64 value. A row that is not base64 reads as
// the bytes it holds rather than dropped: the remote is untrusted, so a
// malformed value must not hide the row from a caller comparing keys.
func decodeKeyValue(value string) []byte {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return []byte(value)
	}
	return decoded
}

// KeyInit writes the material where the remote holds none and answers with
// what it holds afterwards. The write is insert-if-absent and the answer is a
// read, so two machines racing the first one both end up with the winner's
// salt rather than each keeping its own; each racer compares its own check
// against the answer and refuses the log rather than sign under a key no
// other machine shares.
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
// read-back answer.
func (b *TursoBackend) KeyRekey(spec KeyRekeySpec) (KeyMeta, error) {
	if err := b.ready(); err != nil {
		return KeyMeta{}, err
	}
	ctx := context.Background()
	// The pull makes the reads below this attempt's remote rather than the last
	// one's: both the heads and the epoch must be what the remote holds now.
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

// rekeyTx is the one transaction a rekey writes everything in: heads are read
// and the epoch is set before any row is written, so a stale caller writes
// nothing at all.
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

// writeRekeyRows replaces the key material with the new epoch's, stores the
// sealed key it replaced, and writes each origin's cut. Every write is a
// replace: a rekey rewrites what the previous one wrote.
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

// checkPredictedCuts refuses a rekey whose cuts are not the log's own heads: a
// moved head or a named origin the log does not hold is an observation that is
// no longer true, and nothing of the rekey may land on it.
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
// says the remote's epoch is not the one the caller read, and the class says
// so, because no retry with the same material can get past it.
func casEpoch(ctx context.Context, tx *sql.Tx, from, to int) error {
	// The row is inserted at its default before the set, so an epoch-1 remote
	// with no row at all CASes from 1 like any other.
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
// disagree. The row is "<seq> <base64 tag>", both taken from the exporter; a
// batch with a signed entry for an origin with no row is refused unless the
// boundary names its first signed entry's sequence.
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

// readSignedFrom reads each origin's stored boundary. The meta table is written
// by other machines, so a row that does not parse comes back with sequence 0 or
// no tag, and the reader refuses that origin like a boundary whose tag fails.
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

// readEpochCuts reads each origin's stored cut. The meta table is written by
// other machines, so a row that does not parse comes back as the cut nothing
// verifies under, and the reader holds that origin like a failed boundary.
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
// cut.
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
// keyed by the rest of it. The meta table is written by other machines, so a
// row that is not a non-negative number is skipped: one unreadable row must not
// stop a reader from reading the rest, the same rule a malformed mark is read
// under.
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

// keyInit writes the material where the remote holds none and answers with
// what it holds afterwards. A request naming none is refused: an empty salt
// would make the remote look like it holds a key.
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
// does not hold together -- no key, a wrong epoch step, or a cut fencing
// another epoch -- is refused before the backend is reached: a rekey writes
// everything or nothing.
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
