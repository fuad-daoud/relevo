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

The answer lives on the database's `repo` row and is set with:

```
relevo mastermind enable              # this session only; the repo stays unset
relevo mastermind enable --repo       # always in this repository
relevo mastermind disable --repo      # never in this repository
relevo mastermind reset               # clear the answer; the next session asks
```

`disable` without `--repo` forgets the current session's record, the same as
`forget <id|name>`; it writes no repo answer. `reset` takes no flag: the only
answer it touches is the repository's. Precedent: the answer is stored in the
database, so it survives restarts and syncs nowhere today.

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

## 5. The shared rendering

One function decides what a session is told:

```
ConsentText(state Consent, rec *Record) string
```

- `yes` + record -> identity sentence + `Guide()` (today's HookOutput body).
- `yes` + no record -> `Guide()` alone; the harness registers on its own
  (opencode) or the caller registers first (Claude).
- `unset` -> the ask-note:

  ```
  This repository has not answered whether relevo should be its MasterMind.
  Ask the human, then run one of:
    relevo mastermind enable          -- this session only
    relevo mastermind enable --repo   -- this repository from now on
    relevo mastermind disable --repo  -- never in this repository
  Do not bind or send until the human answers.
  ```

- `no` -> the empty string.

The Claude hook wraps this in the `additionalContext` envelope when it is
non-empty. opencode pushes it into the system instructions (§6.2).

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

### 6.2 opencode

A new read verb gives the plugin its text:

```
relevo mastermind guide [--cwd DIR] [--kind K --session S] [--json]
```

- Plain output: the consent text (empty for `no`).
- `--json`: `{"state":"enabled|ask|disabled","text":"...","repo":"...","id":"...","name":"..."}`.
- On `enabled` with `--kind opencode --session <id>`, it ensures the session's
  record exists (idempotent `Init`, HostPID 0, as the TUI plugin did before) and
  reports its id and name, so the identity sentence names the right MasterMind
  and the TUI needs no separate registration call. Other kinds change nothing.
- An internal failure is an error exit; the plugin treats any non-zero as "no
  text" and logs once. The verb never touches the network or a harness.

The shipped plugin gains two changes:

- **`server.ts`** registers `ctx.session.hook("context", ...)`. On
  `event.kind == "primary"` it pushes the cached text into `event.system` as a
  text part. The text is fetched once per `sessionID` (the plugin calls
  `relevo mastermind guide --json --kind opencode --session <id>`; the verb
  resolves the session's directory from opencode's own database, so the
  plugin passes no cwd) and cached in module state. A failed fetch caches
  "nothing" for that session and logs to stderr; it never throws into the
  model call.
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

### 6.3 agy and other harnesses

Unchanged: no hook, no injection. A manual `relevo mastermind init` still
registers, and the repo answer does not block it. Their sessions simply do not
participate in consent until a hook exists.

## 7. Surfaces

- `relevo mastermind enable [--repo]`, `disable [--repo]`, `reset`,
  `guide [--json]`.
- `relevo mastermind list` gains nothing; the answer is per repo.
- `relevo doctor` adds one row in the MasterMind group: the current repo's
  answer and the command that changes it
  (`mastermind consent: unset (relevo mastermind enable --repo)`).
- The ask-note names the three commands, so the model never has to look them up.

## 8. Testing

- **Pure** in `internal/mastermind`: consent states and rendering (`ConsentText`
  for each state, with and without a record), repo ref equality, the ask-note's
  command list, `guide --json`'s state mapping.
- **`internal/db`**: migration 010 applies on a v9 database; consent round-trip
  (set, read, overwrite); a repo with only `common_dir` and one with only
  `origin_url`; NULL read as unset.
- **`cmd/relevo`**: the hook's four branches against a temp XDG root and a temp
  git repo (no harness, no network); `enable`/`disable` write the answer;
  `guide --json` states; doctor's row text. TestMain already isolates HOME,
  XDG roots and `RELEVO_*`.
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
