package board

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"strings"
	"testing"
)

// TestAssetManifestMatchesEmbeddedFiles checks the integrity manifest both
// ways: every embedded file is listed with a matching sha256, and every listed
// file exists. It is the guard that the committed assets and the manifest
// cannot drift apart.
func TestAssetManifestMatchesEmbeddedFiles(t *testing.T) {
	assets := Assets()

	raw, err := fs.ReadFile(assets, "assets.sha256")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	listed := make(map[string]string)
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		fields := strings.SplitN(line, "  ", 2)
		if len(fields) != 2 {
			t.Fatalf("manifest line %q is not \"<sha256>  <path>\"", line)
		}
		listed[fields[1]] = fields[0]
	}
	if len(listed) == 0 {
		t.Fatal("the manifest lists no files")
	}

	seen := make(map[string]bool)
	err = fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || path == "assets.sha256" {
			return nil
		}
		want, ok := listed[path]
		if !ok {
			t.Errorf("embedded file %q is not in the manifest", path)
			return nil
		}
		data, readErr := fs.ReadFile(assets, path)
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 = %s, manifest says %s", path, got, want)
		}
		seen[path] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk assets: %v", err)
	}

	for path := range listed {
		if !seen[path] {
			t.Errorf("manifest lists %q, which is not embedded", path)
		}
	}
}
