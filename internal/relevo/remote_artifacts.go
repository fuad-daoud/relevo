package relevo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

// is404 reports whether err is the server's answer that a round file does not
// exist, which every read in the catch-up takes as "nothing to fetch".
func is404(err error) bool {
	var httpErr *client.HTTPError
	return errors.As(err, &httpErr) && httpErr.Status == 404
}

// downloadTemp writes r into a new temp file beside final and returns its
// path, leaving the rename into place to the apply half. A discarded fetch
// removes it, so the final name never holds a file the apply did not accept.
func downloadTemp(final string, r io.ReadCloser) (string, error) {
	defer r.Close()
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(final)+".fetch.*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	return tmpName, nil
}

// validArtifactRel reports whether a server-sent rel may be written under the
// client's artifact directory: non-empty, relative, no backslash, and never a
// ".." element. A rel that fails is refused, and nothing is written.
func validArtifactRel(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return false
	}
	return !containsDotDotRel(rel)
}

// firstBadArtifactRel returns the index of the first rel the client refuses,
// or -1 when every rel is acceptable. It answers with an index because the
// empty rel is itself a refusal, so "" cannot mean "none".
func firstBadArtifactRel(files []remote.ArtifactFile) int {
	for i, f := range files {
		if !validArtifactRel(f.Rel) {
			return i
		}
	}
	return -1
}

// artifactListTotal sums the listing's sizes.
func artifactListTotal(files []remote.ArtifactFile) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}

// hasArtifactRel reports whether the listing holds rel.
func hasArtifactRel(files []remote.ArtifactFile, rel string) bool {
	for _, f := range files {
		if f.Rel == rel {
			return true
		}
	}
	return false
}

// fetchedArtifact is one downloaded artifact waiting for the apply half: the
// path it belongs at, and the temp file beside it.
type fetchedArtifact struct {
	Final string
	Temp  string
}

// fetchCatchUpArtifacts downloads a closed reader round's artifact files in
// place of the writer's report and diff. The server strips the relevo block
// from the reader's output at close, so the view's ReportOutcome -- never a
// parse of the body -- is the round's status.
//
// A rel the listing should never carry, a listing over the artifact cap, and a
// failed download each stop the fetch before anything is written: the bad rel
// and the failed download abort, and the cap is carried for the apply half to
// halt on.
func fetchCatchUpArtifacts(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	list, err := rt.Remote.RoundArtifacts(ctx, server, name, n)
	if err != nil {
		slog.Warn("list artifacts failed", "server", server, "name", name, "round", n, "err", err)
		cf.Abort = true
		cf.release()
		return false
	}

	if i := firstBadArtifactRel(list.Files); i >= 0 {
		slog.Warn("artifact rel refused", "server", server, "name", name, "round", n, "rel", list.Files[i].Rel)
		cf.Abort = true
		cf.release()
		return false
	}
	if cap := rt.Policy.ArtifactMaxBytes(); artifactListTotal(list.Files) > cap {
		slog.Warn("remote artifacts over the cap; not fetched", "server", server, "name", name, "round", n,
			"bytes", artifactListTotal(list.Files))
		cf.ArtifactCap = artifactCapReason(artifactListTotal(list.Files), cap)
		cf.release()
		return false
	}
	if !fetchArtifactFiles(ctx, rt, b, view, list, cf) {
		return false
	}
	if !hasArtifactRel(list.Files, list.Output) {
		cf.ReportMissing = true
	}
	return true
}

// fetchArtifactFiles downloads each listed file into a temp beside its final
// path: the output at the path reportPathFor records, every other rel under
// the binding's artifact directory.
func fetchArtifactFiles(ctx context.Context, rt Runtime, b store.Binding, view remote.BindingView, list remote.ArtifactList, cf *catchUpFetch) bool {
	server, name, n := b.Builder.Server, b.Name, view.ClosedRound
	dir := rt.Store.ArtifactDir(name, n, bindingRole(b))
	for _, f := range list.Files {
		final := filepath.Join(dir, filepath.FromSlash(f.Rel))
		if f.Rel == list.Output {
			final = reportPathFor(rt, b)
		}
		rc, err := rt.Remote.RoundArtifact(ctx, server, name, n, f.Rel)
		if err != nil {
			slog.Warn("fetch artifact failed", "server", server, "name", name, "round", n, "rel", f.Rel, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		temp, err := downloadTemp(final, rc)
		if err != nil {
			slog.Warn("write artifact failed", "path", final, "err", err)
			cf.Abort = true
			cf.release()
			return false
		}
		cf.Artifacts = append(cf.Artifacts, fetchedArtifact{Final: final, Temp: temp})
		if f.Rel == list.Output {
			cf.ReportTemp = temp
		}
	}
	return true
}

// applyCatchUpArtifacts renames a reader's downloaded artifact temps into
// place. Every temp was written beside its final path, so the rename is the
// same atomic install the writer's report rename is.
func applyCatchUpArtifacts(cf *catchUpFetch) bool {
	for _, a := range cf.Artifacts {
		if err := os.Rename(a.Temp, a.Final); err != nil {
			slog.Warn("write artifact failed", "path", a.Final, "err", err)
			return false
		}
	}
	return true
}
