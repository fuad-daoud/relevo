package serve

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/view"
)

var update = flag.Bool("update", false, "update golden files")

var fixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func fixedKeypair() remote.Keypair {
	var seed [32]byte
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	return remote.Keypair{Private: priv, Public: pub}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	filename := name
	if !strings.HasSuffix(filename, ".golden") {
		filename += ".golden"
	}
	path := filepath.Join("testdata", "contract", filename)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s: re-run with 'go test ./internal/serve -run Contract -update' to generate", path)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch in %s: re-run with 'go test ./internal/serve -run Contract -update' to update\n--- got ---\n%s\n--- want ---\n%s", path, string(got), string(want))
	}
}

type routeEntry struct {
	pattern string
	method  string
	path    string
}

var registeredRoutes = []routeEntry{
	{"GET /v1/whoami", "GET", "/v1/whoami"},
	{"GET /v1/candidates", "GET", "/v1/candidates"},
	{"GET /v1/actors/{actor}", "GET", "/v1/actors/test-actor"},
	{"POST /v1/bindings", "POST", "/v1/bindings"},
	{"GET /v1/bindings", "GET", "/v1/bindings"},
	{"GET /v1/bindings/{name}", "GET", "/v1/bindings/test-binding"},
	{"POST /v1/bindings/{name}/done", "POST", "/v1/bindings/test-binding/done"},
	{"POST /v1/bindings/{name}/unbind", "POST", "/v1/bindings/test-binding/unbind"},
	{"POST /v1/bindings/{name}/stop", "POST", "/v1/bindings/test-binding/stop"},
	{"POST /v1/bindings/{name}/resume", "POST", "/v1/bindings/test-binding/resume"},
	{"POST /v1/bindings/{name}/rounds", "POST", "/v1/bindings/test-binding/rounds"},
	{"GET /v1/bindings/{name}/rounds/{n}/files/{kind}", "GET", "/v1/bindings/test-binding/rounds/1/files/report"},
	{"GET /v1/bindings/{name}/rounds/{n}/artifacts", "GET", "/v1/bindings/test-binding/rounds/1/artifacts"},
	{"GET /v1/bindings/{name}/rounds/{n}/artifacts/{rel...}", "GET", "/v1/bindings/test-binding/rounds/1/artifacts/findings.md"},
	{"GET /v1/bindings/{name}/rounds/{n}/bundle", "GET", "/v1/bindings/test-binding/rounds/1/bundle"},
	{"POST /v1/bindings/{name}/rounds/{n}/ack", "POST", "/v1/bindings/test-binding/rounds/1/ack"},
	{"POST /v1/bindings/{name}/unavailable", "POST", "/v1/bindings/test-binding/unavailable"},
	{"POST /v1/unavailable", "POST", "/v1/unavailable"},
	{"POST /v1/available", "POST", "/v1/available"},
}

func testSignedRequest(t *testing.T, kp remote.Keypair, method, target string, body []byte) *http.Request {
	t.Helper()
	var bodyReader io.Reader
	var sum []byte
	if body != nil {
		bodyReader = bytes.NewReader(body)
		s := sha256.Sum256(body)
		sum = s[:]
	}
	req, err := http.NewRequest(method, target, bodyReader)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatalf("NewNonce: %v", err)
	}
	hdr := remote.Sign(kp, testAudience, method, target, sum, fixedTime, nonce)
	for k, vv := range hdr {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	return req
}

func TestContractRoutes(t *testing.T) {
	d := testServeDB(t)
	cfg := Config{
		DB:             d,
		Root:           t.TempDir(),
		MaxBundleBytes: 10 * 1024 * 1024,
		Now:            func() time.Time { return fixedTime },
		Audiences:      []string{testAudience},
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	handler := srv.Handler()

	kp := fixedKeypair()
	pubLine := remote.MarshalPublic(kp.Public, "client1")
	if _, err := srv.clients.Add("client1", pubLine, fixedTime); err != nil {
		t.Fatalf("clients.Add: %v", err)
	}

	var lines []string

	for _, route := range registeredRoutes {
		// 1. Send unsigned request: must be refused by authentication (401), not by not-found handler (404).
		req, err := http.NewRequest(route.method, route.path, nil)
		if err != nil {
			t.Fatalf("NewRequest %s %s: %v", route.method, route.path, err)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		var errBody remote.ErrorBody
		if err := json.NewDecoder(rec.Body).Decode(&errBody); err != nil {
			t.Fatalf("decode ErrorBody for %s %s: %v", route.method, route.path, err)
		}

		if rec.Code == http.StatusNotFound || errBody.Code == remote.CodeNotFound {
			t.Fatalf("route %s %s reached not-found handler instead of being refused by auth", route.method, route.path)
		}
		if rec.Code != http.StatusUnauthorized || errBody.Code != remote.CodeNotEnrolled {
			t.Fatalf("route %s %s got status %d, error %q; want 401 %s", route.method, route.path, rec.Code, errBody.Code, remote.CodeNotEnrolled)
		}

		lines = append(lines, fmt.Sprintf("%s %s %d %s", route.method, route.path, rec.Code, errBody.Code))
	}

	// Request to /v1/nope must reach the not-found handler.
	nopeReq := testSignedRequest(t, kp, "GET", "/v1/nope", nil)
	nopeRec := httptest.NewRecorder()
	handler.ServeHTTP(nopeRec, nopeReq)

	var nopeErr remote.ErrorBody
	if err := json.NewDecoder(nopeRec.Body).Decode(&nopeErr); err != nil {
		t.Fatalf("decode ErrorBody for /v1/nope: %v", err)
	}
	if nopeRec.Code != http.StatusNotFound || nopeErr.Code != remote.CodeNotFound {
		t.Fatalf("/v1/nope got status %d, error %q; want 404 %s", nopeRec.Code, nopeErr.Code, remote.CodeNotFound)
	}
	lines = append(lines, fmt.Sprintf("GET /v1/nope %d %s", nopeRec.Code, nopeErr.Code))

	out := strings.Join(lines, "\n") + "\n"
	assertGolden(t, "routes", []byte(out))
}

func TestContractRoutesComplete(t *testing.T) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, "routes.go", nil, 0)
	if err != nil {
		t.Fatalf("ParseFile routes.go: %v", err)
	}

	var foundPatterns []string
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) >= 1 {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
				val := strings.Trim(lit.Value, "\"")
				if val != "/v1/" {
					foundPatterns = append(foundPatterns, val)
				}
			}
		}
		return true
	})

	pinnedPatterns := make([]string, len(registeredRoutes))
	for i, r := range registeredRoutes {
		pinnedPatterns[i] = r.pattern
	}

	sort.Strings(foundPatterns)
	sort.Strings(pinnedPatterns)

	if !reflect.DeepEqual(foundPatterns, pinnedPatterns) {
		t.Fatalf("routes.go patterns do not match registeredRoutes in contract_test.go:\nfound in routes.go: %v\npinned in test:     %v", foundPatterns, pinnedPatterns)
	}
}

func TestContractStatusDocument(t *testing.T) {
	builders := remote.BuildersView{
		Running: 1,
		Queued:  2,
		Cap:     4,
		Scopes:  true,
		Slice:   "relevo.slice",
		Quota:   "150%",
	}

	owner1 := OwnerStatus{
		Owner:    remote.ClientID("SHA256:alice11111111111111111111111111111111111111"),
		Label:    "alice",
		LastSeen: fixedTime,
		Report: view.Report{
			Bindings: []view.BindingStatus{
				{
					Name:          "active-task",
					State:         "ACTIVE",
					Round:         2,
					BuilderStatus: "running",
					Owner:         "SHA256:alice11111111111111111111111111111111111111",
					OwnerLabel:    "alice",
				},
			},
		},
	}

	owner2 := OwnerStatus{
		Owner:    remote.ClientID("SHA256:bob2222222222222222222222222222222222222222"),
		Label:    "bob",
		LastSeen: fixedTime.Add(2 * time.Hour),
		Report: view.Report{
			Bindings: []view.BindingStatus{
				{
					Name:          "done-task",
					State:         "DONE",
					Round:         1,
					BuilderStatus: "closed",
					Owner:         "SHA256:bob2222222222222222222222222222222222222222",
					OwnerLabel:    "bob",
				},
			},
		},
	}

	owners := []OwnerStatus{owner1, owner2}
	doc := StatusDocument(owners, builders)

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent StatusDocument: %v", err)
	}
	b = append(b, '\n')
	assertGolden(t, "status-document", b)
}
