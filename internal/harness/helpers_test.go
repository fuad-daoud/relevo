package harness

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
)

func testKV(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

type fakeInstallEnv struct {
	lookPaths   map[string]string
	home        string
	homeErr     error
	files       map[string][]byte
	readErr     error
	dirs        []string
	mkdirErr    error
	writes      map[string][]byte
	writeErrFor map[string]error

	manifest    map[string]string
	manifestErr error
	saveErr     error
	saves       int
}

func (e *fakeInstallEnv) LookPath(binary string) (string, error) {
	if p, ok := e.lookPaths[binary]; ok {
		return p, nil
	}
	return "", fmt.Errorf("binary not found: %s", binary)
}

func (e *fakeInstallEnv) HomePath(rel string) (string, error) {
	if e.homeErr != nil {
		return "", e.homeErr
	}
	return filepath.Join(e.home, rel), nil
}

func (e *fakeInstallEnv) ReadFile(path string) ([]byte, error) {
	if e.readErr != nil {
		return nil, e.readErr
	}
	if b, ok := e.files[path]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

func (e *fakeInstallEnv) MkdirAll(dir string) error {
	if e.mkdirErr != nil {
		return e.mkdirErr
	}
	e.dirs = append(e.dirs, dir)
	return nil
}

func (e *fakeInstallEnv) WriteFile(path string, data []byte) error {
	if err, ok := e.writeErrFor[path]; ok {
		return err
	}
	e.writes[path] = data
	e.files[path] = data
	return nil
}

func (e *fakeInstallEnv) LoadManifest() (map[string]string, error) {
	if e.manifestErr != nil {
		return nil, e.manifestErr
	}
	if e.manifest == nil {
		e.manifest = make(map[string]string)
	}
	return e.manifest, nil
}

func (e *fakeInstallEnv) SaveManifest(m map[string]string) error {
	e.saves++
	if e.saveErr != nil {
		return e.saveErr
	}
	e.manifest = m
	return nil
}

func freshEnv() *fakeInstallEnv {
	return &fakeInstallEnv{
		home:        "/home/u",
		lookPaths:   make(map[string]string),
		files:       make(map[string][]byte),
		writes:      make(map[string][]byte),
		writeErrFor: make(map[string]error),
		manifest:    make(map[string]string),
	}
}

func contains(argv []string, s string) bool {
	for _, a := range argv {
		if a == s {
			return true
		}
	}
	return false
}

func containsAdjacent(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
