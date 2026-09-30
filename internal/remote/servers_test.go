package remote

import (
	"strings"
	"testing"
)

func TestParseServersValidatesEntries(t *testing.T) {
	_, err := ParseServers([]byte(`{"bad":{"url":"http://zen:7777"}}`))
	if err == nil {
		t.Fatal("ParseServers accepted an http entry without insecure")
	}
	if !strings.Contains(err.Error(), "bad") {
		t.Errorf("error %v does not name the entry", err)
	}
}

// TestParseServersRefusesLocal pins the reserved name: `local` is the placement
// sentinel, so a server may never shadow it, and every other name is accepted.
func TestParseServersRefusesLocal(t *testing.T) {
	_, err := ParseServers([]byte(`{"local":{"url":"https://zen:7777","fingerprint":"sha256:1234"}}`))
	if err == nil {
		t.Fatal("ParseServers accepted a server named local")
	}
	if !strings.Contains(err.Error(), "parse servers file: local:") {
		t.Errorf("error = %q, want it to name the file and the reserved name", err)
	}

	s, err := ParseServers([]byte(`{"zen":{"url":"https://zen:7777","fingerprint":"sha256:1234"}}`))
	if err != nil {
		t.Fatalf("ParseServers(other name): %v", err)
	}
	if _, ok := s["zen"]; !ok {
		t.Errorf("servers = %v, want zen kept", s)
	}
}

func TestValidateEntry(t *testing.T) {
	tests := []struct {
		name    string
		entry   ServerEntry
		wantErr bool
	}{
		{"valid https with fingerprint", ServerEntry{URL: "https://zen:7777", Fingerprint: "sha256:1234"}, false},
		{"valid https with ca system", ServerEntry{URL: "https://zen:7777", CA: "system"}, false},
		{"valid http with insecure", ServerEntry{URL: "http://localhost:8080", Insecure: true}, false},
		{"valid https with insecure", ServerEntry{URL: "https://localhost:8080", Insecure: true}, false},
		{"refuse http without insecure", ServerEntry{URL: "http://zen:7777"}, true},
		{"refuse https without pin or ca", ServerEntry{URL: "https://zen:7777"}, true},
		{"refuse invalid url", ServerEntry{URL: "::not-a-url::"}, true},
		{"refuse unsupported ca", ServerEntry{URL: "https://zen:7777", CA: "custom"}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateEntry(tc.entry)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateEntry(%+v) err = %v, wantErr = %v", tc.entry, err, tc.wantErr)
			}
		})
	}
}

// TestAudienceOf pins the audience a client signs for each trust mode: the
// pinned fingerprint in pin mode, the lower-case host in CA and insecure mode.
// The port is always dropped; a mixed-case host is lower-cased.
func TestAudienceOf(t *testing.T) {
	const fp = "sha256:ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	tests := []struct {
		name  string
		entry ServerEntry
		want  string
	}{
		{
			name:  "pinned with an upper-case fingerprint",
			entry: ServerEntry{URL: "https://zen:7777", Fingerprint: fp},
			want:  "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:  "ca system",
			entry: ServerEntry{URL: "https://zen.example.com:7777", CA: "system"},
			want:  "host:zen.example.com",
		},
		{
			name:  "ca system beside a fingerprint wins the host",
			entry: ServerEntry{URL: "https://zen.example.com", CA: "system", Fingerprint: fp},
			want:  "host:zen.example.com",
		},
		{
			name:  "insecure",
			entry: ServerEntry{URL: "http://zen:7777", Insecure: true},
			want:  "host:zen",
		},
		{
			name:  "host with port",
			entry: ServerEntry{URL: "https://zen:7777", CA: "system"},
			want:  "host:zen",
		},
		{
			name:  "mixed-case host",
			entry: ServerEntry{URL: "https://Zen.Example.COM:7777", CA: "system"},
			want:  "host:zen.example.com",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := AudienceOf(tc.entry)
			if err != nil {
				t.Fatalf("AudienceOf(%+v): %v", tc.entry, err)
			}
			if got != tc.want {
				t.Fatalf("AudienceOf(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

// TestAudienceOfErrors pins the two refusals: a URL that does not parse, and
// one that parses with no host, each name no audience to sign for.
func TestAudienceOfErrors(t *testing.T) {
	for _, entry := range []ServerEntry{
		{URL: "::not-a-url::"},
		{URL: "https:///no-host", CA: "system"},
	} {
		if got, err := AudienceOf(entry); err == nil {
			t.Fatalf("AudienceOf(%+v) = %q, want an error", entry, got)
		}
	}
}

// TestHostAudience pins the normalisation -- lower-case, no port, the literal
// host: prefix -- that both sides of the wire share.
func TestHostAudience(t *testing.T) {
	if got, want := HostAudience("Zen.Fuad-Daoud.COM"), "host:zen.fuad-daoud.com"; got != want {
		t.Fatalf("HostAudience = %q, want %q", got, want)
	}
	if got, want := HostAudience("zen"), "host:zen"; got != want {
		t.Fatalf("HostAudience = %q, want %q", got, want)
	}
}
