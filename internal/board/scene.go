package board

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrConflict reports an If-Match that does not match the bytes on disk: the
// caller's view of the scene is stale.
var ErrConflict = errors.New("conflict")

// ErrInvalid reports a body that is not an Excalidraw scene or not an svg.
var ErrInvalid = errors.New("invalid")

// Etag is the content address of a scene: the sha256 of its bytes on disk.
func Etag(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Load reads the scene at path. A missing file is not an error: it is a new
// scene with an empty etag, and nothing is created until the first save.
func Load(path string) (data []byte, etag string, isNew bool, err error) {
	data, err = os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", true, nil
		}
		return nil, "", false, err
	}
	return data, Etag(data), false, nil
}

// ValidateScene reports whether body is an Excalidraw scene: a JSON object
// whose type is "excalidraw" and whose elements is an array.
func ValidateScene(body []byte) error {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("%w: scene is not a JSON object: %w", ErrInvalid, err)
	}
	var typ string
	if raw, ok := doc["type"]; !ok || json.Unmarshal(raw, &typ) != nil || typ != "excalidraw" {
		return fmt.Errorf("%w: scene type is not %q", ErrInvalid, "excalidraw")
	}
	raw, ok := doc["elements"]
	if !ok {
		return fmt.Errorf("%w: scene has no elements", ErrInvalid)
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil || elems == nil {
		return fmt.Errorf("%w: scene elements is not an array", ErrInvalid)
	}
	return nil
}

// ValidateSVG reports whether body is an svg document: it starts with "<svg"
// after an optional XML prolog and leading whitespace.
func ValidateSVG(body []byte) error {
	s := body
	if bytes.HasPrefix(s, []byte("<?xml")) {
		end := bytes.Index(s, []byte("?>"))
		if end < 0 {
			return fmt.Errorf("%w: unterminated XML prolog", ErrInvalid)
		}
		s = s[end+2:]
	}
	s = bytes.TrimLeft(s, " \t\r\n")
	if !bytes.HasPrefix(s, []byte("<svg")) {
		return fmt.Errorf("%w: svg does not start with <svg", ErrInvalid)
	}
	return nil
}

// renameFile is the rename Save steps through, a seam a test replaces to prove
// a crash before the rename leaves the old file and no temp file.
var renameFile = os.Rename

// Save writes scene to path and its companion svg beside it, atomically. The
// If-Match must equal the etag of the bytes on disk (empty for a new file) or
// the save is refused with ErrConflict. The scene is renamed into place first,
// then the svg; the scene's new etag is returned.
func Save(path string, scene, svg []byte, ifMatch string) (string, error) {
	current := ""
	if old, err := os.ReadFile(path); err == nil {
		current = Etag(old)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if ifMatch != current {
		return "", ErrConflict
	}
	if err := writeAtomic(path, scene); err != nil {
		return "", err
	}
	if err := writeAtomic(svgPath(path), svg); err != nil {
		return "", err
	}
	return Etag(scene), nil
}

// svgPath is the companion svg's path: the scene's path with .svg for
// .excalidraw.
func svgPath(scene string) string {
	return strings.TrimSuffix(scene, sceneExt) + ".svg"
}

// writeAtomic writes data to a temp file in path's directory and renames it
// into place. A failure removes the temp file, so an interrupted save leaves
// the old file and no litter.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".board-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := renameFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
