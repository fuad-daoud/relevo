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
