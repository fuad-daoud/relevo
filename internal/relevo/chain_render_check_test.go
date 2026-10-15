package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// TestReviewerSeedNamesTheEngineCheckOverAMemberGate pins that an engine
// chain's reviewer seed reports the engine's recorded check and its log even
// when the builder's round also left a red member-gate record.
func TestReviewerSeedNamesTheEngineCheckOverAMemberGate(t *testing.T) {
	t.Parallel()

	rt, c, key := chainRenderFixture(t)
	dir := t.TempDir()
	memberLog := filepath.Join(dir, "001-gate.log")
	engineLog := filepath.Join(dir, "engine-check.log")
	for _, p := range []string{memberLog, engineLog} {
		if err := os.WriteFile(p, []byte("log"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	if err := rt.Store.AppendLog("shop", store.LogEntry{
		Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Outcome: "done",
		Gate: &store.GateRecord{Result: "fail", LogPath: memberLog},
	}); err != nil {
		t.Fatalf("append the builder report: %v", err)
	}
	st := chainRenderState(key)
	st.Results["check"] = workflow.Result{Round: 1, Status: chainCheckGreen, Artifacts: map[string][]string{"log": {engineLog}}}

	var got string
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = chainRenderShippedSeed(rt, tx, c, workflow.Definition{}, st, "review")
		return err
	})
	if err != nil {
		t.Fatalf("chainRenderShippedSeed: %v", err)
	}
	if !strings.Contains(got, "Check result: green") {
		t.Errorf("seed does not say the engine's check was green:\n%s", got)
	}
	if !strings.Contains(got, "engine-check.log") || strings.Contains(got, "001-gate.log") {
		t.Errorf("seed does not name only the engine check's log:\n%s", got)
	}
}
