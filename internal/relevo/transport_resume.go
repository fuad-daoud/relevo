package relevo

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// sameSessionRecovery resumes an opencode builder whose exit without a report
// follows a transport-class error or a stall abort, in the same session.
// Anything else is not handled and takes the caller's exit path. A transport
// error in the tail names the cause ahead of a stall note.
func sameSessionRecovery(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, now time.Time) (store.Binding, bool, error) {
	if b.Builder.Kind != "opencode" {
		return b, false, nil
	}
	cause, prompt := "", ""
	if line, ok := availability.MatchTransport(currentBuilderTransportText(rt, b, availability.LimitScanLines)); ok {
		cause, prompt = "transport error: "+line, transportPrompt(rt, b)
	} else if stalledSince(b, entries) {
		cause, prompt = "provider stall", stallPrompt(rt, b)
	} else {
		return b, false, nil
	}
	next, resumed, err := resumeSameSession(ctx, rt, tx, b, entries, cause, prompt, now)
	if resumed {
		slog.Info("headless builder resumed in the same session", "binding", b.Name, "round", b.Round, "session", b.Builder.StreamSessionID, "cause", cause)
	}
	return next, resumed, err
}

// stalledSince reports whether this round logged a provider stall after the
// current process started, which is the process the stall aborted.
func stalledSince(b store.Binding, entries []store.LogEntry) bool {
	started := time.Unix(b.Builder.StartedAt, 0)
	for _, e := range entries {
		if e.Round == b.Round && e.Kind == store.KindSwitch &&
			strings.HasPrefix(e.Note, providerStallNotePrefix) && e.TS.After(started) {
			return true
		}
	}
	return false
}
