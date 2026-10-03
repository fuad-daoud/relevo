package relevo

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// streamTailWindow is the first, and smallest, backwards window streamTail
// reads: 64 KiB. It doubles until it yields the wanted number of rendered
// lines or covers the whole file. The spike measured up to 271 KB of stream
// for 40 rendered lines, so one fixed 64 KiB window is not enough.
const streamTailWindow = 64 * 1024

// renderStream renders every complete line of a round's stream -- up to the
// last '\n'; a trailing partial line is ignored -- into the bytes
// drainFile+appendLines would have appended to an empty log for the same
// stream: each non-empty Render result, joined with '\n' and terminated with
// one. The reader's renderer is one per pass, of the harness kind of the
// segment that contains each line's absolute offset (segmentKind); a later
// line can never change an earlier one, so the rendering's prefix is stable as
// the stream grows, which serve's files/log?from= offsets and the client
// mirror (#442) depend on.
//
// lineRenderer turns one raw line of a stream -- with the harness kind of the
// segment that holds it -- into the lines a reader or a scan sees. Rendering
// for a reader is a transcript.Renderer through streamRenderer; a limit scan
// uses transcript.LimitLines, which keeps only the lines the harness itself
// wrote.
type lineRenderer func(kind string, line []byte) []string

func renderStream(stream []byte, segs []store.StreamSegment, fallback string) []byte {
	return renderStreamFrom(stream, 0, segs, fallback, streamRenderer())
}

// renderStreamFrom is renderStream with the window's bytes starting at base:
// the caller that reads only a tail renders the window with the lines'
// absolute offsets in the whole stream, so segment kinds stay correct. render
// is the one place the reader and the limit scan differ.
func renderStreamFrom(stream []byte, base int64, segs []store.StreamSegment, fallback string, render lineRenderer) []byte {
	end := bytes.LastIndexByte(stream, '\n')
	if end < 0 {
		return nil
	}
	var out []string
	off := base
	for _, line := range bytes.Split(stream[:end], []byte{'\n'}) {
		out = append(out, render(segmentKind(segs, off, fallback), line)...)
		off += int64(len(line)) + 1
	}
	if len(out) == 0 {
		return nil
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

// renderedLines splits rendered bytes into lines, the same way logTail reads
// a file: no trailing newline, and no lines for an empty rendering.
func renderedLines(rendered []byte) []string {
	s := strings.TrimRight(string(rendered), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// joinTailLines is the last n of lines joined with '\n', or "" when there are
// none or n <= 0.
func joinTailLines(lines []string, n int) string {
	if n <= 0 || len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// streamTail is the last n lines of a round's stream rendered for humans:
// exactly logTail's conventions (no trailing newline, "" for a missing or
// empty file, or n <= 0), but over renderStream's output, whose entries may
// themselves contain newlines.
//
// from is the earliest byte it may read: 0 for the whole file, a later offset
// to ignore bytes another process wrote into the same file. When path exists
// on disk it reads backwards in doubling windows (streamTailWindow first),
// dropping the first, partial line of a window that does not start at from and
// rendering the rest with their absolute offsets. When the file is not on disk
// -- a sealed round -- it reads it whole through read and renders the bytes at
// from or later.
func streamTail(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64) string {
	return tailWith(path, read, segs, fallback, n, from, streamRenderer())
}

// streamRenderer is the reader's lineRenderer: one Renderer for the whole
// pass, so a claude call and its result rendered in the same pass keep the
// span between them. A scan never uses it: scanTail renders with
// transcript.LimitLines, which is stateless.
func streamRenderer() lineRenderer {
	r := transcript.NewRenderer()
	return func(kind string, line []byte) []string { return r.Render(kind, line) }
}

// scanTail is streamTail's sibling for a limit scan: the same bytes, read the
// same way, but each line is rendered with transcript.LimitLines, so only the
// lines the harness itself wrote reach the patterns. This writer's "lines" are
// the harness-authored text of each raw line, and the reset time is parsed from
// the same text, so model text, tool output, a successful result and thinking
// can never gate a provider.
func scanTail(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64) string {
	return tailWith(path, read, segs, fallback, n, from, transcript.LimitLines)
}

// tailWith is streamTail and scanTail's shared reader: the last n lines of a
// round's stream rendered by render, with exactly logTail's conventions (no
// trailing newline, "" for a missing or empty file, or n <= 0), over
// renderStream's output, whose entries may themselves contain newlines.
//
// from is the earliest byte it may read: 0 for the whole file, a later offset
// to ignore bytes another process wrote into the same file. When path exists
// on disk it reads backwards in doubling windows (streamTailWindow first),
// dropping the first, partial line of a window that does not start at from and
// rendering the rest with their absolute offsets. When the file is not on disk
// -- a sealed round -- it reads it whole through read and renders the bytes at
// from or later.
func tailWith(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64, render lineRenderer) string {
	if n <= 0 {
		return ""
	}
	if tail, ok := diskStreamTail(path, segs, fallback, n, from, render); ok {
		return tail
	}
	data, err := read(path)
	if err != nil {
		return ""
	}
	if from > int64(len(data)) {
		return ""
	}
	return joinTailLines(renderedLines(renderStreamFrom(data[from:], from, segs, fallback, render)), n)
}

// diskStreamTail is streamTail's backwards reader. ok is false when the file
// is not on disk (or cannot be read), so the caller falls back to read.
func diskStreamTail(path string, segs []store.StreamSegment, fallback string, n int, from int64, render lineRenderer) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", false
	}
	size := fi.Size()
	if from > size {
		// Nothing at or after from; the file is on disk, so read must not
		// supply bytes from before it.
		return "", true
	}
	for window := int64(streamTailWindow); ; window *= 2 {
		start := size - window
		if start < from {
			start = from
		}
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
			return "", false
		}
		body, base := buf, start
		if start > from {
			i := bytes.IndexByte(body, '\n')
			if i < 0 {
				// No complete line in the window yet: read a bigger one.
				continue
			}
			base = start + int64(i) + 1
			body = body[i+1:]
		}
		lines := renderedLines(renderStreamFrom(body, base, segs, fallback, render))
		if len(lines) >= n || start == from {
			return joinTailLines(lines, n), true
		}
	}
}

// builderTail is the last n lines of a round's builder output: the log when
// the endpoint writes one (LogPath is this round's BuilderLogPath: a legacy
// round, see legacyLog), otherwise the rendered stream's tail, which holds the
// builder's stderr too. It replaces every logTail(b.Builder.LogPath, n) call
// site.
func builderTail(rt Runtime, b store.Binding, n int) string {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n)
	}
	return streamTail(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, 0)
}

// currentBuilderTail is builderTail limited to what the current builder
// process wrote. A mid-round switch appends the new process's bytes to the
// same stream file, so scanning the whole round would charge the outgoing
// builder's lines -- and its provider's quota text -- to the incoming one.
// The legacy-log branch is unchanged: such a round's log is the round's own
// and every decision about it already keeps to that log.
func currentBuilderTail(rt Runtime, b store.Binding, n int) string {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n)
	}
	from := int64(0)
	if b.Builder.StreamRound == b.Round {
		from = b.Builder.StreamStart
	}
	return streamTail(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, from)
}

// currentBuilderScanText is currentBuilderTail's sibling for a limit scan. It
// reads the same bytes -- the current builder process's own output, or a legacy
// round's log -- but renders each stream line with transcript.LimitLines, so
// only the lines the harness itself wrote can gate; the legacy log keeps
// today's rendered-tail scan, because such a round writes its stderr to the log
// and the rendered text is what the patterns were written against.
func currentBuilderScanText(rt Runtime, b store.Binding, n int) string {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n)
	}
	from := int64(0)
	if b.Builder.StreamRound == b.Round {
		from = b.Builder.StreamStart
	}
	return scanTail(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, from)
}

// roundSegments is the segment list of one round: the live endpoint's own
// when it holds that round and has segments, otherwise the round's stored
// NNN-builder-segments.json row, otherwise nil (the caller then renders with
// the fallback kind). A decode error is a warning and nil.
func roundSegments(live store.Endpoint, round int, read func(string) ([]byte, bool, error), segPath string) []store.StreamSegment {
	if live.StreamRound == round && len(live.StreamSegments) > 0 {
		return live.StreamSegments
	}
	data, found, err := read(segPath)
	if err != nil {
		slog.Warn("read stream segments", "path", segPath, "err", err)
		return nil
	}
	if !found {
		return nil
	}
	var segs []store.StreamSegment
	if err := json.Unmarshal(data, &segs); err != nil {
		slog.Warn("decode stream segments", "path", segPath, "err", err)
		return nil
	}
	return segs
}

// RoundTranscript answers "what did the builder write in round N" for every
// reader: the round's NNN-builder.log when one exists (history, and rounds
// from before the log is dropped), otherwise the round's stream rendered per
// segment, otherwise not found. read reports a miss as found=false, err=nil,
// exactly as readFileMissing and ArchivedFile do; a real read error is
// returned as is.
func RoundTranscript(st *store.Store, name string, round int, live store.Endpoint,
	read func(path string) ([]byte, bool, error)) (text []byte, source string, found bool, err error) {
	logPath := st.BuilderLogPath(name, round)
	if data, ok, rerr := read(logPath); rerr != nil {
		return nil, "", false, rerr
	} else if ok {
		return data, filepath.Base(logPath), true, nil
	}
	for _, streamPath := range []string{st.RunnerStreamPath(name, round), st.BuilderStreamPath(name, round)} {
		data, ok, rerr := read(streamPath)
		if rerr != nil {
			return nil, "", false, rerr
		}
		if !ok {
			continue
		}
		segs := roundSegments(live, round, read, st.BuilderSegmentsPath(name, round))
		return renderStream(data, segs, live.Kind), filepath.Base(streamPath) + " (rendered)", true, nil
	}
	return nil, "", false, nil
}

// readBytesMissing adapts a bytes reader (Store.ReadFile, a test's own) to
// RoundTranscript's read contract: a not-exist error is a miss, any other
// error is returned.
func readBytesMissing(read func(string) ([]byte, error), path string) ([]byte, bool, error) {
	data, err := read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// defaultRound is showLive's no---round rule. A transcript read prefers the
// round in flight: a server-chain member installs its rounds only at close, so
// its open round has no report entry and the completed-round default would
// show the round before it. A round with no readable transcript, and every
// other section, falls back to the newest completed round; ErrNoCompletedRound
// is returned when the binding has no completed round at all.
func defaultRound(rt Runtime, b store.Binding, completed int, section ShowSection) (int, error) {
	if section == ShowTranscript {
		if open, found, err := openTranscriptRound(rt, b); err != nil {
			return 0, err
		} else if found {
			return open, nil
		}
	}
	if completed == 0 {
		completed = b.Round - 1
	}
	if completed < 1 {
		return 0, ErrNoCompletedRound
	}
	return completed, nil
}

// openTranscriptRound returns the binding's open round when it has a readable
// transcript: its mirrored builder log, or its stream. found is false when the
// open round has neither, so a transcript default can fall back to the newest
// completed round. RoundTranscript owns the read; this only names the round.
func openTranscriptRound(rt Runtime, b store.Binding) (round int, found bool, err error) {
	if b.Round < 1 {
		return 0, false, nil
	}
	_, _, found, err = RoundTranscript(rt.Store, b.Name, b.Round, b.Builder, func(path string) ([]byte, bool, error) {
		return readBytesMissing(rt.Store.ReadFile, path)
	})
	if err != nil {
		return 0, false, err
	}
	return b.Round, found, nil
}
