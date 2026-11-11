package relevo

import (
	"context"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestApplyRemoteViewCandidateRefresh pins the candidate refresh on an
// apply tick: a view naming no candidate keeps the stored one, and a view
// naming a new candidate switches to it with its harness kind.
func TestApplyRemoteViewCandidateRefresh(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		viewCandidate string
		wantCandidate string
		wantKind      string
	}{
		{
			name:          "unnamed keeps stored",
			viewCandidate: "",
			wantCandidate: testAgyRef,
			wantKind:      "agy",
		},
		{
			name:          "named switches",
			viewCandidate: testOpencodeRef,
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
			if err := rt.Store.Save(b); err != nil {
				t.Fatalf("Save: %v", err)
			}
			view := remote.BindingView{
				RoundState: remote.RoundQueued,
				Candidate:  tt.viewCandidate,
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
			if got.BuilderCandidate != tt.wantCandidate {
				t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, tt.wantCandidate)
			}
			if got.Builder.Kind != tt.wantKind {
				t.Errorf("Builder.Kind = %q, want %q", got.Builder.Kind, tt.wantKind)
			}
		})
	}
}
