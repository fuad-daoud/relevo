package db

// The repair for a renamed remote: it folds the repo row of the old URL into
// the row of the new one, or renames it, and rewrites the tickets and records
// that still name the old owner/repo. Reading and writing are separate phases
// so a dry run is the plan alone and cannot reach a write.

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrNothingToRename reports that neither URL names a repo row in this
// handle's scope, which is a typo far more often than a repaired state.
var ErrNothingToRename = errors.New("no repo row holds either URL")

// RenameRepoParams names the repair. The URLs are normalised by the caller, and
// so are the owner/repo paths derived from them; an empty path turns the ticket
// pass off.
type RenameRepoParams struct {
	FromURL, ToURL             string
	FromOwnerRepo, ToOwnerRepo string
	AdoptCWD                   []string
	DryRun                     bool
}

// RenameRepoCounts is what the repair changed, or in a dry run what it would.
type RenameRepoCounts struct {
	DryRun            bool   `json:"dry_run"`
	From              string `json:"from"`
	To                string `json:"to"`
	ReposMerged       int    `json:"repos_merged"`
	ReposRenamed      int    `json:"repos_renamed"`
	BindingsRepointed int    `json:"bindings_repointed"`
	TicketsRewritten  int    `json:"tickets_rewritten"`
	BindingsAdopted   int    `json:"bindings_adopted"`
	RecordsRewritten  int    `json:"records_rewritten"`
}

type idValue struct{ id, value string }

type renamePlan struct {
	fromID, toID   string
	targetID       string
	carry          *consentPair
	bindingTickets []idValue
	chainTickets   []idValue
	records        []idValue
	recordTickets  int
	adopt          []string
	repointed      int
}

type consentPair struct{ consent, at sql.Null[string] }

func (d *DB) RenameRepo(p RenameRepoParams) (RenameRepoCounts, error) {
	var counts RenameRepoCounts
	err := d.Tx(func(t *Tx) error {
		var err error
		counts, err = t.RenameRepo(p)
		return err
	})
	return counts, err
}

// RenameRepo plans the whole repair from reads, then applies it only when the
// params are not a dry run.
func (t *Tx) RenameRepo(p RenameRepoParams) (RenameRepoCounts, error) {
	plan, err := t.planRenameRepo(p)
	if err != nil {
		return RenameRepoCounts{}, err
	}
	counts := plan.counts(p)
	if p.DryRun {
		return counts, nil
	}
	if err := t.applyRenameRepo(p, plan); err != nil {
		return RenameRepoCounts{}, err
	}
	return counts, nil
}

func (pl renamePlan) counts(p RenameRepoParams) RenameRepoCounts {
	c := RenameRepoCounts{
		DryRun:            p.DryRun,
		From:              p.FromURL,
		To:                p.ToURL,
		BindingsRepointed: pl.repointed,
		TicketsRewritten:  len(pl.bindingTickets) + len(pl.chainTickets) + pl.recordTickets,
		BindingsAdopted:   len(pl.adopt),
		RecordsRewritten:  len(pl.records),
	}
	switch {
	case pl.fromID != "" && pl.toID != "":
		c.ReposMerged = 1
	case pl.fromID != "":
		c.ReposRenamed = 1
	}
	return c
}

func (t *Tx) planRenameRepo(p RenameRepoParams) (renamePlan, error) {
	var pl renamePlan
	var err error
	if pl.fromID, err = t.findRepoBy("origin_url", p.FromURL); err != nil {
		return pl, err
	}
	if pl.toID, err = t.findRepoBy("origin_url", p.ToURL); err != nil {
		return pl, err
	}
	if pl.fromID == "" && pl.toID == "" {
		return pl, fmt.Errorf("db: rename repo %s -> %s: %w", p.FromURL, p.ToURL, ErrNothingToRename)
	}
	pl.targetID = pl.toID
	if pl.targetID == "" {
		pl.targetID = pl.fromID
	}
	if pl.fromID != "" && pl.toID != "" {
		if err := t.planMerge(&pl); err != nil {
			return pl, err
		}
	}
	if err := t.planTickets(p, &pl); err != nil {
		return pl, err
	}
	if err := t.planRecords(p, &pl); err != nil {
		return pl, err
	}
	return pl, t.planAdopt(p, &pl)
}

func (t *Tx) planMerge(pl *renamePlan) error {
	if err := t.queryRow(`SELECT count(*) FROM binding WHERE repo_id = ?`, pl.fromID).Scan(&pl.repointed); err != nil {
		return fmt.Errorf("db: rename repo: count bindings: %w", mapBusy(err))
	}
	var from, to consentPair
	read := func(id string, c *consentPair) error {
		err := t.queryRow(`SELECT mastermind_consent, consent_at FROM repo WHERE id = ?`, id).Scan(&c.consent, &c.at)
		if err != nil {
			return fmt.Errorf("db: rename repo: read consent: %w", mapBusy(err))
		}
		return nil
	}
	if err := read(pl.fromID, &from); err != nil {
		return err
	}
	if err := read(pl.toID, &to); err != nil {
		return err
	}
	if !to.consent.Valid && from.consent.Valid {
		pl.carry = &from
	}
	return nil
}

// queryIDValues reads two-column (id, value) rows into memory so the writes
// that follow do not run while a cursor is open.
func (t *Tx) queryIDValues(query string, args ...any) ([]idValue, error) {
	rows, err := t.conn.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("db: rename repo: %w", mapBusy(err))
	}
	defer func() { _ = rows.Close() }()
	var out []idValue
	for rows.Next() {
		var v idValue
		if err := rows.Scan(&v.id, &v.value); err != nil {
			return nil, fmt.Errorf("db: rename repo: %w", mapBusy(err))
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: rename repo: %w", mapBusy(err))
	}
	return out, nil
}

// ticketPrefixes is the old and new "owner/repo#" a ticket is rewritten
// between, or ok false when either URL carries no owner/repo path.
func ticketPrefixes(p RenameRepoParams) (oldPrefix, newPrefix string, ok bool) {
	if p.FromOwnerRepo == "" || p.ToOwnerRepo == "" || p.FromOwnerRepo == p.ToOwnerRepo {
		return "", "", false
	}
	return p.FromOwnerRepo + "#", p.ToOwnerRepo + "#", true
}

// planTickets matches by prefix in SQL rather than LIKE: an underscore is a
// valid owner or repo character and a LIKE wildcard.
func (t *Tx) planTickets(p RenameRepoParams, pl *renamePlan) error {
	oldPrefix, newPrefix, ok := ticketPrefixes(p)
	if !ok {
		return nil
	}
	for _, table := range []struct {
		name string
		dst  *[]idValue
	}{{"binding", &pl.bindingTickets}, {"chains", &pl.chainTickets}} {
		rows, err := t.queryIDValues(`SELECT id, ticket FROM `+table.name+` WHERE `+originScope+
			` AND substr(ticket, 1, length(?)) = ?`, t.origin, oldPrefix, oldPrefix)
		if err != nil {
			return err
		}
		for _, r := range rows {
			*table.dst = append(*table.dst, idValue{r.id, newPrefix + strings.TrimPrefix(r.value, oldPrefix)})
		}
	}
	return nil
}

func (t *Tx) planRecords(p RenameRepoParams, pl *renamePlan) error {
	oldPrefix, newPrefix, _ := ticketPrefixes(p)
	rows, err := t.queryIDValues(`SELECT id, record_json FROM binding_record WHERE `+originScope, t.origin)
	if err != nil {
		return err
	}
	for _, r := range rows {
		out, ticketChanged, changed := rewriteRecordJSON([]byte(r.value), oldPrefix, newPrefix, p.FromURL, p.ToURL)
		if !changed {
			continue
		}
		pl.records = append(pl.records, idValue{r.id, string(out)})
		if ticketChanged {
			pl.recordTickets++
		}
	}
	return nil
}

func (t *Tx) planAdopt(p RenameRepoParams, pl *renamePlan) error {
	if len(p.AdoptCWD) == 0 {
		return nil
	}
	rows, err := t.queryIDValues(`SELECT id, cwd FROM binding WHERE `+originScope+` AND repo_id IS NULL`, t.origin)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if cwdUnderAny(r.value, p.AdoptCWD) {
			pl.adopt = append(pl.adopt, r.id)
		}
	}
	return nil
}

// cwdUnderAny reports whether cwd is one of the prefixes or beneath one, by
// whole path components: /a/oldname matches /a/oldname/x and not /a/oldname-plugin.
func cwdUnderAny(cwd string, prefixes []string) bool {
	for _, prefix := range prefixes {
		prefix = strings.TrimRight(filepath.Clean(prefix), string(filepath.Separator))
		if cwd == prefix || strings.HasPrefix(cwd, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// applyRenameRepo is the only place the repair writes. The old row is deleted
// last: foreign keys are on, and every binding naming it has moved by then.
func (t *Tx) applyRenameRepo(p RenameRepoParams, pl renamePlan) error {
	steps := []func() error{
		func() error { return t.applyRepoSide(p, pl) },
		func() error { return t.applyRewrites(pl) },
		func() error { return t.applyAdopt(pl) },
		func() error { return t.applyDeleteFrom(pl) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) execRename(what, query string, args ...any) error {
	if _, err := t.exec(query, args...); err != nil {
		return fmt.Errorf("db: rename repo: %s: %w", what, mapBusy(err))
	}
	return nil
}

func (t *Tx) applyRepoSide(p RenameRepoParams, pl renamePlan) error {
	switch {
	case pl.fromID != "" && pl.toID == "":
		return t.execRename("rename the repo row", `UPDATE repo SET origin = ?, origin_url = ? WHERE id = ?`,
			t.origin, p.ToURL, pl.fromID)
	case pl.fromID != "":
		// Stamped before the delete, so the delete's log entry carries this
		// installation rather than a legacy empty origin.
		if err := t.execRename("stamp the old repo row", `UPDATE repo SET origin = ? WHERE id = ?`, t.origin, pl.fromID); err != nil {
			return err
		}
		if err := t.execRename("repoint bindings", `UPDATE binding SET origin = ?, repo_id = ? WHERE repo_id = ?`,
			t.origin, pl.toID, pl.fromID); err != nil {
			return err
		}
	}
	if pl.carry != nil {
		return t.execRename("carry consent", `UPDATE repo SET origin = ?, mastermind_consent = ?, consent_at = ? WHERE id = ?`,
			t.origin, pl.carry.consent, pl.carry.at, pl.toID)
	}
	return nil
}

func (t *Tx) applyRewrites(pl renamePlan) error {
	for _, w := range []struct {
		table, col string
		rows       []idValue
	}{
		{"binding", "ticket", pl.bindingTickets},
		{"chains", "ticket", pl.chainTickets},
		{"binding_record", "record_json", pl.records},
	} {
		for _, r := range w.rows {
			if err := t.execRename("rewrite "+w.table, `UPDATE `+w.table+` SET origin = ?, `+w.col+` = ? WHERE id = ?`,
				t.origin, r.value, r.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Tx) applyAdopt(pl renamePlan) error {
	for _, id := range pl.adopt {
		if err := t.execRename("adopt a binding", `UPDATE binding SET origin = ?, repo_id = ? WHERE id = ?`,
			t.origin, pl.targetID, id); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tx) applyDeleteFrom(pl renamePlan) error {
	if pl.fromID == "" || pl.toID == "" {
		return nil
	}
	return t.execRename("delete the old repo row", `DELETE FROM repo WHERE id = ?`, pl.fromID)
}

// rewriteRecordJSON rewrites the ticket and repo_ref.origin_url of one stored
// record. Every other key keeps its position and its exact number text, which
// a decode into map[string]any would not: it turns integers into float64.
func rewriteRecordJSON(raw []byte, oldPrefix, newPrefix, fromURL, toURL string) (out []byte, ticketChanged, changed bool) {
	out, changed, ok := mapObject(raw, func(key string, val json.RawMessage) (json.RawMessage, bool) {
		switch key {
		case "ticket":
			var s string
			if oldPrefix == "" || json.Unmarshal(val, &s) != nil || !strings.HasPrefix(s, oldPrefix) {
				return val, false
			}
			ticketChanged = true
			return encodeJSONString(newPrefix + strings.TrimPrefix(s, oldPrefix)), true
		case "repo_ref":
			ref, refChanged, ok := mapObject(val, func(k string, v json.RawMessage) (json.RawMessage, bool) {
				var s string
				if k != "origin_url" || json.Unmarshal(v, &s) != nil || s != fromURL {
					return v, false
				}
				return encodeJSONString(toURL), true
			})
			if !ok || !refChanged {
				return val, false
			}
			return ref, true
		}
		return val, false
	})
	if !ok || !changed {
		return raw, false, false
	}
	return out, ticketChanged, true
}

func encodeJSONString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return bytes.TrimSpace(buf.Bytes())
}

// mapObject applies fn to each member of one JSON object, in order, and
// re-emits the object with the members' raw text otherwise untouched. ok is
// false when raw is not one object.
func mapObject(raw []byte, fn func(key string, val json.RawMessage) (json.RawMessage, bool)) (out []byte, changed, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false, false
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for first := true; dec.More(); first = false {
		keyTok, err := dec.Token()
		key, isStr := keyTok.(string)
		if err != nil || !isStr {
			return nil, false, false
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return nil, false, false
		}
		next, did := fn(key, val)
		changed = changed || did
		if !first {
			buf.WriteByte(',')
		}
		buf.Write(encodeJSONString(key))
		buf.WriteByte(':')
		buf.Write(next)
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false, false
	}
	buf.WriteByte('}')
	return buf.Bytes(), changed, true
}
