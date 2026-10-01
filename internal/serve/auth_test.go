package serve

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestAuthenticateCreatesTmpOwnerOnly pins that the request temp directory
// authenticate creates is owner-only. It compares against a same-umask control
// directory and skips when the umask leaves the control's owner bits clear,
// because then 0755 and 0700 are indistinguishable and the assertion could not
// fail. The request is unsigned, so remote.Verify stops at the empty client
// header and the server's clients and nonces are never called.
func TestAuthenticateCreatesTmpOwnerOnly(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "serve")

	control := filepath.Join(parent, "control")
	if err := os.Mkdir(control, 0o755); err != nil {
		t.Fatalf("mkdir control: %v", err)
	}
	controlInfo, err := os.Stat(control)
	if err != nil {
		t.Fatalf("stat control: %v", err)
	}
	if want := controlInfo.Mode().Perm() & 0o700; want != 0o700 {
		t.Skipf("umask masks the owner bits (control %o), so 0755 and 0700 are indistinguishable", controlInfo.Mode().Perm())
	}

	s := &Server{cfg: Config{Root: root, MaxBundleBytes: 1 << 20, Now: time.Now}, audiences: []string{testAudience}}
	handler := s.authenticate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/v1/whoami", http.NoBody)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d: an unsigned request must stop at Verify", rec.Code, http.StatusUnauthorized)
	}

	info, err := os.Stat(filepath.Join(root, "tmp"))
	if err != nil {
		t.Fatalf("stat <root>/tmp: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("tmp dir mode = %o, want 700", got)
	}
}

// TestServeOldClientSkew pins what a pre-upgrade client gets from an upgraded
// server: HTTP 426 with the version code, and the audience-scheme response
// header on every response -- the refusal, the non-/v1 426, and a 401 alike.
func TestServeOldClientSkew(t *testing.T) {
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Audiences: []string{testAudience}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.clients.Add("old", remote.MarshalPublic(kp.Public, "old"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()

	// A request signed the old way: no Relevo-Audience header at all.
	oldWay := func() *http.Request {
		t.Helper()
		req, err := http.NewRequest("GET", "/v1/whoami", nil)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := remote.NewNonce()
		if err != nil {
			t.Fatal(err)
		}
		hdr := remote.Sign(kp, testAudience, "GET", "/v1/whoami", nil, time.Now(), nonce)
		hdr.Del(remote.HeaderAudience)
		for k, vv := range hdr {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		return req
	}

	requireAudienceScheme := func(t *testing.T, rec *httptest.ResponseRecorder) {
		t.Helper()
		if got := rec.Header().Get(remote.HeaderAuthScheme); got != remote.AuthSchemeAudience {
			t.Fatalf("Relevo-Auth = %q, want %q", got, remote.AuthSchemeAudience)
		}
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, oldWay())
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("old client status = %d, want 426", rec.Code)
	}
	requireAudienceScheme(t, rec)
	var body remote.ErrorBody
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode 426 body: %v", err)
	}
	if body.Code != remote.CodeVersion {
		t.Fatalf("old client code = %q, want %q", body.Code, remote.CodeVersion)
	}

	// The non-/v1 426 carries the header too.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("non-v1 status = %d, want 426", rec.Code)
	}
	requireAudienceScheme(t, rec)

	// So does a 401 refusal.
	bad := oldWay()
	bad.Header.Set(remote.HeaderAudience, testAudience)
	bad.Header.Set(remote.HeaderSignature, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-signature status = %d, want 401", rec.Code)
	}
	requireAudienceScheme(t, rec)
}

// TestReplayAcrossServersIsRefused is the headline case: two servers share one
// enrolled client key but accept different audiences, so a request signed for
// A is refused by B. Rewriting B's audience header breaks the signature (the
// audience is signed), and A still verifies the original: B's refusal never
// consumed A's nonce.
func TestReplayAcrossServersIsRefused(t *testing.T) {
	const fpA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const fpB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	newServer := func(audiences []string) *Server {
		t.Helper()
		s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Audiences: audiences})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
			t.Fatalf("clients.Add: %v", err)
		}
		return s
	}
	serverA := newServer([]string{fpA})
	serverB := newServer([]string{fpB})

	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	hdr := remote.Sign(kp, fpA, "GET", "/v1/whoami", nil, now, nonce)

	serveTo := func(target *Server, h http.Header) (*httptest.ResponseRecorder, remote.ErrorBody) {
		t.Helper()
		req, err := http.NewRequest("GET", "/v1/whoami", nil)
		if err != nil {
			t.Fatal(err)
		}
		for k, vv := range h {
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
		rec := httptest.NewRecorder()
		target.Handler().ServeHTTP(rec, req)
		var body remote.ErrorBody
		_ = json.NewDecoder(rec.Body).Decode(&body)
		return rec, body
	}

	// B refuses: the request was signed for A.
	rec, body := serveTo(serverB, hdr)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("B status = %d, want 401", rec.Code)
	}
	if body.Code != remote.CodeWrongAudience {
		t.Fatalf("B code = %q, want %q", body.Code, remote.CodeWrongAudience)
	}

	// Rewriting the audience to B breaks the signature.
	rewritten := hdr.Clone()
	rewritten.Set(remote.HeaderAudience, fpB)
	rec, body = serveTo(serverB, rewritten)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("B rewritten status = %d, want 401", rec.Code)
	}
	if body.Code != remote.CodeBadSignature {
		t.Fatalf("B rewritten code = %q, want %q", body.Code, remote.CodeBadSignature)
	}

	// The original still verifies at A: its nonce was not burned.
	rec, _ = serveTo(serverA, hdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("A status = %d, want 200: B must not burn the nonce (body %s)", rec.Code, rec.Body.String())
	}
}

// TestServeAudiencesFailsClosed pins the audience helper and the server: with
// no certificate and no --public-host value the set is empty and the call
// fails; a server with an empty set refuses even a well-formed request.
func TestServeAudiencesFailsClosed(t *testing.T) {
	d := testServeDB(t)
	secrets := SecretStore{DB: d}

	if _, err := Audiences(secrets, nil); err == nil {
		t.Fatal("Audiences with no certificate and no hosts returned nil, want an error")
	}

	hosts, err := Audiences(secrets, []string{"Zen.Example.COM"})
	if err != nil {
		t.Fatalf("Audiences with a host: %v", err)
	}
	if len(hosts) != 1 || hosts[0] != "host:zen.example.com" {
		t.Fatalf("Audiences = %v, want [host:zen.example.com]", hosts)
	}

	fp, err := InitTLS(secrets, []string{"zen.example.com"}, time.Now())
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	set, err := Audiences(secrets, []string{"zen.example.com"})
	if err != nil {
		t.Fatalf("Audiences with a certificate: %v", err)
	}
	want := map[string]bool{fp: true, "host:zen.example.com": true}
	if len(set) != len(want) {
		t.Fatalf("Audiences = %v, want the fingerprint and the host", set)
	}
	for _, a := range set {
		if !want[a] {
			t.Fatalf("Audiences = %v, want the fingerprint and the host", set)
		}
	}

	// A server with an empty set refuses a valid request.
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := s.clients.Add("alice", remote.MarshalPublic(kp.Public, "alice"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	requireAuthError(t, s.Handler(), signedRequest(t, kp, "GET", "/v1/whoami", nil), remote.CodeWrongAudience)
}
