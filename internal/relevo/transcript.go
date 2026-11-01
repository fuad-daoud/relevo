package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// streamTailWindow is the first, and smallest, backwards window streamTail
// reads: 64 KiB, doubling until it yields the wanted rendered lines or covers
// the whole file. The spike measured up to 271 KB of stream for 40 rendered
// lines, so one fixed 64 KiB window is not enough.
const streamTailWindow = 64 * 1024

// renderStream renders every complete line of a round's stream -- up to the
// last '\n'; a trailing partial line is ignored -- into the bytes
// drainFile+appendLines would have appended to an empty log for the same
// stream. The reader's renderer is one per pass, of the harness kind of the
// segment holding each line's absolute offset; a later line can never change
// an earlier one, so the rendering's prefix is stable as the stream grows,
// which serve's files/log?from= offsets and the client mirror depend on.
//
// lineRenderer turns one raw line of a stream -- with the harness kind of the
// segment that holds it -- into the lines a reader or a scan sees: a reader
// renders through streamRenderer, a limit scan through transcript.LimitLines,
// which keeps only the lines the harness itself wrote.
type lineRenderer func(kind string, line []byte) []string

func renderStream(stream []byte, segs []store.StreamSegment, fallback string) []byte {
	return renderStreamFrom(stream, 0, segs, fallback, streamRenderer())
}

// renderStreamFrom renders a window starting at base, keeping the lines'
// absolute offsets in the whole stream so segment kinds stay correct.
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

// renderedLines splits rendered bytes into lines with logTail's conventions:
// no trailing newline, and no lines for an empty rendering.
func renderedLines(rendered []byte) []string {
	s := strings.TrimRight(string(rendered), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func joinTailLines(lines []string, n int) string {
	if n <= 0 || len(lines) == 0 {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// streamTail is the last n lines of a round's stream rendered for humans, over
// renderStream's output whose entries may themselves contain newlines: tailWith
// with the reader's renderer. from is the earliest byte it may read: 0 for the
// whole file, a later offset to ignore bytes another process wrote into the
// same file.
func streamTail(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64) string {
	return tailWith(path, read, segs, fallback, n, from, streamRenderer())
}

// streamRenderer is the reader's lineRenderer: one Renderer for the whole
// pass, so a call and its result rendered in the same pass keep the span
// between them. A scan never uses it.
func streamRenderer() lineRenderer {
	r := transcript.NewRenderer()
	return func(kind string, line []byte) []string { return r.Render(kind, line) }
}

// scanTail is streamTail's sibling for a limit scan: the same bytes, read the
// same way, but each line is rendered with transcript.LimitLines, so only the
// lines the harness itself wrote reach the patterns -- and the reset time is
// parsed from that same text, so model text, tool output and thinking can
// never gate a provider.
func scanTail(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64) string {
	text, _ := scanTailBoth(path, read, segs, fallback, n, from)
	return text
}

// scanTailBoth is scanTail paired with the structured reset the same bytes
// carry: one pass, no new I/O. The render closure keeps the last quota and
// allowance instants from the lines the scan already reads; the failure's own
// instant wins over the window's.
func scanTailBoth(path string, read func(string) ([]byte, error), segs []store.StreamSegment, fallback string, n int, from int64) (string, int64) {
	var quotaMS, allowMS int64
	render := func(kind string, line []byte) []string {
		if ms, quota := transcript.LimitResetMS(kind, line); ms != 0 {
			if quota {
				quotaMS = ms
			} else {
				allowMS = ms
			}
		}
		return transcript.LimitLines(kind, line)
	}
	text := tailWith(path, read, segs, fallback, n, from, render)
	if quotaMS != 0 {
		return text, quotaMS
	}
	return text, allowMS
}

// tailWith renders the last n lines of a round's stream with render, using
// logTail's conventions (no trailing newline, "" for a missing or empty file,
// or n <= 0). from is the earliest byte it may read: 0 for the whole file, a
// later offset to ignore bytes another process wrote into the same file. When
// path exists on disk it reads backwards in doubling windows
// (streamTailWindow first), dropping the first, partial line of a window that
// does not start at from; when the file is not on disk -- a sealed round -- it
// reads it whole through read and renders the bytes at from or later.
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

// diskStreamTail reads the stream backwards from disk. ok is false when the
// file is not on disk (or cannot be read), so the caller falls back to read.
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
// the endpoint writes one (a legacy round's own BuilderLogPath, see legacyLog),
// otherwise the rendered stream's tail, which holds the builder's stderr too.
func builderTail(rt Runtime, b store.Binding, n int) string {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n)
	}
	return streamTail(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, 0)
}

// currentBuilderTail is builderTail's read starting at the current builder
// process's first byte. A mid-round switch appends the new process's bytes to
// the same stream file, so scanning the whole round would charge the outgoing
// builder's lines -- and its provider's quota text -- to the incoming one. The
// legacy-log branch needs no cut: such a round's log is the round's own.
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

// currentBuilderScanText reads currentBuilderTail's bytes for a limit scan,
// rendering each stream line with transcript.LimitLines so only the lines the
// harness itself wrote can gate; the legacy log is scanned as rendered tail,
// because such a round writes its stderr to the log and the rendered text is
// what the patterns match.
func currentBuilderScanText(rt Runtime, b store.Binding, n int) string {
	text, _ := currentBuilderScan(rt, b, n)
	return text
}

// currentBuilderScan pairs currentBuilderScanText with the structured reset
// the same bytes carry: one pass, no new I/O. A legacy round's log is
// rendered text with no JSON beside it, so it never carries one.
func currentBuilderScan(rt Runtime, b store.Binding, n int) (string, int64) {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n), 0
	}
	from := int64(0)
	if b.Builder.StreamRound == b.Round {
		from = b.Builder.StreamStart
	}
	return scanTailBoth(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, from)
}

// currentBuilderTransportText is currentBuilderScanText with
// transcript.TransportLines as the renderer: the same bytes and the same cut.
func currentBuilderTransportText(rt Runtime, b store.Binding, n int) string {
	if b.Builder.LogPath != "" && b.Builder.LogPath == rt.Store.BuilderLogPath(b.Name, b.Round) {
		return logTail(b.Builder.LogPath, n)
	}
	from := int64(0)
	if b.Builder.StreamRound == b.Round {
		from = b.Builder.StreamStart
	}
	return tailWith(rt.Store.StreamPath(b.Name, b.Round), rt.Store.ReadFile, b.Builder.StreamSegments, b.Builder.Kind, n, from, transcript.TransportLines)
}

// limitScan pairs limitText with the structured reset the same bytes carry:
// one scan, no new I/O. The gate decision prefers that instant over whatever
// the text names.
func limitScan(ctx context.Context, rt Runtime, b store.Binding) (string, int64) {
	return currentBuilderScan(rt, b, availability.LimitScanLines)
}

// roundSegments is the segment list of one round: the live endpoint's own when
// it holds that round and has segments, otherwise the round's stored
// NNN-builder-segments.json row, otherwise nil. A decode error is a warning
// and nil.
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
// reader: the round's NNN-builder.log when one exists, otherwise the round's
// stream rendered per segment, otherwise not found. read reports a miss as
// found=false, err=nil; a real read error is returned as is.
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

// defaultRound picks the round when --round is absent. A transcript read prefers the
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
// completed round.
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

// openRoundTranscriptRound reports whether an explicit --round transcript read
// may name the round it asks for. A server-chain member installs its rounds
// only at close, so the round in flight is outside the prompt-entry range that
// bounds an explicit --round; when the requested round is that open round with
// a readable transcript, it is answered from the same mirror the default
// reads. Anything else reports false, so the caller's out-of-range refusal
// stands; this adds no call Show did not already make and no network round trip.
func openRoundTranscriptRound(rt Runtime, b store.Binding, section ShowSection, round int) (bool, error) {
	if section != ShowTranscript {
		return false, nil
	}
	open, found, err := openTranscriptRound(rt, b)
	if err != nil {
		return false, err
	}
	return found && round == open, nil
}
