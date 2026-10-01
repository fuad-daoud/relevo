package serve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func waitForAddr(s *Server) (net.Addr, error) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if addr := s.Addr(); addr != nil {
			return addr, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("timed out waiting for server address")
}

// pinnedClient verifies the presented certificate against wantFP.
func pinnedClient(wantFP string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
					if len(rawCerts) == 0 {
						return errors.New("no peer certificates presented")
					}
					if gotFP := FingerprintOf(rawCerts[0]); gotFP != wantFP {
						return fmt.Errorf("fingerprint mismatch: got %s, want %s", gotFP, wantFP)
					}
					return nil
				},
			},
		},
	}
}

// listenTLSConfig initialises the server's TLS material and returns the
// certificate plus a client that pins its fingerprint.
func listenTLSConfig(t *testing.T, dir string, now time.Time) (*tls.Certificate, *http.Client) {
	t.Helper()
	secrets := SecretStore{DB: testServeDB(t)}
	fp, err := InitTLS(secrets, []string{"127.0.0.1"}, now)
	if err != nil {
		t.Fatalf("InitTLS: %v", err)
	}
	cert, err := LoadTLS(secrets)
	if err != nil {
		t.Fatalf("LoadTLS: %v", err)
	}
	return &cert, pinnedClient(fp)
}

// whoAmILabel signs a whoami request with now -- the server's clock is pinned to
// the same instant, so the signature's nonce window is valid -- and returns the
// label it answers.
func whoAmILabel(t *testing.T, client *http.Client, scheme string, addr net.Addr, kp remote.Keypair, now time.Time) string {
	t.Helper()
	req, err := http.NewRequest("GET", fmt.Sprintf("%s://%s/v1/whoami", scheme, addr.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := remote.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	for k, vv := range remote.Sign(kp, testAudience, "GET", "/v1/whoami", nil, now, nonce) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var who remote.WhoAmI
	if err := json.NewDecoder(resp.Body).Decode(&who); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return who.Label
}

// TestListenWhoAmI serves one signed whoami over TLS and over plain HTTP: both
// must authenticate and answer the enrolled label.
func TestListenWhoAmI(t *testing.T) {
	cases := []struct {
		name     string
		insecure bool
		label    string
	}{
		{"tls", false, "alice"},
		{"insecure http", true, "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			now := time.Now()

			lc := ListenConfig{Addr: "127.0.0.1:0", InsecureHTTP: tc.insecure}
			scheme := "http"
			client := &http.Client{}
			if !tc.insecure {
				lc.TLS, client = listenTLSConfig(t, dir, now)
				scheme = "https"
			}

			srv, err := New(Config{DB: testServeDB(t), Root: dir, Now: func() time.Time { return now }, Audiences: []string{testAudience}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			kp, err := remote.Generate()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := srv.clients.Add(tc.label, remote.MarshalPublic(kp.Public, tc.label), "", now); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			errCh := make(chan error, 1)
			go func() { errCh <- srv.ListenAndServe(ctx, lc) }()

			addr, err := waitForAddr(srv)
			if err != nil {
				t.Fatal(err)
			}
			if got := whoAmILabel(t, client, scheme, addr, kp, now); got != tc.label {
				t.Errorf("who.Label = %q, want %q", got, tc.label)
			}

			cancel()
			select {
			case err := <-errCh:
				if err != nil {
					t.Fatalf("ListenAndServe returned error on cancel: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("ListenAndServe did not return within 5 seconds")
			}
		})
	}
}

func TestListenRefusesWithoutTLS(t *testing.T) {
	srv, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Audiences: []string{testAudience}})
	if err != nil {
		t.Fatal(err)
	}

	// The refusal has to be observed with a deadline: take the guard away and
	// ListenAndServe binds the port and blocks in Serve until cancellation, so a
	// direct call would hang instead of failing.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{Addr: "127.0.0.1:0"})
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrNoTLS) {
			t.Fatalf("ListenAndServe without TLS err = %v, want ErrNoTLS", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe without TLS never returned: the no-certificate guard is gone")
	}
}

// TestListenAndServeWaitsForRun: on cancel, ListenAndServe shuts the HTTP server
// down and returns only after Run has finished its in-flight tick. Tick cannot
// be held open through the real implementation, so this uses tickFn.
func TestListenAndServeWaitsForRun(t *testing.T) {
	srv, err := New(Config{DB: testServeDB(t), Root: t.TempDir(), Now: time.Now, Interval: time.Millisecond, Audiences: []string{testAudience}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var mu sync.Mutex
	var sawCancelled, tickReturned bool

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv.tickFn = func(ctx context.Context) error {
		if ctx.Err() != nil {
			mu.Lock()
			sawCancelled = true
			mu.Unlock()
		}
		once.Do(func() { close(started) })
		<-release
		mu.Lock()
		tickReturned = true
		mu.Unlock()
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe(ctx, ListenConfig{Addr: "127.0.0.1:0", InsecureHTTP: true})
	}()

	if _, err := waitForAddr(srv); err != nil {
		t.Fatal(err)
	}

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first tick never started")
	}

	cancel()

	select {
	case err := <-errCh:
		t.Fatalf("ListenAndServe returned %v before the in-flight tick finished", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not return after the in-flight tick finished")
	}

	mu.Lock()
	defer mu.Unlock()
	if !tickReturned {
		t.Error("the tick never returned")
	}
	if sawCancelled {
		t.Error("the tick saw a cancelled context, want the WithoutCancel one")
	}
}
