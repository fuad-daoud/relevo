package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/sync/verb"
)

// Binding the verbs after a restart turns a stranded marker into a failure: a
// marker set on a machine that is off with no failure recorded means the run
// that wrote it is gone with the restart. Every other shape binds quietly.
func TestSetSyncVerbsRecordsAStrandedJoin(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		marked  bool
		enabled bool
		failed  bool
		want    bool
	}{
		{"a stranded marker records the restart", true, false, false, true},
		{"no marker binds quietly", false, false, false, false},
		{"an enabled machine binds quietly", true, true, false, false},
		{"a failure already recorded binds quietly", true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := store.New(t.TempDir())
			mdb, err := st.DB()
			if err != nil {
				t.Fatalf("store db: %v", err)
			}
			local, err := relevosync.LocalHandle(mdb)
			if err != nil {
				t.Fatalf("LocalHandle: %v", err)
			}
			if tc.marked {
				if err := relevosync.WriteJoin(local, at.Add(-time.Hour)); err != nil {
					t.Fatalf("WriteJoin: %v", err)
				}
			}
			if tc.enabled {
				if err := relevosync.MarkEnabled(local, true, at.Add(-time.Hour)); err != nil {
					t.Fatalf("MarkEnabled: %v", err)
				}
			}
			if tc.failed {
				if err := relevosync.WriteJoinFailure(local, at.Add(-time.Hour), "sync: enable: import: an old failure"); err != nil {
					t.Fatalf("WriteJoinFailure: %v", err)
				}
			}
			d := NewDaemon(Runtime{Store: st}, time.Second)
			d.SetSyncVerbs(&verb.Runner{Local: local})

			failure, ok, err := relevosync.ReadJoinFailure(local)
			if err != nil {
				t.Fatalf("ReadJoinFailure: %v", err)
			}
			if ok != (tc.failed || tc.want) {
				t.Fatalf("failure present = %v, want %v", ok, tc.failed || tc.want)
			}
			if tc.failed && failure.Message != "sync: enable: import: an old failure" {
				t.Errorf("the old failure was overwritten: %q", failure.Message)
			}
			if tc.want && failure.Message != "sync: the enable did not finish before the daemon restarted" {
				t.Errorf("failure message = %q, want the restart wording", failure.Message)
			}
		})
	}
}

// A bind with no runner to read a marker from records nothing.
func TestSetSyncVerbsWithNoRunnerBindsQuietly(t *testing.T) {
	t.Parallel()

	d := NewDaemon(Runtime{}, time.Second)
	d.SetSyncVerbs(nil)
	d.SetSyncVerbs(&verb.Runner{})
}
