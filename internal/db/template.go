package db

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// freshTemplate is the migrated database a fresh path is seeded from, or "" to
// seed nothing. SetFreshTemplate owns every write, from a TestMain before
// m.Run or from a test that runs sequentially, so no Open is ever in flight.
var freshTemplate string

// SetFreshTemplate points Open at a migrated database to copy fresh files from,
// or clears the point with "". It exists for tests; relevo never calls it. It
// must not be called while any goroutine may be inside Open.
func SetFreshTemplate(path string) {
	freshTemplate = path
}

// seedFromTemplate copies the template into path when path does not exist yet,
// so a fresh test database inherits the schema without migrating. It is best
// effort and silent: any failure leaves Open to create and migrate the file the
// way it did before.
func seedFromTemplate(path string) {
	tpl := freshTemplate
	if tpl == "" {
		return
	}
	// A path that exists, or that cannot be inspected, is left to Open.
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".relevo-seed-*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := copyBytes(tmp, tpl); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	// Linking is atomic and refuses a path that appeared meanwhile, so a
	// concurrent first open keeps the file it made rather than a torn copy.
	_ = os.Link(tmpName, path)
}

// copyBytes writes every byte of the file at src into dst.
func copyBytes(dst *os.File, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	_, err = io.Copy(dst, in)
	return err
}
