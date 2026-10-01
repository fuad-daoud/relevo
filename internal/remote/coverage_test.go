package remote

import (
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseFileRange pins the honoured range: both headers present and
// non-negative ints, or the zero FileRange.
func TestParseFileRange(t *testing.T) {
	cases := []struct {
		name string
		from string
		size string
		want FileRange
	}{
		{name: "both present", from: "10", size: "20", want: FileRange{Honored: true, From: 10, Size: 20}},
		{name: "from missing", size: "20"},
		{name: "size missing", from: "10"},
		{name: "both missing"},
		{name: "from not a number", from: "x", size: "20"},
		{name: "size not a number", from: "10", size: "y"},
		{name: "negative from", from: "-1", size: "20"},
		{name: "negative size", from: "10", size: "-1"},
		{name: "zero", from: "0", size: "0", want: FileRange{Honored: true, From: 0, Size: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.from != "" {
				h.Set(HeaderFileFrom, tc.from)
			}
			if tc.size != "" {
				h.Set(HeaderFileSize, tc.size)
			}
			if got := ParseFileRange(h); got != tc.want {
				t.Errorf("ParseFileRange = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestIsLegacyPrivatePEM pins the legacy-label detection: only a PEM block
// carrying the pre-rename private-key type reports true.
func TestIsLegacyPrivatePEM(t *testing.T) {
	legacy := pem.EncodeToMemory(&pem.Block{Type: legacyPEMTypePrivate, Bytes: []byte("key")})
	current := pem.EncodeToMemory(&pem.Block{Type: pemTypePrivate, Bytes: []byte("key")})

	if !IsLegacyPrivatePEM(legacy) {
		t.Error("IsLegacyPrivatePEM(legacy label) = false, want true")
	}
	if IsLegacyPrivatePEM(current) {
		t.Error("IsLegacyPrivatePEM(current label) = true, want false")
	}
	if IsLegacyPrivatePEM([]byte("not a pem block")) {
		t.Error("IsLegacyPrivatePEM(garbage) = true, want false")
	}
}

// TestOpenBundleRegularRefusesNonRegular pins the refusal half of
// openBundleRegular: a path that is not a regular file is refused, as is a
// path that cannot be opened at all.
func TestOpenBundleRegularRefusesNonRegular(t *testing.T) {
	if f, err := openBundleRegular(filepath.Join(t.TempDir(), "missing")); err == nil {
		_ = f.Close()
		t.Error("openBundleRegular(missing) = nil error, want a refusal")
	}

	dir := t.TempDir()
	f, err := openBundleRegular(dir)
	if err == nil {
		_ = f.Close()
		t.Fatal("openBundleRegular(directory) = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("openBundleRegular(directory) error = %q, want it to say not a regular file", err)
	}

	regular := filepath.Join(t.TempDir(), "bundle")
	if err := os.WriteFile(regular, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := openBundleRegular(regular)
	if err != nil {
		t.Fatalf("openBundleRegular(regular) = %v, want nil", err)
	}
	_ = got.Close()
}
