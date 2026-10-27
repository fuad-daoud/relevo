package main

// The status document and the line that renders it, kept beside the verb that
// prints them rather than in the sync CLI proper.
//
// The rule this file exists to hold is about what status may say. The endpoint
// and the bucket are named because they are what a reader needs to tell this
// machine's bucket from another's. The key id and the secret are not, in any
// shape, because this document is printed on request, pasted into an issue and
// read aloud, and a credential has no reason in any of those.

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// dbSyncStatusDoc is `db sync status --json`: what this machine is set to be,
// what its last exchange measured, and which origins need a human. TokenPresent
// is a bool because the value is the one thing on this surface that must never
// be printed. The R2 fields are omitempty for the same reason an unconfigured
// machine renders no R2 clause on the line: bodies in a bucket are opt-in, and a
// machine that has not turned them on is in the ordinary state.
type dbSyncStatusDoc struct {
	Enabled      bool   `json:"enabled"`
	RemoteURL    string `json:"remote_url,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	TokenPresent bool   `json:"token_present"`
	// Key is unlocked when this machine holds the sync key, locked when it
	// holds none and the remote does, and none otherwise.
	Key string `json:"key,omitempty"`
	// R2Configured says whether this machine holds a complete set of bucket
	// credentials. R2Endpoint and R2Bucket describe where bodies would go, which
	// is what a reader needs to tell this machine's bucket from another's. There
	// is deliberately no field for the key id or the secret: this document is
	// printed on request and read aloud, and a credential has no reason to be in
	// either. See db_sync_r2.go, which is where the reader and renderer live.
	R2Configured bool   `json:"r2_configured,omitempty"`
	R2Endpoint   string `json:"r2_endpoint,omitempty"`
	R2Bucket     string `json:"r2_bucket,omitempty"`
	// Latched and LatchCause describe a breaker that stopped the machine until
	// `relevo db sync retry`.
	Latched    bool   `json:"latched,omitempty"`
	LatchCause string `json:"latch_cause,omitempty"`
	// RebuildAt and RebuildAside are the last automatic replica rebuild: when
	// it ran and where the copy it kept is. Absent until one ran, so a machine
	// that never rebuilt renders the document it always did.
	RebuildAt    string `json:"rebuild_at,omitempty"`
	RebuildAside string `json:"rebuild_aside,omitempty"`
	// Backlog is the count of this machine's operations the last attempt left
	// unsent.
	Backlog int64 `json:"backlog,omitempty"`
	// LastExport and LastImport are the UTC instants the last completed
	// exchange stamped, empty when it never completed one.
	LastExport string `json:"last_export,omitempty"`
	LastImport string `json:"last_import,omitempty"`
	// HeldOrigins, Dropped and Gaps are the last import's report: an origin a
	// newer writer held, a batch a refusal dropped, a sequence gap a hole left.
	HeldOrigins []string `json:"held_origins,omitempty"`
	Dropped     []string `json:"dropped,omitempty"`
	Gaps        []string `json:"gaps,omitempty"`
	// Fenced lists the bindings a takeover took from this machine, with how many
	// of its local changes the takeover fenced. Nothing is deleted locally.
	Fenced []dbSyncFencedDoc `json:"fenced,omitempty"`
	// IdentityTaken is the installation id this machine adopted whose claim
	// another machine now holds. Export and import are stopped.
	IdentityTaken string `json:"identity_taken,omitempty"`
	// AdoptPending is the installation id whose adoption is unfinished.
	AdoptPending    string `json:"adopt_pending,omitempty"`
	AdoptionPending string `json:"adoption_pending,omitempty"`
	// Joining says a join is in progress and JoinSince is when it began, read
	// from the machine-local join marker. The daemon finishes the join whether
	// the enable that started it waited or not, so a machine can be joining
	// while reading off.
	Joining   bool   `json:"joining,omitempty"`
	JoinSince string `json:"join_since,omitempty"`
	// LastAttempt is the daemon's last steady attempt, absent until it has run
	// one.
	LastAttempt *dbSyncAttemptDoc `json:"last_attempt,omitempty"`
	// Bytes is this month's traffic, absent until something moved, and
	// QuotaTursoSync the allowance the Turso total is read against. They stay
	// after the account reading arrived because JSON consumers may read them.
	Bytes          *relevosync.ByteCounters `json:"bytes,omitempty"`
	QuotaTursoSync int64                    `json:"quota_turso_sync,omitempty"`
	// Storage is the last daily measurement of the replica and the bucket,
	// absent until one was taken, read against the two storage quotas.
	Storage           *relevosync.Storage `json:"storage,omitempty"`
	QuotaTursoStorage int64               `json:"quota_turso_storage,omitempty"`
	QuotaR2Storage    int64               `json:"quota_r2_storage,omitempty"`
	// Account is the month read account-wide, present when there is something
	// to say: this machine moved bytes this month, or the snapshot holds a
	// published row for it.
	Account *dbSyncAccountDoc `json:"account,omitempty"`
	// IdleExports is the last complete hour's export counts, present when that
	// hour booked a row at all.
	IdleExports *dbSyncIdleDoc `json:"idle_exports,omitempty"`
}

// dbSyncAccountDoc is the meter reading as one machine reports it: the
// account's month, this machine's own share, the quota, and the warnings the
// reading already decided. Projected is absent while the month is too young
// to project, so its presence is the projection's answer to "projectable".
type dbSyncAccountDoc struct {
	Month     string `json:"month"`
	Total     int64  `json:"total"`
	Machines  int    `json:"machines"`
	Own       int64  `json:"own"`
	Quota     int64  `json:"quota"`
	Projected int64  `json:"projected,omitempty"`
	ReadAt    string `json:"read_at,omitempty"`
	OverWarn  bool   `json:"over_warn"`
	PaceOver  bool   `json:"pace_over"`
}

// dbSyncIdleDoc is one hour's export counts and whether the idle part of them
// passed the warning line.
type dbSyncIdleDoc struct {
	Hour     string `json:"hour"`
	Exported int    `json:"exported"`
	Idle     int    `json:"idle"`
	Over     bool   `json:"over"`
}

// dbSyncAccount is the meter reading the quotas clause renders, rebuilt from
// the document the reader filled. A projected figure is positive exactly when
// the month was projectable and moved bytes, so the document needs no flag
// beside it; read_at round-trips the instant statusStamp wrote.
func dbSyncAccount(doc dbSyncStatusDoc) relevosync.QuotaReading {
	if doc.Account == nil {
		return relevosync.QuotaReading{}
	}
	a := doc.Account
	var readAt time.Time
	if a.ReadAt != "" {
		if t, err := time.Parse(time.RFC3339, a.ReadAt); err == nil {
			readAt = t
		}
	}
	return relevosync.QuotaReading{
		Account:     relevosync.Account{Month: a.Month, Total: a.Total, Machines: a.Machines, ReadAt: readAt},
		Own:         a.Own,
		Quota:       a.Quota,
		Projected:   a.Projected,
		Projectable: a.Projected > 0,
	}
}

// dbSyncFencedDoc is one binding of the fenced list.
type dbSyncFencedDoc struct {
	Name      string `json:"name"`
	OldWriter string `json:"old_writer"`
	Taker     string `json:"taker"`
	Count     int    `json:"count"`
}

// dbSyncFenced reads the takeover fences from the shared file, where the
// binding rows and their terms live.
func dbSyncFenced(shared *db.DB) ([]dbSyncFencedDoc, error) {
	fenced, err := shared.FencedSummary()
	if err != nil {
		return nil, dbSyncClassify(err)
	}
	out := make([]dbSyncFencedDoc, 0, len(fenced))
	for _, f := range fenced {
		out = append(out, dbSyncFencedDoc{Name: f.Name, OldWriter: shared.Origin(), Taker: f.Taker, Count: f.Count})
	}
	return out, nil
}

// dbSyncStatusLine is the one line `db sync status` prints, as a function of the
// document and nothing else. Pulling it out of the verb is what lets the mapping
// be pinned by bytes: the route a row arrived over is then nowhere in the line,
// so a table of documents renders to the exact same table of strings whichever
// handle the rows came out of.
func dbSyncStatusLine(doc dbSyncStatusDoc) string {
	state := "off"
	if doc.Enabled {
		state = "on"
	}
	line := fmt.Sprintf("sync %s (remote: %s, token: %s)", state, orNone(doc.RemoteURL), presentOrAbsent(doc.TokenPresent))
	if r2 := dbSyncR2Line(doc); r2 != "" {
		line += ", r2: " + r2
	}
	line += dbSyncKeyClause(doc.Key)
	return line + dbSyncStatusDetails(doc) + "\n"
}

// dbSyncStatusDetails is the tail of the status line: each fact a reader can act
// on, in a fixed order and only when it is set. A latch comes first because it
// is the one state that stops the machine until a human acts; the rest is what
// the last exchange measured and what it could not apply.
func dbSyncStatusDetails(doc dbSyncStatusDoc) string {
	var b strings.Builder
	if doc.Joining {
		fmt.Fprintf(&b, " · joining since %s", doc.JoinSince)
	}
	if doc.Latched {
		fmt.Fprintf(&b, " · latched: %s", doc.LatchCause)
	}
	if doc.RebuildAt != "" {
		fmt.Fprintf(&b, " · rebuilt: %s", doc.RebuildAt)
		if doc.RebuildAside != "" {
			fmt.Fprintf(&b, ", aside: %s", sanitize.Text(doc.RebuildAside))
		}
	}
	if doc.Backlog > 0 {
		fmt.Fprintf(&b, " · backlog: %d", doc.Backlog)
	}
	if doc.LastExport != "" {
		fmt.Fprintf(&b, " · exported: %s", doc.LastExport)
	}
	if doc.LastImport != "" {
		fmt.Fprintf(&b, " · imported: %s", doc.LastImport)
	}
	for _, held := range doc.HeldOrigins {
		fmt.Fprintf(&b, " · held: %s", sanitize.Text(held))
	}
	for _, dropped := range doc.Dropped {
		fmt.Fprintf(&b, " · dropped: %s", sanitize.Text(dropped))
	}
	for _, gap := range doc.Gaps {
		fmt.Fprintf(&b, " · gap: %s", sanitize.Text(gap))
	}
	if doc.IdentityTaken != "" {
		fmt.Fprintf(&b, " · %s", sanitize.Text(dbSyncIdentityTakenLine(doc.IdentityTaken)))
	}
	if doc.AdoptPending != "" {
		fmt.Fprintf(&b, " · %s", sanitize.Text(dbSyncAdoptPendingLine(doc.AdoptPending)))
	}
	for _, f := range doc.Fenced {
		fmt.Fprintf(&b, " · %d local changes to %s were fenced by a takeover", f.Count, sanitize.Text(f.Name))
	}
	if doc.Bytes != nil || doc.Storage != nil {
		fmt.Fprintf(&b, " · %s", dbSyncQuotasLine(doc))
	}
	if doc.IdleExports != nil && doc.IdleExports.Over {
		fmt.Fprintf(&b, " · %d entries exported in %sZ with no round open (more than %d an hour is unexpected)",
			doc.IdleExports.Idle, doc.IdleExports.Hour, relevosync.IdleExportsWarn)
	}
	if w := dbSyncMeterWarning(doc); w != "" {
		fmt.Fprintf(&b, " · warning: %s", w)
	}
	b.WriteString(dbSyncAttemptDetails(doc.LastAttempt))
	return b.String()
}

// dbSyncMeterWarning is the one reason the account's traffic earns a warning
// on this line: the pace that has passed the quota outranks a share still
// climbing, the same order `relevo status` puts its notices in.
func dbSyncMeterWarning(doc dbSyncStatusDoc) string {
	if doc.Account == nil {
		return ""
	}
	a := doc.Account
	switch {
	case a.PaceOver:
		return fmt.Sprintf("the account's Turso traffic is on pace for %s this month, over its %s quota",
			relevosync.FormatTraffic(a.Projected), relevosync.FormatTraffic(a.Quota))
	case a.OverWarn:
		return fmt.Sprintf("the account has used %.1f%% of this month's %s Turso quota",
			float64(a.Total)*100/float64(a.Quota), relevosync.FormatTraffic(a.Quota))
	}
	return ""
}

// orNone renders an empty remote as something a reader can tell from a URL.
func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// presentOrAbsent renders a token's presence without rendering the token.
func presentOrAbsent(present bool) string {
	if present {
		return "present"
	}
	return "absent"
}

// dbSyncIdentityTakenLine says another machine adopted the identity this one
// adopted, and what to do about it.
func dbSyncIdentityTakenLine(id string) string {
	return "another machine adopted installation " + id + ": sync is stopped here; run `relevo db sync disable` on one of the two machines"
}

// dbSyncAdoptPendingLine says an adoption was interrupted and what finishes it.
func dbSyncAdoptPendingLine(id string) string {
	return "adoption of " + id + " is unfinished: run relevo db sync enable --as " + id
}
