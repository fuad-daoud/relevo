# Plan: fix #846 — a finished remote round never closes after a resend over NEEDS YOU

Base `origin/main` `213b8418`. New commits only; no remote branch amended or rebased.

## Root cause (reproduced first, then read out of the failures)

The seed's theory was half right, and the reproduction corrected the half that
was wrong.

**Wrong in the seed:** that `remoteRecord` leaves the client at NEEDS YOU. It
does not — it writes `State = StateActive` (`internal/relevo/remote_send.go:231`
at base). What it left behind was the *rest* of the halt's bookkeeping:
`HaltAt`, `HaltNotifiedRound`, `StalledSince`, `StopRequestedAt`,
`RoundClosedTree`, `LandedAt`/`LandedPR`, `Progress`, `ExploringSince`,
`StaleSince`/`StaleNotifiedAt`, `FinishPending` and `QueuedAt` all survived a
send that started a fresh process. So the send-side half of the fix was the
client's full parity reset, not the state word — and the state word alone was
never the bug.

**Right in the seed, and the actual strand:** a halt and a close are not
exclusive on the wire, and every stage of the collect path treated them as if
they were.

| stage | file:line at base | what it did |
|---|---|---|
| server view | `internal/relevo/served.go:22-24` | `RoundStateOf` returned `RoundNeedsYou` **before** it ever looked at `Serve.ClosedRound > Serve.AckedRound` at `:33-35`, so an un-acked close was invisible. |
| fetch | `internal/relevo/remotefetch.go:79-91` | a catch-up was fetched only for `RoundClosed` (or `RoundIdle` with a missing report); any other state returned early at `:89-91`. A `needs_you` view carrying a close skipped the fetch. |
| apply | `internal/relevo/remote_sync.go:345-348` | the `RoundNeedsYou` arm halted and returned `deliver=false` without consulting `ClosedRound` at all. |
| wait | `internal/relevo/wait.go:94-131` | `WaitOutcome` reached `view.WaitingOn` at `:115-117` and answered exit 3, because the client had halted itself on the close it never collected. |

The shape that produces it is `internal/relevo/reconcile.go`: the close sets
`State = StateActive` and advances the round at `:663-664`, and then the reader
artifact cap (`:724-728`), a scope refusal (`:733-735`) or a failed admit
(`internal/relevo/queue.go:72-85`) halts the *new* round. The server's copy is
then NEEDS YOU with an un-acked close, which is exactly the state none of the
three stages above could carry.

## Behaviour and cases

1. A plain or `--candidate` send that starts (or restarts) a round moves both
   copies out of NEEDS YOU: the client's copy (`remoteRecord`) to full `Send`
   parity, the server's copy (deferred `Send` in `finishRoundStart` plus
   `Admit`). Status stops saying NEEDS YOU and `wait` stops exiting 3 on the
   stale halt.
2. A round with a done marker closes whatever the earlier state was. The server
   reports an un-acked close even when the binding has since halted again; the
   client collects a fetched close even when its local state is NEEDS YOU —
   catch-up first, halt second.
3. `status`, `history`/`show --report` and `stop` agree on open vs closed: they
   read one shared predicate.

## The unified open-round predicate

`store.RoundOpen(entries, round)` in `internal/store/log.go` is the single
owner: a prompt entry for the round with no report entry. `store` is the only
package all three readers can import — `relevo` imports `view`, `view` imports
neither, and `ingest` imports neither — so it is the only place a shared
definition can live without a cycle. Its two halves, `store.HasPrompt` and
`store.HasKind`, are exported because readers legitimately ask about one kind
at a time.

Call sites that now read it:

| reader | site |
|---|---|
| `stop` | `internal/relevo/stop.go` `stopDecision` — now takes the log |
| served view | `internal/relevo/served.go` `RoundStateOf` |
| daemon | `internal/relevo/headless.go` `roundOpen`, `internal/relevo/chain_wait.go`, `internal/relevo/remote_gates.go`, `internal/relevo/remote_sync.go` (unreachable arm) |
| `--candidate` refusal | `internal/relevo/builder_change.go` `roundOpenIn` |
| history | `internal/ingest/outcome.go` `deriveOutcome` (documented against the shared definition) |

`relevo.HasPromptEntry` and `relevo.HasEntry` remain, now thin wrappers over
`store.HasPrompt`/`store.HasKind`, so their many existing callers are unchanged.

The log is the whole answer, not the binding's state word: a round with a prompt
and no report is open whatever the state says, so a halt, pause or broken
binding still has a live round, and a round that has reported is closed whatever
the state became since. `stopDecision` previously keyed on `QueuedAt` and
`RoundStartedAt`, which is how it came to say "nothing to stop" on a round that
`status` and `history` called open.

Two existing tests encoded the old, disagreeing side and were corrected to
close their round in the log rather than by clearing a timestamp:
`TestStopNothingToStop/no_open_round` and
`TestChainStopWithAClosedMemberRoundMarksTheChainStopped`.

## Seams

- Send-side reset: `internal/relevo/remote_send.go` `remoteRecord`.
- Server precedence: `internal/relevo/served.go` `RoundStateOf`.
- Fetch gate: `internal/relevo/remotefetch.go` `fetchRemote` — collect by the
  `ClosedRound` the view names, not by the state word. A running or queued round
  has no close to collect and keeps its log mirror; a round whose report the
  client already holds is not fetched again.
- Apply order: `internal/relevo/remote_sync.go` the `RoundNeedsYou` arm.
- Shared predicate: `internal/store/log.go`; `internal/ingest/outcome.go`;
  `internal/relevo/{stop,served,headless,chain_wait,remote_gates,remote_sync,builder_change}.go`.

`internal/relevo/chain_send_remote.go` was left alone: the plan flagged it as
probably a different bug, and no evidence for it turned up.

## What was deleted

Nothing. No flag, command, state or behaviour was removed. The reset in
`remoteRecord` clears fields that were already being set by the local `Send`
branch, so the two halves now agree; no field is gone.

## Tests

`internal/relevo/round_reopen_test.go` (new) and `internal/store/roundopen_test.go`
(new). Each of the three reproductions has one mutation recorded in the round's
report, and every mutation was run and observed to fail its named test.

## Verification

- `go test ./internal/relevo/ ./internal/serve/ ./internal/view/ ./internal/ingest/` — green.
- `go test ./internal/...` — green.
- `make check` — green, no new lint or filesize exclusion.
- Coverage baseline **not** regenerated: no code moved packages, and the one
  `check-coverage` note (`internal/pathscope: not in baseline`) is pre-existing
  on the base commit.