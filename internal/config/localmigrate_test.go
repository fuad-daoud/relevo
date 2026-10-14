package config

import (
	"strings"
	"testing"
)

// TestLocalMigrationMarkerIsIdempotent pins that both local-layer markers end a
// pre-3c database in a state a second run cannot change.
//
// A pre-3c database is one where neither marker is set and the shared bodies
// still carry what the moves exist to take out: a machine field, and an argv row
// naming a file outside this machine's config root. The two existing tests each
// start with one of the moves already recorded, so neither covers the database
// this step was written for. Here both markers are cleared and both moves are
// run, and the second run must report no move and change neither the shared
// bodies nor the local overrides -- the markers, not the state of the bodies,
// are what make a repeat a no-op.
func TestLocalMigrationMarkerIsIdempotent(t *testing.T) {
	t.Parallel()

	s := hooksRootStoreWithoutMarker(t)
	outside := s.root + "-elsewhere/check.sh"
	seedSharedDoc(t, s, map[Section]string{
		Candidates: docCandidates,
		Accounts:   `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/legacy/dir"}]`,
		Hooks:      `{"events":{"PreToolUse":[[` + jsonStr(outside) + `]]}}`,
	})
	// Neither marker is set: this is the state a machine upgrading from before
	// step 3c is in.
	if err := s.db.KVDelete(localLayerMarker); err != nil {
		t.Fatalf("clear the local layer marker: %v", err)
	}
	if err := s.db.KVDelete(hooksLocalMarker); err != nil {
		t.Fatalf("clear the hooks local marker: %v", err)
	}

	// The first run is the move.
	if moved, err := s.EnsureLocalLayer(); err != nil || !moved {
		t.Fatalf("EnsureLocalLayer = %v, %v; want the machine fields moved", moved, err)
	}
	if moved, err := s.EnsureHooksLocal(); err != nil || !moved {
		t.Fatalf("EnsureHooksLocal = %v, %v; want the outside-root argv moved", moved, err)
	}
	for _, marker := range []string{localLayerMarker, hooksLocalMarker} {
		if _, ok, err := s.db.KVGet(marker); err != nil || !ok {
			t.Fatalf("the %q marker = %v, %v; want the move recorded", marker, ok, err)
		}
	}

	before := snapshotLocalMove(t, s)

	// The second run is the idempotence: both report no move, and neither the
	// shared bodies nor the local overrides move under it.
	if moved, err := s.EnsureLocalLayer(); err != nil || moved {
		t.Fatalf("EnsureLocalLayer again = %v, %v; want the marker to make it a no-op", moved, err)
	}
	if moved, err := s.EnsureHooksLocal(); err != nil || moved {
		t.Fatalf("EnsureHooksLocal again = %v, %v; want the marker to make it a no-op", moved, err)
	}

	after := snapshotLocalMove(t, s)
	if len(after) != len(before) {
		t.Errorf("the second run changed what is stored: %d entries became %d", len(before), len(after))
	}
	for key, was := range before {
		now := after[key]
		if was[0] == "" && was[1] == now[1] {
			continue
		}
		if was[1] == "" && was[0] == now[0] {
			continue
		}
		if !sameJSON([]byte(was[0]), []byte(now[0])) {
			t.Errorf("the second run changed the shared body behind %q:\n%s\n%s", key, was[0], now[0])
		}
		if !sameJSON([]byte(was[1]), []byte(now[1])) {
			t.Errorf("the second run changed the local override for %q:\n%s\n%s", key, was[1], now[1])
		}
	}

	// A third run is still a no-op, so a binding that calls the step on every
	// open costs one kv read and no write.
	if moved, err := s.EnsureLocalLayer(); err != nil || moved {
		t.Fatalf("EnsureLocalLayer a third time = %v, %v; want a no-op", moved, err)
	}
	if moved, err := s.EnsureHooksLocal(); err != nil || moved {
		t.Fatalf("EnsureHooksLocal a third time = %v, %v; want a no-op", moved, err)
	}
}

// hooksRootStoreWithoutMarker is a rooted store whose hooks-local marker has
// already been cleared, so a test that seeds the database by hand starts from a
// pre-3c state rather than from one the binding already moved.
func hooksRootStoreWithoutMarker(t *testing.T) *Store {
	t.Helper()
	s, _, _ := hooksRootStore(t)
	if err := s.db.KVDelete(hooksLocalMarker); err != nil {
		t.Fatalf("clear the hooks local marker: %v", err)
	}
	return s
}

// snapshotLocalMove is the state both moves act on: the shared body of every
// section a move can rewrite, and every local override, each keyed by what it
// is stored under.
func snapshotLocalMove(t *testing.T, s *Store) map[string][2]string {
	t.Helper()
	local := s.db.LocalOrSelf()
	out := map[string][2]string{}
	for _, sec := range []Section{Accounts, Hooks, Policy} {
		shared, _, err := local.ConfigGet(string(sec))
		if err != nil {
			t.Fatalf("read the shared %s body: %v", sec, err)
		}
		out["shared/"+string(sec)] = [2]string{string(shared), ""}
	}
	keys, err := local.ConfigLocalKeys("")
	if err != nil {
		t.Fatalf("list the local overrides: %v", err)
	}
	if len(keys) == 0 {
		t.Fatalf("the moves left no local override, so there is nothing to hold still")
	}
	for _, key := range keys {
		patch, _, err := local.ConfigLocalGet(key)
		if err != nil {
			t.Fatalf("read the local override for %q: %v", key, err)
		}
		out["local/"+key] = [2]string{"", string(patch)}
	}
	return out
}

// TestTheLocalMarkersAreDistinct guards the two markers apart: a machine that
// has run one move has not run the other, so a clear of one must not report the
// other as done. This is the same property C6 asks of stamp prefixes, read at
// the markers themselves.
func TestTheLocalMarkersAreDistinct(t *testing.T) {
	t.Parallel()
	if localLayerMarker == hooksLocalMarker {
		t.Fatalf("both local-layer moves are recorded by %q, so one move would report the other done", localLayerMarker)
	}
	for _, m := range []string{localLayerMarker, hooksLocalMarker} {
		if strings.Contains(m, "/") || m == "" {
			t.Errorf("the marker %q is not a bare key", m)
		}
	}
}
