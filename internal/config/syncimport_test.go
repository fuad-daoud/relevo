package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// Machines exchanging the config document through a fake log: every rule here
// runs against real stores and the real importer and exporter.

var syncKey = []byte("0123456789abcdef0123456789abcdef")

type syncMachine struct {
	d      *db.DB
	s      *Store
	origin string
	log    *synclog.MemTransport
}

func newSyncMachine(t *testing.T, origin string, log *synclog.MemTransport) *syncMachine {
	t.Helper()
	d, err := db.OpenWith(filepath.Join(t.TempDir(), "relevo.db"), db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("open %s: %v", origin, err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return &syncMachine{d: d, s: Open(d), origin: origin, log: log}
}

func (m *syncMachine) export(t *testing.T) int {
	t.Helper()
	e := synclog.NewExporter(m.d, m.log.OnLog(m.origin))
	e.SetKey(syncKey)
	e.UseDocs(m.s.Doc())
	res, err := e.Export()
	if err != nil {
		t.Fatalf("export from %s: %v", m.origin, err)
	}
	return res.Appended
}

func (m *syncMachine) importAll(t *testing.T) synclog.ImportResult {
	t.Helper()
	imp := synclog.NewImporter(m.d, m.log.OnLog(m.origin))
	imp.SetKey(syncKey)
	imp.UseDocs(m.s.Doc())
	res, err := imp.Import()
	if err != nil {
		t.Fatalf("import into %s: %v", m.origin, err)
	}
	return res
}

func (m *syncMachine) sources(t *testing.T) string {
	t.Helper()
	rows, err := m.s.Log(0)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Source)
	}
	return strings.Join(out, "|")
}

func (m *syncMachine) server(t *testing.T) string {
	t.Helper()
	items, err := m.s.Doc().Items()
	if err != nil {
		t.Fatalf("Items of %s: %v", m.origin, err)
	}
	return string(items["servers/zen"])
}

const (
	zenA = `{"zen":{"url":"https://zen:1111","fingerprint":"sha256:abcd"}}`
	zenB = `{"zen":{"url":"https://zen:2222","fingerprint":"sha256:abcd"}}`
)

// TestThreeMachinesConvergeWithLoserRevisioned pins that concurrent edits of
// one key on two machines leave all three on the later one, that the earlier
// edit is recorded as superseded on the machine that lost it, and that once
// they agree nothing is sent again.
func TestThreeMachinesConvergeWithLoserRevisioned(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("a")
	a, b, c := newSyncMachine(t, "a", log), newSyncMachine(t, "b", log), newSyncMachine(t, "c", log)
	seedDocConfig(t, a.s)
	a.export(t)
	b.importAll(t)
	c.importAll(t)
	if got, want := b.server(t), docServers; got == "" || got != c.server(t) {
		t.Fatalf("b holds %s and c %s, want both %s from a", got, c.server(t), want)
	}

	if _, err := a.s.As("config set", "").Put(Servers, []byte(zenA)); err != nil {
		t.Fatalf("a edit: %v", err)
	}
	a.export(t)
	time.Sleep(5 * time.Millisecond)
	if _, err := b.s.As("config set", "").Put(Servers, []byte(zenB)); err != nil {
		t.Fatalf("b edit: %v", err)
	}
	b.export(t)
	versionBefore := loadedVersion(t, c)

	for range 2 {
		for _, m := range []*syncMachine{a, b, c} {
			m.importAll(t)
			if n := m.export(t); n != 0 {
				t.Errorf("%s sent %d entries after importing, want none: an applied item is not re-exported", m.origin, n)
			}
		}
	}

	want := canonJSON(t, `{"url":"https://zen:2222","fingerprint":"sha256:abcd"}`)
	for _, m := range []*syncMachine{a, b, c} {
		if got := canonJSON(t, m.server(t)); got != want {
			t.Errorf("%s holds %s, want the later edit %s", m.origin, got, want)
		}
		if _, err := m.s.Load(); err != nil {
			t.Errorf("Load on %s: %v", m.origin, err)
		}
		if n := m.export(t); n != 0 {
			t.Errorf("%s exported %d entries after convergence, want none", m.origin, n)
		}
	}
	if got := b.sources(t); !strings.Contains(got, "sync-superseded:") {
		t.Errorf("b sources %q: the earlier edit should be revisioned as superseded", got)
	}
	if got := a.sources(t); !strings.Contains(got, "sync:") || strings.Contains(got, "sync-superseded:") {
		t.Errorf("a sources %q: the later edit should land as sync:", got)
	}
	if loadedVersion(t, c) <= versionBefore {
		t.Error("c's config version did not move when the item landed, so a running reader would not reload")
	}
}

func loadedVersion(t *testing.T, m *syncMachine) int64 {
	t.Helper()
	l, err := m.s.Load()
	if err != nil {
		t.Fatalf("Load on %s: %v", m.origin, err)
	}
	return l.Version
}

func canonJSON(t *testing.T, raw string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out)
}

// TestInvalidAppliedConfigLandsSuperseded pins that a newer, correctly signed
// item that would leave the merged config invalid changes nothing, is recorded
// as superseded and does not hold its origin.
func TestInvalidAppliedConfigLandsSuperseded(t *testing.T) {
	t.Parallel()
	log := synclog.NewMemTransport("a")
	b := newSyncMachine(t, "b", log)
	seedDocConfig(t, b.s)
	before, _, err := b.s.Body(Candidates)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}

	bad := json.RawMessage(`{"name":"broken","harness":"claude","model":"broken"}`)
	entry, err := synclog.NewDocUpsert("m9", b.s.Doc(), "candidates/broken", 1, 1<<40, bad, time.Now())
	if err != nil {
		t.Fatalf("NewDocUpsert: %v", err)
	}
	entry.MAC = synclog.EntryMAC(syncKey, entry)
	if _, err := log.OnLog("m9").AppendSigned([]synclog.Entry{entry},
		synclog.Boundary{Seq: 1, Tag: synclog.BoundaryTag(syncKey, "m9", 1)}); err != nil {
		t.Fatalf("append: %v", err)
	}

	res := b.importAll(t)
	if len(res.Stalled)+len(res.Dropped) != 0 {
		t.Fatalf("result %+v: a refused item should not hold or drop its origin", res)
	}
	after, _, err := b.s.Body(Candidates)
	if err != nil {
		t.Fatalf("Body: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("candidates = %s, want the unchanged %s", after, before)
	}
	if _, err := b.s.Load(); err != nil {
		t.Errorf("Load after the refused item: %v", err)
	}
	if got := b.sources(t); !strings.Contains(got, "sync-superseded:m9") || strings.Contains(got, "sync:m9") {
		t.Errorf("sources %q, want a sync-superseded:m9 revision and no sync:m9", got)
	}
}
