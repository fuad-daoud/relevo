package db

// EngineStatus is what `relevo doctor` needs to report the engine this binary
// opens databases with: the driver's name, the library it opens through, and the
// library's own error when one stops an open.
type EngineState struct {
	// Name is the engine this build opens databases with: "turso" or "sqlite".
	Name string
	// Library is the extracted library's path; empty when the engine needs
	// none, or when none is extracted yet.
	Library string
	// CacheDir is the directory an open extracts the library into, the
	// `<root>/turso-go` a mismatch's fix names; empty for an engine with no
	// library.
	CacheDir string
	// Missing is true when the engine needs a library and none is under
	// CacheDir yet -- the daemon has not opened a database under this root.
	Missing bool
	// Err is the library loader's error: a hash mismatch or a corrupt cached
	// copy that stops the engine from opening.
	Err error
}

// EngineStatus reports the engine's library state under root, where a direct
// open extracts and loads it. It never retries a load and never removes a
// cached copy: an error is a fact to report, not one to heal.
func EngineStatus(root string) EngineState {
	return engineStatus(root)
}
