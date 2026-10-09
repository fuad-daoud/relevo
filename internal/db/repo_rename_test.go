package db

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	renameOrigin = "01RENAMEORIGIN"
	oldRepoURL   = "https://github.com/o/old_repo"
	newRepoURL   = "https://github.com/o/new_repo"
)

func renameParams() RenameRepoParams {
	return RenameRepoParams{
		FromURL: oldRepoURL, ToURL: newRepoURL,
		FromOwnerRepo: "o/old_repo", ToOwnerRepo: "o/new_repo",
	}
}

func renameDB(t *testing.T) *DB {
	t.Helper()
	return directOpen(t, filepath.Join(t.TempDir(), "relevo.db"), Options{Origin: renameOrigin})
}

type renameFixture struct {
	d            *DB
	fromID, toID string
	repointed    []string
}

// seedRenameFixture holds two repo rows, one per URL, with bindings on the old
// row, one of them written before the origin column existed.
func seedRenameFixture(t *testing.T) renameFixture {
	t.Helper()
	d := renameDB(t)
	f := renameFixture{d: d}
	f.fromID = upsertRepo(t, d, Repo{OriginURL: ptr(oldRepoURL), CommonDir: ptr("/main/.git"), FirstSeen: repoSeenAt})
	f.toID = upsertRepo(t, d, Repo{OriginURL: ptr(newRepoURL), CommonDir: ptr("/static/.git"), FirstSeen: repoSeenAt})
	for i, ticket := range []string{"o/old_repo#7", "#8", "o/older#9", "o/oldXrepo#10"} {
		b := newTestBinding(fmt.Sprintf("b%d", i), repoSeenAt.Add(time.Duration(i)*time.Minute))
		b.RepoID = ptr(f.fromID)
		b.Ticket = ptr(ticket)
		f.repointed = append(f.repointed, upsertBinding(t, d, b))
	}
	execRaw(t, d, `UPDATE binding SET origin = '' WHERE name = 'b0'`)
	return f
}

func execRaw(t *testing.T, d *DB, query string, args ...any) {
	t.Helper()
	if _, err := d.sqlDB.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func queryStrings(t *testing.T, d *DB, query string, args ...any) []string {
	t.Helper()
	rows, err := d.sqlDB.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

func insertChain(t *testing.T, d *DB, name, origin, ticket string) {
	t.Helper()
	execRaw(t, d, `INSERT INTO chains (id, origin, name, status, phase, step, plan, plans, plan_paths, builder, ticket, created_at, updated_at)
		VALUES (?, ?, ?, 'running', 'p', 's', 1, 1, '[]', 'b', ?, '2026-10-01T00:00:00Z', '2026-10-01T00:00:00Z')`,
		NewID(), origin, name, ticket)
}

func TestRenameRepoMergesAndRepointsBindings(t *testing.T) {
	f := seedRenameFixture(t)
	c, err := f.d.RenameRepo(renameParams())
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if c.ReposMerged != 1 || c.ReposRenamed != 0 || c.BindingsRepointed != 4 {
		t.Errorf("counts = %+v, want one merge and four bindings repointed", c)
	}
	if n := countRepos(t, f.d); n != 1 {
		t.Errorf("repo rows = %d, want 1", n)
	}
	for _, id := range f.repointed {
		var repo, origin string
		if err := f.d.sqlDB.QueryRow(`SELECT repo_id, origin FROM binding WHERE id = ?`, id).Scan(&repo, &origin); err != nil {
			t.Fatalf("read binding: %v", err)
		}
		if repo != f.toID || origin != renameOrigin {
			t.Errorf("binding %s repo=%s origin=%s, want %s stamped %s", id, repo, origin, f.toID, renameOrigin)
		}
	}
	if row := readRepoRow(t, f.d, f.toID); row.dir.V != "/static/.git" {
		t.Errorf("merged row common_dir = %q, want the target's own", row.dir.V)
	}
}

func TestRenameRepoRenamesWhenTargetRowAbsent(t *testing.T) {
	d := renameDB(t)
	id := upsertRepo(t, d, Repo{OriginURL: ptr(oldRepoURL), CommonDir: ptr("/main/.git"), FirstSeen: repoSeenAt})
	c, err := d.RenameRepo(renameParams())
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if c.ReposRenamed != 1 || c.ReposMerged != 0 {
		t.Errorf("counts = %+v, want one rename", c)
	}
	if row := readRepoRow(t, d, id); row.originURL.V != newRepoURL {
		t.Errorf("origin_url = %q, want %q", row.originURL.V, newRepoURL)
	}
}

func TestRenameRepoRewritesTickets(t *testing.T) {
	f := seedRenameFixture(t)
	insertChain(t, f.d, "c1", renameOrigin, "o/old_repo#11")
	insertChain(t, f.d, "c2", renameOrigin, "#12")
	// A sibling repo whose name extends the old one: the "#" in the prefix is
	// what keeps it out of the rewrite.
	insertChain(t, f.d, "c3", renameOrigin, "o/old_repo-site#13")
	c, err := f.d.RenameRepo(renameParams())
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if c.TicketsRewritten != 2 {
		t.Errorf("tickets_rewritten = %d, want 2 (one binding, one chain)", c.TicketsRewritten)
	}
	got := queryStrings(t, f.d, `SELECT ticket FROM binding ORDER BY name`)
	want := []string{"o/new_repo#7", "#8", "o/older#9", "o/oldXrepo#10"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("binding tickets = %v, want %v", got, want)
	}
	got = queryStrings(t, f.d, `SELECT ticket FROM chains ORDER BY name`)
	if strings.Join(got, ",") != "o/new_repo#11,#12,o/old_repo-site#13" {
		t.Errorf("chain tickets = %v", got)
	}
}

func TestRenameRepoLeavesShorthandTickets(t *testing.T) {
	f := seedRenameFixture(t)
	if _, err := f.d.RenameRepo(renameParams()); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if got := queryStrings(t, f.d, `SELECT ticket FROM binding WHERE name = 'b1'`); got[0] != "#8" {
		t.Errorf("shorthand ticket = %q, want #8", got[0])
	}
}

const recordBefore = `{"name":"r1","big":9007199254740993,"unknown":{"a":1.50,"h":"<&>"},"ticket":"o/old_repo#5","repo_ref":{"origin_url":"https://github.com/o/old_repo","common_dir":"/main/.git"},"tail":[1,2]}`
const recordAfter = `{"name":"r1","big":9007199254740993,"unknown":{"a":1.50,"h":"<&>"},"ticket":"o/new_repo#5","repo_ref":{"origin_url":"https://github.com/o/new_repo","common_dir":"/main/.git"},"tail":[1,2]}`

func putRecord(t *testing.T, d *DB, name, json string) {
	t.Helper()
	r := testRecord(name)
	r.JSON = json
	if _, err := d.RecordPut(r); err != nil {
		t.Fatalf("RecordPut: %v", err)
	}
}

func TestRenameRepoRewritesRecordJSON(t *testing.T) {
	f := seedRenameFixture(t)
	putRecord(t, f.d, "r1", recordBefore)
	c, err := f.d.RenameRepo(renameParams())
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if c.RecordsRewritten != 1 || c.TicketsRewritten != 2 {
		t.Errorf("counts = %+v, want one record and its ticket counted", c)
	}
	rec, _, err := f.d.RecordGet("", "r1")
	if err != nil {
		t.Fatalf("RecordGet: %v", err)
	}
	if rec.JSON != recordAfter {
		t.Errorf("record_json = %s\nwant %s", rec.JSON, recordAfter)
	}
}

func TestRenameRepoRecordJSONRoundTripKeepsOtherFields(t *testing.T) {
	out, ticketChanged, changed := rewriteRecordJSON([]byte(recordBefore), "o/old_repo#", "o/new_repo#", oldRepoURL, newRepoURL)
	if !changed || !ticketChanged {
		t.Fatalf("changed=%v ticketChanged=%v, want both", changed, ticketChanged)
	}
	if string(out) != recordAfter {
		t.Errorf("rewrite = %s\nwant %s", out, recordAfter)
	}
	same, _, changed := rewriteRecordJSON([]byte(`{"big":9007199254740993}`), "o/old_repo#", "o/new_repo#", oldRepoURL, newRepoURL)
	if changed || string(same) != `{"big":9007199254740993}` {
		t.Errorf("an untouched record changed: %s", same)
	}
}

func TestRenameRepoCarriesConsentWhenTargetUnset(t *testing.T) {
	f := seedRenameFixture(t)
	execRaw(t, f.d, `UPDATE repo SET mastermind_consent = 'yes', consent_at = '2026-10-02T00:00:00Z' WHERE id = ?`, f.fromID)
	if _, err := f.d.RenameRepo(renameParams()); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	got := queryStrings(t, f.d, `SELECT mastermind_consent || '@' || consent_at FROM repo WHERE id = ?`, f.toID)
	if len(got) != 1 || got[0] != "yes@2026-10-02T00:00:00Z" {
		t.Errorf("target consent = %v, want yes carried", got)
	}
}

func TestAdoptCwdPrefixMatchesWholeComponents(t *testing.T) {
	prefixes := []string{"/home/f/projects/oldname/"}
	for cwd, want := range map[string]bool{
		"/home/f/projects/oldname":             true,
		"/home/f/projects/oldname/x":           true,
		"/home/f/projects/oldname-plugin-lock": false,
		"/home/f/projects":                     false,
	} {
		if got := cwdUnderAny(cwd, prefixes); got != want {
			t.Errorf("cwdUnderAny(%q) = %v, want %v", cwd, got, want)
		}
	}
}

func TestRenameRepoAdoptsRepolessBindingsUnderPrefix(t *testing.T) {
	f := seedRenameFixture(t)
	for name, cwd := range map[string]string{"in": "/p/oldname/x", "sibling": "/p/oldname-other", "out": "/q"} {
		b := newTestBinding(name, repoSeenAt.Add(time.Hour))
		b.CWD = cwd
		upsertBinding(t, f.d, b)
	}
	p := renameParams()
	p.AdoptCWD = []string{"/p/oldname"}
	c, err := f.d.RenameRepo(p)
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if c.BindingsAdopted != 1 {
		t.Errorf("bindings_adopted = %d, want 1", c.BindingsAdopted)
	}
	got := queryStrings(t, f.d, `SELECT name FROM binding WHERE repo_id IS NULL ORDER BY name`)
	if strings.Join(got, ",") != "out,sibling" {
		t.Errorf("still repo-less = %v, want out,sibling", got)
	}
}

func TestRenameRepoIsIdempotent(t *testing.T) {
	f := seedRenameFixture(t)
	if _, err := f.d.RenameRepo(renameParams()); err != nil {
		t.Fatalf("first RenameRepo: %v", err)
	}
	before := dumpDB(t, f.d)
	c, err := f.d.RenameRepo(renameParams())
	if err != nil {
		t.Fatalf("second RenameRepo: %v", err)
	}
	if (c != RenameRepoCounts{From: oldRepoURL, To: newRepoURL}) {
		t.Errorf("second run counts = %+v, want zeros", c)
	}
	if after := dumpDB(t, f.d); after != before {
		t.Errorf("the second run changed the database")
	}
}

func TestRenameRepoRefusesWhenNeitherRowExists(t *testing.T) {
	_, err := renameDB(t).RenameRepo(renameParams())
	if !errors.Is(err, ErrNothingToRename) {
		t.Errorf("err = %v, want ErrNothingToRename", err)
	}
}

func TestRenameRepoLeavesOtherOriginRows(t *testing.T) {
	f := seedRenameFixture(t)
	execRaw(t, f.d, `INSERT INTO repo (id, origin, origin_url, first_seen) VALUES ('OTHERREPO', 'OTHER', ?, '2026-10-01T00:00:00Z')`, oldRepoURL)
	execRaw(t, f.d, `INSERT INTO binding (id, origin, name, repo_id, ticket, cwd, builder_mode, created_at, ingest_source)
		VALUES ('OTHERBIND', 'OTHER', 'ob', 'OTHERREPO', 'o/old_repo#1', '/p/oldname', 'headless', '2026-10-01T00:00:00Z', 'live')`)
	insertChain(t, f.d, "oc", "OTHER", "o/old_repo#2")
	putOtherRecord(t, f.d)
	before := dumpWhere(t, f.d, "OTHER")
	p := renameParams()
	p.AdoptCWD = []string{"/p/oldname"}
	if _, err := f.d.RenameRepo(p); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if after := dumpWhere(t, f.d, "OTHER"); after != before {
		t.Errorf("other-origin rows changed:\n%s\n-->\n%s", before, after)
	}
}

func putOtherRecord(t *testing.T, d *DB) {
	t.Helper()
	putRecord(t, d, "other", recordBefore)
	execRaw(t, d, `UPDATE binding_record SET origin = 'OTHER' WHERE name = 'other'`)
}

func dumpWhere(t *testing.T, d *DB, origin string) string {
	t.Helper()
	var parts []string
	for _, tbl := range []string{"repo", "binding", "chains", "binding_record"} {
		parts = append(parts, dumpTable(t, d, tbl, "origin = '"+origin+"'"))
	}
	return strings.Join(parts, "\n")
}

func dumpTable(t *testing.T, d *DB, table, where string) string {
	t.Helper()
	rows, err := d.sqlDB.Query(`SELECT * FROM ` + table + ` WHERE ` + where)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	cols, _ := rows.Columns()
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("dump scan: %v", err)
		}
		line := table
		for _, v := range vals {
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			line += fmt.Sprintf("|%v", v)
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// dumpDB is every row of every table, sync_outbox included.
func dumpDB(t *testing.T, d *DB) string {
	t.Helper()
	tables := queryStrings(t, d, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	var parts []string
	for _, tbl := range tables {
		parts = append(parts, dumpTable(t, d, tbl, "1 = 1"))
	}
	return strings.Join(parts, "\n")
}

func TestRenameRepoDryRunWritesNothing(t *testing.T) {
	f := seedRenameFixture(t)
	insertChain(t, f.d, "c1", renameOrigin, "o/old_repo#11")
	putRecord(t, f.d, "r1", recordBefore)
	b := newTestBinding("lonely", repoSeenAt.Add(time.Hour))
	b.CWD = "/p/oldname/x"
	upsertBinding(t, f.d, b)
	before := dumpDB(t, f.d)

	p := renameParams()
	p.DryRun = true
	p.AdoptCWD = []string{"/p/oldname"}
	c, err := f.d.RenameRepo(p)
	if err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	if !c.DryRun || c.ReposMerged != 1 || c.BindingsRepointed != 4 || c.BindingsAdopted != 1 || c.RecordsRewritten != 1 || c.TicketsRewritten != 3 {
		t.Errorf("dry-run counts = %+v, want the counts an apply would produce", c)
	}
	if after := dumpDB(t, f.d); after != before {
		t.Errorf("a dry run changed the database or sync_outbox")
	}
	p.DryRun = false
	applied, err := f.d.RenameRepo(p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	applied.DryRun = true
	if applied != c {
		t.Errorf("apply counts = %+v, dry-run counts = %+v", applied, c)
	}
}

func TestRenameRepoLogsRepairInSeqOrder(t *testing.T) {
	f := seedRenameFixture(t)
	insertChain(t, f.d, "c1", renameOrigin, "o/old_repo#11")
	putRecord(t, f.d, "r1", recordBefore)
	execRaw(t, f.d, `DELETE FROM sync_outbox`)
	if _, err := f.d.RenameRepo(renameParams()); err != nil {
		t.Fatalf("RenameRepo: %v", err)
	}
	rows, err := f.d.sqlDB.Query(`SELECT tbl, op, origin FROM sync_outbox ORDER BY seq`)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer func() { _ = rows.Close() }()
	deleteAt, lastChild, seen := -1, -1, map[string]bool{}
	for i := 0; rows.Next(); i++ {
		var tbl, op string
		var origin *string
		if err := rows.Scan(&tbl, &op, &origin); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if origin == nil || *origin != renameOrigin {
			t.Errorf("entry %d (%s %s) origin = %v, want %q", i, tbl, op, origin, renameOrigin)
		}
		seen[tbl] = true
		switch {
		case tbl == "repo" && op == "delete":
			deleteAt = i
		case tbl != "repo":
			lastChild = i
		}
	}
	if deleteAt < 0 {
		t.Fatalf("no repo delete in the outbox")
	}
	if lastChild > deleteAt {
		t.Errorf("a child entry at %d follows the repo delete at %d", lastChild, deleteAt)
	}
	for _, tbl := range []string{"binding", "chains", "binding_record"} {
		if !seen[tbl] {
			t.Errorf("no %s entry in the outbox", tbl)
		}
	}
}
