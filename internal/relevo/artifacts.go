package relevo

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrNoArtifact reports a rel that names no file RoundArtifacts lists for a
// round. ReadArtifact wraps it, so a caller can tell "no such artifact" from a
// read failure.
var ErrNoArtifact = errors.New("no such artifact")

// ArtifactFile is one file in a round's artifact directory.
type ArtifactFile struct {
	// Rel is the path inside NNN-<actor>/, slash-separated, e.g.
	// "summary.md" or "site/index.html".
	Rel   string    `json:"rel"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

// RoundArtifacts lists round's artifact files for binding name, live (on disk)
// or sealed (round_file), sorted with output first, then summary.md, then by
// Rel. output is the reader's output rel (e.g. "findings.md"), or "" when the
// caller only needs a total. A live binding is listed through
// Store.RoundFiles, which sees the files still on disk and the live record's
// sealed rows; a binding that is not live is listed from the newest archived
// record's sealed rows, the way Show's archived path reads a flat name.
func RoundArtifacts(rt Runtime, name string, round int, actor, output string) ([]ArtifactFile, error) {
	return roundArtifacts(rt.Store, name, round, actor, output)
}

// roundArtifacts is RoundArtifacts without the Runtime, so a caller that
// already holds a *store.Store (the daemon's seal pass) can list the same
// files.
func roundArtifacts(st *store.Store, name string, round int, actor, output string) ([]ArtifactFile, error) {
	rels, err := artifactRels(st, name, round, actor)
	if err != nil {
		return nil, err
	}
	out := make([]ArtifactFile, 0, len(rels))
	for _, rel := range rels {
		path := filepath.Join(st.ArtifactDir(name, round, actor), filepath.FromSlash(rel))
		size, mtime, ok, serr := st.StatFile(path)
		if !ok {
			if serr == nil {
				serr = fmt.Errorf("%s: not found", rel)
			}
			return nil, serr
		}
		out = append(out, ArtifactFile{Rel: rel, Size: size, MTime: mtime})
	}
	sortArtifacts(out, output)
	return out, nil
}

// ReadArtifact returns one artifact's bytes; rel must be a Rel RoundArtifacts
// lists for the same round and actor. A ".." element is never listed and is
// refused, as is any rel no listing holds: ErrNoArtifact either way.
func ReadArtifact(rt Runtime, name string, round int, actor, rel string) ([]byte, error) {
	if rel == "" || containsDotDotRel(rel) {
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

// artifactRels lists the Rel names of round's artifact files for actor in
// binding name: the nested "NNN-<actor>/<rel>" names Store.RoundFiles reports
// for a live binding, or the newest archived record's sealed names for one
// that is not live. A binding that is neither is ErrNotFound.
func artifactRels(st *store.Store, name string, round int, actor string) ([]string, error) {
	prefix := fmt.Sprintf("%03d-%s/", round, actor)

	var names []string
	if _, err := st.Load(name); err == nil {
		all, lerr := st.RoundFiles(name)
		if lerr != nil {
			return nil, lerr
		}
		names = all
	} else if errors.Is(err, store.ErrNotFound) {
		archived, aerr := st.ListArchived()
		if aerr != nil {
			return nil, aerr
		}
		recordID := ""
		for i := range archived {
			if archived[i].Binding.Name == name {
				recordID = archived[i].RecordID
			}
		}
		if recordID == "" {
			return nil, fmt.Errorf("binding %q not found (live or archived): %w", name, store.ErrNotFound)
		}
		sealed, lerr := st.ArchivedFiles(recordID)
		if lerr != nil {
			return nil, lerr
		}
		names = sealed
	} else {
		return nil, err
	}

	out := make([]string, 0, len(names))
	for _, n := range names {
		rel, ok := strings.CutPrefix(n, prefix)
		if !ok || rel == "" {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}

// sortArtifacts sorts files with output first, then summary.md, then by Rel.
// output == "" puts summary.md first as before, because a caller that passes
// none only wants a total.
func sortArtifacts(files []ArtifactFile, output string) {
	rank := func(rel string) int {
		switch {
		case output != "" && rel == output:
			return 0
		case rel == "summary.md":
			return 1
		default:
			return 2
		}
	}
	sort.Slice(files, func(i, j int) bool {
		ri, rj := rank(files[i].Rel), rank(files[j].Rel)
		if ri != rj {
			return ri < rj
		}
		return files[i].Rel < files[j].Rel
	})
}

// containsDotDotRel reports whether any element of rel is "..", before any
// cleaning: it is how a rel that escapes the artifact directory is refused.
func containsDotDotRel(rel string) bool {
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool {
		return r == '/' || r == '\\' || r == filepath.Separator
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

// artifactDirSize returns the total size in bytes of round's artifact files
// for b's actor, live or sealed.
func artifactDirSize(st *store.Store, b store.Binding, round int) (int64, error) {
	files, err := roundArtifacts(st, b.Name, round, bindingRole(b), "")
	if err != nil {
		return 0, err
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total, nil
}

// artifactCapExceeded reports whether round's artifact directory of b is over
// artifactMaxBytes, and the directory's total size. A size that cannot be read
// is logged and treated as under the cap: a read failure must never block a
// seal or a close.
func artifactCapExceeded(st *store.Store, b store.Binding, round int, artifactMaxBytes int64) (bool, int64) {
	total, err := artifactDirSize(st, b, round)
	if err != nil {
		slog.Warn("artifact cap: size artifact dir", "binding", b.Name, "round", round, "err", err)
		return false, 0
	}
	return total > artifactMaxBytes, total
}

// artifactCapReason is the NEEDS YOU reason for an artifact directory of total
// bytes over cap: the sizes in MB, and how to raise the cap.
func artifactCapReason(total, cap int64) string {
	return fmt.Sprintf("artifacts over the cap: %s > %d MB; raise policy.artifact_max_mb to seal them",
		formatMB(total), cap/(1<<20))
}

// formatMB renders n bytes as megabytes, one decimal place at most with a
// trailing zero removed: 2097152 -> "2", 2621440 -> "2.5".
func formatMB(n int64) string {
	return trimZeroDecimal(float64(n) / float64(1<<20))
}

// ArtifactLine renders one artifact as the `show --artifacts` line: its size,
// the time it was written as HH:MM, and its rel.
func ArtifactLine(f ArtifactFile) string {
	return fmt.Sprintf("%6s  %s  %s", ArtifactSizeText(f.Size), f.MTime.Format("15:04"), f.Rel)
}

// ArtifactSizeText renders n bytes human-readably: under 1000 as-is, under a
// million as decimal k, otherwise decimal M, one decimal place at most.
func ArtifactSizeText(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return trimZeroDecimal(float64(n)/1_000) + "k"
	default:
		return trimZeroDecimal(float64(n)/1_000_000) + "M"
	}
}

// trimZeroDecimal formats v with one decimal place and drops a trailing ".0".
func trimZeroDecimal(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}
