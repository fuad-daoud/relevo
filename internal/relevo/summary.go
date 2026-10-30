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
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// reportPathFor is where a closing round's report is recorded: a reader's
// artifact-directory output file, a writer's flat NNN-report.md.
func reportPathFor(rt Runtime, b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return rt.Store.OutputPath(b.Name, b.Round, resolve.BindingRole(b), readerOutputLabel(rt, b))
	}
	return rt.Store.ReportPath(b.Name, b.Round)
}

// writeReaderSummary records a reader round's report. The reader writes its own
// output file (<label>.md) and that file is what the close keeps: a regular
// file with content in it is left verbatim and the stream is not read for a
// non-chain reader. Only a file that is missing or empty falls back to the
// stream -- the last message that carried a relevo block, kept whole so the
// close can parse its tail and strip it afterwards, else the stream's last
// assistant text -- and the fallback is reported so the caller can mark the
// round. Either is rendered with the harness kind of the last segment that ran,
// which a mid-round switch can change. A runner that wrote only summary.md does
// not keep it as the report: the old file stays an extra artifact. The caller no
// longer calls it when no deliverable exists; an absent deliverable falls
// through to the continuation or halt path. A writer is untouched: it writes its
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
	text := readerOutputText(lastStreamKind(b), stream, chainReader)

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
		if empty, err = readerOutputEmpty(path); err != nil {
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
// without one the file's block is the only verdict and the file stays as is. A
// file the strip would leave empty is also left as is.
func stripKeptChainBlock(st *store.Store, b store.Binding, path string, stream []byte) error {
	if !strings.Contains(transcript.FinalText(lastStreamKind(b), stream), "```relevo") {
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

// readerOutputEmpty reports whether the runner's output file is empty for the
// close's purposes: zero bytes, or content that is only whitespace once
// trimmed. A file a runner created and then wrote nothing into is not an
// artifact, and treating it as one leaves the round with a blank output and no
// say so. The read error is reported rather than guessed at, so a file that
// cannot be read is never silently overwritten by the fallback.
func readerOutputEmpty(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(data)) == 0, nil
}

// readerFallbackNote is the log-note token a close records when it saved the
// stream because the runner's output file was missing or empty, and the token
// readerFallbackHeader looks for when it marks the same output. Both surfaces
// read this one constant, so the note and the header cannot disagree about
// which output was salvaged.
const readerFallbackNote = "output-fallback"

// readerFallbackHeaderLine is the line `show --output` puts above a salvaged
// output, so the MasterMind reading it learns the file is not the runner's own.
const readerFallbackHeaderLine = "note: the runner left no output file, so this is its last message, saved by the close."

// withFallbackHeader puts the fallback line above a salvaged output, so the
// reader of it is never left believing the runner wrote the file.
func withFallbackHeader(entries []store.LogEntry, round int, text string) string {
	if head := readerFallbackHeader(entries, round); head != "" {
		return head + "\n" + text
	}
	return text
}

// readerFallbackHeader returns readerFallbackHeaderLine when name's round closed
// with the fallback marked, else "". entries is the binding's whole log; the
// search is from the newest entry back, so the newest marking of the round wins.
func readerFallbackHeader(entries []store.LogEntry, round int) string {
	for i := len(entries) - 1; i >= 0; i-- {
		if e := entries[i]; e.Round == round && strings.Contains(e.Note, readerFallbackNote) {
			return readerFallbackHeaderLine
		}
	}
	return ""
}

// readerOutputRoot opens the os.Root a reader's output path lives under: the
// binding's out/ root for the new layout, the binding directory's own root for
// a legacy artifact path. out/ is created first when it is the home, and the
// name the root takes is returned; the caller closes the root.
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
// close with: an output file the runner wrote itself that has content in it, or
// text the close would take from the stream. An empty file is neither -- the
// close falls back to the stream for it -- so it does not hold a round open.
// A round with neither is the noreport case the marker hold keeps open inside
// the grace, so this reads the two sources the close writes from and no others.
func readerHasReport(rt Runtime, b store.Binding) bool {
	if present, _ := readerOutputPresent(reportPathFor(rt, b)); present {
		return true
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	return readerOutputText(lastStreamKind(b), stream, chainReaderPart(rt, b.Name)) != ""
}

// readerOutputPresent reports whether path is a regular file the runner wrote
// with content in it, and whether that file is empty. A missing path, a symlink,
// a directory or a fifo is not present and not empty: whether it is a refusal
// is the close's decision, not this one's, so nothing here follows it.
func readerOutputPresent(path string) (present, empty bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false, false
	}
	if empty, err = readerOutputEmpty(path); err != nil {
		return true, false
	}
	return true, empty
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
