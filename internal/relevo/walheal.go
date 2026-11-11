package relevo

import "time"

// MaxWALHealAttempts bounds how many times the daemon re-execs to heal a
// poisoned WAL pool. Past it the pool is declared unhealable and the failure
// is recorded for the human instead of looping the re-exec forever.
const MaxWALHealAttempts = 3

// walHealBackoff is the delay after start before each heal attempt may fire.
// The first heal runs at once; later ones wait, so a pool that repoisons
// instantly does not spin the daemon through re-execs.
var walHealBackoff = []time.Duration{0, 10 * time.Second, 60 * time.Second}

// WALHealDue reports whether a WAL heal may fire now: attempt counts the
// heals already tried, sinceStart how long this image has been up. Past the
// attempt budget it never fires; inside it the attempt's backoff must have
// elapsed.
func WALHealDue(attempt int, sinceStart time.Duration) bool {
	if attempt < 0 || attempt >= MaxWALHealAttempts {
		return false
	}
	return sinceStart >= walHealBackoff[attempt]
}

// WALHealExhausted reports whether the heal budget is spent: the daemon
// records the give-up for the human instead of waiting out another backoff.
// It is separate from WALHealDue because a tick before its backoff must stay
// quiet, while a tick past the budget must be heard.
func WALHealExhausted(attempt int) bool {
	return attempt >= MaxWALHealAttempts
}
