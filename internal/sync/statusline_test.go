package sync

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

// putMarker writes one raw marker into the local file, the way a tick does.
func putMarker(t *testing.T, kv db.KV, key, value string) {
	t.Helper()
	if err := kv.KVPut(key, []byte(value)); err != nil {
		t.Fatalf("KVPut(%s): %v", key, err)
	}
}

// statuslineCases is the whole statusline mapping as markers: one row per
// token, and one row per precedence rule that decides between two.
var statuslineCases = []struct {
	name    string
	markers map[string]string
	want    string
}{
	{
		name:    "never enabled reads off",
		markers: nil,
		want:    TokenOff,
	},
	{
		name:    "enabled off is off",
		markers: map[string]string{KeyEnabled: `false`},
		want:    TokenOff,
	},
	{
		name: "enabled with a good tick is ok",
		markers: map[string]string{
			KeyEnabled:  `true`,
			KeyLastTick: `{"at":"2026-10-04T09:00:00Z","ok":true}`,
			KeyBacklog:  `0`,
		},
		want: TokenOK,
	},
	{
		name: "a failed tick is behind",
		markers: map[string]string{
			KeyEnabled:  `true`,
			KeyLastTick: `{"at":"2026-10-04T09:00:00Z","ok":false}`,
		},
		want: TokenBehind,
	},
	{
		name: "a backlog over the threshold is behind",
		markers: map[string]string{
			KeyEnabled:  `true`,
			KeyLastTick: `{"at":"2026-10-04T09:00:00Z","ok":true}`,
			KeyBacklog:  `1000`,
		},
		want: TokenBehind,
	},
	{
		name: "an error needing attention is an error",
		markers: map[string]string{
			KeyEnabled:   `true`,
			KeyLastTick:  `{"at":"2026-10-04T09:00:00Z","ok":true}`,
			KeyAttention: `{"at":"2026-10-04T09:05:00Z","message":"authorisation refused"}`,
		},
		want: TokenErr,
	},
	{
		name: "an error outranks a failed tick and a backlog",
		markers: map[string]string{
			KeyEnabled:   `true`,
			KeyLastTick:  `{"at":"2026-10-04T09:00:00Z","ok":false}`,
			KeyBacklog:   `9000`,
			KeyAttention: `{"at":"2026-10-04T09:05:00Z","message":"authorisation refused"}`,
		},
		want: TokenErr,
	},
	{
		name: "a backlog at the threshold is still ok",
		markers: map[string]string{
			KeyEnabled:  `true`,
			KeyLastTick: `{"at":"2026-10-04T09:00:00Z","ok":true}`,
			KeyBacklog:  `100`,
		},
		want: TokenOK,
	},
	{
		name: "off outranks every other marker",
		markers: map[string]string{
			KeyEnabled:   `false`,
			KeyBacklog:   `9000`,
			KeyAttention: `{"at":"2026-10-04T09:05:00Z","message":"authorisation refused"}`,
		},
		want: TokenOff,
	},
}

// TestStatuslineReadsLocalOnly pins the four tokens and where each comes from:
// the markers in the machine-local file and nothing else. The handle under the
// test is a local database with no client, no remote and no way to reach one,
// so a mapping that reached for a handle could not answer at all.
func TestStatuslineReadsLocalOnly(t *testing.T) {
	t.Parallel()

	// Only the local handle is in reach: a mapping that needed a client, a
	// remote or a shared row has nothing to find here.
	_, _, local := openSplit(t)

	for _, tc := range statuslineCases {
		for _, key := range []string{KeyEnabled, KeyBacklog, KeyLastTick, KeyAttention} {
			if err := local.KVDelete(key); err != nil {
				t.Fatalf("KVDelete(%s): %v", key, err)
			}
		}
		for key, value := range tc.markers {
			putMarker(t, local, key, value)
		}
		got, err := StatusToken(local)
		if err != nil {
			t.Fatalf("%s: StatusToken: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: token = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestStatuslineTokenRefusesAnUnreadableMarker pins that a marker that is not
// the shape its name says is a failure rather than a default: a machine whose
// last tick cannot be read must not be reported healthy.
func TestStatuslineTokenRefusesAnUnreadableMarker(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	putMarker(t, local, KeyLastTick, `{"ok":"yes"}`)

	if got, err := StatusToken(local); err == nil {
		t.Errorf("StatusToken = %q, want a refusal", got)
	}
}

// TestTokenIsTheWholeMapping pins the mapping on its own, so the order of the
// cases is pinned rather than the order of a test's table.
func TestTokenIsTheWholeMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   State
		want string
	}{
		{"off", State{}, TokenOff},
		{"ok", State{Enabled: true, LastTickOK: true}, TokenOK},
		{"behind on a failed tick", State{Enabled: true}, TokenBehind},
		{
			"behind on a backlog",
			State{Enabled: true, LastTickOK: true, Backlog: BacklogThreshold + 1},
			TokenBehind,
		},
		{
			"err outranks behind",
			State{Enabled: true, Attention: true},
			TokenErr,
		},
		{
			"off outranks err",
			State{Attention: true},
			TokenOff,
		},
	}
	for _, tc := range cases {
		if got := Token(tc.in); got != tc.want {
			t.Errorf("%s: token = %q, want %q", tc.name, got, tc.want)
		}
	}
}
