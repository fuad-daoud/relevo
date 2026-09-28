# Consent round 4 (#632, PR #634): tell the live session at once, and remember a session's own answer

Branch `relevo/mm-consent`. Spec: `docs/specs/2026-09-27-mastermind-consent-design.md`. This round follows R3 (`docs/plans/2026-09-27-mm-consent-r3.md`).

## Where the seed and the code disagree

1. **`ctx.session.prompt` has only been proven for the HTTP endpoint, not for the server plugin.**
   - `docs/specs/2026-09-22-opencode-delivery-design.md` proved `POST /api/session/{id}/prompt` on opencode 2.0.12. relevo's deliverer still uses it (`internal/delivery/deliver_opencode.go:265`).
   - The probe (`docs/specs/2026-09-24-opencode-tui-probe.md`, 2.0.14) only looked at the **TUI** api: `api.client.session.prompt` and `api.data.session.prompt` submit a turn; `api.client.session.synthetic` was called with `resume:false` only.
   - No one has checked that the **server** plugin's `api` (`server.ts` `setup`) exposes `session.prompt` in 2.0.18. There is no opencode binary on this machine, and memory says the installed `@opencode-ai/plugin` types are stale.
   - Step 1 therefore checks this first and halts if the call is absent.
2. **`mastermind.go` is already 587 lines (the cap is 600).** Any change to the SessionStart hook has to move the hook out of that file first (step 4).
3. **Two doc comments sit in the wrong place.**
   - `cmd/relevo/mastermind.go:258-262` is `mastermindOpenTimed`'s comment, but it sits over `appendEnvLine`.
   - `cmd/relevo/mastermind_consent.go:127` is an orphan `// appendEnvLine ...` line.
   - Put both right in the files this round already touches.

## Decisions I made

- **opencode uses `session.prompt`, not `session.synthetic`.**
  - A synthetic item (probe Q14) writes no message row and shows nothing in the chat. With `resume:false` it reaches the model only on the session's next run, which is the "next request" behaviour A is meant to replace.
  - `resume:true` on a synthetic item has never been probed.
  - The delivery spec (§2) already rejected synthetic for anything that must become a turn.
  - `prompt` is the path relevo already relies on. It also puts a visible turn in the chat, so the human sees why the model spoke.
  - Body: `{text, delivery: "steer", resume: true}`. On an idle session `steer` starts a turn now; on a busy one it lands at the next step of the running turn. Both count as "at once". The report's delivery spec chose `queue` only to protect its "deliver when idle" rule, which does not apply to a status line.
  - If the probe shows that `steer` on an idle session starts no turn, use `queue` and say so in the report.
- **No hook loop in opencode.** The only code that sends a prompt is a new poller.
  - It records the new status token in `lastMasterMind` *before* it sends.
  - So the turn its prompt starts reaches the `context` hook with no change left to report.
  - The `context` hook never sends a prompt, and the plugin registers no `prompt`/chat hook.
  - If a send fails, the poller puts the previous token back. The existing `context`-hook notice then still delivers it on the next request, which is today's behaviour kept as the fallback.
- **The poller watches only sessions the plugin has seen.** The watch set is the sessions whose `context` event came in within an idle window.
  - The window is a named constant whose comment says why: the opencode service is long-lived and hosts many sessions, and it spawns one `relevo` per session per tick.
  - It runs on the existing 5 s `guideRecheckMS` and skips a session whose check is still in flight.
- **Claude needs a stored "told" status.** Each hook call is a separate process, so the last status the session was told is stored in the new table (column `told`). opencode keeps its in-memory `lastMasterMind` and does not use the column.
- **Migration 011** creates `session_consent`:
  - Columns: `harness_kind TEXT NOT NULL`, `session_id TEXT NOT NULL`, `answer TEXT` (yes/no/NULL), `answer_at TEXT`, `told TEXT`, `told_at TEXT`, `PRIMARY KEY (harness_kind, session_id)`.
  - A composite primary key has precedent in 003 and 004, and the key covers the only lookup, so no extra index.
  - It is `CREATE TABLE IF NOT EXISTS` only, in the dialect of 001-010: no WITHOUT ROWID, no triggers, no RETURNING.
  - Writes are UPDATE-then-INSERT inside one `Tx`, like `SetRepoConsent`. The package uses no `ON CONFLICT` today, so don't introduce it.
- **`BindingFormat` does not change.** `Binding` and `mastermind.Record` gain no fields: the answer and `told` live in their own table. `recordFormat` stays as it is too.
- **Precedence is a pure function in `internal/mastermind`.** The effective answer is the session answer when set, otherwise the repo answer.
  - A session `no` hides any record: no briefing, no registration.
  - A session `yes` registers.
  - The "a record is always briefed" rule in `ConsentText` stays for sessions with no session answer (a manual `init`).
- **An `--repo` answer also clears the calling session's own answer** when that session can be detected or is named with `--kind/--session`. Otherwise "never in this repository", said from a session that had earlier said "this session only", would leave that session enabled.
- **Verb names and scope:**
  - The Claude verb is `relevo mastermind notice --hook claude`.
  - `reset` gains `[--kind K --session S]` and clears the repo answer plus this session's answer and `told`.
  - `reset` outside a git repo clears only the session, and errors only when there is neither a repo nor a session.
  - `enable`/`disable` without `--repo` write the session answer whether or not the cwd is a repo.

## Behaviour and cases

**B: the session answer**

| call | writes | then |
|---|---|---|
| `enable [--kind K --session S]` | session `yes` | registers (today's `Init`) |
| `enable --repo` | repo `yes`; clears the caller's session answer | registers |
| `disable [--kind K --session S]` | session `no` | forgets the record; a missing record is not an error; `ErrInUse` stays an error whose message says the answer was recorded |
| `disable --repo` | repo `no`; clears the caller's session answer | nothing else |
| `reset [--kind K --session S]` | clears the repo answer and this session's answer and `told` | the next session, or the next prompt, asks |

**How the effective answer is used**

- `guide --kind K --session S`:
  - session `no` → `disabled`, empty text, and no `Init` even when the repo says `yes`.
  - session `yes` → `enabled`, with `Init` when there is no record, and the identity sentence plus the guide.
  - otherwise → today's logic.
- `guide` without a session reads the repo only. `registerTools` is unchanged.
- The SessionStart hook reads the effective answer for the payload's `session_id`:
  - session `no` → `{}`.
  - session `yes` → registers even in an unset repo or a `no` repo.
  - It then writes `told` as the baseline for what it just injected (see the tokens below). If the timed open failed there is no database handle and no write.

**A: status tokens**

- The tokens are `none`, `ask`, and `mastermind:<id>:<name>`. A rename is a token change.
- A pure `StatusNotice(prev, next, record)` returns the text for each transition:
  - → mastermind: the identity sentence and `Guide()`.
  - → `none` from mastermind: the existing "no longer a relevo MasterMind; stop acting as one" line.
  - same id, new name: a one-line rename notice.
  - → `ask`: `AskNote`.
  - `ask` → `none`: one line saying the question is answered no and not to ask again.
  - no change: empty.
- Keep the wording aligned with `server.ts:215-218`.

**A: Claude**

- `hooks.json` gains a `UserPromptSubmit` entry that runs `relevo mastermind notice --hook claude`.
- The verb:
  - reads the payload (`session_id`, `cwd`) and opens the database with the timed open;
  - works out the current token the same way the SessionStart hook does, and registers when the effective answer is `yes` and no record exists (HostPID from `os.Getppid()` as the SessionStart hook does);
  - compares the token with the stored `told`;
  - on a change, prints `additionalContext` with `hookEventName: "UserPromptSubmit"` and writes `told`;
  - otherwise prints `{}`.
- If `told` is NULL (a session from before this round, or a failed baseline), it writes the baseline and prints `{}`.
- It never exits non-zero and never blocks. Any failure prints `{}`, plus stderr.
- `$CLAUDE_ENV_FILE` is only guaranteed in SessionStart. When it is absent, the notice uses the no-env wording (`HookOutputNoEnv`'s note) and host-pid resolution covers the gap.
- Nothing is printed outside a git repo that has no session answer.

**A: opencode**

- When the poller sees a token change on a watched session, it sends one `session.prompt` whose text is the existing status-change line, prefixed `relevo:`.
- `ask` ↔ `disabled` stays silent, as the TUI-side tokens do today.
- After a palette command succeeds, the plugin checks that session at once instead of waiting up to 5 s.
- Both `server.ts` and `tui.tsx` pass `--kind opencode --session <id>` to the `--repo` commands too, so the caller's session answer is cleared.

## Seams

- `internal/db/migrations/011_session_consent.sql` (new), `internal/db/testdata/schema.golden` (regenerate with `go test ./internal/db -run Schema -update`).
- `internal/db/consent.go`: add the session answer and `told` read/write functions after `SetRepoConsent` (l.73-102). If the file gets crowded, put them in a new `internal/db/session_consent.go`. Tests go in `internal/db/consent_test.go`, with a migration-011 test modelled on `TestMigration010AddsRepoConsent` (l.117).
- `internal/mastermind/consent.go` (51 lines): add the precedence function, the status token and `StatusNotice`. `ConsentText` changes only as far as the session-`no` case needs.
- `internal/mastermind/hook.go`: `hookEventName` (l.66) and `encodeHookContext` (l.98) take the event name, so SessionStart and UserPromptSubmit each carry their own.
- `cmd/relevo/mastermind.go`:
  - move `mastermindInitHook` and `mastermindInitHookFailure` (l.158-248) unchanged into a new `cmd/relevo/mastermind_hook.go`, then change them there;
  - update the dispatch and usage (l.33-80) for `notice` and the `reset` flags;
  - fix the misplaced comment at l.258.
- `cmd/relevo/mastermind_hook.go` (new): the SessionStart hook with the effective answer and the `told` baseline, plus `notice --hook claude`.
- `cmd/relevo/mastermind_consent.go`:
  - `cmdMasterMindEnable` (l.129-184) and `cmdMasterMindDisable` (l.188-246): write the session answer; `--repo` clears the caller's answer.
  - `cmdMasterMindReset` (l.250-272): add the flags and clear the session.
  - `cmdMasterMindGuide` (l.296-402): session precedence (l.328-369).
  - Remove the orphan comment at l.127.
- `claude-plugin/hooks/hooks.json`: add the `UserPromptSubmit` entry.
- `internal/harness/opencodeplugin/server.ts`:
  - `setup` (l.191-240): the watch set and poller;
  - the `context` hook (l.203-235): record `lastSeen` per session; its change detection stays as the fallback;
  - `registerEnableCommands` (l.118-157): pass the session to `--repo` and trigger an immediate check.
- `internal/harness/opencodeplugin/tui.tsx` l.917 and l.934: pass `--kind opencode --session` to the `--repo` commands.
- `cmd/relevo/mastermind_test.go`: add the new verb and flag cases here. Split the file if it grows past what reads well; test files have no size cap, but keep it navigable.

## Steps

1. **Check the server plugin's api in 2.0.18.**
   - In the sandbox (`~/sandbox/relevo-oc-test`), temporarily log `Object.keys(api.session ?? {})` and `Object.keys(api.client?.session ?? {})` from the installed `server.ts` `setup`, then restore the file.
   - Use `api.session.prompt` if it exists, otherwise `api.client.session.prompt`. Record the argument shape.
   - Done when the report quotes the keys. If neither exists, **halt**.
2. **Migration 011, the golden and the db functions.**
   - Done when the focused db tests pass: a v10 database upgrades, NULL reads as unset, the answer and `told` round-trip, and a second apply is a no-op.
3. **Pure precedence, tokens and `StatusNotice`, plus the event-name parameter on the hook envelope.**
   - Done when the `internal/mastermind` tests cover every row of the precedence rule and every transition above.
4. **Move the hook into `mastermind_hook.go` unchanged.**
   - Done when the existing `TestMasterMindInitHook*` tests pass untouched.
5. **The hook reads the effective answer and writes the `told` baseline.**
   - Done when there are tests for: session `no` in a `yes` repo → `{}`; session `yes` in an unset repo → registers; the baseline is written.
6. **`enable`/`disable`/`reset`/`guide` apply session precedence.**
   - Done when tests cover:
     - `disable --kind --session` in a `yes` repo, then `guide`, reports `disabled` and creates no record;
     - `enable --kind --session` in a `no` repo gives `enabled`;
     - `reset` clears both answers;
     - `--repo` clears the caller's answer.
   - The existing `TestMasterMindDisableBySession` (l.842) changes: a missing record is no longer an error. Say so in the report.
7. **`notice --hook claude` and the `hooks.json` entry.**
   - Done when tests cover: no change → `{}`; an enable from elsewhere → context with the event name `UserPromptSubmit` and `told` updated; a rename → the rename line; a NULL `told` → baseline only; a broken payload → `{}` and exit 0.
   - The tests use a temp XDG root and a temp git repo, and never spawn a harness or touch the network (TestMain already isolates the environment). If `internal/doctor` has a test that parses the real `hooks.json`, it must still pass.
8. **Plugin: poller and prompt in `server.ts`; session flags on the `--repo` commands in both files.**
   - Done when `make check` passes (install bytes and the embed are unchanged in shape) and step 10's probe passes.
9. **Docs.**
   - Update the spec: §2 (session answers and `reset`), §3 (a new decision on precedence and `--repo` clearing the caller), §4 (migration 011), §5 (session `no` hides the record), §6.1 (`UserPromptSubmit`, `told`), §6.2 (poller, prompt vs synthetic and why, loop guard), §7 (the `notice` verb), §8, §9 (no pruning of `session_consent` rows yet).
   - As the last step, save this plan as `docs/plans/2026-09-27-mm-consent-r4.md`.
   - Done when `scripts/check-comments.sh` passes: no `#NNN`/`§` in any **code** comment added this round. Spec prose may keep its section refs.
10. **Manual probe (this is what covers the plugin, not CI).** In the sandbox, with an opencode session idle in an unset repo:
    - `relevo mastermind enable --repo` from another terminal → within about 5 s a `relevo:` turn appears and the model answers it;
    - `relevo mastermind rename` → a rename turn;
    - `/relevo-disable` → a "no longer" turn, then a restart of the session → `guide` says `disabled`;
    - exactly one turn per change: no repeated prompts, no loop.

    Then a Claude session in the sandbox: enable `--repo` from another terminal, and the next prompt carries the notice.

**Commands**
- Focused, run after each step: `go test -count=1 ./internal/db/ ./internal/mastermind/ ./cmd/relevo/ -run 'Consent|Migration|Schema|MasterMind|Notice|Hook'`
- At the end: `make check`, then `make e2e`.
- If coverage moves between packages, regenerate the baseline with `sh scripts/check-coverage.sh --write` and say so. Never lower it to get green.

## What is deleted

1. Treating a missing record as an error in `disable --kind K --session S` (`mastermind_consent.go:225-228`). It now writes session `no` whether or not a record exists.
2. The orphan comment at `mastermind_consent.go:127`, and the misplaced one at `mastermind.go:258-262` (moved over `mastermindOpenTimed`).

Nothing else is removed. The `context`-hook status-change injection stays as the fallback.

## What the report must include

- Step 1's key listing for the server plugin api, the method used, and whether `steer` on an idle session started a turn (or that `queue` was used instead).
- The final migration 011 SQL, and a statement that `BindingFormat` and `recordFormat` did not change.
- `git diff --stat`, checked against the seams above.
- Every changed existing test, with the reason (at least `TestMasterMindDisableBySession`).
- The probe results from step 10, with the transcript or screenshots of the opencode turns.
- Whether the coverage baseline was regenerated.
- A reminder that the Claude plugin is cached by version: installed users only see the new `hooks.json` after the next release bump. Do not bump the version in this round.
- The output of `make check` and `make e2e`.
