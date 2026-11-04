package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestApplyRemoteViewNodeCopy pins the laptop's node copy: the stored builder
// carries the view's node while the round is forwarded, and reads as local
// again once the view names none. The candidate refresh shares the tick
// without interfering either way.
func TestApplyRemoteViewNodeCopy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		storedNode    string
		viewNode      string
		viewCandidate string
		wantNode      string
		wantCandidate string
		wantKind      string
	}{
		{
			name:          "node set copies",
			storedNode:    "",
			viewNode:      "b12",
			viewCandidate: "",
			wantNode:      "b12",
			wantCandidate: testAgyRef,
			wantKind:      "agy",
		},
		{
			name:          "empty clears",
			storedNode:    "b12",
			viewNode:      "",
			viewCandidate: "",
			wantNode:      "",
			wantCandidate: testAgyRef,
			wantKind:      "agy",
		},
		{
			name:          "candidate switch still fires with node",
			storedNode:    "",
			viewNode:      "b12",
			viewCandidate: testOpencodeRef,
			wantNode:      "b12",
			wantCandidate: testOpencodeRef,
			wantKind:      "opencode",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rt, b := seedBound(t)
			b.Builder.Mode = store.ModeRemote
			b.Builder.Server = "zen"
			b.Builder.RemoteNode = tt.storedNode
			if err := rt.Store.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			view := remote.BindingView{
				RoundState: remote.RoundQueued,
				Candidate:  tt.viewCandidate,
				Node:       tt.viewNode,
			}
			f := &remoteFetch{Name: b.Name, Server: "zen", Round: b.Round, View: view}
			var got store.Binding
			if err := rt.Store.WithLock(func(tx *store.Tx) error {
				var err error
				got, _, err = applyRemoteView(context.Background(), rt, tx, b, f, rt.Now())
				return err
			}); err != nil {
				t.Fatalf("applyRemoteView: %v", err)
			}
			if got.Builder.RemoteNode != tt.wantNode {
				t.Errorf("RemoteNode = %q, want %q", got.Builder.RemoteNode, tt.wantNode)
			}
			if got.BuilderCandidate != tt.wantCandidate {
				t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, tt.wantCandidate)
			}
			if got.Builder.Kind != tt.wantKind {
				t.Errorf("Builder.Kind = %q, want %q", got.Builder.Kind, tt.wantKind)
			}
		})
	}
}
