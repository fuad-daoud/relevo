// Package storetest builds stores whose database closes when the test ends. A
// store opens its database on the first write and keeps it for the store's
// life, so a test that drops its store without Close holds the engine's page
// cache and file handles until the test binary exits.
package storetest

import (
	"testing"

	"github.com/fuad-daoud/relevo/internal/store"
)

// New returns a store over a fresh temp directory, closed by t's cleanup.
func New(t testing.TB) *store.Store {
	t.Helper()
	return At(t, t.TempDir())
}

// At returns a store over root, closed by t's cleanup. The close is
// registered after any cleanup t already holds, so it runs before them --
// before t.TempDir removes the directory it was called for.
func At(t testing.TB, root string) *store.Store {
	t.Helper()
	st := store.New(root)
	t.Cleanup(func() { _ = st.Close() })
	return st
}
