# Plan: `relevo push --ack` replaces the stdin ack (#955, slice 1b, Go only)

One builder round, Go only. `CLAUDE.md` + `docs/runbook.md` bind: export `TMPDIR=$HOME/.cache/relevo-chains/tmp GOTMPDIR=$HOME/.cache/go-tmp` before the full runs; focused package tests while iterating; `make check` + `make e2e` once at the end; no `cmd/relevo` test spawns a harness or reaches the network; no new lint/comment/filesize/coverage exclusion. All work lands as **new commits** on top of HEAD (`68b6232e`; the merged push work `a3c02e29` is history) — no amend, rebase or force-push of anything a remote binding absorbed. Final deliverable includes this plan saved verbatim to `docs/plans/2026-10-09-relevo-push-ack.md`, committed with the code.

## Seed vs. tree

Verified true in this tree: the stdin ack path (`internal/delivery/pushrun.go:245-263` `awaitAck`, `:281-295` `pushLines`, `:395-405` `ackSeq`, stdin-EOF ends `run()` at `:144-147`); `cmd/relevo/push.go:59` wires `os.Stdin`; the `relevo: <code>: <message>` / `--json` frame (`cmd/relevo/main.go:140-175`, `cmd/relevo/clierror.go`); `resolveMCPMasterMind` (`cmd/relevo/mcp.go:140`); the orphan-admit clear (`pushrun.go:411-426`); e2e `startPush`/`pushClient` (`internal/e2e/headless_test.go:679-813`); the help-json push row. Three points the plan decides or flags rather than guesses:

1. **Decision 2's literal "clearing its unacked admits" on a claim-loss exit contradicts the tree's double-delivery invariant.** If the claim was lost, a successor holder may already have run `clearStaleAdmits` (`pushrun.go:349-368`) and re-admitted the same entry; an unconditional clear by the loser makes that entry claimable while the successor is writing its line — exactly the double delivery Round 2 Fix 1 pinned (`TestRunPushNeverPullsAndWritesOneEntry`). The plan therefore guards the exit clear with "own claim still live": on a ctx/write-failure exit the clear runs as today (the `Remove` follows it, defer LIFO at `pushrun.go:81,103`); on a claim-loss exit it is skipped, and the successor's `clearStaleAdmits` or `clearOrphanAdmit` (no live claim) returns the entries to pending — the intent of the decision, without the race. Named in the report.
2. **Idempotent retry vs. claim liveness.** The decision's conditions gate the *confirm act*; an entry already confirmed with route `push` is settled and can never return to claimable, so the idempotent success does **not** require a live claim (otherwise a mod retrying an ack whose first run succeeded just as the holder exited would get a spurious error). Binding-ownership and seq checks still apply to that branch. Pinned by its own test; named in the report so the owner can veto.
3. **The mutation "make RunPush end on stdin EOF again"** can only be performed by reintroducing what step 2 deletes: an `in io.Reader` parameter fed an already-EOF reader plus the `run()` EOF-return branch. The plan words the mutation that way and names the test it must fail.

## Behaviour and cases

**Part A — `relevo push --ack <binding> <seq>` (one-shot verb).** Resolves its MasterMind like every verb (`--mastermind`, `$RELEVO_MASTERMIND`, then host/session — the mod passes `RELEVO_MASTERMIND`). New `delivery.AckPush(d Deps, mastermindID, binding string, seq int) error` confirms the entry with route `push` inside ONE `Store.WithLock` (claim `Live` inside the lock has precedent: `clearOrphanAdmit`, `pushrun.go:411-426`; `KVClaims` rides the same store-root database, `cmd/relevo/wire.go:459`), checking in this order:

1. `tx.Load(binding)`: absent → error wrapping `store.ErrNotFound`; `b.MasterMindID != mastermindID` → `ErrAckForeignBinding`.
2. Scan `tx.ReadLog(binding)` for `Seq == seq && Direction == DirToMasterMind`; none → `ErrAckUnknownSeq` (this also covers seq 0 state lines and to-runner entries — state lines need no ack and are never ackable, decision 4).
3. Entry `Confirmed`: `Route == "push"` → return nil (idempotent retry, no claim check — seed point 2); any other route → `ErrAckAlreadyConfirmed`.
4. `AdmittedAt == nil` → `ErrAckNotAdmitted`.
5. `d.Channels == nil` or `Live(mastermindID, d.Now())` not live → `ErrAckNoClaim`.
6. `tx.ConfirmIndex(binding, idx, "push")`; success prints nothing, exit 0.

Error codes (the repo's frame: `cliError` + catalog, rendered `relevo: <code>: <message>` or the `--json` envelope; every new catalog row exit 1, no next hint; the registry row's `Errors` lists them all):

| case | sentinel (`internal/delivery`) | code (`cmd/relevo`) |
|---|---|---|
| binding does not exist | wrapped `store.ErrNotFound` | `binding_not_found` (existing; `writeError` already maps it, `cmd/relevo/args.go:286`) |
| binding is another MasterMind's | `ErrAckForeignBinding` | `mastermind_mismatch` (new) |
| no mastermind-bound entry with that seq | `ErrAckUnknownSeq` | `push_seq_not_found` (new) |
| entry already confirmed with another route | `ErrAckAlreadyConfirmed` | `push_already_confirmed` (new) |
| entry not (or no longer) admitted | `ErrAckNotAdmitted` | `push_not_admitted` (new) |
| no live push claim (confirm path) | `ErrAckNoClaim` | `push_no_claim` (new) |
| wrong positional count, non-integer seq, positional without `--ack` | — | `usage` (existing, exit 2, refused before any runtime is built) |

**Part B — the long-lived `relevo push` never reads stdin.** `RunPush(ctx, d Deps, mastermindID string, out io.Writer) error` (the `in io.Reader` parameter is deleted; `runPush` keeps its `claimSeam` test seam). After `writeEntry` it waits for that entry's confirm by polling `Store.ReadLog(binding)[idx].Confirmed` every **`pushConfirmPollEvery = 100 * time.Millisecond`** (new constant; a while-live claim only the ack verb can confirm an admitted entry, so `Confirmed` alone is the signal), deletes it from `unacked`, and moves on. `refreshPushClaim` (`pushrun.go:266-278`) keeps refreshing unchanged. It ends on ctx (SIGINT/SIGTERM at `cmd/relevo/push.go:49`) or when it loses the claim — a shared `claimHeld` helper (`Live` answers without error, claim non-nil, `PID == os.Getpid()`; a transient `Live` error counts as held) checked in the confirm poll and on the idle `pushPollEvery` tick; both stop paths return nil after the guarded `clearUnackedAdmits` and the claim `Remove`, as today. SIGKILL stays covered by the merged orphan-admit clear. Unchanged (decision 5): the line format (`PushEvent`), the one-lock find-and-admit (`nextAdmitted`/`claimAndAdmitOne`), `clearStaleAdmits` on start, state lines (seq 0, no ack, single writer between entries), `clearOrphanAdmit`, MCP `wait`, CLI `relevo wait`.

## Seams (file : lines : what)

- `internal/delivery/pushrun.go` — `:33-40` consts + `errPushStop` (reworded: ctx/claim-lost stop, stdin meaning gone); `:55-57` `RunPush` signature; `:62-105` `runPush`; `:108-120` `pushRun` (`lines` field deleted); `:125-151` `run` (lines-select case `:144-147` deleted, claim-lost check added); `:156-171` `step`; `:177-189` `writeEntry` (unchanged); `:245-263` `awaitAck` → new `awaitConfirm`; `:281-295` `pushLines` deleted; `:388-392` `clearUnackedAdmits` (guarded); `:395-405` `ackSeq` deleted; `:349-368`, `:411-426` unchanged (relied on).
- `internal/delivery/pushack.go` (new) — `AckPush` + the five sentinels.
- `internal/delivery/channel.go:14,25-55` — `ClaimTTL`, `Claim`, `ClaimStore.Live` semantics (read-only dependency).
- `internal/store/log.go:318-336,362-388,539-600` — `ConfirmIndex`/`AdmitIndex`/`ClearAdmitIndex`, `Tx` wrappers; `internal/store/lifecycle.go:138` `tx.Load`, `:385-398` load wraps `ErrNotFound`, `:108-119` `read` needs no flock (safe confirm-poll).
- `cmd/relevo/push.go:16-25,31-67` — `--ack` flag, `cmdPushAck` branch, `os.Stdin` dropped from the `RunPush` call (`:59`).
- `cmd/relevo/clierror.go:14-37,49-72` — five new codes + catalog rows.
- `cmd/relevo/args.go:251+` — `writeError`: five new sentinel cases (it is the one classifier for write verbs; `cmd/relevo` already imports `internal/delivery`).
- `cmd/relevo/main.go:61-62` — usage push line; `:140-175` report frame (unchanged, relied on).
- `cmd/relevo/registry.go:84` + `cmd/relevo/registry_rows.go:455-462` — push row: `Args: "[--ack <binding> <seq>] [--mastermind ID]"`, `Flags` + `--ack`, `Errors` alphabetical: `binding_not_found, internal, mastermind_mismatch, push_already_confirmed, push_no_claim, push_not_admitted, push_seq_not_found, usage`; `cmd/relevo/testdata/contract/help-json.golden` regenerated (`registry_test.go:247` requires every named code to have a catalog row).
- `cmd/relevo/mcp.go:140` — `resolveMCPMasterMind` (reused as-is, including its current prose + exit-2 failure path at `push.go:43-47`).
- Tests reworked: `internal/delivery/pushrun_test.go:79-146,248-296,481-533,625-674`; `internal/mcp/wait_push_test.go:74-138`; `internal/e2e/headless_test.go:131-142,165-183,679-813` (`pushClient.in`/`ack` → `delivery.AckPush`; the holder now ends on the scenario's existing `cancel()` at `:139-142`). Helpers reused: `deliver_test.go:14-65` (`fakeClaimStore`, `routeRuntime`, `seedPending`), `channel_test.go:14` `testClaimMasterMind`.
- `cmd/relevo/push_test.go` (new) + `cmd/relevo/contract_write_test.go:373-415` (five new classifier rows).
- `README.md:443-445` (verb entry) and `:2595-2599` (push paragraph: the mod acks with `relevo push --ack <binding> <seq>`); `docs/plans/2026-10-09-relevo-push-ack.md` (new). `claude-plugin/README.md` and `internal/mastermind/guide.md` carry no ack references (checked) and stay untouched.

## Ordered steps (deliverable + how to know it worked)

1. `internal/delivery/pushack.go` + `pushack_test.go`: `AckPush`, the five sentinels, and one test per rule — `TestAckPushConfirmsAdmittedEntry`, `TestAckPushIdempotentOnPushedEntry`, `TestAckPushIdempotentWithoutLiveClaim`, `TestAckPushRefusesForeignBinding`, `TestAckPushRefusesUnadmittedEntry`, `TestAckPushRefusesWithoutLiveClaim` (admitted, unconfirmed, no claim → refused, entry stays admitted for the orphan clear), `TestAckPushUnknownSeq`, `TestAckPushRefusesEntryConfirmedByAnotherRoute`, `TestAckPushUnknownBinding`; know it worked when `go test ./internal/delivery/ -run TestAckPush -count=1` is green.
2. Rework `pushrun.go` per Part B (stdin deleted, `awaitConfirm` at 100 ms, claim-lost exit via `claimHeld`, guarded exit clear) and rework/extend `pushrun_test.go`: rework `TestRunPushAdmitsWritesConfirmsOnAck` (ack via `AckPush`, exit via cancel), `TestRunPushRestartResendsUnackedEntry`, `TestRunPushSecondHolderRefused`, `TestRunPushNeverPullsAndWritesOneEntry`, `TestRunPushStateLineWaitsForEntryAck` (ack via `AckPush`); add `TestRunPushDeliversTwoEntriesInOrderAsAcked` (nothing on stdin anywhere, holder demonstrably still running between entries, binding-name order, no second line before the first ack), `TestRunPushCancelClearsUnackedAdmit`, `TestRunPushEndsWhenItLosesTheClaim` (claim overwritten/removed → clean nil return; the unacked entry stays admitted for the successor/orphan clear, pinning the seed-point-1 guard); know it worked when `go test ./internal/delivery/... -count=1` is green.
3. Wire the CLI: `push.go` (`--ack` flag, `cmdPushAck` — positional/integer validation before `newRuntime`, `writeError` mapping, silent exit 0; long form refuses positionals with `usage`; `os.Stdin` dropped), `clierror.go` five codes + catalog, `args.go` `writeError` cases, `main.go:61-62` usage line; add `cmd/relevo/push_test.go` usage-shape tests (`--ack` with 0/1 positionals, non-integer seq, positional without `--ack`) and five rows in `TestContractWriteUnclassifiedIsInternal` — every test here is flag parsing or the pure `writeError` classifier: no `cmd/relevo` test spawns a harness or reaches the network; know it worked when `go test ./cmd/relevo/ -count=1` is green.
4. Update the registry push row and regenerate the golden with `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update`; know it worked when `go test ./cmd/relevo/ -count=1` is green and the golden diff shows only the push row.
5. Rework `internal/mcp/wait_push_test.go:74-138` to ack through `delivery.AckPush` and end the holder via cancel; know it worked when `go test ./internal/mcp/... -count=1` is green.
6. Rework the e2e push half (`pushClient` carries deps + mastermind id, `ack(t, ev)` calls `delivery.AckPush`, `startPush` drops the ack pipe); know it worked when `go test ./internal/e2e/ -run TestHeadlessE2E -count=1` is green.
7. Reword `README.md:443-445` and `:2595-2599` for the ack verb; know it worked when `grep -rn "ack <seq>" README.md` is empty (docs/plans history aside).
8. Three mutation checks, each reverted after: (a) drop the MasterMind-ownership check in `AckPush` → `TestAckPushRefusesForeignBinding` fails; (b) drop the admitted check → `TestAckPushRefusesUnadmittedEntry` fails; (c) make RunPush end on stdin EOF again (reintroduce an `in io.Reader` fed an already-EOF reader plus the `run()` EOF-return branch, per seed point 3) → `TestRunPushDeliversTwoEntriesInOrderAsAcked` fails; know it worked when each mutation flips its named test red and the revert is green.
9. Save this plan verbatim to `docs/plans/2026-10-09-relevo-push-ack.md`, export `TMPDIR`/`GOTMPDIR` per the runbook, run `make check` then `make e2e`, and commit code + tests + plan as new commit(s); know it worked when both exit 0, `git status --porcelain` is clean, and `git show --stat HEAD` lists exactly the declared scope plus the plan.

Focused command while iterating: `go test ./internal/delivery/... -count=1` (then per package being reworked: `./internal/mcp/...`, `./cmd/relevo`, `./internal/e2e -run TestHeadlessE2E`); final full commands once: `make check`, then `make e2e`.

Declared diff scope: `internal/delivery/{pushrun.go, pushack.go (new), pushrun_test.go, pushack_test.go (new)}`, `cmd/relevo/{push.go, push_test.go (new), clierror.go, args.go, main.go, registry_rows.go, contract_write_test.go, testdata/contract/help-json.golden}`, `internal/mcp/wait_push_test.go`, `internal/e2e/headless_test.go`, `README.md`, `docs/plans/2026-10-09-relevo-push-ack.md` (new). Coverage: the new tests should hold `internal/delivery` at or above `testdata/coverage-baseline.txt`; if the gate trips, regenerate only per CLAUDE.md's rule (`sh scripts/check-coverage.sh --write`) and say so in the report — never lower a baseline to get green.

## What is deleted (closed list)

1. `RunPush`/`runPush`'s `in io.Reader` parameter and `os.Stdin` in the `cmd/relevo/push.go:59` call.
2. `pushRun.lines`, `pushLines` (`pushrun.go:281-295`), `ackSeq` (`:395-405`), `awaitAck` (`:245-263`, replaced by `awaitConfirm`), and the `case _, ok := <-p.lines` branch of `run()` (`:144-147`).
3. `errPushStop`'s stdin-EOF meaning and text (replaced by ctx-cancel and claim-lost clean stops).
4. The stdin ack plumbing in tests: `pushrun_test.go` ack pipes and `fmt.Fprintf(ackWriter, "ack %d\n", …)` writes (`:89-97,123-134,258-278,493-532,637-673`), `internal/mcp/wait_push_test.go:101-137`'s pipe pair and ack write, `internal/e2e/headless_test.go` `pushClient.in` and its `ack` write (`:683,706,717,734-739,782-788`).
5. `README.md:443-445`'s "confirming each on an `ack <seq>` line from stdin" sentence.
6. Nothing else; every commit already on a remote binding's branch stays untouched — no amend, no rebase, no force-push.

## Report must include

- `git diff --stat` against the declared scope; every file outside it named with why.
- Every new/reworked test by name with its focused-command result, and the `make e2e` result.
- Each of the three mutation checks: what was broken, the named test that failed, revert confirmation.
- The seed-point-1 note: the guarded exit clear, why it deviates from decision 2's literal wording, and the test pinning it (`TestRunPushEndsWhenItLosesTheClaim`); the seed-point-2 note: idempotent retry succeeds without a live claim, pinned by `TestAckPushIdempotentWithoutLiveClaim`.
- Coverage-baseline arithmetic: regenerated with `--write` or untouched, and why.
- Confirmation that no `cmd/relevo` test spawns a harness or reaches the network, no commit was amended/rebased/force-pushed, and no lint/comment/filesize/coverage exclusion was added.
- Anything this plan said that the tree proved false.

## MasterMind corrections (review of this plan; these override anything above)

1. **`awaitConfirm` also ends when the entry is no longer admitted.** If the
   entry's admit is cleared by someone else while the holder waits (for
   example the orphan clear ran during a moment the claim read as not live),
   the entry is claimable again and the mod's ack would now fail
   `push_not_admitted`; waiting on `Confirmed` alone would block forever. So
   the poll returns when the entry is `Confirmed` (any route) OR its
   `AdmittedAt` is nil; in the second case drop it from `unacked` and let the
   next step re-find it (it is re-admitted in one lock and re-sent with a new
   line). Test: `TestRunPushReSendsEntryWhoseAdmitWasCleared` (clear the admit
   with `ClearAdmitIndex` while the holder waits; assert a second line for the
   same seq, then ack it and assert one confirm route `push`). Mutation: drop
   the `AdmittedAt == nil` exit; the new test must fail.
2. The plan's two deviations from the seed (guarded exit clear on claim loss;
   idempotent ack without a live claim) are approved as written.

## Round 2

Same branch, on top of ec1ed92c; new commit only (no amend/rebase/force-push).
Append a "## Round 2" section holding this plan verbatim to
`docs/plans/2026-10-09-relevo-push-ack.md` in the same commit.

Found by the MasterMind's sandbox run: `relevo push --ack ev1 4 --json`
fails with `flag provided but not defined: -json` (exit 2). The injected guide
and `relevo help --json` promise every verb takes `--json`; `push` does not.

- Add `--json` to `push` the way the other write verbs do (find the shared
  pattern in `cmd/relevo`; do not invent a new one).
  - `--ack` with `--json`: on success print ONE document, e.g.
    `{"binding":"ev1","seq":4,"route":"push","already_confirmed":false}`
    (`already_confirmed` true on the idempotent retry -- `AckPush` must say
    which happened; adjust its return without changing its rules). Errors use
    the existing `{"error":{"code","message","next"}}` envelope. Without
    `--json`, success stays silent and exit 0.
  - The long form with `--json`: accepted; the stream is already NDJSON, so it
    changes nothing but startup-failure errors, which use the JSON envelope.
- Registry row: add `--json` to `Flags`, set the `output` document name the
  repo's convention uses; regenerate `help-json.golden`.
- Is there a test that every registry verb lists `--json`? If not, add one in
  `cmd/relevo` (pure: reads the registry, spawns nothing) so the next verb
  cannot ship without it -- and if any OTHER verb fails it, list them in the
  report and exempt nothing silently: stop and report instead of editing them.
- Tests: `--ack ... --json` success document (fresh and idempotent), an error
  in the JSON envelope, long form accepts `--json`. Mutation: drop the
  `already_confirmed` assignment; name the failing test.
- `go test ./cmd/relevo/ ./internal/delivery/ -count=1`, then `make check`
  and `make e2e`. Report tests, the mutation, `git diff --stat ec1ed92c..HEAD`.

## Round 3

Same branch, on top of 3b46fb78; new commit only (no amend/rebase/force-push).
Append a "## Round 3" section holding this plan verbatim to
`docs/plans/2026-10-09-relevo-push-ack.md` in the same commit.

### 1. Data race in the test claim store (required)

The MasterMind's `make check` on zen failed `internal/delivery` under `-race`
(intermittent; 3 reports, all the same pair):
- write: `fakeClaimStore.Write` (`internal/delivery/deliver_test.go:21`) from
  `refreshPushClaim` (`pushrun.go:320`, goroutine started at `pushrun.go:95`);
- read: `fakeClaimStore` map access from `(*pushRun).claimHeld`
  (`pushrun.go:306`) inside `awaitConfirm` (`pushrun.go:287`).
`fakeClaimStore` is a bare map with value-receiver methods; `claimHeld` (new
in this branch) reads it concurrently with the refresh goroutine. Production
`KVClaims` is safe; this is test-only.

- Make the fake safe for concurrent use (a mutex-guarded struct; update every
  construction site). Do NOT change production code for this.
- Proof: `go test -race -count=20 ./internal/delivery/` clean, and say how
  long it took. Mutation: remove the lock; the same command must report the
  race (quote one line of it).

### 2. Split the --json exemptions (owner-approved)

In `cmd/relevo/push_ack_json_test.go` replace the single exemption map with two:

- `jsonExemptByDesign` (11): `board`, `config secret`, `config server`,
  `config workflow`, `mastermind` (dispatch-only parents); `daemon`, `serve`,
  `serve ui`, `ui`, `mcp`, `board url`, `mastermind notice` (own a stream or
  terminal, or print the machine answer itself). Keep each reason.
- `jsonNotYet` (8): `config rollback`, `board annotate`, `board comment`,
  `board promote`, `config edit`, `config workflow add`, `config workflow
  edit`, `config workflow rm`. Reason for each: "not yet converted" -- a write
  verb can return what it wrote, as `push --ack --json` does.
- A comment above `jsonNotYet` says the list only shrinks: a verb leaves it
  when converted, and no verb is ever added (same rule CLAUDE.md gives lint
  exclusions). Add a test that fails if `jsonNotYet` names a verb that DOES
  take `--json` (so a converted verb must leave the list), and one that fails
  if any name in either map is not a registry verb.
- Count check: the two maps hold exactly the 19 verbs round 2 found; if the
  tree now differs, stop and report.

### 3. True guide line

`internal/mastermind/guide.md:32` says every verb takes `--json`. Reword to a
true statement in the guide's voice, e.g. "Every verb that prints a result
takes `--json`", keeping the rest of the bullet. Regenerate whatever golden or
shipped copy embeds the guide (find it; `scripts/agents-shipped.sh --write` if
that is the mechanism) and say which.

### Finish

`go test ./cmd/relevo/ ./internal/delivery/ ./internal/mastermind/ -count=1`,
the `-race -count=20` run, then `make check` and `make e2e`. Report each,
the mutation, and `git diff --stat 3b46fb78..HEAD`.
