package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/store"
)

// Env abstracts external system facts for testability.
type Env interface {
	DaemonRunning(ctx context.Context) (bool, error)
	// DaemonInfo reads the daemon's own record; a missing one is
	// (zero, false, nil) and means a daemon that predates it.
	DaemonInfo() (store.DaemonInfo, bool, error)
	LookPath(binary string) (string, error)
	HomePath(rel string) (string, error)
	Stat(path string) error
	// ReadFile reads a file whose existence Stat has already established; a
	// read error is never itself a check failure.
	ReadFile(path string) ([]byte, error)
	// LoadManifest reads the role manifest: a home-relative definition path
	// to the sha256 relevo last wrote there. Missing is an empty map.
	LoadManifest() (map[string]string, error)
	// BinaryVersion runs `<path> --version` and returns the first field of
	// its trimmed stdout, so a harness with a version floor can be held to it.
	BinaryVersion(ctx context.Context, path string) (string, error)
	// Probe tests whether dir is writable by creating and removing a temp file.
	Probe(dir string) error
	// Command runs bin with args and returns its stdout, for a fact no other
	// Env method exposes (opencode's session count via sqlite3).
	Command(ctx context.Context, bin string, args ...string) ([]byte, error)
	// ReleaseState returns the running version, the cached latest (ok false
	// with no usable cache) and the install kind.
	ReleaseState() (running string, latest string, ok bool, kind release.Kind)
}

type realEnv struct {
	store *store.Store

	// self is relevo's own build fact; only package main can see
	// buildVersion(). The zero value reads as release.KindUnknown.
	self release.Inputs
}

// NewEnv returns a real Env backed by the given store. self is optional: a
// caller that never reads ReleaseState need not gather it.
func NewEnv(st *store.Store, self ...release.Inputs) Env {
	env := &realEnv{store: st}
	if len(self) > 0 {
		env.self = self[0]
	}
	return env
}

func (e *realEnv) DaemonRunning(ctx context.Context) (bool, error) {
	if e.store == nil {
		return false, nil
	}
	return e.store.DaemonRunning()
}

// DaemonInfo reads daemon.json; a nil store is "no record", as a missing
// file is.
func (e *realEnv) DaemonInfo() (store.DaemonInfo, bool, error) {
	if e.store == nil {
		return store.DaemonInfo{}, false, nil
	}
	return e.store.ReadDaemonInfo()
}

func (e *realEnv) LookPath(binary string) (string, error) {
	return exec.LookPath(binary)
}

func (e *realEnv) HomePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

func (e *realEnv) Stat(path string) error {
	_, err := os.Stat(path)
	return err
}

func (e *realEnv) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// LoadManifest reads the role manifest from the machine database's
// "agents-manifest" kv row. A root with no database reads as an empty
// manifest.
func (e *realEnv) LoadManifest() (map[string]string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	d, err := store.New(root).DBIfExists()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return map[string]string{}, nil
	}
	return harness.ReadManifest(d.LocalOrSelf())
}

func (e *realEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	return versionField(string(out))
}

// versionField picks the first whitespace field that, after an optional
// leading "v", starts with a digit; the first field when none does.
func versionField(out string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return "", errors.New("empty version output")
	}
	for _, f := range fields {
		v := strings.TrimPrefix(f, "v")
		if v != "" && v[0] >= '0' && v[0] <= '9' {
			return f, nil
		}
	}
	return fields[0], nil
}

func (e *realEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// ReleaseState reads the daemon's cached answer and classifies this install.
// A root or cache it cannot read is not an error: it is "not checked", which
// is what every unrefreshed, offline or unclassifiable install reads as.
func (e *realEnv) ReleaseState() (string, string, bool, release.Kind) {
	kind := release.Detect(e.self)

	root, err := store.DefaultRoot()
	if err != nil {
		return e.self.Version, "", false, kind
	}
	d, err := store.New(root).DBIfExists()
	if err != nil || d == nil {
		return e.self.Version, "", false, kind
	}
	cached, ok, err := release.Load(d.LocalOrSelf())
	if err != nil {
		return e.self.Version, "", false, kind
	}
	return e.self.Version, cached.Latest, ok, kind
}

func (e *realEnv) Probe(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "doctor-probe-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}
