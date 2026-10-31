package relevo

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestReconcileRemoteTakesTheServersAccount: a served round's account is the
// server's own choice, so observing the view carries it onto the client
// binding the owner's own status names it.
func TestReconcileRemoteTakesTheServersAccount(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	b := remoteBinding("zen")
	if err := st.Save(b); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRemote{
		getBindingResp: remote.BindingView{RoundState: remote.RoundIdle, Account: "work"},
	}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderAccount != "work" {
		t.Fatalf("BuilderAccount = %q, want the server's account", got.BuilderAccount)
	}
}
