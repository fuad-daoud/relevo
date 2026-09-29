# Plan: a halted served round accepts a new candidate on send (#687)

One builder round, one commit, the plan committed verbatim as
`docs/plans/2026-09-29-bug-sweep-halt.md` in that commit, body `Fixes #687`,
nothing pushed, `make check` once at the end. Anchors below were verified in
this tree (`19a16eec`); re-check a line before editing. Halt and report if a
step is impossible as written.

**Chosen lever: (b) — `relevo send --candidate <token>` re-points a halted
served round.** A halt is a human decision point, and the decision the incident
needed was "continue *this* round on a candidate I name": the round number, its
plan and its tree stay, the candidate that just failed is not spawned again, no
automatic switch fires, and the mastermind gets no `noreport stopped` close it
must re-dispatch. Lever (a) is honest about a dead process but ends the round
the human was trying to continue, and it would close a round the mastermind is
still waiting on; locally `stop` already ends a halted round, so (a) gives no
local human anything new. Lever (b) removes exactly one refusal — the preflight
blanket "round N open" for `--candidate` — and keeps it everywhere a human can
still act through `stop`: every local binding, and every served round that is
queued or running. A human who wants to abandon a halted served round instead of
re-pointing it keeps `relevo unbind`, since the stop refusal stays.

## 0. Seed vs tree (read this first)

1. **The client guard is the server's guard.** The serve round handler calls
   the same `relevo.Send` with the candidate (`internal/serve/rounds.go:367`,
   `SendOptions{Builder: req.Candidate}`), and `sendPreflight` refuses at
   `internal/relevo/send.go:222` before either side does anything. A client-only
   change cannot restore the lever; the rule must be expressed on the binding.
2. **"Served" has two spellings, and the rule needs both.** The server's copy of
   a served binding carries `Owner` (`internal/serve/bindings.go:217`) and a
   headless builder; the client's copy carries `Builder.Mode == ModeRemote` and
   no `Owner` (`internal/relevo/remote_add.go:334-339`). The predicate uses the
   codebase's own "not local" test, `Owner != "" || Builder.Remote()`
   (`internal/relevo/runningprocs.go:15-27`), with a comment saying why.
3. **The serve handler needs no source change.** `roundStartDecisionOf`
   (`internal/serve/rounds.go:110-125`) already classifies a NEEDS YOU round as
   proceed and keeps `startOpen` for a running round; the serve test still drives
   the handler end to end.
4. **Decision 2's "every local binding keeps today's semantics" is what scopes
   the exemption.** A local binding halted on an open round keeps the refusal
   (its documented `stop` then `send --candidate` path is unchanged). Widening
   the one-step lever to local bindings is deliberately not taken; it goes in the
   report as deferred.
5. **Lever (a) is deliberately not taken.** `handleStop` keeps answering 409
   `round_halted` for a NEEDS YOU round (`internal/serve/bindings.go:456-458`)
   and the client keeps its mapping (`internal/relevo/stop.go:107-110`).
6. **The seed's anchors hold and the cap is moot for (b).** `send.go:222` is the
   guard; `bindings.go:456` is the stop refusal; `bindings.go` is 553 lines and
   this plan adds nothing there. `internal/relevo/send.go` is 844 lines but is in
   `scripts/check-filesize.allow` and gains no line; `internal/relevo` is
   excluded from golangci-lint, so the new predicate has no funlen risk, while
   the new test in `internal/serve` does (tests are linted there).

## 1. Behaviour and cases

### Server path — `POST /v1/bindings/{name}/rounds` (round N, plan, `candidate=<token>`) on a served NEEDS YOU binding whose process is gone

- The decision function already reads it as proceed, before anything is
  absorbed: not a `round_open` (nothing is running) and not an identical retry.
  A running round with a different plan stays 409 `round_open`; an identical
  retry stays 200.
- The candidate is validated against the server's actor/registry/policy as
  today (`rounds.go:181-187`): one it cannot serve is 422 `invalid`, absorbing
  nothing.
- `relevo.Send` (`Defer: true`) now passes both guard sites for this binding:
  `applyBuilder` moves it to the named candidate, round N's plan is restaged,
  the pick and prompt entries are appended under round N, the binding goes
  ACTIVE with `Halt`/`HaltAt` cleared and `RoundSwitches`, `RoundExcluded`,
  `HaltNotifiedRound` reset, `RoundStartedAt` fresh; `Round` does not move.
- `admit` starts the named candidate: exactly one new process, the failed
  candidate is not spawned, no switch. The 201 view is active/`running` with
  `Candidate` = the requested token.
- A spawn failure on the named candidate answers 409 `round_halted` carrying the
  new halt text (`writeSendError`'s default branch) and leaves NEEDS YOU; the
  client writes nothing locally (`remote_send.go:132-138`).
- A report or done marker already on disk for round N still refuses the send
  (`ErrReportPending` → 409 `round_open`): the close wins over the re-point.
- No runner → 503 `no_runner`; a queued round → today's behaviour, untouched.

### Client preflight — one pure rule

`candidateSendRefused(b store.Binding, entries []store.LogEntry) bool` in
`internal/relevo/builder_change.go`, called by both guard sites: true when the
round is open (`roundOpenIn`) and the binding is not a served binding in
NEEDS YOU.

| Round | Binding | Result |
|---|---|---|
| no prompt / closed by a report | any | allowed (today) |
| open | active, local or served | refused, before any staging or spawn (today) |
| open | queued | refused (today) |
| open | NEEDS YOU, served | **allowed (new)** |
| open | NEEDS YOU, local | refused (today, decision 2) |

The client's copy must have seen the halt: a copy still showing ACTIVE keeps the
refusal until the next sync (`relevo status` shows NEEDS YOU), then the re-point
passes. `--dry-run` shares the preflight, so it now describes the re-point
instead of refusing. A refusal still writes nothing and spawns nothing.

### Log, status and report after recovery

- Round N's log gains the pick entry (named candidate) and the prompt entry
  (restaged plan) after whatever the halted attempt wrote; nothing is removed.
- The client records the server's canonical candidate and clears its halt
  (`remote_send.go:159-176`, `194-201`), so the next sync logs no spurious
  switch.
- `relevo status` and `relevo wait` show ACTIVE on the named candidate at round
  N: no NEEDS YOU, no halt line, no waiting row.
- No report entry is written by the recovery (unlike a stop, which queues
  `noreport stopped` and advances the round): round N continues and its report,
  when it closes, is the ordinary one. A later halt in round N notifies again.

### Refusals that must stay

- open + active or queued, local or served: the preflight refusal, and the
  server's 409 `round_open`.
- open + NEEDS YOU + local: the preflight refusal.
- `stop` on a halted served round: 409 `round_halted`; `stop` on an idle or
  closed round: 409 `nothing_to_stop`.
- a start that cannot spawn: 409 `round_halted`; a send while a report or marker
  is on disk: 409 `round_open`.

### Version skew

An old client (its own guard) never reaches the server; a new client against an
old server is answered 409 `round_halted` and words it "round N could not start
on <server>", writing nothing. No protocol feature is added.

## 2. Seams (verified in this tree)

| What | file:line | Role |
|---|---|---|
| the rule's home | `internal/relevo/builder_change.go:67-73` | `roundOpenIn` stays; the predicate sits beside it (file is 84 lines) |
| the preflight guard | `internal/relevo/send.go:222-224` | the refusal that becomes the predicate |
| the in-lock twin | `internal/relevo/send.go:484-491` | same rule under the store lock; both sites move together |
| remote candidate resolution | `internal/relevo/send.go:225-240` | unchanged; a served token is shape-checked here |
| preflight doc | `internal/relevo/send.go:166-171` | one clause naming the guard |
| client's local write after a re-point | `internal/relevo/remote_send.go:159-176` | candidate, plan, halt |
| client's needs_you view handling | `internal/relevo/remote_send.go:132-138` | a failed start writes nothing |
| stop's client mapping | `internal/relevo/stop.go:102-119` (107-110) | unchanged |
| serve decision for NEEDS YOU | `internal/serve/rounds.go:110-125` | already proceed — no change |
| serve candidate validation | `internal/serve/rounds.go:181-187` | unchanged |
| the server's send call | `internal/serve/rounds.go:367` | passes `Builder` — why the guard is shared |
| send error mapping | `internal/serve/rounds.go:288-311` | unchanged |
| stop refusal that stays | `internal/serve/bindings.go:428-459` (456-458) | lever (a) not taken; 553 lines, untouched |
| served spelling | `internal/relevo/runningprocs.go:15-27` | the "not local" test to reuse |
| copy spellings | `internal/relevo/remote_add.go:334-339`, `internal/serve/bindings.go:217` | ModeRemote vs Owner |
| pure-test pattern | `internal/relevo/builder_change_test.go:168-192` | `TestRoundOpenIn` |
| refusal tests that stay green | `internal/relevo/send_test.go:1256-1287`, `internal/relevo/remote_test.go:5813-5839` | local and served active rounds |
| client-path fixture | `internal/relevo/remote_test.go:5658-5688`, `:2192-2206` | `remoteBuilderRT`, `remoteBinding` |
| serve fixture: two candidates | `internal/serve/serve_test.go:2419-2436` | `setupBuilderEnv` |
| serve pattern: candidate at start | `internal/serve/serve_test.go:2438-2486` | `TestRoundStartWithCandidateChangesTheBuilder` |
| serve refusal that stays | `internal/serve/serve_test.go:1531-1550` | `TestRoundStartWhileRunningIs409` |
| real halt fixture | `internal/relevo/abandon_test.go:490`, `internal/relevo/switch.go:98-117`, `internal/relevo/headless.go:988-996` | dead process + spent switch budget |
| serve helpers | `internal/serve/helpers_test.go:112-189`, `269-289`, `526-530` | `scriptRunner`, `roundFormCandidate`, `startedSpecs` |
| wording surfaces | `cmd/relevo/send.go:35`, `internal/mcp/tools.go:126`, `internal/mcp/testdata/contract/tools-list.golden:27`, `README.md:1615` | each promises a blanket refusal |

## 3. Ordered steps

Every focused command runs with `-count=1`; fix every reported error before the
next run.

1. **The rule.** Add `candidateSendRefused` beside `roundOpenIn`
   (`internal/relevo/builder_change.go`): true when the round is open and the
   binding is not a served binding in NEEDS YOU; comment the why (the served
   path's stop is refused, so the candidate is the only lever there; a local
   binding stops and re-sends) and the two spellings of served. Add
   `TestCandidateSendRefusedExceptOnAHaltedServedBinding` to
   `builder_change_test.go` covering the table above (add a report-closed round
   and another round's prompt). Done when:
   `go test -count=1 -run TestCandidateSendRefusedExceptOnAHaltedServedBinding ./internal/relevo/` passes.
2. **Wire both sites.** Replace the two `roundOpenIn(entries, b.Round)`
   conditions in `sendPreflight` (222) and the locked re-check (488) with the
   predicate; keep the error text and the write-nothing property; widen the
   preflight doc where it names the guard. Done when:
   `go test -count=1 -run 'TestCandidateSendRefused|TestSendBuilderRefusedWhileRoundOpen|TestSendRemoteBuilderRefusedWhileRoundOpen' ./internal/relevo/` passes.
3. **The client path.** Add `TestSendRemoteBuilderRepointsAHaltedRound` in
   `remote_test.go` beside `:5813`: `remoteBuilderRT`, the stored binding in
   NEEDS YOU with a halt text and a round-1 prompt entry, `Send` with a
   candidate, the fake remote answering running with the canonical candidate;
   pin that StartRound was called with it and no plan was written before the
   server answered, and that the local binding keeps round 1, takes the served
   candidate, clears the halt and goes active. Done when:
   `go test -count=1 -run TestSendRemoteBuilder ./internal/relevo/` passes.
4. **The served path.** Add `TestRoundStartWithCandidateRepointsAHaltedRound`
   next to `serve_test.go:2438`: `setupBuilderEnv`, create on the first
   candidate, start round 1, spend the switch budget (`RoundSwitches =
   rt.Policy.SwitchLimit()`, as `abandon_test.go:490`), mark the fake process
   exited and tick → NEEDS YOU; then POST round 1 with a new plan and the second
   candidate → 201, active/`running`, candidate changed, round 1, halt cleared,
   `RoundSwitches` 0, exactly one new spec (no spawn of the failed candidate, no
   switch). Add `TestRoundStartWithCandidateLeavesARunningRoundRefused` (same
   fixture before the halt: a different plan with a candidate → 409
   `round_open`). Keep each under funlen's 70 lines. Done when:
   `go test -count=1 -run 'TestRoundStartWithCandidate|TestRoundStartWhileRunningIs409' ./internal/serve/` passes.
5. **The wording sweep (mechanical).** The four surfaces in the table: the
   refusal is now "while a live round is open", and a halted served round may be
   re-pointed. Hand-edit the golden's description line to match `tools.go`
   exactly and run the test *without* `-update` (so no unrelated golden is
   rewritten). No test pins these strings; no cmd/relevo test spawns a harness.
   Done when: `go build ./... && go test -count=1 ./internal/mcp/ ./cmd/relevo/` passes.
6. **Mutations** M1, M1b, M2, M3, M4 below; restore each. Done when: every named
   test fails under its mutation and passes again after the restore.
7. **Ship.** Copy this plan verbatim to
   `docs/plans/2026-09-29-bug-sweep-halt.md`; run `make check` once; commit
   `fix(relevo): send --candidate re-points a halted served round` with body
   `Fixes #687`; do not push. Done when: `make check` exits 0,
   `git status --porcelain` is clean, and `git show --stat HEAD` lists exactly
   the plan plus `internal/relevo/builder_change.go`, `internal/relevo/send.go`,
   `internal/relevo/builder_change_test.go`, `internal/relevo/remote_test.go`,
   `internal/serve/serve_test.go`, `cmd/relevo/send.go`, `internal/mcp/tools.go`,
   `internal/mcp/testdata/contract/tools-list.golden`, `README.md`.

## 4. Mutation checks

| # | Break this | Command | Test that must fail |
|---|---|---|---|
| M1 | make the predicate return `roundOpenIn` alone | `go test -count=1 -run TestCandidateSendRefusedExceptOnAHaltedServedBinding ./internal/relevo/` | that test (the halted-served row), and `TestRoundStartWithCandidateRepointsAHaltedRound` (409 `round_halted`, no 201) |
| M1b | fix only the preflight site, leave `send.go:488` as `roundOpenIn` | `go test -count=1 -run TestRoundStartWithCandidateRepointsAHaltedRound ./internal/serve/` | that test: the in-lock refusal makes the POST a 409 |
| M2 | drop the served half (allow every halted binding) | `go test -count=1 -run TestCandidateSendRefusedExceptOnAHaltedServedBinding ./internal/relevo/` | that test (the halted-local row) |
| M3 | drop the candidate/halt write in `remote_send.go:159-176` | `go test -count=1 -run TestSendRemoteBuilderRepointsAHaltedRound ./internal/relevo/` | that test (the local copy keeps the old candidate) |
| M4 | drop `roundStartDecisionOf`'s running arm (`startOpen`) | `go test -count=1 -run 'TestRoundStartWhileRunningIs409\|TestRoundStartWithCandidateLeavesARunningRoundRefused' ./internal/serve/` | both |

Restore each before the next; the final diff contains none.

## 5. What this round deletes (closed list)

1. The blanket `--candidate` refusal for an open round at
   `internal/relevo/send.go:222-224` and its in-lock twin at `:488-489`: a served
   binding halted on its round no longer gets "binding %q has round N open;
   relevo stop …". The condition moves into `candidateSendRefused`; the message
   stays for every other open-round row.
2. The promise of a blanket refusal in the four descriptions of `--candidate`
   (reworded, not removed).
3. Nothing else. No test, route, error code, message or golden is deleted;
   `stop`'s 409 `round_halted` and 409 `nothing_to_stop` stay; no lint, size or
   coverage exclusion is touched; `internal/serve/bindings.go` is not touched.

## 6. Report must include

- the chosen lever (b) and why, with lever (a) explicitly not taken (the stop
  refusal stays) and the local refusal restated;
- the commit (SHA, subject, body `Fixes #687`), the plan path,
  `git diff --stat` checked against the seam files, and that nothing was pushed;
- each focused command with its pass output, and the single `make check`;
- M1, M1b, M2, M3, M4: the exact edit, the named test that failed with its first
  failure line, and that it was restored;
- statement coverage of `internal/relevo` and `internal/serve` from
  `.coverage.txt` against `testdata/coverage-baseline.txt`, and that no baseline
  was edited;
- the seed-vs-tree items restated (the shared guard, the served spelling, the
  untouched serve handler, the cap check moot for lever (b));
- deferred, with reasons: lever (a) and the halted stop refusal; the one-step
  lever for a local halted round; the queued-round `--candidate` start with a
  different plan, which today falls through `writeSendError`'s default to a 500
  (observed on this path, untouched because decision 2 keeps queued-round
  semantics); no protocol feature bump;
- anything halted on or left undone, instead of improvising.
