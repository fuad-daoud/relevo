# Plan: the Claude Code mod delivers `relevo push` lines (one builder round)

Scope: TypeScript mod only. No Go change: if the tree forces one, halt and report. `CLAUDE.md` and `docs/runbook.md` bind.

## Behaviour and cases

- Hooks module ships inside `claude-plugin`: new `claude-plugin/hooks/register.ts`; `claude-plugin/hooks/hooks.json:1-10` gains `"modules": ["./register.ts"]` beside `hooks`. Nothing else in the plugin changes: no band, pane, statusLine, toasts, commands.
- Identity (no `mastermind init` from the mod): on load, `$.process.run(["relevo","status","--line","--json"], { env: { CLAUDECODE: "1", CLAUDE_CODE_SESSION_ID: await $.session.id() } })`, read `.mastermind.id`, keep it as `RELEVO_MASTERMIND` for every later call. Unresolved (non-zero exit, or the run rejects because `relevo` is missing) = not a MasterMind: retry every 10 s via `$.clock.every`; never surface an error; never spawn while unresolved.
- Once resolved, `$.process.spawn({ argv: ["relevo","push"], env: { RELEVO_MASTERMIND, CLAUDECODE: "1", CLAUDE_CODE_SESSION_ID } })` with no `input` (stdin closed, which `relevo push` expects) for the session's life; restart with backoff when the child exits; leaving the loop (module unload) ends it.
- Buffer stdout chunks to newlines (one chunk may hold part of a line or several). Each NDJSON line `{seq,binding,round,kind,text,state?,old_state?}`: idle → `$.prompt.submit({ text })`; mid-turn → `$.session.append({ message: { type: "user", content: [{ type: "text", text }] } })`. Idle/mid-turn comes from `turn.start`/`turn.complete` of the MAIN loop only (subagent events carrying `agentId` ignored). Delivered text is the line's `text` verbatim.
- Ack ONLY after submit/append resolved without `drop`/`deny`: `$.process.run(["relevo","push","--ack",binding,String(seq),"--json"], { env })`. `seq` 0 (`kind: "state"`) takes no ack. A failed ack retries a bounded number of times (idempotent server-side).
- A `drop`/`deny` or a rejection: no ack; `$.ui.log` it; after a bounded retry, leave the child's loop (ending the child, clearing its admit so the report falls back to `wait`); the supervisor restarts and resumes.

## Seams (file, type/function, lines)

- `claude-plugin/hooks/hooks.json:1-10` — add `modules` key (additive).
- `claude-plugin/hooks/register.ts` (new) — the whole module: resolve loop, supervisor, buffer, deliver, ack.
- `claude-plugin/hooks/register.test.ts` (new, `*.test.ts` on `claude-code/testing`) — all cases below.
- `Makefile:30-56` — new `mod-test` target (`claude plugin validate claude-plugin` + `claude plugin test claude-plugin`); NOT wired into `check` (CI has no `claude`).
- `.gitignore:1-4` — add `claude-plugin/.claude-plugin/types/`.
- `docs/plans/2026-10-09-mod-deliver.md` (new) — this plan verbatim, committed with the code.
- Read-only Go refs (no edits): `cmd/relevo/push.go:16-67` (`--ack <binding> <seq>` + `--json` doc, stdin never read), `internal/delivery/pushack.go:59` `AckPush` returns `(AckResult, error)`, `internal/delivery/pushrun.go:15-26` `PushEvent` (`seq 0` state lines, `state`/`old_state`).
- Mod API refs: reuse `~/.cache/claude-code-types/verified-api-2.1.294.md` items 1-12 without re-verifying; newly pinned here: `ProcessSpawnRequest.input` absent = stdin closed (`claude-code.d.ts:8029+`); `SessionAppendArgs` exact shape `{ message: { type, content: ApiContentBlock[] } }` (`:10468+`); `PromptSubmitArgs` is `{ text }` (`:8956+`); `ProcessRunResult` fields are `exitCode/stdout/stderr` and run rejects only when the command cannot start (`:3487`, `:7969+`); `turn.start` carries no `agentId`, subagent runs raise none (`:13260+`); `hooks.json "modules"` is not in the d.ts and rests on the owner's #951 P1 probe — `claude plugin validate` in step 1 proves it, else halt.

## Ordered steps (each: deliverable + how to know it worked)

1. Module skeleton + identity loop in `claude-plugin/hooks/register.ts` + `hooks.json` `modules` key; deliverable is resolve/retry with no spawn when unresolved; know it worked when `claude plugin validate claude-plugin` passes (else halt: the `modules` premise is false) and the not-a-MasterMind tests (no spawn, 10 s retry, no error surfaced) pass.
2. Supervisor in `register.ts` (`spawn` with `RELEVO_MASTERMIND`, no `input`, backoff restart, unload ends it); deliverable is one live child per resolved session; know it worked when the resolved-spawns-with-`RELEVO_MASTERMIND` and child-exit-restarts tests pass.
3. Chunk buffer + deliver paths in `register.ts` (newline buffering; idle submit vs mid-turn append via main-loop turn events); deliverable is one delivery per line; know it worked when the idle-line (one submit), mid-turn-line (one append), and split-chunk (one delivery) tests pass.
4. Ack gate in `register.ts` (ack only after clean resolve, `seq` 0 never, bounded retry of the `--ack` run); deliverable is exactly one ack per delivered line; know it worked when the one-submit-then-one-ack, one-append-then-one-ack, and state-line-delivered-with-no-ack tests pass.
5. Refusal path in `register.ts` (`drop`/`deny`/rejection → no ack, `$.ui.log`, bounded retry then leave the loop); deliverable is admit cleared with fallback to `wait`; know it worked when the deny (no ack, child ended) test passes and a restart re-delivers.
6. `Makefile` `mod-test` target + `.gitignore` types line; deliverable is a local-only entry point; know it worked when `make mod-test` passes and `make check` is still green (Go suite untouched).
7. Two mutation checks, each reverted after: (a) ack before submit resolves → the idle/mid-turn ack test must fail; (b) ack on deny → the deny test must fail; know it worked when each mutation flips its named test red and the revert is green.
8. Save this plan verbatim to `docs/plans/2026-10-09-mod-deliver.md` and commit code + tests + plan as new commit(s); know it worked when `make mod-test` then `make check` both exit 0, `git status --porcelain` is clean, and `git show --stat HEAD` lists exactly the declared scope plus the plan.

Focused command while iterating: `claude plugin test claude-plugin`. Final full commands once: `make mod-test`, then `make check`. All work lands as new commits; never amend or rebase a commit already on a remote binding's branch. If any step needs a Go change, halt and report instead of editing Go.

## What is deleted (closed list)

1. Nothing: additive only — no existing hook, script, command, or Go file is removed, reworded, or re-routed.

## Report must include

- `git diff --stat` against the declared scope; every file outside it named with why.
- Each new test by name with its focused-command result; the `make mod-test` and `make check` results.
- Both mutation checks: what was broken, the named test that failed, revert confirmation.
- Confirmation that no Go file changed, no commit was amended/rebased/force-pushed, and no lint/comment/filesize/coverage exclusion was added.
- Anything said here that the tree proved false (notably the `modules`-key validate result).

## MasterMind corrections (review of this plan; these override anything above)

1. **A mid-turn append the turn never reads.** `$.session.append` is read "at
   the turn's next step". If the main turn completes after the append but
   before another step starts, the row sits in the transcript unread and the
   session goes idle with a report it has not acted on. Track, per main turn,
   whether an append happened after the last main-loop step began (the
   streaming `turn.step` event, main loop only); on that turn's
   `turn.complete`, if so, `$.prompt.submit({ text })` a one-line nudge naming
   the binding and round (e.g. `relevo: the report for <binding> r<round> is
   above -- act on it`). The ack still follows the append's clean resolve, not
   the nudge. Test: append, then `turn.complete` with no step in between ->
   exactly one nudge submit; append followed by a step -> no nudge. Mutation:
   drop the nudge; the first test must fail.
2. Once the identity resolves, cancel the 10 s retry timer; restart backoff is
   capped (state the cap) and resets after a child has run cleanly for a
   while (state how long).
3. The builder's machine runs Claude Code 2.1.293; if `claude plugin
   validate`/`test` disagree with the 2.1.294 declarations anywhere, record the
   difference in the report rather than coding around it silently.

## Round 2

On top of 71690903 ("Deliver relevo push lines from the Claude Code mod");
new commit only (no amend/rebase/force-push). Harden the mod's delivery
(`claude-plugin/hooks/register.ts`). TypeScript only; if a Go change is needed,
stop and report.

1. **A failed ack stalls delivery.** `relevo push` waits for each entry's
   confirm before writing the next line, so after `ack()` gives up the stream
   stops for good with only a log line. Fix:
   - Keep a per-session set of delivered `(binding, seq)` (seq > 0) in `Ctx`.
   - `ack()` retries with a delay (e.g. 1 s, 2 s, 4 s... up to ~60 s total,
     via `io.sleep`), not 3 immediate tries.
   - If the ack still fails, end the child (return from `consume`), so
     `relevo push` clears the admit and, after the supervisor restarts it,
     sends the same line again.
   - When a line arrives whose `(binding, seq)` is already in the delivered
     set, do NOT deliver it again: only ack it.
   - Tests: ack fails every try -> child ended, nothing delivered twice, the
     resent line is acked without a second submit/append; ack succeeds on the
     third try -> one delivery, one ack, the child keeps running.
   - Mutation: drop the delivered-set check -> the resend test must fail.
2. **Delivery retries are immediate.** Add a short growing delay between the
   `DELIVER_TRIES` attempts (`io.sleep`). Test: a refusal then success
   delivers once and waits between tries.
3. **The nudge text for state lines.** When the unread item came from a
   `kind: "state"` line, say so ("relevo: <binding> r<round> changed state
   (needs_you|broken) -- see above") instead of calling it a report. Test both
   wordings.
4. **`mod-test` is not in `.PHONY`** in the Makefile; add it.

Finish: `make mod-test` (all pass, give the count), `make check`; report the
tests by name, the mutation, and `git diff --stat 71690903..HEAD`.
