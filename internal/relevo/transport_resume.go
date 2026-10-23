package relevo

import (
	"context"
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// transportResume resumes an opencode builder whose exit without a report
// follows a transport-class error, in the same session. Anything else is not
// handled and takes the caller's exit path.
func transportResume(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, now time.Time) (store.Binding, bool, error) {
	if b.Builder.Kind != "opencode" {
		return b, false, nil
	}
	line, ok := availability.MatchTransport(currentBuilderTransportText(rt, b, availability.LimitScanLines))
	if !ok {
		return b, false, nil
	}
	next, resumed, err := resumeSameSession(ctx, rt, tx, b, entries, "transport error: "+line, transportPrompt(rt, b), now)
	if resumed {
		slog.Info("headless builder resumed after transport error", "binding", b.Name, "round", b.Round, "session", b.Builder.StreamSessionID)
	}
	return next, resumed, err
}
