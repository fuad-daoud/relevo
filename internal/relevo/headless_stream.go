package relevo

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/text"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// segmentKind is the harness kind that wrote the stream byte at off: the Kind
// of the last segment whose Start is at or before off, or fallback when no
// segment covers off. The drain asks it per line, so a mid-round switch
// renders the old process's lines with the kind that wrote them.
func segmentKind(segs []store.StreamSegment, off int64, fallback string) string {
	kind := fallback
	for _, s := range segs {
		if s.Start <= off {
			kind = s.Kind
		}
	}
	return kind
}

// carryStream moves the round's stream cursor and its segment list from the
// outgoing endpoint onto its replacement (from -> to) and returns to, so a
// mid-round switch keeps rendering the same round's file from where it left
// off instead of re-rendering it from byte 0. The fields it does not name --
// Kind, AgentName, Mode, PID -- come from to.
func carryStream(from, to store.Endpoint) store.Endpoint {
	to.StreamRound = from.StreamRound
	to.StreamOffset = from.StreamOffset
	to.StreamStart = from.StreamStart
	to.StreamSegments = append([]store.StreamSegment(nil), from.StreamSegments...)
	return to
}

// drainStream brings a legacy round's builder log up to date with its stream
// and, for a round with no log, only advances the cursor and captures the
// session id: every complete line past the endpoint's cursor is rendered with
// one transcript renderer for the pass and, when the round has a log on disk,
// appended to it in one write. A trailing partial line waits for the next
// tick. The cursor is keyed on StreamRound, not b.Round, so a round that
// closed on its marker while the builder was still flushing keeps draining
// until the next round starts. The pass owns one renderer, so a call and its
// result drained in the same range keep their duration. The first drained
// line naming the harness's own session records it on the endpoint; later
// lines cannot change it. It never fails the tick: every problem is a
// slog.Warn and an unchanged binding, and the cursor advances only after the
// append succeeded, so a failed write renders the same lines again next tick
// rather than dropping them.
func drainStream(rt Runtime, b store.Binding) store.Binding {
	round := b.Builder.StreamRound
	if round == 0 {
		return b
	}
	logPath := ""
	if legacyLog(rt, b.Name, round) {
		logPath = rt.Store.BuilderLogPath(b.Name, round)
	}
	r := transcript.NewRenderer()
	b.Builder.StreamOffset = drainFile(
		logPath,
		rt.Store.StreamPath(b.Name, round),
		b.Builder.StreamOffset,
		func(off int64, line []byte) []string {
			kind := segmentKind(b.Builder.StreamSegments, off, b.Builder.Kind)
			if b.Builder.StreamSessionID == "" && off >= b.Builder.StreamStart {
				if id := transcript.SessionID(kind, line); id != "" {
					b.Builder.StreamSessionID = id
				}
			}
			return r.Render(kind, line)
		},
		"stream", "binding", b.Name, "round", round,
	)
	return b
}

// drainFile appends render(off, line) for every complete line of src past off
// to logPath -- nothing when logPath is "", which still advances the offset.
// off is the line's own absolute byte offset in src, so a caller rendering per
// byte range knows where each line came from. It never fails the tick -- every
// problem is a slog.Warn and the offset unchanged, except an offset past EOF,
// which resets to 0. what names the source in warnings ("stream", "session").
func drainFile(logPath, src string, off int64, render func(off int64, line []byte) []string, what string, fields ...any) int64 {
	info, err := os.Stat(src)
	if err != nil {
		return off // not started yet, or gone with the round: nothing to drain
	}
	if off > info.Size() {
		slog.Warn("builder "+what+": cursor past end of "+what+"; rendering from the start",
			append(append([]any{}, fields...), "offset", off, "size", info.Size())...)
		off = 0
	}
	if off == info.Size() {
		return off
	}
	data, err := readFrom(src, off)
	if err != nil {
		slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
		return off
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return off
	}
	var out []string
	lineOff := off
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		out = append(out, render(lineOff, line)...)
		lineOff += int64(len(line)) + 1
	}
	if logPath != "" && len(out) > 0 {
		if err := appendLines(logPath, out); err != nil {
			slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
			return off
		}
	}
	return off + int64(end) + 1
}

func readFrom(path string, off int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// appendLines appends lines, each newline-terminated, to the file at path in
// one write: one buffer interleave with the supervisor's stderr at line
// boundaries.
func appendLines(path string, lines []string) error {
	f, err := openAppend(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	return err
}

// logTailLines is how much of a builder log the exit entry carries: enough to
// see why it died, not enough to flood the round log.
const logTailLines = 20

// logTail is the last n lines of the file at path, without a trailing newline,
// or "" when the file is absent or empty. It is text for humans and is never
// parsed. It reads the tail, not the file: a legacy builder log grows for the
// whole round and only its last few lines are ever shown. The window is grown
// until it holds n lines, so the answer is the one a whole-file read gave --
// including when one line is longer than the window.
func logTail(path string, n int) string {
	if n <= 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return ""
	}
	const window = 64 << 10
	size := info.Size()
	for off := size - window; ; {
		if off < 0 {
			off = 0
		}
		buf, err := io.ReadAll(io.NewSectionReader(f, off, size-off))
		if err != nil {
			return ""
		}
		s := strings.TrimRight(string(buf), "\n")
		if s == "" {
			return ""
		}
		lines := strings.Split(s, "\n")
		// A window starting mid-line yields one short leading line, so the tail
		// settles only once the window holds n breaks or reaches the start.
		if off == 0 || strings.Count(s, "\n") >= n {
			if len(lines) > n {
				lines = lines[len(lines)-n:]
			}
			return strings.Join(lines, "\n")
		}
		off -= window
	}
}

// clearProcess drops the endpoint's process fields; identity -- name, kind,
// mode -- stays.
func clearProcess(e store.Endpoint) store.Endpoint {
	e.PID, e.StartedAt, e.LogPath = 0, 0, ""
	return e
}

// exitEntry is the log record of a headless builder that exited without a
// report: relevo → log only, Confirmed, never a pending payload. codeText is the exit
// code, or "unknown" when the supervisor's trailer is missing. payload is the
// builder's last logTailLines lines for the human; relevo reads nothing out
// of it. shape picks the word for the missing artifact: a reader's is output.
func exitEntry(now time.Time, round int, logPath, codeText, suffix, payload, shape string) store.LogEntry {
	return store.LogEntry{
		TS:        now,
		Round:     round,
		Direction: store.DirToMasterMind,
		Kind:      store.KindExit,
		Path:      logPath,
		Note:      fmt.Sprintf("builder exited (code %s) %s%s", codeText, text.WithoutArtifact(shape == store.ShapeReader), suffix),
		Payload:   payload,
		Confirmed: true,
	}
}

// streamLastActivity is when a live headless builder's stream last moved: the
// later of the stream file's mtime and the round's start. A missing or
// unreadable stream is not an error -- a round yet to produce a line counts
// from its start, and a stream never appearing on a live process is a stall.
func streamLastActivity(rt Runtime, b store.Binding) time.Time {
	st, err := os.Stat(rt.Store.StreamPath(b.Name, b.Round))
	if err != nil {
		return b.RoundStartedAt
	}
	last := st.ModTime()
	if b.RoundStartedAt.After(last) {
		return b.RoundStartedAt
	}
	return last
}

// stallLimitGateRecorded reports whether this stalled round already recorded
// its rate-limit gate. gateOnLimit writes the ledger entry before looking for
// the round's report, so a stalled live builder with a report was re-recording
// the same gate every tick, each record moving Until forward and keeping every
// other binding on the same candidate gated indefinitely. A repeat tick carries
// no new fact about the provider, so it records once per round and otherwise
// falls through to deliverAndSettle. The report must be on disk: a match with
// no report ends the tick in gateOnLimit -> switchBuilder instead. Scoped to
// the current round by RoundStartedAt, which a switch restarts, so a new round
// gates afresh.
func stallLimitGateRecorded(rt Runtime, b store.Binding) bool {
	if rt.Gates == nil {
		return false
	}
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.ReportPath(b.Name, b.Round)); !ok {
		return false
	}
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		// An unreadable ledger is not evidence the gate was already recorded,
		// the same rule gateOnLimit writes under: it records and reports.
		return false
	}
	for _, e := range l.Entries {
		if e.Kind != availability.RateLimited || e.Binding != b.Name {
			continue
		}
		if e.At.Before(b.RoundStartedAt) {
			continue
		}
		return true
	}
	return false
}
