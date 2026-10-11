# MasterMind amendments (these override the plan below)

1. **The heal runs only in the calling session's own process** (the CLI and the MCP server a session spawns), never in the daemon or in `relevo serve`. Before wiring it into `Resolve`, grep every caller of `mastermind.Resolve`; if any runs in the daemon or serves a request on another process's behalf, its `Detect(env, PPID)` would describe the daemon, not the session -- gate the heal off there (e.g. an option on the resolver that only CLI/MCP entry points set) and pin that with a test.
2. **Status cost:** `mastermindRoute` is called once per binding. Look up and classify each MasterMind record at most once per status call (memoise by id within the call), so a status over many bindings does one `Get` + one `ProcStart` per MasterMind, not per binding.
3. **Step 1 reproduction is unit-level:** a test fixture with a record whose host pid is dead and a live push claim, showing `mastermindRoute` answers push/live today. Do not start a daemon or touch the server's installed relevo or its state dir.
4. Case 6 in the plan (HostPID == 0): such a record is explicit, so it is never `gone` -- do not add a heal path for it; just keep the "explicit registrations never clear an existing host" rule.
5. Do not fetch (no origin remote). Never use `git stash`. Commit the plan with these amendments as docs/plans/2026-10-11-mastermind-self-heal.md.

---

# Plan: MasterMind self-heal after crash + gone annotation (slug: mastermind-self-heal)

Issue: after a crash/reboot, the resumed session's SessionStart hook runs
`relevo mastermind init` while the daemon is still starting; init fails, the
hook swallows it by design (`mastermindInitHookFailure`,
`cmd/relevo/mastermind_hook.go:160-164`), nothing retries, the record keeps
the old pid and reads `gone`, push delivery reaches nobody, and
`relevo status` still says `route push`. Manual `relevo mastermind init`
reattaches at once.

Base: `origin/main f1931b74`. The builder's worktree has no origin remote:
never fetch. Never `git stash` (shared stash stack); use a WIP commit or
copies outside the repo for mutation checks.

## Cause (file:line evidence)

- Env resolution short-circuits liveness. `mastermind.Resolve`
  (`internal/mastermind/resolve.go:58-76`) tries flag, then
  `resolveFromEnv` (`resolve.go:131-148`), which returns the named record via
  `hit` (a `Touch` only, `resolve.go:169-172`) with no `RecordState` check.
  `$RELEVO_MASTERMIND` wins and the finder is never called (pinned by
  `internal/mastermind/resolve_test.go:394`). A stale id therefore resolves
  to a `gone` record on every verb.
- `gone` is derived, never stored: `RecordState`
  (`internal/mastermind/state.go:17-28`) compares `HostPID/HostStartedAt`
  against `ProcStart`; absent reader, error, or pid reuse all read `gone`.
- Init's reattach rule is what heals: `findCaller` looks by host then by
  session (`internal/mastermind/hook.go:225-246`), `reattach` renames/moves/
  re-points host (`hook.go:250-280`). Host identity comes from
  `mastermind.Detect` (`ident.go:35-63`, `$CLAUDE_PID` else ppid) plus
  `proc.StartTime` via `mastermindHostStart`
  (`cmd/relevo/mastermind.go:197-210`) and `mastermindCaller`
  (`cmd/relevo/mastermind_consent.go:73-106`).
- Status route ignores record liveness: `mastermindRoute`
  (`internal/relevo/status.go:29-51`) returns `("push", true)` on a live
  claim alone; `Delivery.DeliverPending` does the same
  (`internal/delivery/deliver.go:88-96`, reason `mastermind has a push claim`),
  and `AckPush.claimIsLive` checks only the claim
  (`internal/delivery/pushack.go:113-123`). Nothing consults `RecordState`.
- Human print of the route is `writeMasterMindLine`
  (`internal/view/render.go:157-165`, `route %s`). The statusline text row
  carries no route (`internal/view/statusline.go:87-146`, `Route` is JSON-only
  at `:443,585`); `internal/ui` never prints the route (only tests carry the
  field). `relevo push --ack --json` prints machine JSON
  (`cmd/relevo/writejson.go:234-250`, goldens pin `"route": "push"`).
- Extra victim: `runStatusline` returns early when `PushLive`
  (`cmd/relevo/status.go:277-279`); `PushLive`
  (`internal/relevo/push_live.go:7-11`) also ignores record state, so a gone
  MasterMind with a live claim hides every row.

## Decision 1 — heal in `mastermind.Resolve` (one place), not the daemon

Every verb funnels through `mastermind.Resolve`: `resolveVerbMasterMind`
(`internal/relevo/bind.go:42-82`, covers bind/add/fork/ask/chain), 
`resolveMCPMasterMind` (`cmd/relevo/mcp.go:140-152`, covers push/mcp),
`gcResolver` (`cmd/relevo/unbind.go:275-296`, covers unbind/status scope),
doctor checks (`cmd/relevo/doctor_checks.go:351`). One guarded call inside
`Resolve` therefore heals once per CLI invocation with no caller edits, which
keeps this round out of every other lane's files. The daemon is rejected:
it never sees the calling session's environment/`Detect` identity, so it
cannot tell "session evidently alive" from "session really over"; it would
need new IPC, and `cmd/relevo/daemon.go` belongs to another lane.
`Resolve.hit` already performs a best-effort write (`Touch`, error dropped),
so a best-effort reattach is the same class of side effect, not a new one
(the `It never creates a record` comment on `resolve.go:55-57` is updated to
match, since `Touch` already writes).

## Behaviour and cases

Self-heal (applies uniformly to flag, env, host, and session hits):
1. Hit record with `RecordState(rec, ProcStart) == live` (or explicit,
   `HostPID <= 0`): untouched, zero extra writes.
2. Hit record `gone`, and the current session evidently alive — `Detect(env,
   PPID)` names the same `(kind, session)` as the record, and the current
   host pid (when `> 0`) resolves through `ProcStart` without error, and no
   *other* record holds that live host: reattach exactly as init does
   (`MoveSession` when the session moved, `SetHost` to the current host),
   minus rename. Then return the updated record.
3. Hit record `gone` but the session is really over or unprovable — no
   `Detect` match, current host unreadable, or another record holds the live
   host: touch nothing, return the record as today.
4. Reattach write fails (conflict, store error): swallow like `Touch`; the
   verb proceeds with the stale record and never fails because of the heal.
5. At most one heal attempt per `Resolve` call; the `RecordState` pre-check
   keeps the live path to one extra `ProcStart` read.
6. Opencode/`HostPID == 0` records: heal on session match alone (no host to
   prove); explicit (`--kind/--session`, host 0) registrations never clear
   an existing host, mirroring `hook.go:270-271`.

Say so:
7. `mastermindRoute` returns `("push", false)` when the claim is live but
   the binding's record is `gone` (`Get` + `RecordState`; any lookup failure
   keeps today's answer — failures read as before, never fail status).
   `PushLive` inherits this through the same function (it already delegates
   to `mastermindRoute` with a synthetic binding carrying only the id).
8. Human status line renders `route push (MasterMind gone: run relevo
   mastermind init)` when route is `push` and live is false, derived from the
   existing `MasterMindRoute`/`MasterMindRouteLive` fields — no struct or
   JSON change, so `status --json`, `--line --json`, and the contract
   goldens keep byte-identical documents and consumers keep reading
   `mastermind_route_live`/`push_live`.
9. Statusline text rows and ui print no route today (verified by grep above);
   they change only if the builder's re-grep finds a human `route push`
   print this plan missed, and the report must say which. Push-ack JSON and
   delivery `Route` values stay `"push"` (pinned machine contract).

## Seams (files the builder may touch; nothing else)

- `internal/mastermind/heal.go` (new): the heal helper against the
  `Registry` interface (`registry.go:23-37` already offers `ByHost`,
  `BySession`, `MoveSession`, `SetHost`), reusing `findCaller`/`reattach`
  semantics from `hook.go:212-280` (move or export them, do not duplicate).
- `internal/mastermind/resolve.go:58-125`: single guarded heal call after a
  successful hit; update the `never creates` comment.
- `internal/relevo/status.go:29-51` (`mastermindRoute`): record-liveness
  gate; `internal/relevo/push_live.go:7-11` only if it cannot inherit it.
- `internal/view/render.go:157-165` (`writeMasterMindLine`): parenthetical.
- Tests only: `internal/mastermind/heal_test.go` (new),
  `internal/mastermind/resolve_test.go`,
  `internal/relevo/status_route_test.go` / `status_bulk_test.go`,
  `internal/view/status_render_test.go`, plus golden/testdata updates the
  render change forces (find via `grep -rn 'route push'` over
  `internal/view`, `internal/ui`, `cmd/relevo/testdata`).
- `docs/plans/2026-10-11-mastermind-self-heal.md` (new, this plan).
- `testdata/coverage-baseline.txt` only if regenerated with
  `sh scripts/check-coverage.sh --write` (never lowered to get green).
- Forbidden (parallel lanes; halt rather than enter): `cmd/relevo/args.go`,
  `internal/db/**`, `cmd/relevo/daemon.go`, `dist/*.service`,
  `internal/harness/**` agent definitions, builder spawn env,
  `internal/mastermind/guide.md`. No new lint exclusions; functions ≤ 70
  lines, non-test files ≤ 600 lines, comments say why only, no issue
  numbers in code or tests (the commit message alone carries `Fixes #1127`).

## Ordered steps (one line each: deliverable + done-check)

1. Reproduce on the base (no fetch): seed a record with a dead pid, hold a
   live claim, show `status` says `route push` while push reaches nobody
   and hand-run `init` reattaches; done when the premise is confirmed with
   commands logged, or HALT with evidence if the premise is wrong on main.
2. New `internal/mastermind/heal.go` helper (pure against injected
   registry/`Detect` inputs/`ProcStart`): done when cases 1–6 above each
   have a test in `internal/mastermind/heal_test.go` that kills its named
   mutation (stale-host always-heal; session-mismatch heal; taken-host
   steal; write-error failing the verb; live-path extra writes).
3. Wire one guarded heal call into `Resolve` (`resolve.go`): done when every
   existing `resolve_test.go` case still passes and a new env-hit-gone test
   shows the returned record re-pointed exactly once with errors swallowed.
4. Gate `mastermindRoute` on record liveness (`status.go`) so a live claim
   plus a `gone` record yields `("push", false)` and `PushLive` goes false
   with it: done when new `internal/relevo` tests pin push/live for
   live-claim+live-record, live-claim+gone-record, and lookup-failure, each
   killing its mutation (drop the `RecordState` check; force live on
   error), tested as pure functions in `internal/relevo` (no subcommand
   spawn, no harness, no network, per CI rules).
5. Annotate `writeMasterMindLine` (`render.go`) with the parenthetical on
   route-push-and-not-live: done when `status_render_test.go` pins the
   exact string and JSON goldens are byte-identical.
6. Re-grep every human print of the push route (status text, statusline,
   ui, log/show columns) and either annotate the shared helper or record
   why it prints history rather than liveness: done when the grep command
   and its result are in the report and goldens are regenerated, not
   hand-edited.
7. Run the focused suites (`go test ./internal/mastermind/...
   ./internal/relevo/... ./internal/view/...`), fixing every failure
   before the next run, then `make check` green once at the end: done when
   both commands pass on the final tree.
8. Save this plan as `docs/plans/2026-10-11-mastermind-self-heal.md` and
   commit code plus plan as new commits (never amend/rebase a pushed
   branch) with messages naming the issue (`Fixes #1127`): done when
   `git log --oneline -3` shows the new commit(s) and `git diff --stat`
   against the base touches only the declared scope.

## What is deleted

1. Nothing. This round adds behaviour only: no flags, verbs, columns, JSON
   keys, or files are removed.

## Report must include

- Reproduction commands and output (or halt evidence with file:line).
- Chosen heal location and the daemon rejection reason in one sentence.
- Each test with its exact killing mutation and how it was checked (WIP
  commit or outside-repo copies, never `git stash`).
- The re-grep for human `route push` prints and what was/wasn't changed.
- Focused test command(s) plus final `make check` result; coverage-baseline
  regenerated or not; `git diff --stat` vs the declared scope.
