package board

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sceneJSON = `{"type":"excalidraw","elements":[],"appState":{}}`

func TestLoadMissingIsNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.excalidraw")
	data, etag, isNew, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !isNew || etag != "" || data != nil {
		t.Errorf("Load(missing) = (%q, %q, %v), want (nil, \"\", true)", data, etag, isNew)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("Load created the file: stat error = %v", statErr)
	}
}

func TestSaveWritesCompanionSVG(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "board.excalidraw")
	svg := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>")

	etag, err := Save(scene, []byte(sceneJSON), svg, "")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := Etag([]byte(sceneJSON)); etag != want {
		t.Errorf("etag = %q, want %q", etag, want)
	}
	gotScene, err := os.ReadFile(scene)
	if err != nil || !bytes.Equal(gotScene, []byte(sceneJSON)) {
		t.Fatalf("scene on disk = %q, %v", gotScene, err)
	}
	gotSVG, err := os.ReadFile(filepath.Join(dir, "board.svg"))
	if err != nil || !bytes.Equal(gotSVG, svg) {
		t.Fatalf("companion svg = %q, %v", gotSVG, err)
	}
}

func TestSaveRefusesStaleEtag(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "board.excalidraw")
	if err := os.WriteFile(scene, []byte(sceneJSON), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, err := Save(scene, []byte(`{"type":"excalidraw","elements":[]}`), []byte("<svg/>"), "deadbeef")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Save with a stale If-Match = %v, want ErrConflict", err)
	}
	got, _ := os.ReadFile(scene)
	if !bytes.Equal(got, []byte(sceneJSON)) {
		t.Errorf("a refused save rewrote the file: %q", got)
	}
}

// TestSaveRenameFailureLeavesOldFile: a crash at the rename leaves the old
// scene in place and no temp file behind.
func TestSaveRenameFailureLeavesOldFile(t *testing.T) {
	dir := t.TempDir()
	scene := filepath.Join(dir, "board.excalidraw")
	if err := os.WriteFile(scene, []byte(sceneJSON), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	orig := renameFile
	renameFile = func(string, string) error { return errors.New("crash before rename") }
	t.Cleanup(func() { renameFile = orig })

	_, err := Save(scene, []byte(`{"type":"excalidraw","elements":[{"id":"x"}]}`), []byte("<svg/>"), Etag([]byte(sceneJSON)))
	if err == nil {
		t.Fatal("Save with a failing rename succeeded")
	}
	got, _ := os.ReadFile(scene)
	if !bytes.Equal(got, []byte(sceneJSON)) {
		t.Errorf("old scene = %q, want it intact", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".board-") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
}

func TestValidateScene(t *testing.T) {
	good := []string{
		`{"type":"excalidraw","elements":[]}`,
		`{"type":"excalidraw","elements":[{"id":"a"}],"appState":{}}`,
	}
	for _, body := range good {
		if err := ValidateScene([]byte(body)); err != nil {
			t.Errorf("ValidateScene(%s) = %v, want nil", body, err)
		}
	}
	bad := []string{
		``,
		`[]`,
		`{"type":"other","elements":[]}`,
		`{"type":"excalidraw"}`,
		`{"type":"excalidraw","elements":null}`,
		`{"type":"excalidraw","elements":{}}`,
	}
	for _, body := range bad {
		if err := ValidateScene([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateScene(%s) = %v, want ErrInvalid", body, err)
		}
	}
}

func TestValidateSVG(t *testing.T) {
	good := []string{
		`<svg></svg>`,
		`  <svg xmlns="http://www.w3.org/2000/svg"/>`,
		"<?xml version=\"1.0\"?>\n<svg></svg>",
	}
	for _, body := range good {
		if err := ValidateSVG([]byte(body)); err != nil {
			t.Errorf("ValidateSVG(%q) = %v, want nil", body, err)
		}
	}
	bad := []string{
		``,
		`<html></html>`,
		`{"svg": true}`,
		`<?xml version="1.0"?><html></html>`,
	}
	for _, body := range bad {
		if err := ValidateSVG([]byte(body)); !errors.Is(err, ErrInvalid) {
			t.Errorf("ValidateSVG(%q) = %v, want ErrInvalid", body, err)
		}
	}
}
