package relevo

import (
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestNewRemoteClient pins every arm of the rule turning a servers section and
// a stored key into a client. Only construction is exercised: no network call
// is made.
func TestNewRemoteClient(t *testing.T) {
	t.Parallel()

	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("remote.Generate: %v", err)
	}
	pem, err := remote.MarshalPrivate(kp)
	if err != nil {
		t.Fatalf("remote.MarshalPrivate: %v", err)
	}
	servers := remote.Servers{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00"}}

	cases := []struct {
		name       string
		servers    remote.Servers
		key        []byte
		wantClient bool
		wantErr    bool
		wantNoKey  bool
	}{
		{"no servers is no client", nil, pem, false, false, false},
		{"servers without a key name the fix", servers, nil, false, true, true},
		{"an unusable key is an error", servers, []byte("not a pem key"), false, true, false},
		{"a keyed server builds a client", servers, pem, true, false, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewRemoteClient(c.servers, c.key)
			if c.wantErr && err == nil {
				t.Fatal("err = nil, want an error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if c.wantNoKey {
				if !errors.Is(err, ErrNoClientKey) {
					t.Errorf("err = %v, want ErrNoClientKey", err)
				}
				if !strings.Contains(ErrNoClientKey.Error(), "run relevo config server key") {
					t.Errorf("ErrNoClientKey = %q, want it to name the fix", ErrNoClientKey.Error())
				}
			} else if errors.Is(err, ErrNoClientKey) {
				t.Errorf("err = %v, want an error other than ErrNoClientKey", err)
			}
			if c.wantClient && got == nil {
				t.Error("client = nil, want a constructed client")
			}
			if !c.wantClient && got != nil {
				t.Errorf("client = %v, want nil", got)
			}
		})
	}
}
