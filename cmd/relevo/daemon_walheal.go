package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// walHealAttemptEnv carries the heals already tried across the re-exec: the
// count's home is the environment because it survives the exec and the
// poisoned database cannot be written.
const walHealAttemptEnv = "RELEVO_WAL_HEAL_ATTEMPT"

// walHealExhaustedReason is the marker's reason when the attempt budget runs
// out without a heal.
const walHealExhaustedReason = "attempts exhausted"

// walHealReexecEnv carries the heal count into the next image after a heal,
// and drops a stale count on every other re-exec, so an unrelated later
// poisoning starts from the full budget.
func walHealReexecEnv(env []string, h *daemonHandles) []string {
	if h.walHealFired {
		return withEnv(env, walHealAttemptEnv, strconv.Itoa(h.walAttempt+1))
	}
	return withoutEnv(env, walHealAttemptEnv)
}

// withoutEnv drops every KEY= entry from env. It is how an upgrade re-exec
// stops carrying a heal count the next image must not inherit. Pure.
func withoutEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// walHealAttemptFromEnv parses the inherited heal count; anything unparseable
// reads as zero, the full budget. Pure.
func walHealAttemptFromEnv(v string) int {
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// initWALHeal arms this image's heal request from the environment: the count
// already tried, this image's start, and the hook the owner calls on a short
// read. It returns the hook for the owner to install; nil without re-exec,
// where the hook would be pointless.
func initWALHeal(h *daemonHandles) func() {
	var walRequested atomic.Bool
	h.walRequested = &walRequested
	h.walStart = time.Now()
	h.walAttempt = walHealAttemptFromEnv(os.Getenv(walHealAttemptEnv))
	if !h.reexecOK {
		return nil
	}
	return func() { walRequested.Store(true) }
}

// walHealDue reports whether this tick should re-exec to heal a poisoned WAL
// pool. The owner hook and the probe below are the two detectors; either one
// sets the request, the attempt budget decides, and exhaustion is recorded
// once for the human instead of looping the re-exec forever.
func (h *daemonHandles) walHealDue(context.Context) bool {
	if !h.reexecOK {
		return false
	}
	if h.walHealFired {
		return false
	}
	if !h.walRequested.Load() {
		// The tick swallows database errors, so its return alone is not a
		// detector: probe the handle directly, the way any daemon read
		// trips over a poisoned pool.
		if h.d == nil {
			return false
		}
		_, err := h.d.Version()
		if err == nil {
			// A probe that answers clears the marker: a restarted daemon
			// inherits no count (only the exec child gets one), so
			// attempt 0 with a stale marker is the normal post-restart
			// state. The count itself is forgotten only once the image
			// has answered for the settle window, which hands a later,
			// unrelated poisoning the full budget without letting a pool
			// that repoisons after every heal re-exec forever. A clear
			// that fails keeps the count so the next tick retries it.
			if cerr := h.rt.Store.ClearWALHealFailure(); cerr != nil {
				slog.Warn("relevo daemon: wal heal marker not cleared", "err", cerr)
				return false
			}
			if relevo.WALHealSettled(h.walAttempt, time.Since(h.walStart)) {
				h.walAttempt = 0
			}
			return false
		}
		if !wire.IsShortReadWAL(err) {
			return false
		}
		h.walRequested.Store(true)
	}
	if relevo.WALHealDue(h.walAttempt, time.Since(h.walStart)) {
		h.walHealFired = true
		return true
	}
	if relevo.WALHealExhausted(h.walAttempt) && !h.walMarked {
		h.walMarked = true
		if werr := h.rt.Store.WriteWALHealFailure(h.walAttempt, time.Now(), walHealExhaustedReason); werr != nil {
			slog.Warn("relevo daemon: wal heal failure not recorded", "err", werr)
		}
		slog.Error("relevo daemon: WAL heal gave up; the pool is still poisoned")
	}
	return false
}
