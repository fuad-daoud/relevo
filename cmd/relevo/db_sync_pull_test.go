//go:build unix

package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestSyncCallTextNamesEachHoldAndWhereToLook pins the pull's text answer: the
// outcome line stays as it was for a pull with nothing held, and a held origin
// is named under it with the command that reads it again. These are pure
// functions; no harness is spawned and no network is reached.
func TestSyncCallTextNamesEachHoldAndWhereToLook(t *testing.T) {
	const name = "db sync pull"
	if got, want := syncCallText(name, &wire.SyncResult{}), "relevo db sync pull: nothing to apply\n"; got != want {
		t.Errorf("clean pull = %q, want %q", got, want)
	}
	got := syncCallText(name, &wire.SyncResult{Applied: true, Holds: []string{"m2 is held at 3", "m3 needs unlock"}})
	want := "relevo db sync pull applied\nheld: m2 is held at 3\nheld: m3 needs unlock\nnext: relevo db sync status\n"
	if got != want {
		t.Errorf("held pull = %q, want %q", got, want)
	}
}

// TestSyncCallDocCarriesTheHoldsAndNext pins the --json answer agreeing with
// the text: the same holds, plus the next command only when there are holds.
func TestSyncCallDocCarriesTheHoldsAndNext(t *testing.T) {
	doc := syncCallDoc(&wire.SyncResult{Holds: []string{"m2 is held at 3"}})
	if !reflect.DeepEqual(doc.Holds, []string{"m2 is held at 3"}) || doc.Next != "relevo db sync status" {
		t.Errorf("held doc = %+v, want the hold and the next command", doc)
	}
	body, err := json.Marshal(syncCallDoc(&wire.SyncResult{Applied: true}))
	if err != nil || string(body) != `{"applied":true}` {
		t.Errorf("clean doc = %s (%v), want only applied", body, err)
	}
}

// TestPullThenStatusShowsTheHoldWithoutATick pins that a status read straight
// after a pull lists the hold the pull printed, with no steady tick between.
func TestPullThenStatusShowsTheHoldWithoutATick(t *testing.T) {
	tc := dbSyncStatusCase{enabled: true, tickOK: true, remote: syncStatusRemote, namespace: syncStatusNamespace, token: syncStatusSecret}
	t.Setenv("XDG_STATE_HOME", shortStateRoot(t))
	shared, err := openDBDirect(machineDBPath())
	if err != nil {
		t.Fatalf("openDBDirect: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local := shared.Local()
	seedSyncStatusLocal(t, local, tc)

	log := synclog.NewMemTransport(shared.Origin())
	_, known := shared.SchemaVersions()
	body, err := json.Marshal(map[string]any{
		"id": "b3", "name": "b3", "cwd": "/far", "builder_mode": "local",
		"created_at": "t", "ingest_source": "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := synclog.NewUpsert("m3", "binding", `["b3"]`, known+1, body, time.Unix(0, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.OnLog("m3").Append([]synclog.Entry{entry}); err != nil {
		t.Fatal(err)
	}

	runner := &verb.Runner{
		Shared: shared, Local: local, Path: shared.Path(),
		Runner: &relevosync.Runner{Client: log, Local: local},
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{Verb: wire.SyncVerbPull}, nil)
	if !res.OK || len(res.Holds) != 1 {
		t.Fatalf("pull = %+v, want one hold", res)
	}
	doc := readSyncStatusDoc(t, local)
	if !reflect.DeepEqual(doc.HeldOrigins, res.Holds) {
		t.Errorf("status held = %v, want the pull's holds %v", doc.HeldOrigins, res.Holds)
	}
	if !strings.Contains(syncCallText("db sync pull", res), "next: relevo db sync status") {
		t.Error("pull text does not point at status")
	}
}
