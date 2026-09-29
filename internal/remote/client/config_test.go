package client

import (
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
)

func TestServersRoundTrip(t *testing.T) {
	saved := remote.Servers{
		"zen": remote.ServerEntry{
			URL:         "https://zen:7777",
			Fingerprint: "sha256:abcd",
		},
		"local": remote.ServerEntry{
			URL:      "http://localhost:8888",
			Insecure: true,
		},
	}
	data, err := EncodeServers(saved)
	if err != nil {
		t.Fatalf("EncodeServers: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Errorf("EncodeServers output does not end in a newline: %q", data)
	}

	loaded, err := remote.ParseServers(data)
	if err != nil {
		t.Fatalf("ParseServers: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("len(loaded) = %d, want 2", len(loaded))
	}
	if loaded["zen"] != saved["zen"] {
		t.Fatalf("zen entry = %+v, want %+v", loaded["zen"], saved["zen"])
	}
	if loaded["local"] != saved["local"] {
		t.Fatalf("local entry = %+v, want %+v", loaded["local"], saved["local"])
	}
}

func TestEnrollLine(t *testing.T) {
	kp, err := remote.Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	line := EnrollLine(kp)
	if !strings.HasPrefix(line, "ed25519 ") {
		t.Fatalf("EnrollLine = %q, want an ed25519 line", line)
	}
	if comment := PublicComment(); comment != "" {
		if !strings.HasSuffix(line, " "+comment) {
			t.Errorf("EnrollLine = %q, want suffix %q", line, " "+comment)
		}
	}

	pub, err := remote.ParsePublic(line)
	if err != nil {
		t.Fatalf("ParsePublic(EnrollLine): %v", err)
	}
	if remote.IDOf(pub) != remote.IDOf(kp.Public) {
		t.Errorf("enrolment line names a different key")
	}
}
