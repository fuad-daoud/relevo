package serve

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// createBinding posts a signed create request for name and returns the view it
// answered with and the row the server stored.
func createBinding(t *testing.T, env *testEnv, name string, req remote.CreateBindingRequest) (remote.BindingView, store.Binding) {
	t.Helper()
	req.Name = name
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, raw := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", body, "application/json")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create %s status = %d, want 201; body: %s", name, resp.StatusCode, string(raw))
	}
	var view remote.BindingView
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	stored, err := env.runtime(t).Store.Load(name)
	if err != nil {
		t.Fatalf("Load(%s): %v", name, err)
	}
	return view, stored
}

// TestCreateBindingViewCarriesLink pins the server half of the link: the create
// response carries the server's own record id and installation id, and the
// created row's link is the request's Client* pair. A client that sends no
// Client* fields -- an older client -- gets a row with no link, and still a
// view naming the server's record.
func TestCreateBindingViewCarriesLink(t *testing.T) {
	inst := installation.Installation{ID: "01SRVINSTALLATION", Label: "zen"}
	env := setupTestEnv(t, func(c *Config) { c.Installation = inst })

	view, stored := createBinding(t, env, "linked", remote.CreateBindingRequest{
		RepoID:             env.repoID,
		BaseCommit:         env.headSHA,
		Role:               "builder",
		ClientInstallation: "01CLIENTINSTALLATION",
		ClientBindingID:    "01CLIENTRECORD",
	})

	if view.ID == "" {
		t.Error("view.ID is empty, want the server's record id")
	}
	if view.Installation != inst.ID {
		t.Errorf("view.Installation = %q, want %q", view.Installation, inst.ID)
	}
	if stored.Link == nil || stored.Link.Installation != "01CLIENTINSTALLATION" || stored.Link.ID != "01CLIENTRECORD" {
		t.Fatalf("stored Link = %+v, want the request's Client* pair", stored.Link)
	}
	if got, err := env.runtime(t).Store.RecordID("linked"); err != nil || got != view.ID {
		t.Errorf("RecordID = %q (err %v), want the view's ID %q", got, err, view.ID)
	}

	viewOld, storedOld := createBinding(t, env, "unlinked", remote.CreateBindingRequest{
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	if storedOld.Link != nil {
		t.Fatalf("a client that sends no Client* fields stored Link = %+v, want nil", storedOld.Link)
	}
	if viewOld.ID == "" || viewOld.Installation != inst.ID {
		t.Errorf("view for an old client = ID %q, installation %q, want the server's own facts",
			viewOld.ID, viewOld.Installation)
	}
}
