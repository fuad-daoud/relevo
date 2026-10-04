package ui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/lipgloss"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The formatting this view draws with, kept apart from the view that draws.
// Every function here is pure: it takes the values a snapshot handed over and
// turns one into the string a reader sees, and none of them can reach a handle,
// a clock or a database. That is what makes the render path safe to assert on --
// the reason a Body cannot touch the network is visible in these signatures.

// syncOr renders value, or fallback when it is empty, so an absent field reads
// as a statement rather than as an empty cell.
func syncOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// unixAge is how long ago a remote-reported unix time was, at the shell's clock.
// A time in the future reads as "just now" rather than as a negative age,
// because a remote whose clock leads ours is a clock problem and not something
// a user can act on from this screen.
func unixAge(unix int64, now time.Time) string {
	t := time.Unix(unix, 0)
	if t.After(now) {
		return "just now"
	}
	age := ago(t, now)
	if age == "" {
		return "just now"
	}
	return age + " ago"
}

// unixClock is the same instant as a local wall clock, so an age and a time can
// be read together without a second lookup.
func unixClock(unix int64) string {
	return time.Unix(unix, 0).Local().Format("15:04")
}

// syncBytes is a byte count in the largest unit that keeps it readable. It is
// the same rule for every count on this screen, so a reader never has to work
// out which of two sizes is bigger.
func syncBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f := float64(n)
	for _, suffix := range []string{"kB", "MB", "GB", "TB"} {
		f /= unit
		if f < unit {
			return fmt.Sprintf("%.1f %s", f, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", f/unit)
}

// syncDate is a day, which is the resolution an installation's stamps are read
// at: a second would say more than the column has room for and less than a user
// comparing two machines needs.
func syncDate(t time.Time) string {
	if t.IsZero() {
		return syncDash
	}
	return t.Local().Format("2006-01-02")
}

// syncTokenStyle colours the token by the state it names, using S2's own four
// tokens rather than a second opinion about which is which.
func syncTokenStyle(token string) lipgloss.Style {
	switch token {
	case relevosync.TokenOff:
		return faintStyle
	case relevosync.TokenErr:
		return redStyle
	case relevosync.TokenBehind:
		return warnStyle
	default:
		return greenStyle
	}
}

// syncSharedTables is what crosses to the remote: the shared-history tables, in
// the names the migrations and the scopes document give them.
var syncSharedTables = []string{
	"binding_record, binding_event",
	"chains, chain_event, chain_member, chain_check",
	"round_file",
	"installation (the directory projection)",
	"repo, mastermind, binding, round, event, artifact",
}

// syncLockedTables is what cannot cross, however sync is configured. Every line
// is an absolute rather than a default: the whole claim is that no setting
// reaches these, so a line hedged with "by default" would understate it.
var syncLockedTables = []string{
	"every secret, including turso.token",
	"every machine-local kv namespace (sync.*, planner/*, ledger, …)",
	"every config section, this one included",
	"this machine's own installation identity",
}

// confirmLines is the pre-on confirm, naming the database it would seed. It is
// drawn rather than asked: an enable is the CLI's verb, and the only thing this
// screen owes a user before they go and run it is which remote they are about

// syncLine is one labelled row of the form or status block: a fixed-width label
// and a value, so a reader can find a row by its label rather than by counting.
// The three-space indent is the cockpit's body gutter, so these rows line up with
// the section headings below them rather than sitting out at the margin.
func syncLine(label, value string, style lipgloss.Style) string {
	return "   " + pad(label, syncLabelW) + style.Render(value)
}

// preOnLines is the form for a machine that has not enabled sync: where the
