package db

// The repo upsert, on its own because it is the one write in this package whose
// key and whose uniqueness guard are different sets of columns. Everything else
// here resolves a record by the columns its unique index covers; repo has two
// partial indexes and a record carries both keys, so resolving it is a question
// about which of the two indexes is asked first.

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UpsertRepo inserts or updates r by its natural key within this handle's
// origin. The natural key is a pair, not a column: a repo row is addressed by
// its origin URL or by its common dir, and a record carrying both resolves to
// whichever of the two this origin already holds a row under. A hit fills the
// column the row does not carry and no other row holds.
//
// The key it resolves by and the key the row is unique on are not the same set,
// and that is the whole of what this function is about. `repo` carries two
// independent partial unique indexes -- migration 014's
// repo_origin_origin_url_uidx and repo_origin_common_dir_uidx -- so a row is
// addressed by its origin URL *or* by its common dir, and a row holding only
// one of the two is in exactly one index and not the other. Resolving by the
// preferred column alone therefore cannot see a row that holds only the other
// one, and the insert that follows supplies both columns and is refused by the
// index the row was sitting in: `UNIQUE constraint failed:
// repo.(origin, common_dir)`.
//
// That is not a hypothetical shape. A repo row gets there whenever the checkout
// had no remote when it was first recorded and has one now, and the origin
// backfill leaves rows in exactly that shape: the twin it drops is a second row
// for the same key, so what survives is one row carrying the one column it
// happened to hold. Once that row is the only one for its dir, every later
// upsert naming the dir misses it and collides with it.
//
// So the resolution falls back: origin_url first, because a URL is the more
// stable of the two, then common_dir, then the insert. The fallback is what
// makes insert-then-update converge on one row per origin per dir whichever
// column the row happens to carry and whichever order the columns arrive in.
func (t *Tx) UpsertRepo(r Repo) (string, error) {
	switch {
	case r.OriginURL != nil && r.CommonDir != nil:
		return t.upsertRepoBy(r)
	case r.OriginURL != nil:
		return t.upsertRepoByOne("origin_url", *r.OriginURL, r)
	case r.CommonDir != nil:
		return t.upsertRepoByOne("common_dir", *r.CommonDir, r)
	default:
		return "", fmt.Errorf("db: upsert repo: Repo needs OriginURL or CommonDir: %w", ErrInvalid)
	}
}

// upsertRepoByOne is UpsertRepo for a record carrying only one of the two keys.
//
// It takes the column and the value rather than reading them off r, so the one
// column a caller resolved on is never also treated as absent. A key this
// origin's index holds none of is a row this call inserts, which is the whole
// difference from upsertRepoBy and the reason it is its own function: there is
// no second key to fall back to, so the miss is final. And there is no second
// key to write either, so nothing here can collide: the only column the record
// carries is the one the lookup resolved on and the row already holds.
func (t *Tx) upsertRepoByOne(col, val string, r Repo) (string, error) {
	id, err := t.findRepoBy(col, val)
	if err != nil {
		return "", err
	}
	if id == "" {
		return t.insertRepo(r)
	}
	return t.fillRepo(id, "", "", r)
}

// upsertRepoBy is UpsertRepo for a record carrying both keys.
//
// Both indexes are read before either is written, because which row the record
// resolves to and which columns that row may take are two different questions.
// The URL wins the resolution, since it is the more stable key; the dir decides
// only whether the URL row may be given the dir, which it may not where another
// row already holds it.
func (t *Tx) upsertRepoBy(r Repo) (string, error) {
	byURL, err := t.findRepoBy("origin_url", *r.OriginURL)
	if err != nil {
		return "", err
	}
	byDir, err := t.findRepoBy("common_dir", *r.CommonDir)
	if err != nil {
		return "", err
	}
	switch {
	case byURL != "":
		return t.fillRepo(byURL, byURL, byDir, r)
	case byDir != "":
		return t.fillRepo(byDir, byURL, byDir, r)
	default:
		return t.insertRepo(r)
	}
}

// findRepoBy is the row this origin's origin_url or common_dir index holds for
// one key, or the empty id when the index holds none.
//
// The scope is originScope's rather than this origin alone, and that is what a
// row written before the origin column existed takes: fillRepo stamps the origin
// alongside every column it writes, so a row this scope sees and this origin does
// not is a row that is about to become this origin's.
func (t *Tx) findRepoBy(col, val string) (string, error) {
	var id string
	err := t.queryRow(`SELECT id FROM repo WHERE `+originScope+` AND `+col+` = ?`,
		t.origin, val).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	default:
		return "", fmt.Errorf("db: upsert repo: select %s: %w", col, mapBusy(err))
	}
}

// fillRepo is the update half. It writes onto id every column of r that the row
// does not carry and that no other row of this origin holds, and returns id
// either way.
//
// A column another row holds is left alone, and that guard is what keeps the
// update half from being the insert half's failure one step later. Writing it
// would move the key off the row that holds it, and moving a key is a repoint:
// the rows naming that row, and the bindings whose history followed it, are not
// this call's to move. So a record naming a URL and a dir that belong to two
// different rows resolves to the URL's row and leaves the dir where it is --
// which is a decision the repoint pass settles, and not one this call makes
// twice by colliding with it.
//
// The origin is stamped alongside a column rather than on its own, for the same
// reason: stamping a row whose two keys another row already holds is exactly the
// collision the backfill's twin rule exists to settle, and this call has not
// settled it. A row that needs only a stamp is left for that pass.
func (t *Tx) fillRepo(id, urlRow, dirRow string, r Repo) (string, error) {
	var originURL, commonDir sql.Null[string]
	if err := t.queryRow(`SELECT origin_url, common_dir FROM repo WHERE id = ?`, id).
		Scan(&originURL, &commonDir); err != nil {
		return "", fmt.Errorf("db: upsert repo: read the row to fill: %w", mapBusy(err))
	}
	free := func(held string) bool { return held == "" || held == id }
	if !originURL.Valid && r.OriginURL != nil && free(urlRow) {
		if _, err := t.exec(`UPDATE repo SET origin = ?, origin_url = ? WHERE id = ?`, t.origin, *r.OriginURL, id); err != nil {
			return "", fmt.Errorf("db: upsert repo: fill origin_url: %w", mapBusy(err))
		}
	}
	if !commonDir.Valid && r.CommonDir != nil && free(dirRow) {
		if _, err := t.exec(`UPDATE repo SET origin = ?, common_dir = ? WHERE id = ?`, t.origin, *r.CommonDir, id); err != nil {
			return "", fmt.Errorf("db: upsert repo: fill common_dir: %w", mapBusy(err))
		}
	}
	return id, nil
}

// insertRepo is the record whose two keys this origin holds neither of. The
// values that go in are r's, unwritten and unnormalised here: normalising is
// the caller's decision and it is the same string the lookup used, so a
// normalisation the caller did not apply would only move the collision.
func (t *Tx) insertRepo(r Repo) (string, error) {
	id := NewID()
	firstSeen := r.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}
	if _, err := t.exec(`INSERT INTO repo (id, origin, origin_url, common_dir, first_seen) VALUES (?, ?, ?, ?, ?)`,
		id, t.origin, nullableString(r.OriginURL), nullableString(r.CommonDir), formatTime(firstSeen)); err != nil {
		return "", fmt.Errorf("db: upsert repo: insert: %w", mapBusy(err))
	}
	return id, nil
}

func (d *DB) UpsertRepo(r Repo) (string, error) {
	var id string
	err := d.Tx(func(t *Tx) error {
		var err error
		id, err = t.UpsertRepo(r)
		return err
	})
	return id, err
}
