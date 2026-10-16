package main

import (
	"encoding/json"
	"github.com/fuad-daoud/relevo/internal/config"
	"strconv"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The rules for applying a synced item live in internal/config and
// internal/synclog. What a test here pins is what the CLI shows of a revision
// that arrived from another machine. No test here reaches a network or spawns
// a harness: the synced revisions are written straight through the config doc.

const (
	syncZenA = `{"zen":{"url":"https://zen:1111","fingerprint":"sha256:abcd"}}`
	syncZenB = `{"url":"https://zen:2222","fingerprint":"sha256:abcd"}`
)

// seedSyncedRevisions leaves a cli revision holding zenA, then a synced one
// holding zenB from "laptop" and a superseded one from "contabo".
func seedSyncedRevisions(t *testing.T) {
	t.Helper()
	if _, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "set", "servers", syncZenA})
	}); err != nil {
		t.Fatalf("set servers: %v (stderr: %s)", err, stderr)
	}
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	doc := config.Open(d).Doc()
	if err := d.LocalOrSelf().Tx(func(tx *db.Tx) error {
		refusal, err := doc.ApplyIn(tx, "servers/zen", json.RawMessage(syncZenB), "laptop")
		if err != nil || refusal != nil {
			t.Fatalf("ApplyIn: refusal %v, err %v", refusal, err)
		}
		return doc.SupersedeIn(tx, "servers/zen", json.RawMessage(syncZenA), "contabo", "older than the value in force")
	}); err != nil {
		t.Fatalf("seed synced revisions: %v", err)
	}
}

// revisionOf returns the newest revision recorded with source.
func revisionOf(t *testing.T, source string) revisionJSON {
	t.Helper()
	out, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log", "--json"}) })
	if err != nil {
		t.Fatalf("config log --json: %v (stderr: %s)", err, stderr)
	}
	var list []revisionJSON
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatalf("log is not JSON: %v\n%s", err, out)
	}
	for _, r := range list {
		if r.Source == source {
			return r
		}
	}
	t.Fatalf("no revision with source %q in %s", source, out)
	return revisionJSON{}
}

func revisionWithSnapshot(t *testing.T, rev int64) revisionJSON {
	t.Helper()
	out, stderr, err := captureOutput(t, func() error {
		return run([]string{"config", "log", "--rev", strconv.FormatInt(rev, 10), "--json"})
	})
	if err != nil {
		t.Fatalf("config log --rev %d: %v (stderr: %s)", rev, err, stderr)
	}
	var r revisionJSON
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("revision is not JSON: %v\n%s", err, out)
	}
	return r
}

func zenURL(t *testing.T) string {
	t.Helper()
	out, stderr, err := captureOutput(t, func() error { return run([]string{"config", "get", "servers"}) })
	if err != nil {
		t.Fatalf("get servers: %v (stderr: %s)", err, stderr)
	}
	return string(out)
}

// TestConfigLogShowsSyncSources pins that both kinds of synced revision are
// listed with their whole source, label included, and that the source column
// is as wide as the longest one, so the columns after it line up. A superseded revision
// leaves the config as the applied one left it.
func TestConfigLogShowsSyncSources(t *testing.T) {
	initRoot(t)
	seedSyncedRevisions(t)

	out, stderr, err := captureOutput(t, func() error { return run([]string{"config", "log"}) })
	if err != nil {
		t.Fatalf("config log: %v (stderr: %s)", err, stderr)
	}
	text := string(out)
	for _, want := range []string{
		"sync:laptop" + strings.Repeat(" ", len("sync-superseded:contabo")-len("sync:laptop")) + "  ",
		"sync-superseded:contabo  ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("config log =\n%s\nwant a source column holding %q", text, want)
		}
	}
	lost, applied := revisionWithSnapshot(t, revisionOf(t, "sync-superseded:contabo").Rev), revisionWithSnapshot(t, revisionOf(t, "sync:laptop").Rev)
	if string(lost.Snapshot) != string(applied.Snapshot) {
		t.Errorf("superseded snapshot = %s, want the config the applied revision left: it changed nothing", lost.Snapshot)
	}
}

// TestRollbackRestoresASyncedRevision pins that a revision that arrived from
// another machine is a rollback target like any other, in both directions.
func TestRollbackRestoresASyncedRevision(t *testing.T) {
	initRoot(t)
	seedSyncedRevisions(t)
	synced := revisionOf(t, "sync:laptop").Rev
	local := revisionOf(t, "cli").Rev

	roll := func(rev int64) {
		t.Helper()
		if _, stderr, err := captureOutput(t, func() error {
			return run([]string{"config", "rollback", strconv.FormatInt(rev, 10), "--yes"})
		}); err != nil {
			t.Fatalf("rollback to #%d: %v (stderr: %s)", rev, err, stderr)
		}
	}
	roll(local)
	if got := zenURL(t); !strings.Contains(got, "zen:1111") {
		t.Errorf("servers after rolling back to the cli revision = %s, want zen:1111", got)
	}
	roll(synced)
	if got := zenURL(t); !strings.Contains(got, "zen:2222") {
		t.Errorf("servers after rolling back to the synced revision = %s, want zen:2222", got)
	}
}

// A peer chooses the label and the message of a synced revision. The text views
// print them inert while --json and the stored row keep the raw bytes.
func TestConfigLogRendersAHostilePeerLabelInert(t *testing.T) {
	initRoot(t)
	seedSyncedRevisions(t)
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	d, err := rt.Store.DB()
	if err != nil {
		t.Fatalf("DB: %v", err)
	}
	doc := config.Open(d).Doc()
	const label = "evil\x1b[2J\nforged line"
	if err := d.LocalOrSelf().Tx(func(tx *db.Tx) error {
		return doc.SupersedeIn(tx, "servers/zen", json.RawMessage(syncZenA), label, "why\x1b[31m\nforged message")
	}); err != nil {
		t.Fatalf("SupersedeIn: %v", err)
	}
	raw := revisionOf(t, "sync-superseded:"+label)

	for _, args := range [][]string{{"config", "log"}, {"config", "log", "--rev", strconv.FormatInt(raw.Rev, 10)}} {
		out, stderr, err := captureOutput(t, func() error { return run(args) })
		if err != nil {
			t.Fatalf("%v: %v (stderr: %s)", args, err, stderr)
		}
		if strings.ContainsRune(string(out), 0x1b) || strings.Contains(string(out), "\nforged") {
			t.Errorf("%v printed peer-chosen control bytes:\n%q", args, out)
		}
	}
	if !strings.Contains(raw.Message, "\x1b[31m\nforged message") {
		t.Errorf("--json message = %q, want the raw stored bytes", raw.Message)
	}
}
