# Plan: never accept a round that has no usable work (slug: round-without-usable-work)

Two close paths can hand the mastermind a "done" that is not one: a round whose tree
still holds uncommitted work, and a round whose stream was cut inside its last turn.
This plan closes both, with the least movement each can take.

## 0. Scope

- Case 1 (done over dirty): the dirty close, previously guarded only for a chain
  writer closing on its own tree, is extended to both sides of the wire.
- Case 2 (stream cut mid-turn): the nudge, previously offered to every zero-code
  exit without a report, is narrowed to turns the stream itself shows ending
  cleanly.
- Chain red gate and repair: untouched (see §8).
- Deleted behaviour: none. The nudge path is narrowed to clean turn-ends but kept.

## 1. The dirty gate's coverage (the holes, verified)

`internal/relevo/chain_close.go:134-142` — the dirty gate covers a writer close only
when `rt.Git` can read the tree it owns: readers excluded, then `b.Serve != nil` and
`b.Builder.Remote()` excluded. Both exclusions name the same fact through two shapes:
for a served binding the tree belongs to the server (`served.go:62-109` reads
`b.Worktree` there), for a mirror-placed writer the round closed on that server and
only the pulled result commit and `DirtyCommit` fact reached the client.

- Serve side, case 1: a served writer's close runs `queueReport`
  (`reconcile.go:759-1144`) → `chainApply` (`chain.go:137-163`) →
  `chainEventFromCloseWF` (`chain_close.go:39-106`) on the server itself: the server's
  own chain row carries no `Server` field (`chain_served.go:243`, `chainRow` sets no
  column), so `chainOnServer` is false there and the event is built. The server can
  read the tree — `bindings.go:264` and `chain_served.go:277` set `CWD` to the served
  worktree, which `closeServedRound` already treats as readable (`served.go:81`). The
  exclusion is therefore dropped for served writers.
- Client side, case 1: a chain member whose builder is remote — the mirror member of a
  local chain, or the lone remote binding out of chain scope entirely — closes here
  only on the pulled facts. For a chain member the halt belongs on the close event;
  for a lone binding there is no chain to advance and the report must carry the fact.

## 2. Serve side: the dirty gate covers served writers

`chainDirtyGateApplies` (`chain_close.go:134-142`) loses its `b.Serve != nil` arm. The
reader arm and the `Builder.Remote()` arm stay — a mirror row's `CWD` is this
machine's own repo (`chain_server_start.go:514`), not the round's tree, so its dirty
answer would be about the wrong repository.

A dirty serve close now halts the chain exactly as a local one does: the writer branch
in `chainEventFromCloseWF` (the gate call at `chain_close.go:90-95`) returns the
`chainDirtyReason` wording (`147-150`, snapshot ref via `chainRoundSnapshotRef`
`174-176`), the event closes the step halted, and `chainAdvance`
(`chain_engine.go:137-161`) halts the row. Ordering note: `closeServedRound` runs after
the close returns (`headless.go:1399-1400`, via `markerClose`), so the snapshot ref the
halt names is written by the same tick's end and the wording never references a ref
that does not exist in the store's final state. The #886 shape — unreadable tree is
not a clean tree — comes along unchanged (`chainDirtyUnreadReason`, `155-158`).

Mutation check: the new test fails when the serve arm is put back; the two existing
pins (`TestWorkflowDirtyWriterCloseDoesNotAdvance`, `TestWorkflowCleanWriterCloseStillAdvances`)
keep the local arms.

## 3. Client side: a mirror done-close over pulled uncommitted work halts

`remote.ClosedRoundView.DirtyCommit` (`remote/chain.go:68-72`) — the newest round
completes from the binding view (`remote_catchup.go:356-374`) and each chain member
round then lands in `View.DirtyCommit` (`chain_pull_rounds.go:19-40`). Today that fact
informs the wire payload note only (`remote_catchup.go:342-344`) and the bundle's
allow-list (`remote_bundle.go:122-126`); the close itself advances.

The fact travels to the chain event: `chainCloseWF` gains `DirtyCommit`
(`chain_close.go:19-31`), `queueReport` gains it as a parameter and stamps it on the
close event (`reconcile.go:844-847`), and the one caller that holds it passes it —
`applyCatchUpReport` (`remote_catchup.go:274`, straight from `a.View`). Every other
caller sends "", exactly as before.

In the writer branch of `chainEventFromCloseWF`, beside the local gate, a non-empty
`wf.DirtyCommit` on a remote-builder close halts: `chainMirrorDirtyCloseReason`
returns the same-shaped wording — what is uncommitted (the file list read from the
round's sealed diff patch), the snapshot ref, the sealed diff key, the branch the work
never reached — and the chain halts instead of seeding the next member.

Scope notes, both verified:

- A served chain member's close returns early in `chainApply`
  (`chain.go:148-150`, `chainOnServer`): the client builds no event for it, so the
  server's own halt is the only one — one halt per round, nothing left to double.
  The payload gains the snapshot-ref line for that same round, which is not a halt.
- A lone remote binding (no chain member row at all) never reaches
  `chainEventFromCloseWF`; it records, it does not halt, and the report names the
  fact: `catchUpPayload` (`remote_catchup.go:313-346`) appends the snapshot ref to the
  mastermind payload, with the paths inside the round diff the payload already
  carries (the round diff for a dirty round includes the worktree, tagged by
  `DiffLineFromNote`'s tree clause `capture/capture.go:376-383`). The note keeps its
  existing ref line. This mirrors the local lone close, which has always recorded the
  diff note and gone on (`reconcile.go:897-937` — the `KindDiff` entry with its
  `Tree` mark, no halt ever).

Parity record for the MasterMind (local handling, unchanged): a LOCAL chain writer
close halts via `chainDirtyCloseReason` on a readable dirty tree; a LOCAL lone close
records the diff note in `queueReport` and closes done regardless. Both keep that
behaviour byte for byte; the remote halves now behave like their local twins.

Mutation check: with the mirror check deleted, a close carrying `DirtyCommit` advances
to the next member — the new test halts, fails on the deletion.

## 4. Case 2: the classifier (pure, table-tested)

The turn-end signals, per harness renderer (`internal/transcript/opencode.go:9-10`,
`claude.go:17-24`, `codex.go:13-16`, `agy.go:8-53`):

| kind | marks a clean turn end | the events that only appear mid-turn | marks the turn cut off |
|---|---|---|---|
| `opencode` | `type:"step_finish"` | `step_start`, `text`, `tool_use`, `reasoning`, `error` | (mid-turn events with no end) |
| `claude` | `type:"result"` | `assistant`, `user`, `error` | (mid-turn events with no end) |
| `codex` | `type:"turn.completed"` | `item.started`, `item.completed`, `error` | `type:"turn.failed"` — the turn itself said it did not finish |
| `agy` | `event:"result"` | `step_update` | (mid-turn events with no end) |

New `internal/transcript` helper (`turnend.go`): `TurnEndState(kind, stream) TurnEnd`
returns Clean when an end event appears; Unclean on positive evidence of the turn
being cut (a mid-turn event — or codex's `turn.failed` — followed by nothing), else
Unknown. Unknown is the verdict for an unknown kind, an empty stream and an
unreadable one, because an unclean verdict needs positive evidence, not absence of
evidence. Reading stream bytes keeps it beside the renderers that give the table its
shapes.

New `internal/relevo` classifier (`roundend.go`): `classifyRoundEnd` turns the
exit-without-report decision into a pure function of (turn end, code, report present,
marker present):

| # | turn end | code | report | marker | verdict |
|---|---|---|---|---|---|
| 1 | (any) | (any) | yes | – | report – closes on the report, as today |
| 2 | (any) | (any) | no | yes | marker – the `noreport` close of `closeOnMarker` (`reconcile.go:750-756`), kept |
| 3 | unclean | any | no | no | failure – skips the nudge even at code 0 |
| 4 | clean | non-`"0"` | no | no | failure – as today, no nudge |
| 5 | clean | `"0"` | no | no | nudge – the once-per-plan resume, kept |
| 6 | unknown | `"0"` | no | no | nudge – cleanliness could not be established, today's path |
| 7 | unknown | non-`"0"` | no | no | failure – today's path (`nudge.go:108`'s gate) |

`nudgeResume` keeps every gate it has (session, once-per-plan, limit `nudge.go:43-49`);
the classifier decides whether it is offered at all.

## 5. Wiring the classifier into the exit path

`reconcileHeadless`'s exited-without-report path (`headless.go:1035-1074`) reaches the
nudge call (`1229-1234`). The wiring reads the round's stream once — full read; unread
becomes Unknown — classifies, and calls `nudgeResume` only on a nudge verdict.
Everything below (the reader's undelivered halt `1236-1238`, the not-switchable halt
`1240-1242`, the outage gate, the exclusion + switch) stays exactly where it is: a
failure verdict falls into it today through `nudgeResume`'s own code gate, so only the
unclean-code-zero case changes its destination — from nudge to runner-failure path.
`closeOnMarker` and `queueReport` are not touched by this seam.

Mutation checks: the unclean-row close-path test fails when the classifier's unclean
arm is removed; the clean-row test fails when the wiring is blinded. Every classifier
row fails when its verdict is flipped.

## 6. Tests remain pure-function tests

CI has no harness binary and no network: the transcript helper and the classifier are
tables of bytes and values; the exit-path tests drive the existing fake; the
chain-close halt tests drive `chainEventFromCloseWF` / `queueReport` on the store and
fake git. No `cmd/relevo` test spawns a harness.

## 7. Coverage

No code moves packages. `RELEVO_REQUIRE_COVERAGE=1 sh scripts/check-coverage.sh`
reads `check-coverage: ok` against the go1.27 linux/amd64 baseline, so the baseline
is not regenerated and none of the packages drops below its recorded floor.

## 8. Red gate — not touched

`repair.go`'s `isRepairPlan` and budget, `checkClosed` (`internal/workflow/next.go:
310-331`), `reconcile.go:1080-1083` (passing-gate repair reset) and
`chain_resume_wf.go`'s repair identification are all out of scope; the focused suite's
repair and red-gate tests stay green and unchanged.

## 9. The case-1 recommendation, recorded

Halt, not auto-commit. A remote or served done-close over uncommitted work is halting
(NEEDS-YOU/unmarked with the `chainDirtyReason` shape), and the snapshot commit
(`[relevo] <name>: round N, uncommitted work`, `served.go:86-104`) stays the safety
net it already is rather than being pulled onto the branch: the builder never stood
behind that work, an auto-commit would advance a chain under the client's git
identity over work the chain's next member would then be told is finished, and the
#886 local behaviour already reads differently — a halt — so both sides behaving
alike is the cheaper pair to keep. The MasterMind decides (commit / repair / resend).
