package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// seedClosedReaderRound starts a reader round over the wire and closes it with
// an output and a nested artifact, and returns the binding's artifact
// directory.
func seedClosedReaderRound(t *testing.T, env *testEnv) string {
	t.Helper()
	startReaderRound(t, env, "review")
	rt := env.runtime(t)
	b, err := rt.Store.Load("review")
	if err != nil {
		t.Fatal(err)
	}
	dir := rt.Store.ArtifactDir(b.Name, 1, "reviewer")
	if err := os.MkdirAll(filepath.Join(dir, "site"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		filepath.Join(dir, "findings.md"):        "the findings\n",
		filepath.Join(dir, "site", "index.html"): "<html>hi</html>\n",
	}
	for path, body := range files {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	finishRound(t, env, rt, "review", 1)
	return dir
}

// seedWriterBinding creates a writer binding whose round 1 looks closed, so
// the artifact routes must refuse it as not a reader binding.
func seedWriterBinding(t *testing.T, env *testEnv, name string) {
	t.Helper()
	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name: name, RepoID: env.repoID, BaseCommit: env.headSHA, Role: "builder",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load(name)
	if err != nil {
		t.Fatal(err)
	}
	if b.Serve == nil {
		b.Serve = &store.ServeFacts{}
	}
	b.Serve.ClosedRound = 1
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
}

// TestReaderArtifactListAndDownload pins the two routes: the listing decodes
// with the output first and the nested rel, and the nested rel downloads
// byte-for-byte.
func TestReaderArtifactListAndDownload(t *testing.T) {
	env := setupTestEnv(t, withReviewerCandidate(t))
	seedClosedReaderRound(t, env)

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/review/rounds/1/artifacts", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	var list remote.ArtifactList
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode artifact list: %v", err)
	}
	if list.Actor != "reviewer" || list.Output != "findings.md" {
		t.Fatalf("list = %+v, want actor reviewer and output findings.md", list)
	}
	var rels []string
	for _, f := range list.Files {
		rels = append(rels, f.Rel)
	}
	if !slices.Equal(rels, []string{"findings.md", "site/index.html"}) {
		t.Fatalf("rels = %v, want the output first then the nested rel", rels)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/review/rounds/1/artifacts/site/index.html", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != "<html>hi</html>\n" {
		t.Fatalf("nested artifact = %q", string(body))
	}
}

// TestReaderArtifactRoutesNotFound pins the 404s: an unlisted rel, a
// traversing rel, a round that is not closed, a writer binding, and another
// owner.
func TestReaderArtifactRoutesNotFound(t *testing.T) {
	env := setupTestEnv(t, withReviewerCandidate(t))
	seedClosedReaderRound(t, env)
	seedWriterBinding(t, env, "w")
	other := addOwner(t, env, "bob")

	cases := []struct {
		name string
		path string
		kp   remote.Keypair
	}{
		{"unlisted rel", "/v1/bindings/review/rounds/1/artifacts/nope.txt", env.kp},
		{"traversing rel", "/v1/bindings/review/rounds/1/artifacts/..%2Fescape.txt", env.kp},
		{"round not closed", "/v1/bindings/review/rounds/2/artifacts", env.kp},
		{"writer binding", "/v1/bindings/w/rounds/1/artifacts", env.kp},
		{"another owner", "/v1/bindings/review/rounds/1/artifacts", other.kp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doSigned(t, env.ts, tc.kp, "GET", tc.path, nil, "")
			requireStatus(t, resp, body, http.StatusNotFound)
		})
	}
}

// TestReaderArtifactsServedAfterSeal pins that a sealed round still lists and
// serves every artifact, from the round_file rows.
func TestReaderArtifactsServedAfterSeal(t *testing.T) {
	env := setupTestEnv(t, withReviewerCandidate(t))
	dir := seedClosedReaderRound(t, env)

	rt := env.runtime(t)
	// Seal round 1 the way the daemon's seal pass does: its files become
	// round_file rows and leave the binding directory.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		_, err := tx.SealRound("review", 1)
		return err
	}); err != nil {
		t.Fatalf("seal round 1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "findings.md")); !os.IsNotExist(err) {
		t.Fatalf("the output is still on disk after the seal (stat err = %v)", err)
	}

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/review/rounds/1/artifacts/site/index.html", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != "<html>hi</html>\n" {
		t.Fatalf("sealed nested artifact = %q", string(body))
	}
}
