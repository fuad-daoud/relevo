# MasterMind consent: ask once per repository, and inject the guide on opencode

Issue: #632. Depends on #633 (the MasterMind rename). Supersedes nothing.
Status: designed 2026-09-27.

## 1. Problem

relay registered every harness session as its MasterMind, whatever the project:

- **Claude Code.** `claude-plugin/hooks/hooks.json` runs
  `relevo mastermind init --hook claude` on every `SessionStart`
  (`startup`, `resume`, `clear`, `compact`). The hook mints a record, prints
  `hookSpecificOutput.additionalContext` naming the MasterMind plus the guide,
  and appends `export RELEVO_MASTERMIND=<id>` to `$CLAUDE_ENV_FILE`.
- **opencode.** The shipped TUI plugin runs
  `relevo mastermind init --kind opencode --session <id>` for every session
  (`internal/harness/opencodeplugin/tui.tsx`). It injects no model-facing text:
  no `additionalContext` envelope exists for `--kind opencode`, and relevo
  configures no MCP server there, so an opencode session gets neither the
  identity sentence nor the guide nor the `status`/`send`/`done` tools.
- **agy** has no hook; sessions are registered by whatever calls
  `relevo mastermind init`.

There is no repo check, no consent and no per-project opt-out. `mastermind
forget` removes a record, but the next session in that repo registers again.

This design answers both halves together because they are one decision: the
injected text depends on the repo's answer.

## 2. Shape

Consent is a property of the **repository**:

- `unset` (the default) -- ask; do not register; inject the ask-note.
- `yes` -- register and inject the identity sentence and guide.
- `no` -- do not register; inject nothing.

The answer lives in two places: the database's `repo` row for the repository,
and the new `session_consent` table for one session's own answer. A session's
answer, when it has one, outranks the repository's.

```
relevo mastermind enable                       # this session answers yes; the repo stays unset
relevo mastermind enable --repo                # this repository answers yes; the caller's own answer is cleared
relevo mastermind enable --kind K --session S  # the same, named by a plugin that knows its session
relevo mastermind disable                      # this session answers no; the repo stays unset
relevo mastermind disable --repo               # never in this repository
relevo mastermind reset [--kind K --session S]  # clear the answers; the next session -- or the next prompt -- asks
```

`enable`/`disable` without `--repo` write the session's own answer whether or
not the cwd is a git repository, and `enable` still registers the calling
session. `disable` also forgets the session's record; a session that never had
one is not an error, so a session that never registered can still say never.
`ErrInUse` stays an error, and its message says the answer was recorded anyway.
`disable --kind K --session S` names one session explicitly, for a plugin that
knows its id.

An `--repo` answer also clears the calling session's own answer when that
session can be detected or is named with `--kind/--session`: otherwise "never in
this repository", said from a session that had earlier said "this session only",
would leave that session enabled.

`reset` clears the repository's answer and the calling session's answer and its
`told` baseline. Outside a git repository it clears only the session, and it
errors only when there is neither a repository nor a session to clear.

Precedent: the answers are stored in the database, so they survive restarts and
sync nowhere today.

## 3. Decisions

1. **Per repo, not per (repo, harness).** One project uses one answer. Asking
   again per harness would nag a user who runs two harnesses in one repo. The
   harness is recorded only in the ask's wording, if at all.
2. **The ask is the model's job.** `SessionStart` is non-interactive and the
   opencode hook is not a prompt, so neither can ask the human directly. On
   `unset` the hook injects one short note asking the model to put the question
   to the human and then run one of the three commands. This works the same on
   both harnesses, needs no TUI work, and matches the Claude plugin's existing
   contract (hooks never prompt).
3. **Non-git cwd is `no` everywhere.** Without a repo there is nothing to
   remember, so asking would repeat every session and registering would be
   silent. A session outside a git repo gets no record and no injection; the
   doctor row says why. A manual `relevo mastermind init` still works (it is
   explicit).
4. **Existing records are not grandfathered.** Migration 010 backfills
   nothing. The next session in each repo asks, as #632 asks for. Existing
   records stay until the daemon's prune or `forget`; they are not deleted by
   answering `no`.
5. **`no` suppresses registration and injection only.** It does not delete
   bindings or records, and it does not stop the Claude plugin's MCP server
   from starting (the declaration is static in `plugin.json`); with no
   MasterMind, `relevo mcp` runs tools-only and every verb that needs an
   identity fails with `ErrNoMasterMind`. Tightening that is later work
   (§9).
6. **`enable --repo` registers the calling session too.** Otherwise answering
   "always" would leave the current session unregistered until the next one.
   `enable` alone registers and writes no repo answer.
7. **The session answer outranks the repository's, and a session `no` hides a
   record.** The effective answer is the session's own when it has one,
   otherwise the repository's. A session `no` means no briefing and no
   registration even where the repository says yes and a record exists; a
   session `yes` enables, registers and briefs even where the repository is
   unset or says no. Both answers are read by one timed open, so the hook and
   the notice agree on what governs a session.
8. **An `--repo` answer clears the caller's own answer.** Otherwise "never in
   this repository", said from a session that had earlier said "this session
   only", would leave that one session enabled. The clear happens when the
   caller can be detected or is named with `--kind/--session`; a caller relevo
   cannot identify is left alone rather than guessed at.

## 4. Data

Migration 010 adds two columns to `repo` (Turso-safe, add-only):

```sql
ALTER TABLE repo ADD COLUMN mastermind_consent TEXT;
ALTER TABLE repo ADD COLUMN consent_at TEXT;
```

- `mastermind_consent` is `yes`, `no`, or NULL (unset). `consent_at` is the
  RFC3339 time of the last answer.
- Repo identity comes from `git.RepoFacts(cwd)` as `captureRepo` reads it
  (`internal/relevo/repo.go`): `OriginURL` normalised, `CommonDir` absolute.
  `db.UpsertRepo` already keys on `origin_url` first, then `common_dir`, and
  fills the other column when it is null. Consent reads and writes reuse those
  rules; a repo row is created on the first answer, not on the first session.
- Values are validated in Go (`Consent.Valid`); the column stays free text so
  a newer binary's value survives an older one, per the format rule.

Migration 011 adds the session's own answer and its told baseline:

```sql
CREATE TABLE IF NOT EXISTS session_consent (
    harness_kind TEXT NOT NULL,
    session_id TEXT NOT NULL,
    answer TEXT,
    answer_at TEXT,
    told TEXT,
    told_at TEXT,
    PRIMARY KEY (harness_kind, session_id)
);
```

- `answer` is the session's own `yes`, `no`, or NULL (unset: the repository's
  answer applies). `answer_at` is when it was written.
- `told` is the last status token the session was told, and `told_at` when.
  NULL means nothing has been delivered yet -- a session from before this
  round, or one whose baseline write failed -- and the next hook read writes
  the baseline instead of reporting a change.
- The composite primary key covers the only lookup, `(harness_kind,
  session_id)`, so no extra index is needed; 003 and 004 set the precedent.
- It is `CREATE TABLE IF NOT EXISTS` only, in the dialect of 001-010: no
  `WITHOUT ROWID`, no triggers, no `RETURNING`. Writes are UPDATE-then-INSERT
  inside one `Tx`, like `SetRepoConsent`; the package uses no `ON CONFLICT`.
- `BindingFormat` and `mastermind.Record`'s format do not change: the answer and
  `told` live in their own table, so no stored JSON shape moves.

## 5. The shared rendering

One function decides what a session is told:

```
ConsentText(state Consent, rec *Record) string
```

- a record -> identity sentence + `Guide()`, whatever the repository's answer:
  the session answered for itself (`relevo mastermind enable`) or the repo did.
- `yes` with no record -> `Guide()` alone; the harness registers on its own
  (opencode) or the caller registers first (Claude).
- `unset` with no record -> the ask-note:

  ```
  Before anything else, ask the human whether relevo should be this repository's MasterMind. If you have an interactive question or choice tool, use it; otherwise ask in text. Offer exactly these three options:
    relevo mastermind enable          -- this session only
    relevo mastermind enable --repo   -- this repository from now on
    relevo mastermind disable --repo  -- never in this repository
  Wait for their answer, then run the command they choose. Do not work on anything else first.
  ```

- `no` with no record -> the empty string.
- A `no` hides any record: the rendering is empty even when a record exists, and
  the callers do not register. A session's own `no` and a repository's `no` are
  the same value once precedence has been applied.

The Claude hook wraps this in the `additionalContext` envelope when it is
non-empty. opencode pushes it into the system instructions, and while the
answer is unset onto the user's own turn (§6.2).

A **status token** names what the session currently is, so a change can be
noticed: `none`, `ask`, or `mastermind:<id>:<name>`. A pure `StatusNotice`
renders the transition: a grant is the identity sentence and the guide, a
revocation is the "no longer a relevo MasterMind" line, a rename is one line,
`ask` is the ask-note, and an answered question says not to ask again. A rename
is therefore a token change, and no change renders nothing.

## 6. Harness wiring

### 6.1 Claude Code

`mastermindInitHook` resolves the repo from the payload's `cwd` and reads the
consent before `Init`:

| consent | hook does | `Init` |
|---|---|---|
| `yes` | today's answer + `$CLAUDE_ENV_FILE` line | yes |
| `unset` | the ask-note | no |
| `no` | `{}` (no `additionalContext`) | no |
| no repo | `{}` | no |

A consent read that fails (database, git) falls back to `unset`, the safe
answer: no silent registration.

`relevo mastermind enable --repo` run from a session appends the
`RELEVO_MASTERMIND` line to `$CLAUDE_ENV_FILE` when it is set, so later Bash
calls in that session resolve the record without a restart; host-pid
resolution covers the window before that.

**The session's own answer and `told`.** The hook reads the effective answer for
the payload's `session_id`: a session `no` answers `{}`, a session `yes`
registers even in an unset or `no` repository. It then writes the status token
it just injected (`told`) as the baseline, so the notice below can compare. A
timed open that failed means no database handle and no write; the notice then
treats its first read as the baseline.

**`UserPromptSubmit` tells the session at once.** `hooks.json` gains a
`UserPromptSubmit` entry running `relevo mastermind notice --hook claude`. Each
hook call is a separate process, so the last token a session was told lives in
the database's `told` column rather than in memory. The verb works out the
current token the same way the SessionStart hook does (registering a granted
session that has no record yet, with `HostPID` from `os.Getppid()`), compares it
with `told`, and on a change prints an `additionalContext` envelope whose
`hookEventName` is `UserPromptSubmit`. Otherwise, and on any failure, it prints
`{}` and exits 0: a notice never blocks a prompt. A NULL `told` is written as
the baseline and reported as no change.

`$CLAUDE_ENV_FILE` is only guaranteed in SessionStart. When it is absent the
notice's grant text uses the no-env wording and host-pid resolution covers the
gap; nothing is printed outside a git repository that has no session answer.

### 6.2 opencode

A new read verb gives the plugin its text:

```
relevo mastermind guide [--cwd DIR] [--kind K --session S] [--json]
```

- Plain output: the consent text (empty for `no`).
- `--json`: `{"state":"enabled|ask|disabled","text":"...","repo":"...","id":"...","name":"..."}`.
- With `--kind opencode --session <id>` it reports the session's own record
  when one exists, whatever the repository's answer: `relevo mastermind
  enable` (session-only) answers for that session. When the repo is `enabled`
  and no record exists yet it creates one (idempotent `Init`, HostPID 0), so
  the identity sentence names the right MasterMind and the TUI needs no
  separate registration call. Other kinds change nothing.
- An internal failure is an error exit; the plugin treats any non-zero as "no
  text" and logs once. The verb never touches the network or a harness.

The shipped plugin gains two changes:

- **`server.ts`** registers `ctx.session.hook("context", ...)`, the
  model-facing channel that delivers in opencode 2.0.18 (a `prompt` hook's
  text mutation is accepted but never delivered; probe-verified). The hook
  runs for the agent loop only — compaction, title and generate have their own
  hooks, and the context event carries no `kind` — and pushes the cached text
  into `event.system` on every request. While the answer is unset it also
  prepends the text to the last user message's content: a weak model can skip
  a system part, but it reads its own message. The text is fetched once per
  `sessionID` (the plugin calls `relevo mastermind guide --json --kind opencode
  --session <id>`; the verb resolves the session's directory from opencode's
  own database, so the plugin passes no cwd) and cached in module state. A
  failed fetch caches "nothing" for that session and logs to stderr; it never
  throws into the model call.
- **`tui.tsx`** calls `relevo mastermind guide --json --kind opencode --session
  <id>` and uses the answer: `enabled` carries the record's id and name (the
  guide created it), `ask` and `disabled` register nothing. The old
  unconditional `mastermind init` call is gone.
- **Tools (`server.ts`, at setup).** It reads the location's answer with
  `relevo mastermind guide --json --cwd <location>` and, for `enabled`, registers
  `relevo mcp --kind opencode` through `ctx.mcp.transform`
  (`codemode: false`), so the session gets `relevo_status` / `relevo_send` /
  `relevo_done` and the opencode instruction prelude. `ask` and `no` register
  nothing, and a consent change applies at the next plugin load, like every
  transform.
- **`relevo mcp --kind opencode`** serves tools only (no Claude channel) and
  resolves each tool call's MasterMind from `_meta.sessionID`, which opencode
  sends with every call; the Claude path keeps its startup resolution. The
  opencode prelude says reports arrive as new turns, so no background wait is
  started and a `send` result carries none.
- **Mid-session answers take effect without a restart, and the session is told.**
  Every session re-reads its answer every 5 s. When the session's status changes
  — it becomes a MasterMind, stops being one, or changes name — the next model
  request carries a one-line status change ("this session is now relevo
  MasterMind X" / "this session is no longer a relevo MasterMind; stop acting as
  one") in the same two places as the guide. A failed read keeps the last
  answer, so it never reads as a status change. The TUI re-checks on the same
  cadence and returns to "not enabled" when the answer is gone.
  `ctx.command.transform` registers `/relevo-enable`, `/relevo-enable-repo`,
  `/relevo-disable` and `/relevo-disable-repo`, which run the CLI with the
  location's cwd — a human can answer from the command palette without a shell.
  The TUI registers the same four in opencode's `Ctrl+P` palette (its own
  surface, with a toast on the result), and the sidebar names the two enable
  commands while a session has no MasterMind.
- **A poller sends the status change as a turn.** The context hook's status line
  only reaches a session that asks for context, so a session sitting idle would
  not hear an enable from another terminal. A new poller watches the sessions
  the plugin has seen -- the ones whose context event arrived within an idle
  window -- and on the existing 5 s re-check cadence notices a token change and
  sends **one** `ctx.session.prompt` whose text is the existing status-change
  line, prefixed `relevo:`. The window is a named constant because the opencode
  service is long-lived and hosts many sessions, and each watched session costs
  one `relevo` spawn per tick; a session idle past the window is told at its next
  request, as before. A check already in flight is skipped rather than stacked.
- **`session.prompt`, not `session.synthetic`.** A synthetic item writes no
  message row and shows nothing in the chat: with `resume:false` it reaches the
  model only on the session's next run, which is the behaviour A replaces, and
  `resume:true` on a synthetic item was never probed. The delivery spec already
  rejected synthetic for anything that must become a turn. `prompt` is the path
  relevo already relies on and it puts a visible turn in the chat, so the human
  sees why the model spoke. Body: `{text, delivery: "steer", resume: true}`; the
  probe confirmed that `steer` on an idle session starts a turn at once, and on
  a busy one it lands at the next step of the running turn. Both count as "at
  once", which is all a status line needs.
- **No hook loop.** The only code that sends a prompt is the poller, and it
  records the new token in its in-memory map *before* it sends, so the turn its
  prompt starts reaches the context hook with no change left to report. The
  context hook never sends a prompt, and the plugin registers no `prompt`/chat
  hook. A failed send puts the previous token back, so the context hook still
  delivers the change on the session's next request: the existing behaviour kept
  as the fallback. `ask` and `disabled` transitions stay silent, as the TUI-side
  tokens do today.
- **A palette command checks at once.** After `/relevo-enable`,
  `/relevo-enable-repo`, `/relevo-disable` or `/relevo-disable-repo` succeeds,
  the plugin checks that session immediately instead of waiting up to 5 s for
  the poller's next tick. Both `server.ts` and `tui.tsx` pass `--kind opencode
  --session <id>` to the `--repo` commands too, so the caller's own answer is
  cleared as §2 and §3 describe.

### 6.3 agy and other harnesses

Unchanged: no hook, no injection. A manual `relevo mastermind init` still
registers, and the repo answer does not block it. Their sessions simply do not
participate in consent until a hook exists.

## 7. Surfaces

- `relevo mastermind enable [--repo] [--kind K --session S]`,
  `disable [--repo] [--kind K --session S]`, `reset [--kind K --session S]`,
  `guide [--json]`.
- `relevo mastermind notice --hook claude` is the UserPromptSubmit hook's verb:
  it prints an `additionalContext` envelope only when the session's status
  changed since the token it was told. It is not a human surface and is absent
  from the usage unless the hook flag is given.
- `relevo mastermind list` gains nothing; the answers are per repository and per
  session.
- `relevo doctor` adds one row in the MasterMind group: the current repo's
  answer and the command that changes it
  (`mastermind consent: unset (relevo mastermind enable --repo)`).
- The ask-note names the three commands, so the model never has to look them up.

## 8. Testing

- **Pure** in `internal/mastermind`: consent states and rendering (`ConsentText`
  for each state, with and without a record), repo ref equality, the ask-note's
  command list, `guide --json`'s state mapping.
- **`internal/db`**: migration 010 applies on a v9 database; migration 011
  applies on a v10 database and creates `session_consent`; consent round-trip
  (set, read, overwrite); a repo with only `common_dir` and one with only
  `origin_url`; NULL read as unset; the session answer and `told` round-trip and
  clear, and a second apply of either migration is a no-op.
- **`internal/mastermind`**: the precedence rule for every pair of answers; the
  status token per answer and record; `StatusNotice` for every transition (no
  change, a grant, a rename, a revocation, an answered question); the
  `UserPromptSubmit` envelope carries its own event name.
- **`cmd/relevo`**: the hook's four branches against a temp XDG root and a temp
  git repo (no harness, no network); a session `no` in a `yes` repo answers `{}`
  and registers nothing; a session `yes` in an unset repo registers; the `told`
  baseline is written; `enable`/`disable`/`reset`/`guide` apply session
  precedence; the notice's no-change, grant, rename, NULL-baseline and
  broken-payload cases; `guide --json` states; doctor's row text. TestMain
  already isolates HOME, XDG roots and `RELEVO_*`.
- **`internal/mcp`**: a `tools/call`'s `_meta.sessionID` reaches the verbs; the
  opencode prelude and its use by initialize; an opencode `send` carries no
  wait line; the per-session resolver's filter and error.
- **opencode**: no CI coverage (no opencode in CI). A manual probe per
  `docs/specs/2026-09-24-opencode-tui-probe.md`: with `yes`, the first model
  call carries the guide in its system text; with `unset`, the ask-note; with
  `no`, neither; a non-relevo project gets nothing; title and compaction calls
  are untouched (`kind` filter).

## 9. Out of scope (later rounds)

- A TUI dialog for the ask (the model-ask ships first).
- Per-harness answers and a repo picker in the cockpit.
- Deleting records on `disable --repo`; `forget` stays the explicit cleanup.
- agy injection and agy tools; agy has no hook to hang them on.
- Pruning `session_consent` rows. A row is one (harness_kind, session_id) pair,
  a few dozen bytes, and a dead session's row is harmless -- worse, deleting it
  would make the notice re-baseline a session that is still alive. Rows are left
  until a later round has a reason to collect them.
