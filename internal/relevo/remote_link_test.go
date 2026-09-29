package relevo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestAddRemoteRecordsLink pins the client half of the link: a server that
// advertises FeatureOrigin is sent this client's installation and the record id
// minted for the binding, the view's server installation and record id land on
// the client row's link, and the stored row carries the id the server was told.
func TestAddRemoteRecordsLink(t *testing.T) {
	ctx := context.Background()
	st := store.New(t.TempDir())
	fg := &fakeGit{
		headCommitID:  "1111111111111111111111111111111111111111",
		rootCommitSHA: "2222222222222222222222222222222222222222",
	}
	fr := &fakeRemote{
		whoAmIResp: remote.WhoAmI{Features: []string{remote.FeatureOrigin}},
		createBindingResp: remote.BindingView{
			Name:         "api",
			Candidate:    "claude/anthropic/haiku",
			ID:           "01SRVRECORD",
			Installation: "01SRVINSTALL",
		},
	}
	rt := Runtime{Store: st, Git: fg, Remote: fr, Now: time.Now, MasterMinds: addRemoteMasterMind(t)}

	if _, err := Add(ctx, rt, AddOptions{Name: "api", Server: "zen", Repo: "/fake/repo"}); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if fr.createBindingReq.ClientBindingID == "" {
		t.Fatal("CreateBindingRequest.ClientBindingID is empty, want the pre-minted record id")
	}
	inst, err := installation.Load(filepath.Dir(st.DBPath()))
	if err != nil {
		t.Fatalf("installation.Load: %v", err)
	}
	if fr.createBindingReq.ClientInstallation != inst.ID {
		t.Fatalf("ClientInstallation = %q, want the store root's installation %q",
			fr.createBindingReq.ClientInstallation, inst.ID)
	}

	stored, err := st.Load("api")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stored.Link == nil || stored.Link.Installation != "01SRVINSTALL" || stored.Link.ID != "01SRVRECORD" {
		t.Fatalf("stored Link = %+v, want the view's installation and id", stored.Link)
	}
	recordID, err := st.RecordID("api")
	if err != nil {
		t.Fatalf("RecordID: %v", err)
	}
	if recordID != fr.createBindingReq.ClientBindingID {
		t.Fatalf("stored record id = %q, want the id sent to the server %q",
			recordID, fr.createBindingReq.ClientBindingID)
	}
}
