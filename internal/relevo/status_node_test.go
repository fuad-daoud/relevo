package relevo

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// TestStatusRowNode pins the row's node: a remote binding whose stored builder
// carries a node names it, and the JSON gains only the additive node key. A
// binding with no node reads exactly as before.
func TestStatusRowNode(t *testing.T) {
	t.Parallel()

	rowFor := func(t *testing.T, node string) (view.BindingStatus, []byte) {
		t.Helper()
		st := store.New(t.TempDir())
		b := store.Binding{
			Name:             "api",
			CWD:              "/repo",
			State:            store.StateActive,
			Round:            1,
			RoundStartedAt:   baseTime,
			BuilderCandidate: testAgyRef,
			Builder: store.Endpoint{
				Mode:         store.ModeRemote,
				Server:       "contabo",
				RemoteStatus: "running",
				RemoteNode:   node,
			},
		}
		if err := st.Save(b); err != nil {
			t.Fatalf("Save: %v", err)
		}
		rt := Runtime{Store: st, Now: func() time.Time { return baseTime }}
		row, err := statusRow(context.Background(), rt, b, statusConfig{})
		if err != nil {
			t.Fatalf("statusRow: %v", err)
		}
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return row, raw
	}

	t.Run("node set", func(t *testing.T) {
		t.Parallel()

		row, raw := rowFor(t, "b12")
		if row.Node != "b12" {
			t.Errorf("Node = %q, want %q", row.Node, "b12")
		}
		if !strings.Contains(string(raw), `"node":"b12"`) {
			t.Errorf("JSON = %s, want a node key", raw)
		}
	})

	t.Run("no node", func(t *testing.T) {
		t.Parallel()

		row, raw := rowFor(t, "")
		if row.Node != "" {
			t.Errorf("Node = %q, want empty", row.Node)
		}
		if strings.Contains(string(raw), `"node"`) {
			t.Errorf("JSON = %s, must carry no node key", raw)
		}
	})
}
