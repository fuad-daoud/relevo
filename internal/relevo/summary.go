package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// reportPathFor is where a closing round's report is recorded: a reader's
// artifact-directory output file, a writer's flat NNN-report.md.
func reportPathFor(rt Runtime, b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return rt.Store.OutputPath(b.Name, b.Round, bindingRole(b), readerOutputLabel(rt, b))
	}
	return rt.Store.ReportPath(b.Name, b.Round)
}

// writeReaderSummary records a reader round's report. A reader's output file
// (<label>.md) is written here when the runner did not write one itself: it is
// taken from the last message that carried a relevo block, kept whole so the
// close can parse its tail and strip it afterwards, or from the stream's last
// assistant text when no message carried a block. Either is rendered with the
// harness kind of the last segment that ran, which a mid-round switch can
// change. A runner that wrote only summary.md does not keep it as the report:
// the old file stays an extra artifact. The caller no longer calls it when no
// deliverable exists; an absent deliverable falls through to the continuation
// or halt path. A writer is untouched: it writes its own report.
//
// A chain's reviewer and security members are the exception: their report is
// the message that carried the block their seed demands (a recap after the
// block would otherwise hide the whole review), and the file is relevo's to
// rewrite with that message, stripped of the block the parse already read.
//
// The path a close should record is returned either way, so a caller can stat
// what it is about to queue. An error is the write's own; the callers log it
// and close without a report rather than failing the round, which is why the
// path is returned with it.
func writeReaderSummary(rt Runtime, b store.Binding) (string, error) {
	path := reportPathFor(rt, b)
	if b.Shape != store.ShapeReader {
		return path, nil
	}

	chainReader := chainReaderPart(rt, b.Name)
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	text := readerOutputText(lastStreamKind(b), stream, chainReader)

	// A regular file at the output path is the runner's own report and is left
	// alone -- except for a chain reader, whose report relevo owns. Anything
	// else there -- a symlink a runner planted, a directory, a fifo -- must not
	// be followed: it is refused and nothing is created.
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return path, fmt.Errorf("reader output %s is not a regular file", path)
		}
		if !chainReader || text == "" {
			return path, nil
		}
		root, rel, err := readerOutputRoot(rt.Store, b, path)
		if err != nil {
			return path, err
		}
		defer func() { _ = root.Close() }()
		return path, replaceReaderOutput(root, rel, []byte(text+"\n"))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return path, err
	}
	if text == "" {
		return path, nil
	}
	// The artifact directory is created through the output's os.Root: a symlink
	// (even one pointing at a real directory) or any other non-directory is
	// refused, so the create never descends one.
	root, rel, err := readerOutputRoot(rt.Store, b, path)
	if err != nil {
		return path, err
	}
	defer func() { _ = root.Close() }()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return path, err
		}
	}
	return path, writeReaderOutput(root, rel, text)
}

// readerOutputRoot opens the os.Root a reader's output path lives under: the
// binding's out/ root for the new layout, the binding directory's own root for
// a legacy artifact path. out/ is created first when it is the home, and the
// name the root takes is returned; the caller closes the root.
func readerOutputRoot(st *store.Store, b store.Binding, path string) (*os.Root, string, error) {
	if rel, ok := relWithin(st.OutDir(b.Name), path); ok {
		if err := st.EnsureOutDir(b.Name); err != nil {
			return nil, "", err
		}
		root, err := st.OutRoot(b.Name)
		if err != nil {
			return nil, "", err
		}
		return root, rel, nil
	}
	rel, ok := relWithin(st.Dir(b.Name), path)
	if !ok {
		return nil, "", fmt.Errorf("reader output %s is outside the binding", path)
	}
	root, err := os.OpenRoot(st.Dir(b.Name))
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

// relWithin reports path's name relative to dir, and whether path lies in dir.
func relWithin(dir, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// readerOutputText is the message a reader's output file is written from. A
// chain reviewer or security member's block-carrying message is returned with
// its block stripped: relevo rewrites the file the mastermind reads, and the
// parse already read the block. Any other reader prefers the last message that
// carried a block, block kept so the close can parse and strip it, falling back
// to the stream's last assistant text when no message carried one.
func readerOutputText(kind string, stream []byte, chainReader bool) string {
	withBlock := transcript.LastWithBlock(kind, stream)
	if chainReader {
		if withBlock != "" {
			return string(reporttail.StripTail([]byte(withBlock)))
		}
		return transcript.FinalText(kind, stream)
	}
	if withBlock != "" {
		return withBlock
	}
	return transcript.FinalText(kind, stream)
}

// readerHasReport reports whether a reader round already has something to
// close with: an output file the runner wrote itself, or text the close would
// take from the stream. A round with neither is the noreport case the marker
// hold keeps open inside the grace, so this reads the two sources the close
// writes from and no others.
func readerHasReport(rt Runtime, b store.Binding) bool {
	if fi, err := os.Lstat(reportPathFor(rt, b)); err == nil && fi.Mode().IsRegular() {
		return true
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	return readerOutputText(lastStreamKind(b), stream, chainReaderPart(rt, b.Name)) != ""
}

// chainReaderPart reports whether name is the reviewer or the security member
// of a chain: the readers whose final message must carry the chain's block, so
// their saved artifact is relevo's to write from the block-carrying message.
func chainReaderPart(rt Runtime, name string) bool {
	c, err := rt.Store.ChainByMember(name)
	if err != nil {
		return false
	}
	switch chainPartOf(c, name) {
	case chain.MemberReviewer, chain.MemberSecurity:
		return true
	}
	return false
}

// writeReaderOutput creates name inside root and writes text with a trailing
// newline. The Lstat checks in writeReaderSummary are the refusal; O_EXCL and
// O_NOFOLLOW are the race backstop behind them, so a link swapped in after the
// check cannot be followed either.
func writeReaderOutput(root *os.Root, name, text string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// replaceReaderOutput truncates an existing regular file in place inside root
// and writes data over it. A close only reaches here for an output it just
// read, so a missing path, a symlink, a directory or a fifo is refused rather
// than followed or recreated, and nothing is created. The Lstat is the refusal
// and O_NOFOLLOW the race backstop behind it; the check also keeps a fifo open
// from blocking. Without O_CREATE the mode is inert, so an existing file's mode
// and owner are left as they were.
func replaceReaderOutput(root *os.Root, name string, data []byte) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("reader output %s is not a regular file", filepath.Join(root.Name(), name))
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC|oNoFollow, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// lastStreamKind is the harness kind of the round's last stream segment: the
// kind of the process that last wrote, which a mid-round switch changes. The
// endpoint's own Kind is the fallback for a round that recorded no segment.
func lastStreamKind(b store.Binding) string {
	if n := len(b.Builder.StreamSegments); n > 0 {
		return b.Builder.StreamSegments[n-1].Kind
	}
	return b.Builder.Kind
}

// removeReaderScratch takes a reader round's throwaway worktree away. Every
// failure is logged and swallowed: cleanup never fails the close, the stop, the
// done or the unbind it runs from. A writer has no scratch, and a Runtime with
// no Git has none to remove.
func removeReaderScratch(ctx context.Context, rt Runtime, b store.Binding, round int) {
	if b.Shape != store.ShapeReader || rt.Git == nil {
		return
	}
	if err := RemoveScratch(ctx, rt, b, round); err != nil {
		slog.Warn("scratch worktree not removed", "binding", b.Name, "round", round, "err", err)
	}
}
