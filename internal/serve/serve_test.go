package serve

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

func TestClientsAddRevokeLookup(t *testing.T) {
	d := testServeDB(t)
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c, err := LoadClients(d, clientsPath)
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "test client")

	if _, status := c.Lookup(id); status != remote.KeyUnknown {
		t.Fatalf("Lookup unknown: got %v, want KeyUnknown", status)
	}

	cl, err := c.Add("client1", pubLine, time.Now())
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if cl.ID != id || cl.Label != "client1" {
		t.Fatalf("Add result mismatch: %+v", cl)
	}
	pub, status := c.Lookup(id)
	if status != remote.KeyActive || !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup after add = (%v, %v), want the active key", status, pub)
	}

	if _, err := c.Add("client1", pubLine, time.Now()); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Fatalf("Add duplicate: got %v, want ErrAlreadyEnrolled", err)
	}

	if err := c.Revoke(id, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, status = c.Lookup(id); status != remote.KeyRevoked {
		t.Fatalf("Lookup after revoke: got %v, want KeyRevoked", status)
	}
	if err := c.Revoke("SHA256:unknown", time.Now()); !errors.Is(err, ErrNoSuchClient) {
		t.Fatalf("Revoke unknown: got %v, want ErrNoSuchClient", err)
	}

	cl2, err := c.Add("client1-renewed", pubLine, time.Now())
	if err != nil {
		t.Fatalf("Re-add: %v", err)
	}
	if cl2.Label != "client1-renewed" {
		t.Fatalf("Re-add label mismatch: %+v", cl2)
	}
	if _, status = c.Lookup(id); status != remote.KeyActive {
		t.Fatalf("Lookup after re-add: got %v, want KeyActive", status)
	}

	cLoaded, err := LoadClients(d, clientsPath)
	if err != nil {
		t.Fatalf("LoadClients reload: %v", err)
	}
	pubLoaded, statusLoaded := cLoaded.Lookup(id)
	if statusLoaded != remote.KeyActive || !bytes.Equal(pubLoaded, kp.Public) {
		t.Fatalf("Lookup after reload = (%v, %v), want the active key", statusLoaded, pubLoaded)
	}
	if cLoaded.LabelOf(id) != "client1-renewed" {
		t.Fatalf("LabelOf: got %q, want client1-renewed", cLoaded.LabelOf(id))
	}
}

func TestClientsLookupSeesEnrollFromAnotherInstance(t *testing.T) {
	d := testServeDB(t)
	clientsPath := filepath.Join(t.TempDir(), "clients.json")
	c1, err := LoadClients(d, clientsPath)
	if err != nil {
		t.Fatalf("LoadClients 1: %v", err)
	}
	c2, err := LoadClients(d, clientsPath)
	if err != nil {
		t.Fatalf("LoadClients 2: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "client 1")

	if _, status := c2.Lookup(id); status != remote.KeyUnknown {
		t.Fatalf("Lookup before add: got %v, want KeyUnknown", status)
	}
	if _, err := c1.Add("client1", pubLine, time.Now()); err != nil {
		t.Fatalf("Add on c1: %v", err)
	}

	pub, status := c2.Lookup(id)
	if status != remote.KeyActive || !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup on c2 = (%v, %v), want the active key without a reload", status, pub)
	}
}

func TestClientsRefreshKeepsListOnParseError(t *testing.T) {
	d := testServeDB(t)
	c, err := LoadClients(d, filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := c.Add("client1", remote.MarshalPublic(kp.Public, "client 1"), time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// KVPut refuses invalid JSON, so a document the reader cannot parse is one
	// of the wrong shape.
	if err := d.KVPut(clientsKVKey, []byte(`{"not":"a client list"}`)); err != nil {
		t.Fatalf("corrupt serve.clients: %v", err)
	}

	pub, status := c.Lookup(id)
	if status != remote.KeyActive || !bytes.Equal(pub, kp.Public) {
		t.Fatalf("Lookup after corrupt row = (%v, %v), want the old list", status, pub)
	}
}

func TestClientsRefreshOnDelete(t *testing.T) {
	d := testServeDB(t)
	c, err := LoadClients(d, filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := c.Add("client1", remote.MarshalPublic(kp.Public, "client 1"), time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, status := c.Lookup(id); status != remote.KeyActive {
		t.Fatalf("Lookup before delete: got %v, want KeyActive", status)
	}

	if err := d.KVDelete(clientsKVKey); err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if _, status := c.Lookup(id); status != remote.KeyUnknown {
		t.Fatalf("Lookup after remove: got %v, want KeyUnknown", status)
	}
}

func TestOwnerLabel(t *testing.T) {
	c, err := LoadClients(testServeDB(t), filepath.Join(t.TempDir(), "clients.json"))
	if err != nil {
		t.Fatalf("LoadClients: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := c.Add("laptop", remote.MarshalPublic(kp.Public, "test client"), time.Now()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := ownerLabel(c, id); got != "laptop" {
		t.Errorf("ownerLabel(enrolled) = %q, want laptop", got)
	}
	if got := ownerLabel(c, ""); got != "-" {
		t.Errorf(`ownerLabel(unauthenticated) = %q, want "-"`, got)
	}
}

func requireAuthError(t *testing.T, handler http.Handler, req *http.Request, wantCode remote.Code) {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if errBody.Code != wantCode {
		t.Fatalf("error code = %q, want %q", errBody.Code, wantCode)
	}
}

func TestAuthRejects(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kpEnrolled, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pubLine := remote.MarshalPublic(kpEnrolled.Public, "enrolled")
	if _, err := s.clients.Add("enrolled", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Run("not_enrolled", func(t *testing.T) {
		kpUnknown, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		requireAuthError(t, handler, signedRequest(t, kpUnknown, "GET", "/v1/whoami", nil), remote.CodeNotEnrolled)
	})

	t.Run("revoked", func(t *testing.T) {
		kpRevoked, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		line := remote.MarshalPublic(kpRevoked.Public, "revoked")
		if _, err := s.clients.Add("revoked", line, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := s.clients.Revoke(remote.IDOf(kpRevoked.Public), time.Now()); err != nil {
			t.Fatal(err)
		}
		requireAuthError(t, handler, signedRequest(t, kpRevoked, "GET", "/v1/whoami", nil), remote.CodeRevoked)
	})

	t.Run("bad_signature", func(t *testing.T) {
		req := signedRequest(t, kpEnrolled, "GET", "/v1/whoami", nil)
		req.Header.Set(remote.HeaderSignature, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		requireAuthError(t, handler, req, remote.CodeBadSignature)
	})

	t.Run("stale", func(t *testing.T) {
		staleTime := time.Now().Add(-10 * time.Minute)
		nonce, err := remote.NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(nil)
		req, err := http.NewRequest("GET", "/v1/whoami", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, vv := range remote.Sign(kpEnrolled, "GET", "/v1/whoami", sum[:], staleTime, nonce) {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		requireAuthError(t, handler, req, remote.CodeStale)
	})
}

func TestAuthBodyCap(t *testing.T) {
	s, _ := newTestServer(t, 1024)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("cap-tester", remote.MarshalPublic(kp.Public, "cap-tester"), time.Now()); err != nil {
		t.Fatal(err)
	}

	// 1025 bytes is 1 byte over 1024.
	req := signedRequest(t, kp, "POST", "/v1/whoami", make([]byte, 1025))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if errBody.Code != remote.CodeTooLarge {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTooLarge)
	}
}

func TestWhoAmI(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	pubLine := remote.MarshalPublic(kp.Public, "alice")
	if _, err := s.clients.Add("alice", pubLine, time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if who.ID != id || who.Label != "alice" {
		t.Fatalf("who = %+v, want id %q label alice", who, id)
	}
	if who.ServerVersion != remote.Version {
		t.Fatalf("ServerVersion = %d, want %d", who.ServerVersion, remote.Version)
	}
	if len(who.Transports) != 1 || who.Transports[0] != "git-bundle" {
		t.Fatalf("Transports = %v, want [git-bundle]", who.Transports)
	}
	wantFeatures := []string{remote.FeatureTier, remote.FeatureQueue, remote.FeatureStop, remote.FeatureBuilder, remote.FeatureIdempotentSend, remote.FeatureAuthor, remote.FeatureRoles}
	if !slices.Equal(who.Features, wantFeatures) {
		t.Fatalf("Features = %v, want %v", who.Features, wantFeatures)
	}
	if who.Builders == nil || who.Builders.Cap <= 0 {
		t.Fatalf("Builders = %+v, want a positive Cap", who.Builders)
	}
	if who.BuilderTier != "harness" || who.MaxTier != "edit" {
		t.Fatalf("tiers = (%q, %q), want (harness, edit)", who.BuilderTier, who.MaxTier)
	}
	if who.Builders.Quota != "" {
		t.Fatalf("Builders.Quota = %q, want empty without a scope", who.Builders.Quota)
	}
}

// TestServerRefusesACreateWithoutAnActor pins A4: a client of this release
// always names the actor, so an empty one is a 400 naming the field and stores
// nothing.
func TestServerRefusesACreateWithoutAnActor(t *testing.T) {
	s, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := createBindingRequest(t, s, kp, remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(errBody.Message, "actor") {
		t.Fatalf("message = %q, want it to name actor", errBody.Message)
	}
	if _, err := testRuntime(t, s, id).Store.Load("api"); err == nil {
		t.Fatal("binding was stored despite the missing actor")
	}
}

// TestWhoAmIAdvertisesCandidateAndActors pins the A4 feature token values: the
// per-round candidate token is "candidate" and the actor token is "actors", and
// the old "builder"/"roles" tokens are gone.
func TestWhoAmIAdvertisesCandidateAndActors(t *testing.T) {
	s, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	for _, want := range []string{"candidate", "actors"} {
		if !slices.Contains(who.Features, want) {
			t.Errorf("Features = %v, want %q", who.Features, want)
		}
	}
	for _, gone := range []string{"builder", "roles"} {
		if slices.Contains(who.Features, gone) {
			t.Errorf("Features = %v, must not carry %q", who.Features, gone)
		}
	}
}

func TestWhoAmIScope(t *testing.T) {
	scoped, err := New(Config{
		DB:    testServeDB(t),
		Root:  t.TempDir(),
		Now:   time.Now,
		Scope: &spawn.ScopeSpec{Slice: "relevo.slice", CPUQuota: "200%"},
	})
	if err != nil {
		t.Fatalf("New scoped server: %v", err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	scoped.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode scoped body: %v", err)
	}
	if who.Builders == nil || who.Builders.Quota != "200%" || who.Builders.Slice != "relevo.slice" {
		t.Fatalf("scoped Builders = %+v, want the slice and quota", who.Builders)
	}
}

func TestWhoAmIBuilderTierFromPolicy(t *testing.T) {
	srv, kp := newTierTestServer(t, policy.Policy{Tier: map[string]string{"builder": "edit"}, MaxTier: "yolo"})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/whoami", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var who remote.WhoAmI
	if err := json.NewDecoder(rec.Body).Decode(&who); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if who.BuilderTier != "edit" || who.MaxTier != "yolo" {
		t.Fatalf("tiers = (%q, %q), want (edit, yolo)", who.BuilderTier, who.MaxTier)
	}
}

func TestNonV1Is426(t *testing.T) {
	s, _ := newTestServer(t, 0)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v0/whoami", nil))

	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want 426", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if errBody.Code != remote.CodeVersion || errBody.Message != "this server speaks v1" {
		t.Fatalf("error = (%q, %q), want version/this server speaks v1", errBody.Code, errBody.Message)
	}
}

func TestCreateBinding(t *testing.T) {
	s, root := newTestServer(t, 0)
	handler := s.Handler()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
		Role:       "builder",
	})
	req := signedRequest(t, kp, "POST", "/v1/bindings", createBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}
	var view remote.BindingView
	if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if view.Name != "api" || view.Round != 1 || view.State != "active" {
		t.Fatalf("view mismatch: %+v", view)
	}

	idDir, ok := id.Dir()
	if !ok {
		t.Fatalf("id.Dir() failed for %s", id)
	}
	bareRepoPath := filepath.Join(root, "repos", idDir, "repo123.git")
	if _, err := os.Stat(filepath.Join(bareRepoPath, "HEAD")); err != nil {
		t.Fatalf("bare repo HEAD missing at %s: %v", bareRepoPath, err)
	}

	b, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Owner != string(id) {
		t.Fatalf("binding.Owner = %q, want %q", b.Owner, id)
	}
}

func TestOwnerDirIsFlatHex(t *testing.T) {
	s, root := newTestServer(t, 0)

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo123",
		BaseCommit: strings.Repeat("a", 40),
		Role:       "builder",
	})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "POST", "/v1/bindings", createBody))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	bindingsDir := filepath.Join(root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", bindingsDir, err)
	}
	if len(entries) != 1 {
		t.Fatalf("len(entries) = %d, want 1", len(entries))
	}
	entry := entries[0]
	expectedDir, ok := id.Dir()
	if !ok {
		t.Fatalf("id.Dir() failed for %s", id)
	}
	if entry.Name() != expectedDir || len(entry.Name()) != 64 || !entry.IsDir() {
		t.Fatalf("entry = %q (dir %v), want the 64-char hex owner dir %q", entry.Name(), entry.IsDir(), expectedDir)
	}
	for _, c := range entry.Name() {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("entry name %q contains non-lower-hex char: %c", entry.Name(), c)
		}
	}

	// The binding's record lives in the machine database, scoped to the owner.
	if _, err := s.ownerStore(filepath.Join(bindingsDir, entry.Name())).Load("api"); err != nil {
		t.Fatalf("owner store Load(api): %v", err)
	}
}

func newTierTestServer(t *testing.T, pol policy.Policy) (*Server, remote.Keypair) {
	t.Helper()
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		DB:         testServeDB(t),
		Root:       t.TempDir(),
		Candidates: cSet,
		Policy:     pol,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}
	return srv, kp
}

func createBindingRequest(t *testing.T, srv *Server, kp remote.Keypair, req remote.CreateBindingRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedRequest(t, kp, "POST", "/v1/bindings", body))
	return rec
}

// roleTestRegistry builds the server's own roles.json for the role tests.
func roleTestRegistry(t *testing.T, set *candidate.Set, pol policy.Policy) *roles.Registry {
	t.Helper()
	writer := "writer"
	reg, err := roles.Build(&roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/anthropic/haiku"}},
		"ui-builder": {
			Shape:       &writer,
			Candidates:  []string{"claude/anthropic/haiku"},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "srv-ui"}},
		},
	}}, set, pol)
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}
	return reg
}

func newRoleTestServer(t *testing.T, pol policy.Policy, reg *roles.Registry) (*Server, remote.Keypair) {
	t.Helper()
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Config{
		DB:         testServeDB(t),
		Root:       t.TempDir(),
		Candidates: cSet,
		Policy:     pol,
		Registry:   reg,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("creator", remote.MarshalPublic(kp.Public, "creator"), time.Now()); err != nil {
		t.Fatal(err)
	}
	return srv, kp
}

func TestCreateBindingTier(t *testing.T) {
	cases := []struct {
		name       string
		pol        policy.Policy
		tier       string
		wantStatus int
		wantTier   string
	}{
		{"policy tier", policy.Policy{Tier: map[string]string{"builder": "edit"}}, "", http.StatusCreated, "edit"},
		{"no policy defaults to harness", policy.Policy{}, "", http.StatusCreated, "harness"},
		{"above max", policy.Policy{}, "yolo", http.StatusUnprocessableEntity, ""},
		{"bogus tier", policy.Policy{}, "bogus", http.StatusBadRequest, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, kp := newTierTestServer(t, tc.pol)
			rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
				Name:       "api",
				RepoID:     "repo123",
				BaseCommit: strings.Repeat("a", 40),
				Role:       "builder",
				Tier:       tc.tier,
			})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			id := remote.IDOf(kp.Public)
			if tc.wantStatus != http.StatusCreated {
				if _, err := testRuntime(t, srv, id).Store.Load("api"); err == nil {
					t.Fatal("binding was stored despite the refusal")
				}
				return
			}
			var view remote.BindingView
			if err := json.NewDecoder(rec.Body).Decode(&view); err != nil {
				t.Fatalf("decode view: %v", err)
			}
			if view.Tier != tc.wantTier {
				t.Fatalf("view.Tier = %q, want %q", view.Tier, tc.wantTier)
			}
			b, err := testRuntime(t, srv, id).Store.Load("api")
			if err != nil {
				t.Fatalf("Load binding: %v", err)
			}
			if b.Tier != tc.wantTier {
				t.Fatalf("stored binding Tier = %q, want %q", b.Tier, tc.wantTier)
			}
		})
	}
}

func TestCreateBindingRole(t *testing.T) {
	cases := []struct {
		name       string
		role       string
		wantStatus int
		wantMsg    string
		wantCand   string
		wantNoRepo bool
	}{
		{"server role", "ui-builder", http.StatusCreated, "", "claude/anthropic/haiku", false},
		{"unknown role", "nope", http.StatusBadRequest, `unknown actor "nope"`, "", true},
		{"reader role", "reviewer", http.StatusBadRequest, "reader actors run locally only; bind without --server", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := builderCandidateSet(t)
			if err != nil {
				t.Fatal(err)
			}
			srv, kp := newRoleTestServer(t, policy.Policy{}, roleTestRegistry(t, set, policy.Policy{}))
			rec := createBindingRequest(t, srv, kp, remote.CreateBindingRequest{
				Name:       "api",
				RepoID:     "repo123",
				BaseCommit: strings.Repeat("a", 40),
				Role:       tc.role,
			})
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantStatus != http.StatusCreated {
				requireCreateRefused(t, srv, kp, rec, tc.wantMsg, tc.wantNoRepo)
				return
			}
			requireRoleStored(t, srv, kp, tc.role, tc.wantCand)
		})
	}
}

// requireCreateRefused checks the refusal's message and that nothing was stored.
func requireCreateRefused(t *testing.T, srv *Server, kp remote.Keypair, rec *httptest.ResponseRecorder, wantMsg string, wantNoRepo bool) {
	t.Helper()
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if !strings.Contains(errBody.Message, wantMsg) {
		t.Errorf("message = %q, want it to contain %q", errBody.Message, wantMsg)
	}
	id := remote.IDOf(kp.Public)
	if _, err := testRuntime(t, srv, id).Store.Load("api"); err == nil {
		t.Fatal("binding was stored despite the refusal")
	}
	if !wantNoRepo {
		return
	}
	repoRoot, err := srv.repoRoot(id)
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(repoRoot, "repo123.git")
	if _, err := os.Stat(bare); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bare repo %s exists after the refusal (stat err = %v)", bare, err)
	}
}

func requireRoleStored(t *testing.T, srv *Server, kp remote.Keypair, role, wantCand string) {
	t.Helper()
	b, err := testRuntime(t, srv, remote.IDOf(kp.Public)).Store.Load("api")
	if err != nil {
		t.Fatalf("Load binding: %v", err)
	}
	if b.Role != role {
		t.Fatalf("stored binding Role = %q, want %q", b.Role, role)
	}
	if b.BuilderCandidate != wantCand {
		t.Fatalf("BuilderCandidate = %q, want %q", b.BuilderCandidate, wantCand)
	}
}

func TestCreateInvalid(t *testing.T) {
	s, _ := newTestServer(t, 0)
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("user", remote.MarshalPublic(kp.Public, "user"), time.Now()); err != nil {
		t.Fatal(err)
	}

	badNameBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "invalid/name",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("a", 40),
	})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, signedRequest(t, kp, "POST", "/v1/bindings", badNameBody))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var errBody remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}
}

func TestCreateDuplicate(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("user", remote.MarshalPublic(kp.Public, "user"), time.Now()); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("b", 40),
		Role:       "builder",
	})
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, signedRequest(t, kp, "POST", "/v1/bindings", body))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("status 1 = %d, want 201", rec1.Code)
	}
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, signedRequest(t, kp, "POST", "/v1/bindings", body))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("status 2 = %d, want 409", rec2.Code)
	}
}

func TestListIsOwnerScoped(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}
	kpB, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("bob", remote.MarshalPublic(kpB.Public, "bob"), time.Now()); err != nil {
		t.Fatal(err)
	}

	bodyA, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("1", 40),
		Role:       "builder",
	})
	recCreate := httptest.NewRecorder()
	handler.ServeHTTP(recCreate, signedRequest(t, kpA, "POST", "/v1/bindings", bodyA))
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body: %s", recCreate.Code, recCreate.Body.String())
	}

	recList := httptest.NewRecorder()
	handler.ServeHTTP(recList, signedRequest(t, kpB, "GET", "/v1/bindings", nil))
	if recList.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", recList.Code)
	}
	var views []remote.BindingView
	if err := json.NewDecoder(recList.Body).Decode(&views); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("B's list returned %d views, want 0", len(views))
	}

	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, signedRequest(t, kpB, "GET", "/v1/bindings/api", nil))
	if recGet.Code != http.StatusNotFound {
		t.Fatalf("B's GET of api status = %d, want 404", recGet.Code)
	}
}

func TestGetTouchesLastSeen(t *testing.T) {
	s, _ := newTestServer(t, 0)
	handler := s.Handler()
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	id := remote.IDOf(kp.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     "repo1",
		BaseCommit: strings.Repeat("2", 40),
		Role:       "builder",
	})
	recCreate := httptest.NewRecorder()
	handler.ServeHTTP(recCreate, signedRequest(t, kp, "POST", "/v1/bindings", createBody))
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201", recCreate.Code)
	}

	b, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	initial := b.Serve.LastSeen

	time.Sleep(10 * time.Millisecond)
	recGet := httptest.NewRecorder()
	handler.ServeHTTP(recGet, signedRequest(t, kp, "GET", "/v1/bindings/api", nil))
	if recGet.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200", recGet.Code)
	}

	bAfter, err := testRuntime(t, s, id).Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if !bAfter.Serve.LastSeen.After(initial) {
		t.Fatalf("touched LastSeen = %v not after initial = %v", bAfter.Serve.LastSeen, initial)
	}
}

func TestUnavailableGatesServerWide(t *testing.T) {
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}

	s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Candidates: cSet, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	kpA, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	idA := remote.IDOf(kpA.Public)
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kpA.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}

	unavailBody, _ := json.Marshal(remote.UnavailableRequest{
		Token:  "claude/anthropic/haiku",
		Reason: "rate limited test",
	})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/unavailable", unavailBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// The server-wide gate lives under the serve. prefix, not this machine's own
	// ledger key, and there is no per-owner database at all.
	if _, ok, err := s.DB().KVGet("serve.ledger"); err != nil || !ok {
		t.Fatalf("serve.ledger row = (_, %v, %v), want it present", ok, err)
	}
	if _, ok, err := s.DB().KVGet("ledger"); err != nil || ok {
		t.Fatalf("local ledger row = (_, %v, %v), want none", ok, err)
	}
	idADir, ok := idA.Dir()
	if !ok {
		t.Fatalf("idA.Dir() failed for %s", idA)
	}
	ownerDB := filepath.Join(s.cfg.Root, "bindings", idADir, "relevo.db")
	if _, err := os.Stat(ownerDB); !os.IsNotExist(err) {
		t.Fatalf("owner database at %s (err %v), want none", ownerDB, err)
	}
}

func newAvailableServer(t *testing.T) (*Server, remote.Keypair) {
	t.Helper()
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Candidates: cSet, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), time.Now()); err != nil {
		t.Fatal(err)
	}
	return s, kp
}

func TestAvailableClearsServerWideGate(t *testing.T) {
	s, kpA := newAvailableServer(t)
	handler := s.Handler()

	unavailBody, _ := json.Marshal(remote.UnavailableRequest{Token: "claude/anthropic/haiku", Reason: "rate limited test"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/unavailable", unavailBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("unavailable status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	availBody, _ := json.Marshal(remote.AvailableRequest{Subject: "anthropic"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", availBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("available status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var resp remote.AvailableResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode available response: %v", err)
	}
	if resp.Removed != 1 || resp.Provider != "anthropic" {
		t.Errorf("available = %+v, want provider anthropic removed 1", resp)
	}

	l, err := availability.LoadLedger(s.DB(), "")
	if err != nil {
		t.Fatalf("ledger.LoadLedger: %v", err)
	}
	for _, e := range l.Entries {
		if e.Kind == availability.RateLimited {
			t.Errorf("ledger still holds %+v, want the rate-limit gate gone", e)
		}
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", availBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("second available status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	resp = remote.AvailableResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode second available response: %v", err)
	}
	if resp.Removed != 0 {
		t.Errorf("second removed = %d, want 0", resp.Removed)
	}
}

func TestAvailableRejectsBadSubject(t *testing.T) {
	s, kpA := newAvailableServer(t)
	handler := s.Handler()

	emptyBody, _ := json.Marshal(remote.AvailableRequest{Subject: ""})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", emptyBody))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty subject status = %d, want 400", rec.Code)
	}

	unknownBody, _ := json.Marshal(remote.AvailableRequest{Subject: "claude/anthropic/nope"})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", unknownBody))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown token status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
}

func TestAvailableRefusesUnknownProvider(t *testing.T) {
	s, kpA := newAvailableServer(t)
	handler := s.Handler()

	body, _ := json.Marshal(remote.AvailableRequest{Subject: "anthropc"})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, signedRequest(t, kpA, "POST", "/v1/available", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown provider status = %d, want 422; body: %s", rec.Code, rec.Body.String())
	}
	var errBody remote.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if want := `no configured candidate uses provider "anthropc"`; !strings.Contains(errBody.Message, want) {
		t.Errorf("message = %q, want it containing %q", errBody.Message, want)
	}
}

func TestRoundStartAbsorbsAndChecksOut(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if _, err := os.Stat(b.Worktree); err != nil {
		t.Fatalf("worktree stat: %v", err)
	}
	wtHead, ok, err := env.gitClient.RefSHA(ctx, b.Worktree, "HEAD")
	if err != nil || !ok || wtHead != env.headSHA {
		t.Fatalf("worktree HEAD = (%q, %v, %v), want %q", wtHead, ok, err, env.headSHA)
	}
	planContent, err := os.ReadFile(rt.Store.PlanPath("api", 1))
	if err != nil || string(planContent) != "# Round 1 Plan\nDo stuff" {
		t.Fatalf("plan content: got (%q, %v)", string(planContent), err)
	}

	specs := startedSpecs(env)
	if len(specs) != 1 {
		t.Fatalf("runner specs count = %d, want 1", len(specs))
	}
	if specs[0].Dir != b.Worktree {
		t.Fatalf("runner spec Dir = %q, want %q", specs[0].Dir, b.Worktree)
	}
}

func TestRoundStartSetsShippedTags(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")

	// The client tags its base commit, and also ships a tag for a commit the
	// server has never seen: the round must still start, that tag skipped.
	runGit(t, env.clientDir, "-c", "tag.gpgsign=false", "tag", "v1.2.3", env.headSHA)
	missingSHA := strings.Repeat("f", 40)
	tagsJSON, err := json.Marshal([]remote.TagRef{
		{Name: "v1.2.3", SHA: env.headSHA},
		{Name: "unrelated", SHA: missingSHA},
		{Name: "release/1.0", SHA: env.headSHA},
		{Name: "..", SHA: env.headSHA},
	})
	if err != nil {
		t.Fatal(err)
	}

	formBytes, ct := makeRoundFormTags(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes, string(tagsJSON))
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	ctx := context.Background()
	got, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/v1.2.3")
	if err != nil || !ok || got != env.headSHA {
		t.Fatalf("refs/tags/v1.2.3 = (%q, %v, %v), want the base sha %q", got, ok, err, env.headSHA)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/unrelated"); err != nil || ok {
		t.Fatalf("refs/tags/unrelated: ok=%v err=%v, want it absent", ok, err)
	}
	got, ok, err = env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/release/1.0")
	if err != nil || !ok || got != env.headSHA {
		t.Fatalf("refs/tags/release/1.0 = (%q, %v, %v), want the base sha %q", got, ok, err, env.headSHA)
	}
	if _, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/tags/.."); err != nil || ok {
		t.Fatalf("refs/tags/..: ok=%v err=%v, want it absent (skipped, invalid tag)", ok, err)
	}

	pointsAtHead := strings.TrimSpace(runGit(t, b.Worktree, "tag", "--points-at", "HEAD"))
	for _, want := range []string{"v1.2.3", "release/1.0"} {
		if !slices.Contains(strings.Split(pointsAtHead, "\n"), want) {
			t.Fatalf("worktree tag --points-at HEAD = %q, want it to include %q", pointsAtHead, want)
		}
	}
}

func TestRoundStartHonoursTierField(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	rt := env.runtime(t)
	before, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if before.Tier != "harness" {
		t.Fatalf("binding stored at Tier = %q, want harness", before.Tier)
	}

	formBytes, ct := makeRoundFormWithTier(t, 1, "# Round 1 Plan\nDo stuff", "edit", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	specs := startedSpecs(env)
	if len(specs) != 1 {
		t.Fatalf("runner specs count = %d, want 1", len(specs))
	}
	if !slices.Contains(specs[0].Argv, "--permission-mode") || !slices.Contains(specs[0].Argv, "acceptEdits") {
		t.Fatalf("runner spec Argv = %v, want --permission-mode acceptEdits", specs[0].Argv)
	}

	after, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if after.RoundTier != "edit" {
		t.Fatalf("binding RoundTier = %q, want edit", after.RoundTier)
	}
}

func TestRoundStartTierAboveMaxIs422(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundFormWithTier(t, 1, "# Round 1 Plan\nDo stuff", "yolo", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusUnprocessableEntity)

	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeTierAboveMax {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeTierAboveMax)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if relevo.RoundStateOf(b, entries) != remote.RoundIdle {
		t.Fatalf("round state = %v, want idle", relevo.RoundStateOf(b, entries))
	}
	if n := len(startedSpecs(env)); n != 0 {
		t.Fatalf("runner specs count = %d, want 0", n)
	}
}

func TestRoundStartWhileRunningIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	// A different plan is what makes the second start refuse: an identical retry
	// is a no-op 200.
	changedBytes, changedCT := makeRoundForm(t, 1, "# Plan 1 (changed)", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", changedBytes, changedCT)
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

func TestRoundStartWithUndeliveredDoneMarkerIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.Round != 2 {
		t.Fatalf("round = %d, want 2 after the close", b.Round)
	}
	// Round 2's marker is on disk with the daemon not yet ticked: the window a
	// new start must refuse.
	if err := os.WriteFile(rt.Store.DonePath("api", 2), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	bytes2, ct2 := makeRoundForm(t, 2, "# Plan 2", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", bytes2, ct2)
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

func TestRoundStartRunningSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	first := decodeView(t, body)
	if first.RoundState != remote.RoundRunning {
		t.Fatalf("first round_state = %q, want running", first.RoundState)
	}

	rt := env.runtime(t)
	entriesBefore, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	before := startedSpecs(env)

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusOK)
	got := decodeView(t, body)
	if got.Round != first.Round || got.RoundState != remote.RoundRunning {
		t.Errorf("view = %+v, want round %d running", got, first.Round)
	}

	entriesAfter, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("log entries = %d, want %d (an identical retry appends nothing)", len(entriesAfter), len(entriesBefore))
	}
	if after := startedSpecs(env); len(after) != len(before) {
		t.Errorf("runner starts = %d, want %d (an identical retry starts no builder)", len(after), len(before))
	}
}

func TestRoundStartRunningDifferentPlanIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	otherBytes, otherCT := makeRoundForm(t, 1, "# A Different Plan", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", otherBytes, otherCT)
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

func TestRoundStartQueuedSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t, func(cfg *Config) { cfg.MaxBuilders = 1 })
	ownerB := addOwner(t, env, "bob")

	respA, bodyA := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, respA, bodyA, "A")
	respB, bodyB := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, respB, bodyB, "B")
	if viewB := decodeView(t, bodyB); viewB.RoundState != remote.RoundQueued {
		t.Fatalf("B round_state = %q, want queued", viewB.RoundState)
	}

	rtB := testRuntime(t, env.srv, ownerB.id)
	before, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B: %v", err)
	}
	if before.QueuedAt.IsZero() {
		t.Fatal("B QueuedAt is zero, want set")
	}
	entriesBefore, err := rtB.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog B: %v", err)
	}

	bundleBytes := snapshotRef(t, env, ownerB.clientDir, "refs/relevo/api/out")
	formBytes, ct := makeRoundForm(t, 1, "# Plan B", bundleBytes)
	resp, body := doSigned(t, env.ts, ownerB.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusOK)

	after, err := rtB.Store.Load("api")
	if err != nil {
		t.Fatalf("load B after: %v", err)
	}
	if !after.QueuedAt.Equal(before.QueuedAt) {
		t.Errorf("QueuedAt = %v, want unchanged %v", after.QueuedAt, before.QueuedAt)
	}
	entriesAfter, err := rtB.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog B after: %v", err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("log entries = %d, want %d (an identical retry appends nothing)", len(entriesAfter), len(entriesBefore))
	}
	if relevo.RoundStateOf(after, entriesAfter) != remote.RoundQueued {
		t.Errorf("round_state = %q, want queued", relevo.RoundStateOf(after, entriesAfter))
	}
	if after.Builder.PID != 0 {
		t.Errorf("PID = %d, want 0 (an identical retry starts no builder)", after.Builder.PID)
	}
}

func TestRoundResendSamePlanIs200(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)

	resendBytes, ct2 := makeRoundForm(t, 1, "# Plan 1", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", resendBytes, ct2)
	requireStatus(t, resp, body, http.StatusOK)
}

// TestRoundStartWithABrokenRunnerAcceptsThenHaltsAsync: the spawn happens in
// admit after the round is accepted, so its failure halts the binding instead of
// failing the send.
func TestRoundStartWithABrokenRunnerAcceptsThenHaltsAsync(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")

	env.runner.mu.Lock()
	env.runner.startErr = errors.New("boom: no such binary")
	env.runner.mu.Unlock()

	formBytes, ct := makeRoundForm(t, 1, "# Round 1 Plan\nDo stuff", bundleBytes)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)

	var view remote.BindingView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal binding view: %v; body: %s", err, string(body))
	}
	if view.State != string(store.StateNeedsYou) || !strings.Contains(view.Halt, "spawn failed") {
		t.Errorf("response = (state %q, halt %q), want needs_you with a spawn failure", view.State, view.Halt)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.State != store.StateNeedsYou || !strings.Contains(b.Halt, "spawn failed") {
		t.Errorf("binding = (state %s, halt %q), want needs_you with a spawn failure", b.State, b.Halt)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	sawPlan := false
	for _, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPlan {
			sawPlan = true
		}
	}
	if !sawPlan {
		t.Error("want a round 1 plan log entry: the round was staged, only the spawn failed")
	}
}

func TestRoundResendDifferentPlanIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)

	resendBytes, ct2 := makeRoundForm(t, 1, "# Different Plan", nil)
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", resendBytes, ct2)
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundStarted {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundStarted)
	}
}

func TestRoundStartNotFastForward(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "commit", "--allow-empty", "-m", "server commit")
	finishRound(t, env, rt, "api", 1)

	// Client commits on top of the initial commit, diverging from the server
	// branch.
	clientFile := filepath.Join(env.clientDir, "client.txt")
	if err := os.WriteFile(clientFile, []byte("client divergence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, env.clientDir, "add", "client.txt")
	runGit(t, env.clientDir, "commit", "-m", "client commit")
	clientHead, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("client HEAD: %v, ok=%v", err, ok)
	}
	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, clientHead, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}

	bundleBytes2 := snapshotRef(t, env, env.clientDir, outRef)
	formBytes2, ct2 := makeRoundForm(t, 2, "# Plan 2", bundleBytes2)
	resp2, body2 := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes2, ct2)
	requireStatus(t, resp2, body2, http.StatusUnprocessableEntity)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body2, &errBody)
	if errBody.Code != remote.CodeNotFastForward {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeNotFastForward)
	}
}

func TestRoundCloseServesFiles(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Worktree, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "round 1 result")

	streamText := `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n" +
		`{"type":"error","message":"Unexpected server error"}` + "\n"
	if err := os.WriteFile(rt.Store.RunnerStreamPath("api", 1), []byte(streamText), 0o644); err != nil {
		t.Fatal(err)
	}
	finishRound(t, env, rt, "api", 1)

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeView(t, body)
	if view.RoundState != remote.RoundClosed || view.ClosedRound != 1 {
		t.Fatalf("view = %+v, want closed round 1", view)
	}
	bareBranchSHA, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, "refs/heads/relevo/api")
	if err != nil || !ok {
		t.Fatalf("bare branch sha: %v, ok=%v", err, ok)
	}
	if view.ResultCommit != bareBranchSHA {
		t.Fatalf("result_commit = %q, want %q", view.ResultCommit, bareBranchSHA)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/report", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if !strings.Contains(string(body), "status: done") {
		t.Fatalf("report body missing status: done:\n%s", string(body))
	}
	for _, kind := range []string{"diff", "log"} {
		resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/"+kind, nil, "")
		requireStatus(t, resp, body, http.StatusOK)
	}
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/stream", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != streamText {
		t.Fatalf("stream body = %q, want %q", string(body), streamText)
	}
}

func TestRoundBundleAndAck(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Worktree, "result.txt"), []byte("result\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, b.Worktree, "add", "result.txt")
	runGit(t, b.Worktree, "commit", "-m", "round 1 result")
	finishRound(t, env, rt, "api", 1)

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeView(t, body)

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/bundle?since="+env.headSHA, nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	moved, err := env.transport.Absorb(ctx, env.clientDir, remote.ContentTypeGitBundle, bytes.NewReader(body), []string{"refs/heads/relevo/api"})
	if err != nil {
		t.Fatalf("client Absorb: %v", err)
	}
	if moved["refs/heads/relevo/api"] != view.ResultCommit {
		t.Fatalf("client moved branch = %q, want %q", moved["refs/heads/relevo/api"], view.ResultCommit)
	}
	clientHeadSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/heads/relevo/api")
	if err != nil || !ok || clientHeadSHA != view.ResultCommit {
		t.Fatalf("client branch sha = (%q, %v, %v), want %q", clientHeadSHA, ok, err, view.ResultCommit)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if ackView := decodeView(t, body); ackView.AckedRound != 1 {
		t.Fatalf("acked_round = %d, want 1", ackView.AckedRound)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if idleView := decodeView(t, body); idleView.RoundState != remote.RoundIdle {
		t.Fatalf("round_state = %q, want idle", idleView.RoundState)
	}
}

// TestRoundCloseRecordsStreamUsage: the report entry carries the stream's summed
// tokens and a measured/estimated cost, not "no reader".
func TestRoundCloseRecordsStreamUsage(t *testing.T) {
	prices := usage.Prices{Models: map[string]usage.ModelPrice{
		"anthropic/haiku": {In: 1, Out: 5},
	}}
	fu := &fakeStreamUsage{
		samples: []usage.Sample{
			{Provider: "anthropic", Model: "haiku", Tokens: usage.Tokens{In: 100, Out: 200}, USD: 0.05, HasCost: true},
			{Provider: "anthropic", Model: "haiku", Tokens: usage.Tokens{In: 1_000_000}},
		},
	}
	env := setupTestEnv(t, func(c *Config) {
		c.Usage = fu
		c.Prices = prices
	})

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)

	asked := false
	for _, src := range fu.sources {
		if src.Mode == usage.ModeHeadless && strings.HasSuffix(src.StreamPath, "001-runner.jsonl") {
			asked = true
		}
	}
	if !asked {
		t.Fatalf("reader never asked for the round's stream; sources: %+v", fu.sources)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	var report *store.LogEntry
	for i := range entries {
		if entries[i].Kind == store.KindReport {
			report = &entries[i]
		}
	}
	if report == nil {
		t.Fatal("no report entry after the round closed")
	}
	u := report.Usage
	if u == nil {
		t.Fatalf("report entry Usage = nil, want the stream's figure; note %q", report.Note)
	}
	if u.Tokens.In != 1_000_100 || u.Tokens.Out != 200 {
		t.Fatalf("Usage.Tokens = %+v, want in 1000100 out 200", u.Tokens)
	}
	if u.Cost.Basis != usage.Estimated {
		t.Fatalf("Usage.Cost.Basis = %q, want estimated", u.Cost.Basis)
	}
	// One measured sample (0.05) plus one estimated at 1/M input tokens over 1M
	// input tokens (1.00).
	if u.Cost.USD != 1.05 {
		t.Fatalf("Usage.Cost.USD = %v, want 1.05", u.Cost.USD)
	}
	if u.Model != "haiku" {
		t.Fatalf("Usage.Model = %q, want haiku", u.Model)
	}
}

func TestRoundCloseDirtyShipsSideRef(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Worktree, "untracked.txt"), []byte("dirty content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	finishRound(t, env, rt, "api", 1)

	resp, body := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeView(t, body)
	if view.DirtyCommit == "" {
		t.Fatal("view.DirtyCommit is empty, want non-empty")
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/bundle?since="+env.headSHA, nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	inboundRefs := []string{"refs/heads/relevo/api", "refs/relevo/api/round-1"}
	moved, err := env.transport.Absorb(ctx, env.clientDir, remote.ContentTypeGitBundle, bytes.NewReader(body), inboundRefs)
	if err != nil {
		t.Fatalf("Absorb: %v", err)
	}
	if _, movedBranch := moved["refs/heads/relevo/api"]; movedBranch {
		t.Fatal("branch was unexpectedly moved in absorb")
	}

	clientBranchSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/heads/relevo/api")
	if err != nil {
		t.Fatalf("client branch: %v", err)
	}
	if ok && clientBranchSHA != env.headSHA {
		t.Fatalf("client branch sha = %q, want %q", clientBranchSHA, env.headSHA)
	}
	sideSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "refs/relevo/api/round-1")
	if err != nil || !ok || sideSHA != view.DirtyCommit {
		t.Fatalf("side ref sha = (%q, %v, %v), want %q", sideSHA, ok, err, view.DirtyCommit)
	}
	catOut := runGit(t, env.clientDir, "cat-file", "-p", sideSHA)
	if !strings.Contains(catOut, "parent "+env.headSHA) {
		t.Fatalf("side ref commit missing parent %q:\n%s", env.headSHA, catOut)
	}
}

// TestStopRunningRoundKeepsBinding: stop kills the builder and closes the round
// but leaves the binding active; a second stop is 409 nothing_to_stop.
func TestStopRunningRoundKeepsBinding(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "A")
	if view := decodeView(t, body); view.RoundState != remote.RoundRunning {
		t.Fatalf("round_state = %q, want running", view.RoundState)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	pid := b.Builder.PID
	if pid == 0 {
		t.Fatal("builder PID = 0, want a running process")
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeView(t, body)
	if view.State != string(store.StateActive) || view.RoundState != remote.RoundClosed || view.Stopped != "killed" || view.ClosedRound != 1 {
		t.Errorf("view = %+v, want active, closed round 1, killed", view)
	}

	env.runner.mu.Lock()
	alive := env.runner.alive[pid]
	env.runner.mu.Unlock()
	if alive {
		t.Errorf("builder pid %d still alive after stop, want killed", pid)
	}
	if _, err := rt.Store.Load("api"); err != nil {
		t.Fatalf("Load after stop: %v, want the binding to still exist", err)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/stop", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeNothingToStop {
		t.Errorf("second stop code = %q, want %q", errBody.Code, remote.CodeNothingToStop)
	}
}

func TestStopAnotherOwnersBindingIs404(t *testing.T) {
	env := setupTestEnv(t)
	ownerB := addOwner(t, env, "bob")

	resp, body := sendRound(t, env, ownerB.kp, ownerB.clientDir, ownerB.repoID, ownerB.headSHA, "api", "# Plan B")
	requireCreated(t, resp, body, "B")

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/stop", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stop of another owner's binding = %d, want 404; body: %s", resp.StatusCode, string(body))
	}
}

func TestGetBindingRunningHasLive(t *testing.T) {
	env := setupTestEnv(t)

	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan A")
	requireCreated(t, resp, body, "A")
	if view := decodeView(t, body); view.RoundState != remote.RoundRunning {
		t.Fatalf("round_state = %q, want running", view.RoundState)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.Builder.PID == 0 {
		t.Fatal("stored builder PID = 0, want a running process")
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	view := decodeView(t, body)
	if view.Live == nil || view.Live.PID == 0 {
		t.Fatal("view.Live = nil or PID 0, want the running process")
	}
	if view.Live.PID != b.Builder.PID {
		t.Errorf("view.Live.PID = %d, want stored PID %d", view.Live.PID, b.Builder.PID)
	}

	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/stop", nil, "")
	requireStatus(t, resp, body, http.StatusOK)

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if viewAfter := decodeView(t, body); viewAfter.Live != nil {
		t.Errorf("view.Live after stop = %+v, want nil", viewAfter.Live)
	}
}

func TestFilesBeforeCloseIs404(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	for _, kind := range []string{"report", "diff", "plan", "stream"} {
		resp, _ := doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/"+kind, nil, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET files/%s status = %d, want 404", kind, resp.StatusCode)
		}
	}
}

func TestBundleWrongRoundIs404(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	rt := env.runtime(t)
	finishRound(t, env, rt, "api", 1)

	resp, _ = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/2/bundle", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET round 2 bundle status = %d, want 404", resp.StatusCode)
	}
}

func TestAckUnclosedIs409(t *testing.T) {
	env := setupTestEnv(t)

	bundleBytes := createAndAbsorb(t, env, "api")
	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, nil, http.StatusCreated)

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	requireStatus(t, resp, body, http.StatusConflict)
	var errBody remote.ErrorBody
	_ = json.Unmarshal(body, &errBody)
	if errBody.Code != remote.CodeRoundOpen {
		t.Fatalf("error code = %q, want %q", errBody.Code, remote.CodeRoundOpen)
	}
}

// seedRunningOwner enrolls label, creates its binding and starts round 1 on it.
func seedRunningOwner(t *testing.T, srv *Server, ts *httptest.Server, gitClient *git.Client, label string) remote.ClientID {
	t.Helper()
	ctx := context.Background()
	clientDir := t.TempDir()
	runGit(t, clientDir, "init")
	runGit(t, clientDir, "config", "user.name", label)
	runGit(t, clientDir, "config", "user.email", label+"@example.com")
	if err := os.WriteFile(filepath.Join(clientDir, "f.txt"), []byte(label+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, clientDir, "add", "f.txt")
	runGit(t, clientDir, "commit", "-m", "init "+label)

	head, _, _ := gitClient.RefSHA(ctx, clientDir, "HEAD")
	root, _ := gitClient.RootCommit(ctx, clientDir)
	repoID, _ := remote.RepoID(root)
	kp, _ := remote.Generate()
	id := remote.IDOf(kp.Public)
	_, _ = srv.clients.Add(label, remote.MarshalPublic(kp.Public, label), time.Now())

	name := "binding-" + label
	createBody, _ := json.Marshal(remote.CreateBindingRequest{Name: name, RepoID: repoID, BaseCommit: head, Role: "builder"})
	doSigned(t, ts, kp, "POST", "/v1/bindings", createBody, "application/json")
	_ = gitClient.UpdateRef(ctx, clientDir, "refs/relevo/"+name+"/out", head, "")
	trans := remote.NewBundleTransport(gitClient, t.TempDir())
	snap, _ := trans.Snapshot(ctx, clientDir, []string{"refs/relevo/" + name + "/out"}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()
	form, ct := makeRoundForm(t, 1, "# Plan "+label, bundleBytes)
	doSigned(t, ts, kp, "POST", "/v1/bindings/"+name+"/rounds", form, ct)
	return id
}

func TestTickWalksEveryOwner(t *testing.T) {
	ctx := context.Background()
	gitClient := git.NewClient("git", 0, 0)
	serverRoot := t.TempDir()
	cSet, err := builderCandidateSet(t)
	if err != nil {
		t.Fatal(err)
	}
	runner := newScriptRunner()
	srv, err := New(Config{
		DB:         testServeDB(t),
		Root:       serverRoot,
		Candidates: cSet,
		Runner:     runner,
		Git:        gitClient,
		Now:        time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	idA := seedRunningOwner(t, srv, ts, gitClient, "alice")
	idB := seedRunningOwner(t, srv, ts, gitClient, "bob")
	kpC, _ := remote.Generate()
	_, _ = srv.clients.Add("charlie", remote.MarshalPublic(kpC.Public, "charlie"), time.Now())

	runner.mu.Lock()
	initialAliveCount := len(runner.aliveHandles)
	runner.mu.Unlock()

	if err := srv.Tick(ctx); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	runner.mu.Lock()
	newAliveHandles := runner.aliveHandles[initialAliveCount:]
	specsCount := len(runner.specs)
	runner.mu.Unlock()
	if len(newAliveHandles) != 2 {
		t.Fatalf("new alive calls = %d, want exactly 2", len(newAliveHandles))
	}
	if specsCount != 2 {
		t.Fatalf("runner specs count = %d, want 2", specsCount)
	}

	bA, _ := testRuntime(t, srv, idA).Store.Load("binding-alice")
	bB, _ := testRuntime(t, srv, idB).Store.Load("binding-bob")
	seenA, seenB := false, false
	for _, h := range newAliveHandles {
		if h.PID == bA.Builder.PID {
			seenA = true
		}
		if h.PID == bB.Builder.PID {
			seenB = true
		}
	}
	if !seenA || !seenB {
		t.Fatalf("seenA=%v, seenB=%v; want both true", seenA, seenB)
	}

	requireNoOwnerDatabases(t, serverRoot, idA, idB, remote.IDOf(kpC.Public))
}

func requireNoOwnerDatabases(t *testing.T, root string, ids ...remote.ClientID) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "relevo.db")); !os.IsNotExist(err) {
		t.Errorf("serve-root database exists (err %v), want none", err)
	}
	for _, id := range ids {
		dir, ok := id.Dir()
		if !ok {
			t.Fatalf("id.Dir() failed for %s", id)
		}
		ownerDB := filepath.Join(root, "bindings", dir, "relevo.db")
		if _, err := os.Stat(ownerDB); !os.IsNotExist(err) {
			t.Errorf("per-owner database at %s (err %v), want none", ownerDB, err)
		}
	}
}

func TestTickSkipsMissingBindingsDir(t *testing.T) {
	srv, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Tick(context.Background()); err != nil {
		t.Fatalf("Tick on missing bindings dir returned error: %v", err)
	}
}

// candidatesViewServer builds a server with two builder candidates and a
// spawn-failed gate on haiku, so the view has a gated and a picked row.
func candidatesViewServer(t *testing.T) (*Server, remote.Keypair) {
	t.Helper()
	root := t.TempDir()
	candPath := filepath.Join(root, "candidates.json")
	candJSON := `[
		{"harness": "claude", "provider": "anthropic", "model": "haiku", "roles": ["builder"]},
		{"harness": "claude", "provider": "anthropic", "model": "sonnet", "roles": ["builder"]}
	]`
	if err := os.WriteFile(candPath, []byte(candJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	cSet, err := candidate.Load(candPath)
	if err != nil {
		t.Fatal(err)
	}
	pol := policy.Policy{Order: map[string][]string{
		"builder": {"claude/anthropic/haiku", "claude/anthropic/sonnet"},
	}}
	now := time.Now()
	l := availability.Ledger{Entries: []availability.Entry{{
		Kind:    availability.SpawnFailed,
		Subject: "claude/anthropic/haiku",
		Source:  "relevo",
		At:      now,
		Until:   now.Add(time.Hour),
		Note:    "test failure",
	}}}
	seedDB := testServeDB(t)
	if err := availability.SaveLedger(db.PrefixKV{KV: seedDB, Prefix: "serve."}, l); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Config{
		DB:         seedDB,
		Root:       root,
		Candidates: cSet,
		Policy:     pol,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), now); err != nil {
		t.Fatal(err)
	}
	return srv, kp
}

func TestCandidatesView(t *testing.T) {
	srv, kp := candidatesViewServer(t)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, signedRequest(t, kp, "GET", "/v1/candidates", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp remote.CandidatesResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Candidates) != 2 {
		t.Fatalf("got %d candidates, want 2", len(resp.Candidates))
	}

	haiku, sonnet := resp.Candidates[0], resp.Candidates[1]
	if haiku.Token != "claude/anthropic/haiku" || !haiku.Gated || haiku.Pick {
		t.Errorf("haiku = %+v, want Token=claude/anthropic/haiku, Gated=true, Pick=false", haiku)
	}
	if sonnet.Token != "claude/anthropic/sonnet" || sonnet.Gated || !sonnet.Pick {
		t.Errorf("sonnet = %+v, want Token=claude/anthropic/sonnet, Gated=false, Pick=true", sonnet)
	}

	pickCount := 0
	for _, c := range resp.Candidates {
		if c.Pick {
			pickCount++
		}
	}
	if pickCount != 1 {
		t.Errorf("pickCount = %d, want exactly 1", pickCount)
	}
}

// setupBuilderEnv is setupTestEnv with two builder candidates.
func setupBuilderEnv(t *testing.T) *testEnv {
	t.Helper()
	return setupTestEnv(t, func(c *Config) {
		body := `[
			{"harness":"claude","provider":"anthropic","model":"haiku","roles":["builder"]},
			{"harness":"opencode","provider":"anthropic","model":"haiku","roles":["builder"]}
		]`
		path := filepath.Join(t.TempDir(), "candidates.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		set, err := candidate.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		c.Candidates = set
	})
}

func TestRoundStartWithCandidateChangesTheBuilder(t *testing.T) {
	env := setupBuilderEnv(t)

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
		Candidate:  "claude/anthropic/haiku",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)

	outRef := "refs/relevo/api/out"
	if err := env.gitClient.UpdateRef(context.Background(), env.clientDir, outRef, env.headSHA, ""); err != nil {
		t.Fatalf("updateRef out: %v", err)
	}
	bundleBytes := snapshotRef(t, env, env.clientDir, outRef)

	formBytes, ct := roundFormCandidate(t, 1, "# Round 1 Plan", bundleBytes, "opencode/anthropic/haiku")
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusCreated)
	if view := decodeView(t, body); view.Candidate != "opencode/anthropic/haiku" {
		t.Errorf("view.Candidate = %q, want opencode/anthropic/haiku", view.Candidate)
	}

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	if b.BuilderCandidate != "opencode/anthropic/haiku" || b.Builder.Kind != "opencode" {
		t.Errorf("binding = (candidate %q, kind %q), want opencode/anthropic/haiku opencode", b.BuilderCandidate, b.Builder.Kind)
	}

	entries, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Round == 1 && e.Kind == store.KindPick && strings.Contains(e.Note, "opencode/anthropic/haiku") {
			found = true
		}
	}
	if !found {
		t.Errorf("no round 1 pick entry naming the new candidate: %+v", entries)
	}
}

// TestRoundStartUnknownCandidateRefusesBeforeAbsorb: validation runs before the
// absorb, so a bad token leaves the outbound ref unmoved.
func TestRoundStartUnknownCandidateRefusesBeforeAbsorb(t *testing.T) {
	env := setupBuilderEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
		Candidate:  "claude/anthropic/haiku",
	})
	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")
	requireStatus(t, resp, body, http.StatusCreated)

	rt := env.runtime(t)
	b, err := rt.Store.Load("api")
	if err != nil {
		t.Fatalf("load binding: %v", err)
	}
	outRef := "refs/relevo/api/out"

	// A new client commit, so an absorb during the request would move the ref.
	if err := os.WriteFile(filepath.Join(env.clientDir, "file.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, env.clientDir, "add", "file.txt")
	runGit(t, env.clientDir, "commit", "-m", "second commit")
	secondSHA, ok, err := env.gitClient.RefSHA(ctx, env.clientDir, "HEAD")
	if err != nil || !ok {
		t.Fatalf("second head: %v, ok=%v", err, ok)
	}
	if err := env.gitClient.UpdateRef(ctx, env.clientDir, outRef, secondSHA, ""); err != nil {
		t.Fatalf("updateRef client out: %v", err)
	}
	bundleBytes := snapshotRef(t, env, env.clientDir, outRef)

	// Seed the server's out ref, then rewind it to the base commit: an absorb
	// during the refused request would visibly move it to secondSHA.
	if _, err := env.transport.Absorb(ctx, b.Serve.BareRepo, remote.ContentTypeGitBundle, bytes.NewReader(bundleBytes), []string{outRef}); err != nil {
		t.Fatalf("seed server out ref: %v", err)
	}
	if err := env.gitClient.UpdateRef(ctx, b.Serve.BareRepo, outRef, env.headSHA, secondSHA); err != nil {
		t.Fatalf("rewind server out ref: %v", err)
	}

	formBytes, ct := roundFormCandidate(t, 1, "# Round 1 Plan", bundleBytes, "bogus/nope/x")
	resp, body = doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	requireStatus(t, resp, body, http.StatusUnprocessableEntity)
	var errBody remote.ErrorBody
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if errBody.Code != remote.CodeInvalid {
		t.Errorf("error code = %q, want %q", errBody.Code, remote.CodeInvalid)
	}

	got, ok, err := env.gitClient.RefSHA(ctx, b.Serve.BareRepo, outRef)
	if err != nil || !ok {
		t.Fatalf("server out ref: %v, ok=%v", err, ok)
	}
	if got != env.headSHA {
		t.Errorf("server out ref = %q, want it unmoved at %q", got, env.headSHA)
	}
}

func TestSweepTmp(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * time.Hour)

	write := func(name string, mtime time.Time) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
		return path
	}

	oldReq := write("req-body-x", old)
	freshReq := write("req-body-y", now)
	oldPlan := write("plan-z", old)
	oldOther := write("other", old)

	removed, err := sweepTmp(dir, time.Hour, now)
	if err != nil {
		t.Fatalf("sweepTmp: %v", err)
	}
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	for _, path := range []string{oldReq, oldPlan} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s still exists (stat err %v), want it removed", path, err)
		}
	}
	for _, path := range []string{freshReq, oldOther} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s stat err = %v, want it kept", path, err)
		}
	}
}

func TestRequestLogClientVersion(t *testing.T) {
	s, _ := newTestServer(t, 0)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	send := func(version string) string {
		t.Helper()
		buf.Reset()
		req := httptest.NewRequest("GET", "/v1/whoami", nil)
		if version != "" {
			req.Header.Set(remote.HeaderClientVersion, version)
		}
		s.Handler().ServeHTTP(httptest.NewRecorder(), req)
		return buf.String()
	}

	if got := send("v1.2.3"); !strings.Contains(got, "client_version=v1.2.3") {
		t.Errorf("log with the header = %q, want it to contain client_version=v1.2.3", got)
	}
	if got := send(""); strings.Contains(got, "client_version") {
		t.Errorf("log without the header = %q, want no client_version attribute", got)
	}
}

func TestRoundFileLogFrom(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan 1")
	requireCreated(t, resp, body, "api")

	rt := env.runtime(t)
	logContent := []byte("line 1\nline 2\nline 3\n")
	if err := os.WriteFile(rt.Store.BuilderLogPath("api", 1), logContent, 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	totalLenStr := strconv.Itoa(len(logContent))

	// No from: whole body, size = len, from = 0.
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := resp.Header.Get(remote.HeaderFileSize); got != totalLenStr {
		t.Fatalf("X-Relevo-Size = %q, want %q", got, totalLenStr)
	}
	if got := resp.Header.Get(remote.HeaderFileFrom); got != "0" {
		t.Fatalf("X-Relevo-From = %q, want 0", got)
	}
	if string(body) != string(logContent) {
		t.Fatalf("body = %q, want %q", string(body), string(logContent))
	}

	// ?from=k inside the file: suffix and From = k.
	k := 7
	kStr := strconv.Itoa(k)
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log?from="+kStr, nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := resp.Header.Get(remote.HeaderFileFrom); got != kStr {
		t.Fatalf("X-Relevo-From = %q, want %q", got, kStr)
	}
	if string(body) != string(logContent[k:]) {
		t.Fatalf("body = %q, want %q", string(body), string(logContent[k:]))
	}

	// ?from= past the end: empty body, size = len.
	pastEnd := totalLen(env, t) + 100
	pastEndStr := strconv.Itoa(pastEnd)
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log?from="+pastEndStr, nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := resp.Header.Get(remote.HeaderFileFrom); got != pastEndStr {
		t.Fatalf("X-Relevo-From = %q, want %q", got, pastEndStr)
	}
	if len(body) != 0 {
		t.Fatalf("body = %q, want empty", string(body))
	}

	// A negative or non-numeric from is a 400.
	for _, from := range []string{"-1", "x"} {
		resp, _ = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log?from="+from, nil, "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("from=%s status = %d, want 400", from, resp.StatusCode)
		}
	}
}

// TestRoundFileStreamServesAPreRenameRound: a round whose stream is only
// NNN-builder.jsonl still answers kind "stream" with the raw file and kind
// "log" with the rendered file, resolved through the store and RoundTranscript.
func TestRoundFileStreamServesAPreRenameRound(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan 1")
	requireCreated(t, resp, body, "api")

	rt := env.runtime(t)
	// The fake round start wrote its own stream at the new name; drop it so the
	// round has the pre-rename file only.
	if err := os.Remove(rt.Store.RunnerStreamPath("api", 1)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove new-name stream: %v", err)
	}
	stream := "stream line 1\nstream line 2\n"
	if err := os.WriteFile(rt.Store.BuilderStreamPath("api", 1), []byte(stream), 0o644); err != nil {
		t.Fatalf("write pre-rename stream: %v", err)
	}
	finishRound(t, env, rt, "api", 1)

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/stream", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != stream {
		t.Fatalf("stream body = %q, want the pre-rename stream %q", string(body), stream)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != stream {
		t.Fatalf("log body = %q, want the rendered pre-rename stream %q", string(body), stream)
	}
	if got := resp.Header.Get(remote.HeaderFileSize); got != strconv.Itoa(len(stream)) {
		t.Fatalf("X-Relevo-Size = %q, want %d", got, len(stream))
	}
}

func totalLen(env *testEnv, t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(env.runtime(t).Store.BuilderLogPath("api", 1))
	if err != nil {
		t.Fatal(err)
	}
	return len(data)
}

// TestRoundFileLogRendersTheStream: a stream with no log answers the rendered
// bytes, and a fetch from the old size returns exactly the new lines.
func TestRoundFileLogRendersTheStream(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan 1")
	requireCreated(t, resp, body, "api")

	rt := env.runtime(t)
	streamPath := rt.Store.RunnerStreamPath("api", 1)
	stream := "stream line 1\nstream line 2\n"
	if err := os.WriteFile(streamPath, []byte(stream), 0o644); err != nil {
		t.Fatalf("write stream: %v", err)
	}
	totalLenStr := strconv.Itoa(len(stream))
	// The round start writes a "builder started" log; this case is the logless
	// one, so drop it and let the stream answer.
	if err := os.Remove(rt.Store.BuilderLogPath("api", 1)); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove log: %v", err)
	}

	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if got := resp.Header.Get(remote.HeaderFileSize); got != totalLenStr {
		t.Fatalf("X-Relevo-Size = %q, want %q", got, totalLenStr)
	}
	if string(body) != stream {
		t.Fatalf("body = %q, want the rendered stream %q", string(body), stream)
	}

	k := 7
	kStr := strconv.Itoa(k)
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log?from="+kStr, nil, "")
	if got := resp.Header.Get(remote.HeaderFileFrom); got != kStr {
		t.Fatalf("X-Relevo-From = %q, want %q", got, kStr)
	}
	if string(body) != stream[k:] {
		t.Fatalf("body = %q, want %q", string(body), stream[k:])
	}

	// A fetch from the old size returns exactly the new rendered lines.
	if err := os.WriteFile(streamPath, []byte(stream+"stream line 3\n"), 0o644); err != nil {
		t.Fatalf("append stream: %v", err)
	}
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/log?from="+totalLenStr, nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != "stream line 3\n" {
		t.Fatalf("body = %q, want exactly the new rendered lines", string(body))
	}
}

func TestRoundFileDriftRunning(t *testing.T) {
	env := setupTestEnv(t)
	resp, body := sendRound(t, env, env.kp, env.clientDir, env.repoID, env.headSHA, "api", "# Plan 1")
	requireCreated(t, resp, body, "api")

	rt := env.runtime(t)

	// A missing drift file is 404 "file not found", not "round 1 is not closed".
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/drift", nil, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if !strings.Contains(string(body), "file not found") || strings.Contains(string(body), "not closed") {
		t.Fatalf("body = %q, want 'file not found' and not 'not closed'", string(body))
	}

	driftContent := []byte("diff --git a/foo b/foo\n+drift\n")
	if err := os.WriteFile(rt.Store.DriftPath("api", 1), driftContent, 0o644); err != nil {
		t.Fatalf("write drift: %v", err)
	}
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/drift", nil, "")
	requireStatus(t, resp, body, http.StatusOK)
	if string(body) != string(driftContent) {
		t.Fatalf("body = %q, want %q", string(body), string(driftContent))
	}

	// A report on a running round is still 404 "not closed".
	resp, body = doSigned(t, env.ts, env.kp, "GET", "/v1/bindings/api/rounds/1/files/report", nil, "")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "not closed") {
		t.Fatalf("report on running round = (%d, %q), want 404 not closed", resp.StatusCode, string(body))
	}
}

func TestWireUnbindReleasesRefs(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bare, _ := seedServedBinding(t, env, "target", store.ServeFacts{RepoID: env.repoID})
	_, _ = seedServedBinding(t, env, "sibling", store.ServeFacts{RepoID: env.repoID})

	rec := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rec, signedRequest(t, env.kp, "POST", "/v1/bindings/target/unbind", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/bindings/target/unbind status = %d, body: %s", rec.Code, rec.Body.String())
	}

	requireRefGone(t, ctx, env.gitClient, bare, "refs/heads/relevo/target")
	targetRefs, err := env.gitClient.ListRefs(ctx, bare, "refs/relevo/target/")
	if err != nil {
		t.Fatalf("list target refs: %v", err)
	}
	if len(targetRefs) != 0 {
		t.Errorf("target refs = %v; want none", targetRefs)
	}

	requireRefPresent(t, ctx, env.gitClient, bare, "refs/heads/relevo/sibling")
	siblingRefs, err := env.gitClient.ListRefs(ctx, bare, "refs/relevo/sibling/")
	if err != nil {
		t.Fatalf("list sibling refs: %v", err)
	}
	if len(siblingRefs) != 2 {
		t.Errorf("sibling refs = %v; want 2 refs", siblingRefs)
	}
}

func TestAdminUnbindAndGCAbandonedReleaseRefs(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bareAdmin, _ := seedServedBinding(t, env, "admin-target", store.ServeFacts{RepoID: env.repoID})
	res, err := AdminUnbind(ctx, env.srv, string(env.id), "admin-target", false)
	if err != nil {
		t.Fatalf("AdminUnbind: %v", err)
	}
	if !res.Archived {
		t.Error("AdminUnbind res.Archived = false; want true")
	}
	requireRefGone(t, ctx, env.gitClient, bareAdmin, "refs/heads/relevo/admin-target")
	adminRefs, err := env.gitClient.ListRefs(ctx, bareAdmin, "refs/relevo/admin-target/")
	if err != nil {
		t.Fatalf("list admin refs: %v", err)
	}
	if len(adminRefs) != 0 {
		t.Errorf("admin refs = %v; want none", adminRefs)
	}

	bareGC, _ := seedServedBinding(t, env, "gc-target", store.ServeFacts{
		RepoID:   env.repoID,
		LastSeen: env.srv.cfg.Now().Add(-2 * time.Hour),
	})
	gcResults, err := GCAbandoned(ctx, env.srv, time.Hour, env.srv.cfg.Now(), true)
	if err != nil {
		t.Fatalf("GCAbandoned dry run: %v", err)
	}
	if len(gcResults) != 1 || gcResults[0].Name != "gc-target" || gcResults[0].Archive {
		t.Fatalf("gcResults = %+v", gcResults)
	}
	requireRefPresent(t, ctx, env.gitClient, bareGC, "refs/heads/relevo/gc-target")

	gcResults, err = GCAbandoned(ctx, env.srv, time.Hour, env.srv.cfg.Now(), false)
	if err != nil {
		t.Fatalf("GCAbandoned real run: %v", err)
	}
	if len(gcResults) != 1 || !gcResults[0].Archive {
		t.Fatalf("gcResults = %+v", gcResults)
	}
	requireRefGone(t, ctx, env.gitClient, bareGC, "refs/heads/relevo/gc-target")
	gcRefsAfter, err := env.gitClient.ListRefs(ctx, bareGC, "refs/relevo/gc-target/")
	if err != nil {
		t.Fatalf("list gc refs after: %v", err)
	}
	if len(gcRefsAfter) != 0 {
		t.Errorf("gc refs after = %v; want none", gcRefsAfter)
	}
}

func TestUnbindKeepingADirtyWorktreeKeepsItsRefs(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	bare, wt := seedServedBinding(t, env, "dirty-target", store.ServeFacts{RepoID: env.repoID})
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("dirty uncommitted content\n"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	rec := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rec, signedRequest(t, env.kp, "POST", "/v1/bindings/dirty-target/unbind", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/bindings/dirty-target/unbind status = %d, body: %s", rec.Code, rec.Body.String())
	}

	requireRefPresent(t, ctx, env.gitClient, bare, "refs/heads/relevo/dirty-target")
	refs, err := env.gitClient.ListRefs(ctx, bare, "refs/relevo/dirty-target/")
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("refs/relevo/dirty-target/* = %v; want 2 kept refs", refs)
	}
}
