package relevo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

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
// the old file stays an extra artifact. An empty text writes nothing, so the
// round closes without a report exactly as a writer that wrote none. A writer
// is untouched: it writes its own report.
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
		return path, replaceReaderOutput(path, []byte(text+"\n"))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return path, err
	}
	if text == "" {
		return path, nil
	}
	// The artifact directory is created only when it is absent: a symlink (even
	// one pointing at a real directory) or any other non-directory is refused,
	// so MkdirAll never descends one.
	dir := filepath.Dir(path)
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return path, fmt.Errorf("reader artifact path %s is not a directory", dir)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return path, err
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	return path, writeReaderOutput(path, text)
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

// writeReaderOutput creates path and writes text with a trailing newline. The
// Lstat checks in writeReaderSummary are the refusal; O_EXCL and O_NOFOLLOW are
// the race backstop behind them, so a link swapped in after the check cannot be
// followed either.
func writeReaderOutput(path, text string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|oNoFollow, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text + "\n"); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// replaceReaderOutput truncates an existing regular file in place and writes
// data over it. A close only reaches here for an output it just read, so a
// missing path, a symlink, a directory or a fifo is refused rather than
// followed or recreated, and nothing is created. The Lstat is the refusal and
// O_NOFOLLOW the race backstop behind it; the check also keeps a fifo open from
// blocking. Without O_CREATE the mode is inert, so an existing file's mode and
// owner are left as they were.
func replaceReaderOutput(path string, data []byte) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("reader output %s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|oNoFollow, 0o644)
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
