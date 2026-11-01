package relevo

import (
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running: one process per round, since two at once
// in one tree would race each other's edits. As a refusal.Sentinel it unwraps
// to refusal.ErrRefused, so the CLI classifies the busy send as refused
// (exit 2) without each call site naming both sentinels.
var ErrBuilderBusy = refusal.Sentinel("the previous process is still running; wait for the round to close, or relevo done")

// ErrReportPending reports a send refused because the current round already
// has its completion marker or report on disk, but the daemon has not yet
// ingested the close. The round is over: restaging its plan would make the
// daemon close on the stale marker and deliver the old report. The caller
// retries once the report is delivered.
var ErrReportPending = refusal.Sentinel("the round's output is on disk but not yet delivered; relevo wait delivers it, then send the next round")

// ErrScopeActive reports a send refused because this round's systemd scope
// unit is still loaded: a builder for the round is already alive, most
// likely started by an earlier send whose bookkeeping failed. Nothing
// was spawned and nothing was saved.
var ErrScopeActive = refusal.Sentinel("this round's builder scope is still running")

// isScopeBusy reports whether a Start error is the host refusing this round's
// scope unit rather than the candidate failing to run: systemd-run's own
// already-loaded wording, and its variant about an existing fragment file.
// Both are text matching because systemd surfaces the collision as a wrapped
// command error, not as a sentinel this binary can type -- and a host-side
// collision says nothing about the candidate, so startProcess must not treat
// one as a spawn failure.
func isScopeBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "already loaded") || strings.Contains(msg, "has a fragment file")
}

// handleOf reads the endpoint's stored process fields as the Runner's handle;
// StartedAt is Unix seconds on the endpoint.
func handleOf(e store.Endpoint) spawn.ProcHandle {
	return spawn.ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// scopeKind is what a scoped spawn is: the word between "relevo-" and the
// owner in its unit name.
type scopeKind string

const (
	scopeRound   scopeKind = "round"
	scopeGate    scopeKind = "gate"
	scopeConsult scopeKind = "consult"
	scopeVerify  scopeKind = "verify"
)

// scopeOwner8 is the owner component of a unit name: "local" for a binding
// with no owner; the first 8 hex characters of the owning client's id when
// Owner parses as a ClientID; "unknown" otherwise, which must never block a
// spawn.
func scopeOwner8(owner string) string {
	if owner == "" {
		return "local"
	}
	if dir, ok := remote.ClientID(owner).Dir(); ok {
		return dir[:8]
	}
	return "unknown"
}

// scopeUnitNameFor is the systemd scope unit's base name (the runner appends
// ".scope") for any scoped spawn. owner8 and the sanitising are shared with
// the round path, so relevo-round-* names match it byte for byte.
func scopeUnitNameFor(kind scopeKind, owner, name string, round int, id string) string {
	unit := "relevo-" + string(kind) + "-" + scopeOwner8(owner) + "-" + safeUnitPart(name) + "-" + strconv.Itoa(round)
	if id != "" {
		unit += "-" + safeUnitPart(id)
	}
	return unit
}

func scopeUnitName(b store.Binding) string {
	return scopeUnitNameFor(scopeRound, b.Owner, b.Name, b.Round, "")
}

// scopeFor builds a spawn's ScopeSpec from the runtime's template:
// nil when the template is nil (scopes off), else a copy of the template with
// Unit set, GateCPUQuota zeroed, and AllowedCPUs set to cpus when cpus is
// non-empty. When kind is scopeGate and the template sets GateCPUQuota, the
// gate's spec uses it as CPUQuota; every other kind keeps the template's
// CPUQuota. It never mutates rt.Scope.
func scopeFor(rt Runtime, kind scopeKind, unit, cpus string) *spawn.ScopeSpec {
	if rt.Scope == nil {
		return nil
	}
	s := *rt.Scope
	s.Unit = unit
	s.GateCPUQuota = ""
	if cpus != "" {
		s.AllowedCPUs = cpus
	}
	if kind == scopeGate && rt.Scope.GateCPUQuota != "" {
		s.CPUQuota = rt.Scope.GateCPUQuota
	}
	return &s
}

// safeUnitPart replaces every rune outside [A-Za-z0-9:_.-] with '-', for a
// string destined for a systemd unit name.
func safeUnitPart(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// defaultRoundBudget mirrors the store's default round timeout for a binding
// that was never saved; Store.Save fills RoundTimeoutMS on every real
// binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short.
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}
