package syncworker

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// seedTablesWithFormat creates a remote holding exactly the tables stmts name,
// with meta carrying format. It is how a test holds a remote mid-migration: a
// shape no older-format seeder builds.
func seedTablesWithFormat(t *testing.T, path, format string, stmts ...string) {
	t.Helper()
	execOn(t, path, append(stmts,
		"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '"+format+"')")...)
}

// migratingTableSets is every table set a crashed migrate run can leave: the
// format-5 set, the new tables beside it, and the sets a half-applied drop
// leaves behind.
var migratingTableSets = map[string][]string{
	"format five": {
		logWithMACAndAfter, createStatements[1], marksTableStatement,
	},
	"with heads": {
		logWithMACAndAfter, createStatements[0], createStatements[1], marksTableStatement,
	},
	"with shared": {
		logWithMACAndAfter, createStatements[1], createStatements[2], marksTableStatement,
	},
	"with heads and shared": {
		logWithMACAndAfter, createStatements[0], createStatements[1], createStatements[2], marksTableStatement,
	},
	"drop applied": {
		createStatements[0], createStatements[1], createStatements[2],
	},
	"drop half applied": {
		logWithMACAndAfter, createStatements[0], createStatements[1], createStatements[2],
	},
}

// hasLogTable reports whether stmts build a log table, which decides whether
// a test can pin the replica's rows as well as its tables.
func hasLogTable(stmts []string) bool {
	for _, stmt := range stmts {
		if stmt == logWithMACAndAfter {
			return true
		}
	}
	return false
}

// migrateHello is the handshake that opens an unmigrated remote for the
// migrate verb alone.
func migrateHello(id string) Request {
	req := helloRequest(id)
	req.Migrate = true
	return req
}

// TestNormalHelloRefusesMigratingRemoteWithMigrate pins that a remote whose
// format row is still migrating answers a normal hello with the migrate
// message: the machine migrates once every machine runs this release, even
// when the remote already holds the new tables. The replica is never written
// into and never discarded.
func TestNormalHelloRefusesMigratingRemoteWithMigrate(t *testing.T) {
	const wantSentence = "run relevo db sync migrate once every machine runs this release"
	for name, stmts := range migratingTableSets {
		t.Run(name, func(t *testing.T) {
			path := tempReplica(t)
			seedTablesWithFormat(t, path, migratingFormat, stmts...)
			var beforeRows []logRow
			if hasLogTable(stmts) {
				execOn(t, path, "INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at)"+
					" VALUES ('origin-a', 1, 1, 'board', '[\"a\"]', 'upsert', 3, '{\"title\":\"one\"}', '2026-10-08T09:30:00Z')")
				beforeRows = snapshotLog(t, path)
			}
			beforeTables := replicaTables(t, path)

			driver := &localDriver{path: path}
			b := NewTursoBackend(driver)
			resp := startWorker(t, b).ask(helloRequest("h"))
			if resp.OK {
				t.Fatalf("hello = %+v, want the migrate refusal", resp)
			}
			if resp.Code != CodeMigrate {
				t.Errorf("code = %q, want %q: the refusal repeats on every attempt", resp.Code, CodeMigrate)
			}
			if !strings.Contains(resp.Error, wantSentence) {
				t.Errorf("refusal %q does not contain %q", resp.Error, wantSentence)
			}
			if afterTables := replicaTables(t, path); !reflect.DeepEqual(afterTables, beforeTables) {
				t.Errorf("tables after the refusal = %v, want %v", afterTables, beforeTables)
			}
			if hasLogTable(stmts) {
				if afterRows := snapshotLog(t, path); !reflect.DeepEqual(afterRows, beforeRows) {
					t.Errorf("log rows after the refusal = %+v, want %+v", afterRows, beforeRows)
				}
			}
			if driver.pushes != 0 {
				t.Errorf("a refused remote pushed %d times, want none", driver.pushes)
			}
			if driver.pulls != 1 {
				t.Errorf("pulls = %d, want exactly one: the migrating shape pulls once before the refusal", driver.pulls)
			}
			if driver.discards != 0 {
				t.Errorf("discards = %d, want none: a migration in progress holds unpushed state", driver.discards)
			}
		})
	}
}

// TestNormalHelloRefusesMismatchedMigratingShapesAsNotALog pins that the
// migrating format row on any other table set, and any other format row on a
// migrating set that is no older format's, is refused as not a log: the row
// and the tables are matched together, so a remote whose two disagree is
// refused rather than opened as something it is not.
func TestNormalHelloRefusesMismatchedMigratingShapesAsNotALog(t *testing.T) {
	type mismatched struct {
		name   string
		stmts  []string
		format string
	}
	var cases []mismatched
	for name, stmts := range migratingTableSets {
		// The format-5 set at format row 5 is genuinely format 5, so it
		// answers the migrate message rather than the not-a-log refusal;
		// and the drop-applied set at row 6 is genuinely the current log,
		// so it opens.
		formats := []string{"5", "6", "garbage"}
		if name == "format five" {
			formats = []string{"6", "garbage"}
		}
		if name == "drop applied" {
			formats = []string{"5", "garbage"}
		}
		for _, format := range formats {
			cases = append(cases, mismatched{name: name + " at " + format, stmts: stmts, format: format})
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			seedTablesWithFormat(t, path, tc.format, tc.stmts...)
			driver := &localDriver{path: path}
			b := NewTursoBackend(driver)
			resp := startWorker(t, b).ask(helloRequest("h"))
			if resp.OK {
				t.Fatalf("hello = %+v, want a refusal", resp)
			}
			if resp.Code != CodeRemote {
				t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
			}
			if !strings.Contains(resp.Error, "not a relevo sync log") {
				t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
			}
			if driver.pulls != 0 {
				t.Errorf("pulls = %d, want none: a mismatched shape never pulls", driver.pulls)
			}
			if driver.pushes != 0 {
				t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
			}
			if driver.discards != 0 {
				t.Errorf("discards = %d, want none", driver.discards)
			}
		})
	}
}

// TestNormalHelloRefusesAnExtraTableAsNotALog pins that a migrating format row
// on tables holding anything else is refused as not a log. The extra table
// makes the shape unknown, so the first open discards the replica and the
// re-bootstrap -- reseeded here the way a remote holding that shape would
// come back -- meets the refusal instead of a second discard.
func TestNormalHelloRefusesAnExtraTableAsNotALog(t *testing.T) {
	path := tempReplica(t)
	extra := append(append([]string(nil), migratingTableSets["format five"]...), "CREATE TABLE extra (x TEXT)")
	seedTablesWithFormat(t, path, migratingFormat, extra...)
	driver := &localDriver{path: path}
	driver.afterDiscard = func(p string) {
		reseed := append(append([]string(nil), extra...),
			"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '"+migratingFormat+"')")
		reseedReplica(t, p, reseed)
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if resp.OK {
		t.Fatalf("hello = %+v, want a refusal", resp)
	}
	if resp.Code != CodeRemote {
		t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
	}
	if !strings.Contains(resp.Error, "not a relevo sync log") {
		t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
	}
	if driver.discards != 1 {
		t.Errorf("discards = %d, want exactly one: the second mismatch is refused, not reset again", driver.discards)
	}
	if driver.pushes != 0 {
		t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
	}
}

// TestMigratingRemoteOpensWhenThePullBringsTheCurrentLog pins the machine that
// was offline while another finished the migration: its replica still carries
// the migrating row, the pull brings the format-6 tables and row, and hello
// opens without a discard.
func TestMigratingRemoteOpensWhenThePullBringsTheCurrentLog(t *testing.T) {
	path := tempReplica(t)
	seedTablesWithFormat(t, path, migratingFormat, migratingTableSets["format five"]...)
	driver := &localDriver{path: path}
	driver.afterPull = func(db *sql.DB) {
		driver.afterPull = nil
		for _, stmt := range []string{
			createStatements[0],
			createStatements[2],
			"UPDATE meta SET value = '6' WHERE key = '" + metaFormatKey + "'",
			"DROP TABLE log",
			"DROP TABLE marks",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Errorf("the pull brings %q: %v", stmt, err)
			}
		}
	}
	b := NewTursoBackend(driver)
	resp := startWorker(t, b).ask(helloRequest("h"))
	if !resp.OK {
		t.Fatalf("hello = %+v, want the migrated replica to open", resp)
	}
	if driver.pulls != 1 {
		t.Errorf("pulls = %d, want exactly one", driver.pulls)
	}
	if driver.discards != 0 {
		t.Errorf("discards = %d, want none: the pull brought the log", driver.discards)
	}
	if driver.opens != 1 {
		t.Errorf("opens = %d, want one: the replica was never re-bootstrapped", driver.opens)
	}
}

// TestMigrateHelloOpensOnlyWhatTheVerbCanFinish pins the migrate hello: it
// opens a format-5 remote and a resumable mid-migration shape without pulling,
// opens an already-migrated remote, and refuses everything else -- an empty
// remote it must never create, a drop the verb cannot resume, and formats the
// verb does not migrate.
func TestMigrateHelloOpensOnlyWhatTheVerbCanFinish(t *testing.T) {
	t.Run("format five opens without pulling", func(t *testing.T) {
		path := tempReplica(t)
		seedFormatFiveRemote(t, path)
		beforeTables := replicaTables(t, path)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		if resp := startWorker(t, b).ask(migrateHello("h")); !resp.OK {
			t.Fatalf("migrate hello = %+v, want the format-5 remote to open", resp)
		}
		if driver.pulls != 0 {
			t.Errorf("pulls = %d, want none: the verb pulls itself", driver.pulls)
		}
		if driver.pushes != 0 {
			t.Errorf("pushes = %d, want none: the open writes nothing", driver.pushes)
		}
		if afterTables := replicaTables(t, path); !reflect.DeepEqual(afterTables, beforeTables) {
			t.Errorf("tables after the open = %v, want %v", afterTables, beforeTables)
		}
	})

	for name, set := range map[string][]string{
		"migrating with source tables opens":     migratingTableSets["format five"],
		"migrating with new tables beside opens": migratingTableSets["with heads and shared"],
	} {
		t.Run(name, func(t *testing.T) {
			path := tempReplica(t)
			seedTablesWithFormat(t, path, migratingFormat, set...)
			driver := &localDriver{path: path}
			b := NewTursoBackend(driver)
			if resp := startWorker(t, b).ask(migrateHello("h")); !resp.OK {
				t.Fatalf("migrate hello = %+v, want the resumable shape to open", resp)
			}
			if driver.pulls != 0 {
				t.Errorf("pulls = %d, want none: the verb pulls itself", driver.pulls)
			}
		})
	}

	t.Run("migrating past the drop is stuck", func(t *testing.T) {
		path := tempReplica(t)
		seedTablesWithFormat(t, path, migratingFormat, migratingTableSets["drop half applied"]...)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		resp := startWorker(t, b).ask(migrateHello("h"))
		if resp.OK {
			t.Fatalf("migrate hello = %+v, want the stuck refusal", resp)
		}
		if resp.Code != CodeRemote {
			t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
		}
		if !strings.Contains(resp.Error, "mid-migration in a shape this release cannot resume") {
			t.Errorf("refusal %q does not say the remote is stuck mid-migration", resp.Error)
		}
		if !strings.Contains(resp.Error, "origin-a/log/") {
			t.Errorf("refusal %q does not name the bucket's segment objects as intact", resp.Error)
		}
		if !strings.Contains(resp.Error, "human") {
			t.Errorf("refusal %q does not say a human must look", resp.Error)
		}
		if driver.pulls != 0 {
			t.Errorf("pulls = %d, want none: the stuck shape never pulls", driver.pulls)
		}
		if driver.pushes != 0 {
			t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
		}
		if driver.discards != 0 {
			t.Errorf("discards = %d, want none", driver.discards)
		}
	})

	t.Run("format six opens", func(t *testing.T) {
		path := tempReplica(t)
		execOn(t, path, append(append([]string(nil), createStatements...),
			"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '6')")...)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		if resp := startWorker(t, b).ask(migrateHello("h")); !resp.OK {
			t.Fatalf("migrate hello = %+v, want the migrated remote to open", resp)
		}
	})

	t.Run("empty refuses without creating", func(t *testing.T) {
		path := tempReplica(t)
		driver := &localDriver{path: path}
		b := NewTursoBackend(driver)
		resp := startWorker(t, b).ask(migrateHello("h"))
		if resp.OK {
			t.Fatalf("migrate hello = %+v, want a refusal", resp)
		}
		if resp.Code != CodeRemote {
			t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
		}
		if !strings.Contains(resp.Error, "not a relevo sync log") {
			t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
		}
		if driver.pushes != 0 {
			t.Errorf("pushes = %d, want none: a migrate hello never creates a log", driver.pushes)
		}
		if tables := replicaTables(t, path); len(tables) != 0 {
			t.Errorf("a refused empty remote holds %v, want no tables", tables)
		}
	})

	for _, tc := range []struct {
		format string
		seed   func(t *testing.T, path string)
	}{
		{"1", seedFormatOneRemote},
		{"2", seedFormatTwoRemote},
		{"3", seedFormatThreeRemote},
		{"4", seedFormatFourRemote},
	} {
		t.Run("format "+tc.format+" is too old", func(t *testing.T) {
			path := tempReplica(t)
			tc.seed(t, path)
			driver := &localDriver{path: path}
			b := NewTursoBackend(driver)
			resp := startWorker(t, b).ask(migrateHello("h"))
			if resp.OK {
				t.Fatalf("migrate hello = %+v, want the too-old refusal", resp)
			}
			if resp.Code != CodeRemote {
				t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
			}
			if !strings.Contains(resp.Error, "this release migrates format 5 only") {
				t.Errorf("refusal %q does not say only format 5 migrates", resp.Error)
			}
			if !strings.Contains(resp.Error, "format "+tc.format) {
				t.Errorf("refusal %q does not name format %s", resp.Error, tc.format)
			}
			if driver.pulls != 0 {
				t.Errorf("pulls = %d, want none: the too-old shape never pulls", driver.pulls)
			}
			if driver.pushes != 0 {
				t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
			}
		})
	}
}

// TestMigrateHelloRefusesUnclassifiedRemoteAsNotALog pins that a migrate hello
// refuses any remote the verb cannot finish as not a log. The tables are
// classified before the format row is read, because a remote without a meta
// table carries no row to read: reading first would fail on the missing table
// instead of refusing with the remote's name. A migrating row on the wrong
// tables and an unreadable format row are refused the same way, without a
// push, a pull, or a second discard past the reset's once-only one.
func TestMigrateHelloRefusesUnclassifiedRemoteAsNotALog(t *testing.T) {
	const want = "not a relevo sync log"
	extraBesideMigration := append(append([]string(nil), migratingTableSets["format five"]...),
		"CREATE TABLE extra (x TEXT)")
	olderTables := []string{
		logWithMACAndAfter, headTableStatement, createStatements[1], marksTableStatement,
	}
	for _, tc := range []struct {
		name string
		// seed writes the remote the migrate hello meets. reseed, when set,
		// writes what the re-bootstrap finds after the reset discards an
		// unknown shape once.
		seed, reseed func(t *testing.T, path string)
		discards     int
		// absent, when set, must not appear in the refusal.
		absent string
	}{
		{
			name: "an unrelated table",
			seed: func(t *testing.T, path string) {
				execOn(t, path, "CREATE TABLE users (id INT)")
			},
			reseed: func(t *testing.T, path string) {
				reseedReplica(t, path, []string{"CREATE TABLE users (id INT)"})
			},
			discards: 1,
			absent:   "no such table",
		},
		{
			name: "an extra table beside a migration",
			seed: func(t *testing.T, path string) {
				seedTablesWithFormat(t, path, migratingFormat, extraBesideMigration...)
			},
			reseed: func(t *testing.T, path string) {
				reseedReplica(t, path, append(extraBesideMigration,
					"INSERT INTO meta (key, value) VALUES ('"+metaFormatKey+"', '"+migratingFormat+"')"))
			},
			discards: 1,
		},
		{
			name: "older tables at the migrating row",
			seed: func(t *testing.T, path string) {
				seedTablesWithFormat(t, path, migratingFormat, olderTables...)
			},
			discards: 0,
		},
		{
			name: "format-five tables at an unreadable row",
			seed: func(t *testing.T, path string) {
				seedFormatFiveRemote(t, path)
				execOn(t, path, "UPDATE meta SET value = 'garbage' WHERE key = '"+metaFormatKey+"'")
			},
			discards: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tempReplica(t)
			tc.seed(t, path)
			driver := &localDriver{path: path}
			if tc.reseed != nil {
				driver.afterDiscard = func(p string) { tc.reseed(t, p) }
			}
			b := NewTursoBackend(driver)
			resp := startWorker(t, b).ask(migrateHello("h"))
			if resp.OK {
				t.Fatalf("migrate hello = %+v, want a refusal", resp)
			}
			if resp.Code != CodeRemote {
				t.Errorf("code = %q, want %q", resp.Code, CodeRemote)
			}
			if !strings.Contains(resp.Error, want) {
				t.Errorf("refusal %q does not say the remote is not a log", resp.Error)
			}
			if tc.absent != "" && strings.Contains(resp.Error, tc.absent) {
				t.Errorf("refusal %q names the missing table instead of the remote", resp.Error)
			}
			if driver.pushes != 0 {
				t.Errorf("pushes = %d, want none: a refused remote is never written into", driver.pushes)
			}
			if driver.pulls != 0 {
				t.Errorf("pulls = %d, want none: a refused remote is never read past its tables", driver.pulls)
			}
			if driver.discards != tc.discards {
				t.Errorf("discards = %d, want %d", driver.discards, tc.discards)
			}
		})
	}
}

// TestMigrationPipeRefusesEveryOtherVerb pins the migration pipe: after a
// migrate hello, stats, pull and export are refused as not the pipe's
// protocol while hello and shutdown still answer, and a normal hello serves
// stats as before.
func TestMigrationPipeRefusesEveryOtherVerb(t *testing.T) {
	path := tempReplica(t)
	b := backendFor(t, &localDriver{path: path}, "origin-a")
	w := startWorker(t, b)
	if resp := w.ask(migrateHello("h")); !resp.OK {
		t.Fatalf("migrate hello = %+v, want the migrated remote to open", resp)
	}
	for _, req := range []Request{
		{ID: "s", Verb: VerbStats},
		{ID: "p", Verb: VerbPull},
		{ID: "e", Verb: VerbExport},
	} {
		resp := w.ask(req)
		if resp.OK {
			t.Errorf("%s = %+v, want a refusal on a migration pipe", req.Verb, resp)
		}
		if !strings.Contains(resp.Error, ErrProtocol.Error()) {
			t.Errorf("%s refusal %q does not carry the protocol error", req.Verb, resp.Error)
		}
		if !strings.Contains(resp.Error, "migration pipe") {
			t.Errorf("%s refusal %q does not name the migration pipe", req.Verb, resp.Error)
		}
	}
	if resp := w.ask(Request{ID: "bye", Verb: VerbShutdown}); !resp.OK {
		t.Errorf("shutdown = %+v, want ok: the pipe stays usable", resp)
	}

	other := backendFor(t, &localDriver{path: tempReplica(t)}, "origin-a")
	v := startWorker(t, other)
	v.askHello()
	if resp := v.ask(Request{ID: "s", Verb: VerbStats}); !resp.OK {
		t.Errorf("stats after a normal hello = %+v, want ok", resp)
	}
}

// TestHelloOmitsMigrateWhenUnset pins the additive hello: a hello that does
// not ask for migration carries no migrate key, so an older worker reads it
// as the hello it already speaks.
func TestHelloOmitsMigrateWhenUnset(t *testing.T) {
	line, err := json.Marshal(helloRequest("h"))
	if err != nil {
		t.Fatalf("marshal hello: %v", err)
	}
	if strings.Contains(string(line), "migrate") {
		t.Errorf("hello without Migrate carries a migrate key: %s", line)
	}
	set, err := json.Marshal(migrateHello("h"))
	if err != nil {
		t.Fatalf("marshal migrate hello: %v", err)
	}
	if !strings.Contains(string(set), `"migrate":true`) {
		t.Errorf("migrate hello carries no migrate key: %s", set)
	}
}
