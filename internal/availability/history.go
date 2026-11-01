package availability

import (
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/db"
)

// HistoryRetainWindow is how long an event is kept before Prune drops it. It
// is db.HistoryWindow rather than a number written here, because the importer
// and the reconciler judge an arriving or a proposed row against the same
// window and the three must not be able to disagree about where it ends.
const HistoryRetainWindow = db.HistoryWindow

// Cleared is the history-only kind a manual clear records. It never
// enters the ledger (ledger validation would reject it there); it exists
// so availability.json can say when a block ended, not only when it began.
const Cleared Kind = "cleared"

// Event records one ledger observation, mirrored into the 30-day window the
// ledger itself does not keep.
type Event struct {
	At       time.Time `json:"at"`
	Kind     Kind      `json:"kind"`
	Provider string    `json:"provider"`
	// Account is the login an account gate names, the account half of a
	// group@account key. Display-only: counts and durations stay on Provider.
	Account string `json:"account,omitempty"`
	Token   string `json:"token,omitempty"`
	Source  string `json:"source"`
	Binding string `json:"binding,omitempty"`
	Note    string `json:"note,omitempty"`
	// Since is, on a Cleared event, the At of the oldest ledger entry the clear
	// removed: At - Since is how long the provider was blocked. Zero on every
	// other kind, and omitted from the JSON when zero.
	Since time.Time `json:"since,omitzero"`
}

// History holds an ordered collection of availability events.
type History struct {
	Events []Event `json:"events"`
}

// The json tags above and below are the shape the retired availability document
// was written in. MigrateHistoryDocuments decodes it once, on the way into the
// table that replaced it; nothing writes it and nothing reads it after that, so
// the tags are the migration's rather than a live wire format.

// Prune keeps every event no older than HistoryRetainWindow, in order, without mutating the receiver.
func (h History) Prune(now time.Time) History {
	cutoff := now.Add(-HistoryRetainWindow)
	var kept []Event
	for _, e := range h.Events {
		if !e.At.Before(cutoff) {
			kept = append(kept, e)
		}
	}
	return History{Events: append([]Event(nil), kept...)}
}

// Append adds e with no deduplication and without mutating the receiver.
func (h History) Append(e Event) History {
	cp := append([]Event(nil), h.Events...)
	return History{Events: append(cp, e)}
}

// FromEntry mirrors a ledger entry: a RateLimited Subject is a provider, a SpawnFailed Subject is a token providerOf resolves.
func FromEntry(e Entry, providerOf func(token string) string) Event {
	ev := Event{
		At:      e.At,
		Kind:    e.Kind,
		Source:  e.Source,
		Binding: e.Binding,
		Note:    e.Note,
	}
	switch e.Kind {
	case RateLimited:
		// A rate-limit subject is a bare group or a group@account key, named as group and account.
		group, accountName, ok := account.ParseGateKey(e.Subject)
		if !ok {
			group, accountName = e.Subject, ""
		}
		ev.Provider = group
		ev.Account = accountName
	case SpawnFailed:
		ev.Token = e.Subject
		ev.Provider = providerOf(e.Subject)
	}
	return ev
}

// HourCounts buckets provider's events of kind by hour in loc. Pure; loc keeps tests timezone-independent.
func HourCounts(h History, provider string, kind Kind, loc *time.Location) [24]int {
	var counts [24]int
	for _, e := range h.Events {
		if e.Provider != provider || e.Kind != kind {
			continue
		}
		counts[e.At.In(loc).Hour()]++
	}
	return counts
}

// BlockedDurations returns At - Since for every valid Cleared event on provider, in order. Pure.
func BlockedDurations(h History, provider string) []time.Duration {
	var out []time.Duration
	for _, e := range h.Events {
		if e.Kind != Cleared || e.Provider != provider {
			continue
		}
		if e.Since.IsZero() || e.Since.After(e.At) {
			continue
		}
		out = append(out, e.At.Sub(e.Since))
	}
	return out
}
