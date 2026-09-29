# Bug sweep, batch 3 — delivery bookkeeping (#673, #674)

One builder round, two commits, one issue each. The plan is committed verbatim as
`docs/plans/2026-09-29-bug-sweep-delivery.md`; do not push. Anchors below were
verified in this tree (`64d2715b`); re-check a line before editing if anything
looks shifted.

## Behaviour and cases

### #674 — a deliverer reads the session back before it gives up

`OpencodeDeliverer.Deliver` (`internal/delivery/deliver_opencode.go:118`) and
`AgyDeliverer.Deliver` (`internal/delivery/deliver_agy.go:130`) run the fallback
gate before they look for the payload, so past `FallbackAfter` every tick
returns "gave up" without a read: a payload that was posted and admitted late
can never be confirmed, stays pending forever, and — oldest-first — blocks every
later payload.

After this round, in both deliverers:

- the session read-back (`seen` / the agy inbox scan) runs **before**
  `pastFallback`, so a payload whose text is already in the session is
  `OutcomeDelivered` with reason `"already present"` at any age, and never
  POSTs or sends again;
- everything else keeps its current outcome and reason: unavailable
  service/session, POST/send failure, `"posted but not seen in the session"`,
  `"sent to agy but not yet read"`, and the give-up `OutcomeNotMine`
  (`"opencode/agy push gave up after …"`) when nothing is in the session past
  the window;
- a posted-but-unconfirmed payload is re-read on every later tick, so it flips
  to delivered when the text appears instead of the give-up short-circuiting the
  read forever; `seen`/`hasPosted` keep preventing a double post.

The order is fixed and non-obvious; comment it:

- opencode: guards → `origin` → `seen` → `pastFallback` → service resolution →
  `hasPosted` → POST → confirm.
- agy: guards → conversation id → `origin` → inbox scan → `pastFallback` →
  creds/loopback → send → confirm.

`pastFallback` must stay **above** the service/creds checks: a dead service past
the window must still return the give-up `OutcomeNotMine`, which
`TestOpencodeDeliverFallsBackAfterFallbackAfter` and
`TestAgyDeliverGaveUpAfterFallback` pin today. Do not move those checks.

### #673 — show claims the pending payload it reads

`relevo show` prints a pending `DirToMasterMind` report without claiming it, so
the route pushes the same text afterwards. After this round, on the **live**
branch of `Show` only (`internal/relevo/show.go:100-103`):

- a non-peek show claims the oldest pending `DirToMasterMind` payload through
  the existing `delivery.Pull(ctx, rt.Store, opts.Name, "show")` — the same
  lock, take-oldest and route-field call the cockpit makes with `"tui"`
  (`internal/ui/actions.go:444`). The claim runs once the section resolved
  without error (a failed read must not consume a payload; a `Missing` section
  still claims), and the pulled text is **discarded**: stdout stays the requested
  section, byte for byte, and `--json` output is unchanged;
- `--peek` skips the claim — the `wait --peek` contract;
- archived reads (`showArchived`) and database reads (`showDB`) never claim:
  nothing is pending there;
- `relevo show --owner` never claims: it is the admin's read of another owner's
  binding and stays read-only (comment at `cmd/relevo/serve_show.go:61-68`,
  `TestPrintLogAndPrintShowWriteNothing`);
- the `--diff`/`--drift` and whole-`--log` forms take `printDiff`/`printLog`
  (`cmd/relevo/show.go:197-206`) and are untouched: they never go through `Show`
  and do not claim. `--peek` there is a harmless no-op; do not invent usage
  errors for it, and likewise not for `--peek --owner` (the owner read always
  peeks);
- a failed claim is returned as the show error (the CLI prints it and exits 1):
  a silent failure would leave the payload to be pushed after it was read.

Deliberate non-goals, to state in the report:

- #674's idea 3 (show a posted-but-unconfirmed entry differently in `status`)
  is not done. The `posted` map is daemon-process memory
  (`deliver_opencode.go:44-48`) and `status` is another process, so it cannot
  fit without changing what the pending query returns.
- README (`README.md:1069-1097`), the injected guide
  (`internal/mastermind/guide.md:17`) and the show command doc
  (`claude-plugin/commands/show.md:12`) are not touched: the seed fixes the file
  scope. Say so; they are the natural follow-up.

## Seams (verified against this tree)

| What | file:line | Role |
| --- | --- | --- |
| opencode `Deliver` | `internal/delivery/deliver_opencode.go:118-188` | reorder inside |
| — give-up gate | `:128-130` | moves below the read-back |
| — service resolution | `:132-149` | stays after `pastFallback` |
| — origin + `seen` | `:151-161` | moves up, right after the guards |
| — `hasPosted`, POST, confirm | `:163-188` | unchanged |
| — `pastFallback` | `:190-202` | function unchanged |
| agy `Deliver` | `internal/delivery/deliver_agy.go:130-188` | same reorder |
| — give-up gate | `:142-144` | moves below the inbox scan |
| — creds/loopback | `:146-152` | stay after `pastFallback` |
| — origin + inbox scan | `:154-164` | moves up; send/confirm `:166-187` unchanged |
| — `pastFallback` | `:190-202` | function unchanged |
| `Pull` / `pullPending` | `internal/delivery/pull.go:14-20, 60-94` | the claim; widen the comment to name both callers |
| pending query | `internal/store/log.go:379-396` | oldest-unconfirmed, unchanged |
| `ShowOptions` | `internal/relevo/show.go:51-63` | gains `Peek bool` |
| `Show` live branch | `internal/relevo/show.go:100-103` | claims after a successful read |
| `showLive`, `showArchived`, `showDB` | `internal/relevo/show.go:202-264, 367-435, 440-533` | reads unchanged; no claim in the last two |
| show flags + usage | `cmd/relevo/show.go:20-23, 94-113, 208-209` | `--peek`, usage line, opts |
| owner read | `cmd/relevo/serve_show.go:69-94` | opts get `Peek: true` |
| show fixtures | `internal/relevo/route_helpers_test.go:30-65`, `fixture_test.go:86-104`, `show_test.go:174-219` | reuse for the claim/peek tests |
| delivery fakes | `deliver_opencode_test.go:23-63, 305-330`; `deliver_agy_test.go:36-60, 85-106, 110-156, 222-244, 329-350` | reuse for the two deliverer tests |
| cmd fixtures | `cmd/relevo/show_test.go:57-80`; `cmd/relevo/owner_reads_test.go:39-58, 66-103` | patterns for the two cmd tests |

## Steps (each names its "done when" command)

1. Reorder the opencode deliverer (`internal/delivery/deliver_opencode.go`,
   `Deliver`): `origin` + `seen` right after the guards, `pastFallback` after
   the `already present` return and before the service resolution, with a
   one-line why. Add `TestOpencodeDeliverConfirmsAPayloadSeenPastFallback`
   (`fakeSqliteExec{seenFrom: 1}`, `queuedAt` 31s before `d.Now`, expect
   `OutcomeDelivered`/`"already present"` and no POST).
   Done when: `go test ./internal/delivery/ -run 'TestOpencodeDeliver' -count=1` passes.
2. Reorder the agy deliverer (`internal/delivery/deliver_agy.go`, `Deliver`)
   the same way. Add `TestAgyDeliverConfirmsAReadMessagePastFallback` (inbox
   message matching the payload and marked read, `FallbackAfter` 1s,
   `queuedAt` 2s before `Now`, expect `OutcomeDelivered`/`"already present"` and
   zero sends).
   Done when: `go test ./internal/delivery/ -run 'TestAgyDeliver' -count=1` passes.
3. Run mutations M1 and M2 below; restore each.
   Done when: both named tests fail under their mutation and pass again after the restore.
4. Commit 1: write the received plan verbatim to
   `docs/plans/2026-09-29-bug-sweep-delivery.md`; commit it with the deliverer
   files and their tests as `fix(delivery): …`, body `Fixes #674`. Do not push.
   Done when: `git show --stat HEAD` lists the plan plus the two deliverer files and their tests.
5. The claim: add `Peek bool` to `ShowOptions` (`internal/relevo/show.go:51-63`)
   and the `delivery.Pull(…, "show")` claim in `Show`'s live branch (100-103),
   gated on `!opts.Peek`, after `showLive` succeeded (`internal/relevo` already
   imports `internal/delivery` — no cycle). Widen `delivery.Pull`'s comment
   (`pull.go:14-20`) to name the cockpit and the show verb. Add
   `TestShowLiveClaimsPendingMasterMindPayload` and
   `TestShowLivePeekLeavesPendingMasterMindPayload` to `show_test.go`, built on
   `routeRuntime` + `seedPending` (plus a report file so the section prints).
   Done when: `go test ./internal/relevo/ -run 'TestShowLive' -count=1` passes.
6. The verb: add the `--peek` flag and wire `opts.Peek` in
   `cmd/relevo/show.go` (94-113, 208-209) and `[--peek]` to `showUsage` (20-23);
   set `Peek: true` in `serveShow`'s opts (`cmd/relevo/serve_show.go:93`) so the
   owner read stays read-only. Add
   `TestShowPeekFlagLeavesPendingMasterMindPayload` (`cmd/relevo/show_test.go`,
   seeded like `seedShowDiffStore`, two cases: plain `--report` claims route
   `"show"`; `--report --peek` leaves it pending) and
   `TestShowOwnerLeavesPendingMasterMindPayload` (`owner_reads_test.go`, reuse
   `seedServeOwnerState` plus a queued entry, owner read leaves it pending).
   Both store-only: no harness, no network.
   Done when: `go test ./cmd/relevo/ -run 'TestShow' -count=1` passes.
7. Run mutations M3, M4, M5 below; restore each.
   Done when: each named test fails under its mutation and passes again after the restore.
8. Commit 2: `fix(show): …`, body `Fixes #673`.
   Done when: `git log -2 --format='%h %s'` shows both commits and `git log -2 --format='%b'` names both issues.
9. Full gate, once: `make check` (gofmt, vet, lint, comment and filesize
   checks, `go mod tidy` check, `go test -race -cover ./...`, coverage
   baseline). Never lower or edit a baseline.
   Done when: `make check` exits 0 and `git diff --stat HEAD~2..HEAD` lists exactly the declared files.
10. Report (next section). Done when every item is present and `git status --porcelain` is clean.

Focused commands before the gate (in this order):
`go test ./internal/delivery/ -count=1`; `go test ./internal/relevo/ -count=1`;
`go test ./cmd/relevo/ -run 'TestShow' -count=1`;
`go test ./internal/delivery/ ./internal/relevo/ ./cmd/relevo/ -count=1`.

## Mutation checks

| # | Break this | Command | Test that must fail |
| --- | --- | --- | --- |
| M1 | put the opencode give-up back above the `seen` read-back | `go test ./internal/delivery/ -run TestOpencodeDeliverConfirmsAPayloadSeenPastFallback -count=1` | `TestOpencodeDeliverConfirmsAPayloadSeenPastFallback` |
| M2 | put the agy give-up back above the inbox scan | `go test ./internal/delivery/ -run TestAgyDeliverConfirmsAReadMessagePastFallback -count=1` | `TestAgyDeliverConfirmsAReadMessagePastFallback` |
| M3 | drop the `delivery.Pull` call in `Show` | `go test ./internal/relevo/ -run TestShowLiveClaimsPendingMasterMindPayload -count=1` | `TestShowLiveClaimsPendingMasterMindPayload` (and the claim half of the cmd test) |
| M4 | claim even when `opts.Peek` | `go test ./internal/relevo/ -run TestShowLivePeekLeavesPendingMasterMindPayload -count=1` | `TestShowLivePeekLeavesPendingMasterMindPayload` |
| M5 | remove `Peek: true` from `serveShow`'s opts | `go test ./cmd/relevo/ -run TestShowOwnerLeavesPendingMasterMindPayload -count=1` | `TestShowOwnerLeavesPendingMasterMindPayload` |

Revert every mutation before the next; the final diff must contain none.

## What is deleted

1. The ordering "give up before reading the session" in both deliverers: the
   `pastFallback` call site moves below the read-back. The function, its log
   line, its rate limit, and every reason string stay.
2. `relevo show`'s read-only treatment of a live binding's pending payload: a
   non-peek live read confirms the entry with route `"show"`.
3. Nothing else. No file, function, flag, route, reason, test or golden is
   removed; show's stdout, stderr, exit codes and `--json` are unchanged.

## Report must include

- the two commits (hashes, subjects, bodies naming `Fixes #NNN`), the plan file
  path, and that nothing was pushed;
- `git diff --stat HEAD~2..HEAD` checked against the seam files, calling out the
  two lines outside decision 1's file list — `cmd/relevo/serve_show.go`'s
  `Peek: true` (keeps #216's owner read read-only) and the `pull.go` comment —
  and that no file outside them changed;
- the focused commands and results, and the single `make check` result;
- coverage of `internal/delivery`, `internal/relevo`, `cmd/relevo` against
  `testdata/coverage-baseline.txt`, and that no baseline was edited;
- M1–M5: what was broken, the command, the failing test, the restore;
- the dropped `status` idea and why the pending query was not changed;
- the docs left alone and the `--diff`/`--drift`/whole-`--log` bypass of the
  claim;
- any halt, skipped step or divergence from this plan.
