package relevo

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ErrNoArtifact reports a rel that names no file RoundArtifacts lists for a
// round. ReadArtifact wraps it, so a caller can tell "no such artifact" from a
// read failure.
var ErrNoArtifact = errors.New("no such artifact")

// RoundArtifacts lists round's artifact files for binding name, live (on disk)
// or sealed (round_file), sorted with output first, then summary.md, then by
// Rel. output is the reader's output rel (e.g. "findings.md"), or "" when the
// caller only needs a total. A live binding is listed through
// Store.RoundFiles, which sees the files still on disk and the live record's
// sealed rows; a binding that is not live is listed from the newest archived
// record's sealed rows, the way Show's archived path reads a flat name.
func RoundArtifacts(rt Runtime, name string, round int, actor, output string) ([]view.ArtifactFile, error) {
	return view.RoundArtifacts(rt.Store, name, round, actor, output)
}

// ReadArtifact returns one artifact's bytes; rel must be a Rel RoundArtifacts
// lists for the same round and actor. A ".." element is never listed and is
// refused, as is any rel no listing holds: ErrNoArtifact either way.
func ReadArtifact(rt Runtime, name string, round int, actor, rel string) ([]byte, error) {
	if rel == "" || remote.ContainsDotDotRel(rel) {
		return nil, fmt.Errorf("artifact %q: %w", rel, ErrNoArtifact)
	}
	files, err := RoundArtifacts(rt, name, round, actor, "")
	if err != nil {
		return nil, err
	}
	listed := false
	for _, f := range files {
		if f.Rel == rel {
			listed = true
			break
		}
	}
	if !listed {
		return nil, fmt.Errorf("artifact %q: %w", rel, ErrNoArtifact)
	}
	path := filepath.Join(rt.Store.ArtifactDir(name, round, actor), filepath.FromSlash(rel))
	return rt.Store.ReadFile(path)
}

// OutputFile is the rel a reader round's final message is saved under: the
// actor's resolved output label plus ".md". Exported for the ui's artifacts
// caption.
func OutputFile(rt Runtime, b store.Binding) string {
	return readerOutputLabel(rt, b) + ".md"
}

// readOutput returns the round's output bytes: the output rel when the listing
// holds it, else the pre-rename summary.md, else nil. Read through the artifact
// helper, so a live, sealed or archived round all answer.
func readOutput(rt Runtime, name string, round int, actor, output string) ([]byte, error) {
	files, err := RoundArtifacts(rt, name, round, actor, output)
	if err != nil {
		return nil, err
	}
	has := func(rel string) bool {
		for _, f := range files {
			if f.Rel == rel {
				return true
			}
		}
		return false
	}
	for _, rel := range []string{output, "summary.md"} {
		if rel != "" && has(rel) {
			return ReadArtifact(rt, name, round, actor, rel)
		}
	}
	return nil, nil
}
