package relevo

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// reportPathFor is where a closing round's report is recorded: a reader's
// artifact-directory summary.md, a writer's flat NNN-report.md.
func reportPathFor(rt Runtime, b store.Binding) string {
	if b.Shape == store.ShapeReader {
		return rt.Store.SummaryPath(b.Name, b.Round, bindingRole(b))
	}
	return rt.Store.ReportPath(b.Name, b.Round)
}

// writeReaderSummary records a reader round's report. A reader's final message
// is its summary.md: when the runner did not write one itself, it is written
// here from the stream's last assistant text, rendered with the harness kind of
// the last segment that ran, which a mid-round switch can change. An empty
// final message writes nothing, so the round closes without a report exactly as
// a writer that wrote none. A writer is untouched: it writes its own report.
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
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	stream, _ := rt.Store.ReadFile(rt.Store.StreamPath(b.Name, b.Round))
	text := transcript.FinalText(lastStreamKind(b), stream)
	if text == "" {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, err
	}
	return path, os.WriteFile(path, []byte(text+"\n"), 0o644)
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
