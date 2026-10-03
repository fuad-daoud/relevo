# Plan: #932 — switches stay status-only, surfaced as one line per switch in the round's report

One builder round. Decision (made by the owner, on the issue): no queued
switch message; `queueReport` adds one line per switch in the closing
round; the rule is pinned next to `switchEntry` with the test the issue
asks for.

## Base and branch

Base on the `origin/main` tip at round start; the builder records the actual
base SHA in its report. New branch, new commits only, never amend or rebase.
Save this plan as `docs/plans/2026-10-03-ev932-switch-line.md` in the first
commit.

## Behaviour and cases

Rationale (do not relitigate): a switch needs no action — relevo already
handled it and the round continues. A queued message would cost a mid-round
turn on opencode/agy, a pending payload in tools mode (tripping #906), and
wait-result pollution. Halt-out-of-candidates already messages via #901;
rate-limit gating already surfaces in the unmarked path.

Change: when `queueReport` (`internal/relevo/reconcile.go:530`) queues the
closing round's report, it appends one line per `KindSwitch` entry of that
round found in the passed `entries`, e.g.
`builder switched: A → B (rate-limited, gated until …)`. The line derives
from the switch entry's own record (reason + resolution); no new stored
fact. `switchEntry` (`internal/relevo/switch.go:103-114`) gains a comment
stating the rule: switches are never pending payloads, and their trace is
the report line.

Cases: round with one mid-round switch closes → report payload contains
the switch line and no separate switch entry is pending (wait returns the
report, one delivery, nothing left claimable); round with two switches →
two lines in order; round with no switch → payload byte-identical to
today (builder proves with a golden-ish assertion on the payload shape, or
names why the shape cannot be pinned).

## Seams

- `internal/relevo/switch.go` `switchEntry` :103-114 (comment only, plus
  whatever accessor the report line needs — the entry already carries
  reason + resolution in its note).
- `internal/relevo/reconcile.go` `queueReport` :530+ (line assembly;
  all `queueReport` callers keep working — the lines come from `entries`,
  not new parameters).
- `internal/relevo/wait.go` (read-only, for the test's wait assertion).
- Deliberately untouched: `Confirmed:true` on switch entries, pick
  entries, halt/broken messaging (#901), the unmarked rate-limit line.

## Ordered steps

1. Branch from `origin/main` tip, record SHA, commit this plan doc.
   Done when `git log --oneline -1` shows the base and status is clean.
2. Pin test first: switch mid-round, close, assert `Wait` returns the
   report containing the switch line AND no switch entry remains
   claimable/pending. Done when it fails (no line today).
3. Fix: report-line assembly + `switchEntry` rule comment. Mutations: skip
   the line assembly → pin test fails; queue a separate switch payload →
   the no-pending half fails. Done when the focused suites are green and
   each mutation fails exactly its named test.
4. Full verification: focused suites for every touched package, then
   `make check` and `sh scripts/check-comments.sh` green. Done when the
   report quotes the commands and outputs.

Constraints on all steps: pure-function tests with fakes only (no harness
spawn, no network — no `cmd/relevo` tests); no `#NNN` in code or comments;
test names say what they pin; new commits only.

## Deleted behaviour

Nothing. This round adds a report line; the no-queued-message rule is
written down, not newly introduced.

## Report must include

Commits with SHAs and the recorded base SHA; the exact switch-line format
chosen with an example; focused test commands and outputs plus `make check`
/ `check-comments.sh` outputs; each mutation and the named test it failed;
the no-switch payload-unchanged evidence.
