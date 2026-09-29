package serve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
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

	s := &Server{cfg: Config{Root: root, MaxBundleBytes: 1 << 20, Now: time.Now}}
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
