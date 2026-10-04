package config

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// The base config every machine in the two-file test starts from, one body per
// section, and the changed body machine A stores over each of them. A section
// whose body cannot differ between the two would leave the acceptance untested
// for that one section.
var (
	syncBaseBodies = map[Section]string{
		Candidates: placementCandidates,
		Agents:     `{"my-exec":{"shape":"writer","native":{"claude":{"agent":"my-exec"}}}}`,
		Actors:     `{"builder":{"agent":"plan-executor","candidates":["claude/p/m"]}}`,
		Accounts:   `[]`,
		Policy:     `{"order":{"builder":["claude/p/m"]}}`,
		Roles:      `{}`,
		Prices:     `{"as_of":"2026-01-01","source":"file","models":{}}`,
		Servers:    placementServers,
		Hooks:      `{}`,
		Workflows:  `{}`,
		Sync:       `{"enabled":false}`,
	}
)

func changedBodies(t *testing.T) map[Section]string {
	t.Helper()
	return map[Section]string{
		Candidates: `[{"harness":"claude","provider":"other","model":"other-m"}]`,
		Agents:     `{"other-exec":{"shape":"writer","native":{"claude":{"agent":"other-exec"}}}}`,
		Actors:     `{"builder":{"agent":"plan-executor","candidates":["claude/other/other-m"]}}`,
		Accounts:   `[{"name":"home","harness":"claude","groups":["anthropic"],"config_dir":"/home/other/.claude"}]`,
		Policy:     `{"order":{"builder":["claude/other/other-m"]}}`,
		Roles:      `{"reviewer":{"shape":"reader"}}`,
		Prices:     `{"as_of":"2026-02-02","source":"file","models":{}}`,
		Servers:    `{"zen":{"url":"https://other:7777","fingerprint":"sha256:ffff"}}`,
		Hooks:      `{"state_changed":[["10-x","/bin/true"]]}`,
		Workflows:  changedWorkflows(t),
		Sync:       `{"enabled":true,"remote_url":"libsql://other.turso.io"}`,
	}
}

// changedWorkflows is the workflow section machine A stores: the one workflow
// under a definition its source actually parses to.
func changedWorkflows(t *testing.T) string {
	t.Helper()
	body, err := EncodeWorkflows(map[string]StoredWorkflow{
		"custom": {Source: wfOtherSource, Definition: wfParse(t, wfOtherSource)},
	})
	if err != nil {
		t.Fatalf("EncodeWorkflows: %v", err)
	}
	return string(body)
}

// openMachine opens one machine's pair and returns a store bound to its local
// file: where a section belongs, because a section never syncs.
func openMachine(t *testing.T) (path string, shared *db.DB, local *db.DB, s *Store) {
	t.Helper()
	path = t.TempDir() + "/relevo.db"
	shared, err := db.OpenSplit(path, db.Options{})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local = shared.Local()
	if local == nil {
		t.Fatal("OpenSplit returned a handle with no local file")
	}
	return path, shared, local, Open(local)
}

// putSections stores every body in doc through s, in registry order.
func putSections(t *testing.T, s *Store, doc map[Section]string) {
	t.Helper()
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		if _, err := s.As("test", "write "+string(sec)).Put(sec, []byte(body)); err != nil {
			t.Fatalf("Put(%s): %v", sec, err)
		}
	}
}

// TestConfigNeverSyncs pins the acceptance that no configuration section syncs:
// machine A changes every section, the shared file syncs to machine B, and
// machine B's sections are byte-identical to the ones it had before.
func TestConfigNeverSyncs(t *testing.T) {
	t.Parallel()

	aPath, aShared, _, a := openMachine(t)
	bPath, _, bLocal, b := openMachine(t)

	putSections(t, a, syncBaseBodies)
	putSections(t, b, syncBaseBodies)
	putSections(t, a, changedBodies(t))

	bBefore, err := b.Current()
	if err != nil {
		t.Fatalf("Current(B): %v", err)
	}
	if len(bBefore) != len(Sections) {
		t.Fatalf("B stored %d sections, want all %d", len(bBefore), len(Sections))
	}

	// Every section must now differ on A, or the acceptance leaves that one
	// section untested: a body that is the same on both machines cannot be
	// shown to have failed to sync.
	aAfter, err := a.Current()
	if err != nil {
		t.Fatalf("Current(A): %v", err)
	}
	for _, sec := range Sections {
		got, ok := aAfter[sec]
		if !ok {
			t.Fatalf("A stored no %s section", sec)
		}
		if bytes.Equal(got, bBefore[sec]) {
			t.Errorf("A's %s is the body B already had, so nothing changed to sync", sec)
		}
	}

	// The sync: the shared file is what crosses, so A's copy of it is what B
	// receives. A's local file stays on A.
	if err := aShared.Close(); err != nil {
		t.Fatalf("Close(A): %v", err)
	}
	if err := bLocal.Close(); err != nil {
		t.Fatalf("Close(B local): %v", err)
	}
	if err := os.Remove(bPath); err != nil {
		t.Fatalf("remove B's shared file: %v", err)
	}
	if err := syncSharedInto(aPath, bPath); err != nil {
		t.Fatalf("sync the shared file: %v", err)
	}

	bLocal, err = reopenMachine(t, bPath)
	if err != nil {
		t.Fatalf("reopen B: %v", err)
	}
	bSynced := Open(bLocal)
	bAfter, err := bSynced.Current()
	if err != nil {
		t.Fatalf("Current(B after sync): %v", err)
	}
	if !reflect.DeepEqual(bAfter, bBefore) {
		t.Errorf("B's sections changed across a sync:\nbefore %s\nafter  %s",
			sectionBodies(bBefore), sectionBodies(bAfter))
	}
}

// TestNoSectionBodyReachesTheSharedFile pins the same rule from the other side:
// after every section is written through the local handle, the shared file
// carries no config row at all. A body that was routed shared would be in the
// copy another machine receives even if this machine's own store still read it.
func TestNoSectionBodyReachesTheSharedFile(t *testing.T) {
	t.Parallel()

	sharedPath, _, local, s := openMachine(t)
	putSections(t, s, syncBaseBodies)
	putSections(t, s, changedBodies(t))

	if err := local.Close(); err != nil {
		t.Fatalf("Close(local): %v", err)
	}
	countRows(t, sharedPath, "config_doc")
	countRows(t, sharedPath, "config_revision")
}

// countRows fails the test unless table in the database at path is empty.
func countRows(t *testing.T, path, table string) {
	t.Helper()
	pool, err := db.OpenRaw(path)
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = pool.Close() }()
	var n int
	if err := pool.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if n != 0 {
		t.Errorf("%s holds %d rows in the shared file, want none", table, n)
	}
}

// syncSharedInto copies one shared file over another, which is the whole of a
// sync: the shared file is the only thing that crosses machines.
func syncSharedInto(from, to string) error {
	body, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, body, 0o600)
}

// reopenMachine reopens the pair at sharedPath and returns its local handle, so
// a store built over it reads the files as they are after the sync.
func reopenMachine(t *testing.T, sharedPath string) (*db.DB, error) {
	t.Helper()
	pair, err := db.OpenSplit(sharedPath, db.Options{})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = pair.Close() })
	if pair.Local() == nil {
		t.Fatal("the reopened handle has no local file")
	}
	return pair.Local(), nil
}

// sectionBodies renders a document for a failure message, one section per line.
func sectionBodies(doc Doc) string {
	var out bytes.Buffer
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		out.WriteString(string(sec))
		out.WriteString("=")
		out.Write(body)
		out.WriteString(" ")
	}
	return out.String()
}
