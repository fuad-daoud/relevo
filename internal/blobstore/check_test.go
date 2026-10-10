package blobstore

import (
	"errors"
	"strings"
	"testing"
)

// An origin from a remote entry becomes a key's first segment and part of a
// signed URL, so anything that could address another key or reshape the URL is
// refused, and an installation id is not.
func TestCheckOriginRefusesKeyShapingCharacters(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "evil/x", "..", "a.b", "ev?il", "frag#ment", "pct%2f", "sp ace", strings.Repeat("a", maxOriginLen+1)} {
		if err := CheckOrigin(bad); !errors.Is(err, ErrBadOrigin) {
			t.Errorf("CheckOrigin(%q) = %v, want ErrBadOrigin", bad, err)
		}
	}
	for _, good := range []string{"01M3QA4BMKM66Q5SQKCTZXD7RV", "origin-a", "m_1"} {
		if err := CheckOrigin(good); err != nil {
			t.Errorf("CheckOrigin(%q) = %v, want nil", good, err)
		}
	}
}

// A signed request crosses the network only over https; plain http is for a
// fake store on loopback.
func TestCheckEndpointRequiresHTTPSOffLoopback(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"https://acct.r2.cloudflarestorage.com", "http://127.0.0.1:9000", "http://localhost:9000", "http://[::1]:9000"} {
		if err := CheckEndpoint(ok); err != nil {
			t.Errorf("CheckEndpoint(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"http://acct.r2.cloudflarestorage.com", "acct.r2.cloudflarestorage.com", "ftp://127.0.0.1", "https://", "http://10.0.0.5:9000"} {
		if err := CheckEndpoint(bad); !errors.Is(err, ErrInsecureEndpoint) {
			t.Errorf("CheckEndpoint(%q) = %v, want ErrInsecureEndpoint", bad, err)
		}
	}
}
