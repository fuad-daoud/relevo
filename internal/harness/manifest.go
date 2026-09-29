package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/db"
)

// manifestKey is the kv row the role manifest lives in.
const manifestKey = "agents-manifest"

// docSHA is the manifest value for one definition: the sha256 of the raw bytes,
// never of the DocEqual-normalised form, so it records what is on disk.
func docSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadManifest loads the role manifest from kv. An absent row is an empty map
// and no error; malformed JSON is an error, and the caller decides what to do
// with it.
func ReadManifest(kv db.KV) (map[string]string, error) {
	raw, ok, err := kv.KVGet(manifestKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]string{}, nil
	}

	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decode role manifest: %w", err)
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, nil
}

// WriteManifest stores the whole manifest in the kv row "agents-manifest".
func WriteManifest(kv db.KV, m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal role manifest: %w", err)
	}
	return kv.KVPut(manifestKey, append(raw, '\n'))
}

// writeFileAtomic writes via a temp file then renames, so no crash can leave a
// truncated file behind.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".relevo-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}
