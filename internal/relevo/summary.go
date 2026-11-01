package relevo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/view"
)

// reportPathFor is where a closing round's report is recorded: a reader's
// artifact-directory output file, a writer's flat NNN-report.md.
func reportPathFor(rt Runtime, b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return rt.Store.OutputPath(b.Name, b.Round, b.WriterRole(), readerOutputLabel(rt, b))
	}
	return rt.Store.ReportPath(b.Name, b.Round)
}

// writeReaderSummary records a reader round's report. The runner's own output
// file (<label>.md) is what the close keeps when it is a regular file with
// content in it, left verbatim without reading the stream; only a missing or
// empty file falls back to the stream -- the last message that carried a
// relevo block, kept whole so the close can parse its tail and strip it
// afterwards, else the stream's last assistant text -- and the fallback is
// reported so the caller can mark the round. Either is rendered with the
// harness kind of the last segment that ran, which a mid-round switch can
// change. A runner that wrote only summary.md does not keep it as the report:
// the old file stays an extra artifact. A writer is untouched: it writes its
// own report.
//
// A chain's reviewer and security members keep a file that has content in it
// too: their verdict comes from the final message's block, and a block inside
// the kept file is stripped in place when the final message carries its own. A
// file that is missing or empty is saved from the block-carrying message,
// stripped of its block, so a recap after the block cannot become the whole
// review.
//
// The path a close should record is returned either way, so a caller can stat
// what it is about to queue; fellBack reports whether the stream was saved
// instead of the file the runner wrote. An error is the write's own; the callers
// log it and close without a report rather than failing the round, which is why
// the path is returned with it.
func writeReaderSummary(rt Runtime, b store.Binding) (string, bool, error) {
	path := reportPathFor(rt, b)
	if b.Shape != store.ShapeReader {
		return path, false, nil
	}

	chainReader := chainReaderPart(rt, b.Name)
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	text := view.ReaderOutputText(view.LastStreamKind(b), stream, chainReader)

	// A regular file at the output path with content in it is the runner's own
	// report and is kept. Anything else there -- a symlink a runner planted, a
	// directory, a fifo -- must not be followed: it is refused and nothing is
	// created, which is a refusal and not a fallback.
	present, empty := false, false
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return path, false, fmt.Errorf("reader output %s is not a regular file", path)
		}
		present = true
		if empty, err = view.ReaderOutputEmpty(path); err != nil {
			return path, false, err
		}
		if !empty {
			if chainReader {
				return path, false, stripKeptChainBlock(rt.Store, b, path, stream)
			}
			return path, false, nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return path, false, err
	}
	if text == "" {
		return path, false, nil
	}
	// The artifact directory is created through the output's os.Root: a symlink
	// (even one pointing at a real directory) or any other non-directory is
	// refused, so the create never descends one.
	root, rel, err := readerOutputRoot(rt.Store, b, path)
	if err != nil {
		return path, false, err
	}
	defer func() { _ = root.Close() }()
	if present {
		return path, true, replaceReaderOutput(root, rel, []byte(text+"\n"))
	}
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return path, false, err
		}
	}
	return path, true, writeReaderOutput(root, rel, text)
}

// stripKeptChainBlock removes a fenced relevo block from a chain reader's kept
// findings file, but only when the final stream message carries its own block:
// without one the file's block is the only verdict. A file the strip would
// leave empty is also left as is.
func stripKeptChainBlock(st *store.Store, b store.Binding, path string, stream []byte) error {
	if !strings.Contains(transcript.FinalText(view.LastStreamKind(b), stream), "```relevo") {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	stripped := reporttail.StripTail(data)
	if bytes.Equal(stripped, data) || len(bytes.TrimSpace(stripped)) == 0 {
		return nil
	}
	root, rel, err := readerOutputRoot(st, b, path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return replaceReaderOutput(root, rel, stripped)
}

// readerOutputRoot opens the os.Root a reader's output path lives under: the
// binding's out/ root for the new layout, the binding directory's own root
// for a legacy artifact path. out/ is created first when it is the home; the
// caller closes the root.
func readerOutputRoot(st *store.Store, b store.Binding, path string) (*os.Root, string, error) {
	if rel, ok := store.RelWithin(st.OutDir(b.Name), path); ok {
		if err := st.EnsureOutDir(b.Name); err != nil {
			return nil, "", err
		}
		root, err := st.OutRoot(b.Name)
		if err != nil {
			return nil, "", err
		}
		return root, rel, nil
	}
	rel, ok := store.RelWithin(st.Dir(b.Name), path)
	if !ok {
		return nil, "", fmt.Errorf("reader output %s is outside the binding", path)
	}
	root, err := os.OpenRoot(st.Dir(b.Name))
	if err != nil {
		return nil, "", err
	}
	return root, rel, nil
}

// readerHasReport reports whether a reader round already has something to
// close with: an output file the runner wrote itself that has content in it,
// or text the close would take from the stream. An empty file is neither --
// the close falls back to the stream for it -- so it does not hold a round
// open. A round with neither is the noreport case the marker hold keeps open
// inside the grace, so this reads the two sources the close writes from.
func readerHasReport(rt Runtime, b store.Binding) bool {
	if present, _ := view.ReaderOutputPresent(reportPathFor(rt, b)); present {
		return true
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	return view.ReaderOutputText(view.LastStreamKind(b), stream, chainReaderPart(rt, b.Name)) != ""
}

// chainReaderPart reports whether name is the reviewer or the security member
// of a chain: the readers whose final message must carry the chain's block, so
// their saved artifact is relevo's to write from the block-carrying message.
func chainReaderPart(rt Runtime, name string) bool {
	c, err := rt.Store.ChainByMember(name)
	if err != nil {
		return false
	}
	switch chain.PartOf(c, name) {
	case chain.MemberReviewer, chain.MemberSecurity:
		return true
	}
	return false
}

// writeReaderOutput creates name inside root and writes text with a trailing
// newline. O_EXCL and O_NOFOLLOW are the race backstop behind the Lstat
// refusal in writeReaderSummary, so a link swapped in after the check cannot
// be followed either.
func writeReaderOutput(root *os.Root, name, text string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|housekeep.NoFollow, 0o644)
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
// than followed or recreated. The Lstat is the refusal and O_NOFOLLOW the race
// backstop behind it; the check also keeps a fifo open from blocking. Without
// O_CREATE the mode is inert, so an existing file's mode and owner are left as
// they were.
func replaceReaderOutput(root *os.Root, name string, data []byte) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("reader output %s is not a regular file", filepath.Join(root.Name(), name))
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC|housekeep.NoFollow, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// removeReaderScratch takes a reader round's throwaway worktree away. Every
// failure is logged and swallowed: cleanup never fails the close, the stop,
// the done or the unbind it runs from. A writer has no scratch, and a Runtime
// with no Git has none to remove.
func removeReaderScratch(ctx context.Context, rt Runtime, b store.Binding, round int) {
	if b.Shape != store.ShapeReader || rt.Git == nil {
		return
	}
	if err := housekeep.RemoveScratch(ctx, HousekeepDeps(rt), b, round); err != nil {
		slog.Warn("scratch worktree not removed", "binding", b.Name, "round", round, "err", err)
	}
}
