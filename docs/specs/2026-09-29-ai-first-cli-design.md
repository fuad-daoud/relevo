# AI-first CLI: one machine contract on every verb, human renderings kept

**Issue:** #505.
**Amends the issue's 2026-09-25 decision (owner, 2026-09-29):** humans keep using
the CLI. No human rendering is removed; the only ones already gone are the ones the
cleanup dropped (`history --tab`, `history --stats`, and the text forms of `history`
and `serve status`). The CLI stays usable at a terminal, and agents get a complete,
uniform machine contract on the same binary.
**Sequencing (owner, 2026-09-29):** #505 owns `cmd/relevo` output from here. The
cleanup's phase-3 finish for `cmd/relevo` runs after this work, on the new code; this
work adds no lint, comment or file-size exclusions.
**Depends on:** nothing open. If the cleanup's `Runtime` dissolve is still in flight,
land it before the wave that edits `cmd/relevo/wire.go`.
**Keep:** the cockpit is still the rich human surface; `relevo ui` is unchanged.

## 1. Problem

The CLI grew as a human tool that agents also use. Human output stays (owner,
2026-09-29), but the machine half is uneven:

- `--json` exists on some verbs only, and its shape varies; `history` and
  `serve status` always print JSON and declare an ignored `--json`.
- Errors are prose on stderr. A stable code and the next command to run exist only
  as separate sentences, if at all.
- The verb surface is a hand-written `usage` string; tests extract verbs with a
  regex over `main.go`. There is no machine-readable description for an agent to
  fetch.
- The MCP server exposes three tools; the Claude plugin has two read commands; the
  guide carries the flags and workflow in prose.
- Nothing measures whether any of this costs the planner fewer tokens or steps.

## 2. The contract

1. **stdout is the answer; stderr is diagnostics.** Human notices, headers and
   progress lines never go to stdout in `--json` mode.
2. **Every verb accepts `--json`, with one meaning:** one JSON document on stdout
   (or NDJSON for streams). Where a verb is JSON-only already, `--json` is accepted
   and has no effect. Where JSON is the document, the human default stays what it
   is today.
3. **Human renderings stay, and are derived from the document** where practical.
   One source builds both, so they cannot disagree.
4. **Streams are NDJSON under `--json`:** one object per line, `--follow` keeps
   streaming (`show --log`, transcript events).
5. **Errors are data.** Every failure names a catalog code and, when one exists,
   the next command:
   - human stderr: `relevo: <code>: <message>` then `  next: <command>`;
   - `--json` stderr: `{"error":{"code":"<code>","message":"<message>","next":"<command>"}}`.
   The code is stable; the message is not part of any contract.
6. **Exit codes stay coarse and stable:** 0 ok, 1 failure, 2 usage or refusal.
   `wait` keeps its protocol codes 0/2/3/4/5/6/124 (C8; 6 = WaitNotStarted).
7. **`relevo help --json` describes the surface**, and a contract test keeps the
   description and the dispatcher in step. The human `relevo help` output stays.
8. **Nothing on stdout in `--json` mode except the document.** One round of output,
   no decorations.

## 3. Per-verb decisions

Read verbs. "kept" means the current human rendering stays as the default.

| verb | default stdout | `--json` document |
|---|---|---|
| `status` | table, kept | `view.Report`, unchanged (C1) |
| `status --line` | mastermind + status line, kept | `view.StatusLineDoc`, unchanged (C2) |
| `history` | JSON array (already) | same; `--json` is a no-op (C4) |
| `show` | section text, kept | `ShowResult`, unchanged (C3); `--log` is NDJSON (C3) |
| `wait` | outcome line + pending payload, kept | **new** `WaitDoc`; exit codes unchanged (C8) |
| `doctor` | table + `fix:` lines, kept | **new** `DoctorDoc` |
| `version` | version line, kept | **new** `{"version": "..."}` |
| `help` | usage text, kept | **new** registry document (§5) |
| `mastermind list` | table, kept | existing document (C6) |
| `mastermind guide` | guide text, kept | existing consent document |
| `config` (bare) | actors/pick/candidates tables, kept | **new** `ConfigDoc` |
| `config export` / `get` | JSON (already) | same (C11) |
| `config log` | revision lines, kept | existing document (C6) |
| `config server list` | table, kept | **new** rows |
| `config server key` | `client id` + enroll line, kept | **new** `{id, enroll_line}` |
| `config secret list` | names, one per line, kept | **new** array of names |
| `serve clients` | table, kept | **new** rows |
| `serve status` | JSON (already) | same (C5) — burst on contabo parses it |
| `serve fingerprint` | line, kept | **new** `{fingerprint}` |
| `gate` / `gate --serve` (list) | table, kept | **new** rows |

Write verbs. The human line stays; `--json` returns the verb's result document.
Fields are pinned per verb with a golden in the wave that converts it; the document
carries at least what a planner needs to proceed without parsing prose.

| verb | result document (new) |
|---|---|
| `bind` | `{name, dir, actor, candidate, tier, state, resumed}` |
| `unbind` | `{name, released, archived}` |
| `send` | `{name, round, candidate, tier, deferred}`; `--dry-run` its own document |
| `stop` | `{name, round, killed}` |
| `done` | `{name, released, worktree, branch}` |
| `gate` (set/clear) | `{subject, until, candidates, mode}` |
| `config set/unset/import/init/agents`, `config server add/rm`, `config secret set/rm` | small outcome documents |
| `mastermind init/enable/disable/reset/rename/forget` | small outcome documents |
| `serve init/enroll/revoke/gc/unbind` | small outcome documents |
| `update` | outcome document; `--check` keeps its block |

Exempt: `daemon`, `serve` (run), `mcp` (processes), `config edit` (interactive), and
the `mastermind * --hook` verbs (their stdout is already Claude's hook contract).

## 4. Error catalog

- Shape fixed in §2.5. The code is a stable lowercase snake string; codes are never
  reused for a different meaning.
- One table in code is the catalog; a test fails when an error path emits a code the
  catalog does not list, and when a catalog entry carries no `next` hint where one
  exists.
- Initial catalog (extended only by adding entries): `usage`, `refused`,
  `unmigrated`, `binding_not_found`, `round_not_found`, `artifact_not_found`,
  `conflict`, `config_invalid`, `policy_refused`, `tier_cap`, `no_daemon`,
  `remote_unreachable`, `remote_auth`, `gate_active`, `not_available`, `internal`.
- Exit codes: `usage` and `refused` are 2; every other failure is 1 unless §2.6
  pins otherwise.
- A code is chosen by the class of the error, never by its prose. `internal` means
  relevo's own failure; a caller's own argument is `usage`, and the state of a
  thing that makes the call impossible right now is `refused`. Three families
  follow that rule at the point the error is created, and each maps once:
  - a name, feature label or ticket the rule refuses (`store.ErrInvalidName`,
    `store.ErrInvalidFeature`, `store.ErrInvalidTicket`) and a caller's own
    input in bind/add (`relevo.ErrBadInput`) → `usage`, `next: relevo help`;
  - a send refused because a round's process, output or scope is still live
    (`ErrBuilderBusy`, `ErrReportPending`, `ErrScopeActive`, all of which also
    carry `ErrRefused`) → `refused`, `next` naming `relevo done`, `relevo wait`
    or `relevo stop` as the way out;
  - a `--round` past the binding's counter (`relevo.ErrRoundNotFound`) →
    `round_not_found`, `next: relevo history`.
  Classifying happens with `errors.Is` against a typed sentinel, so no site in
  `cmd/relevo` matches on an error's text.

### Gate expiry from a reason

A gate recorded by hand (`relevo gate <token> --reason …` with no `--for`) and one
recorded by a server (`POST /v1/unavailable`) expire when the reason names its own
reset, using the same limit-text parse the decision point applies to builder output:
a reason reading `RESOURCE_EXHAUSTED 429: … Resets in 51m30s` is gated until that
reset and is pruned by the ledger's ordinary expiry. A reason naming no reset this
parser trusts keeps the until-cleared default, because nothing in it states when the
limit lifts and relevo does not invent one.

## 5. Discovery: `relevo help --json`

One registry table in `cmd/relevo` is the source; it is filled by hand once and then
guarded by a test that walks the dispatcher both ways: every dispatched verb has an
entry, every entry is dispatched, and a verb's flag set contains the flags the
registry lists.

```json
{"tool":"relevo","version":1,"build":"v0.14.0",
 "verbs":[
   {"name":"send","summary":"stage a plan as a round and start the runner",
    "args":"--name <n> --file <plan>","flags":["--tier","--verify","--regate","--dry-run","--json"],
    "output":"json:SendResult","exit":[0,1,2],
    "errors":["binding_not_found","conflict","tier_cap","policy_refused"]}
 ],
 "errors":{
   "binding_not_found":{"exit":1,"next":"relevo status --all"}
 }}
```

`relevo help --json <verb>` prints one entry. The mastermind guide points at the
registry for flags; it does not restate them.

## 6. Agent integration

- **MCP** (`internal/mcp`): add tools `show` (name, round, section) and `gate`
  (token, for, reason, clear). `wait` stays the command appended to a `send` result,
  because MCP hosts handle long blocking calls badly. Instructions stay the single
  workflow source and gain the registry pointer. Goldens updated in the same round.
- **Claude plugin** (`claude-plugin/`): one skill for the planner loop — send, wait,
  read the report, run the check, gate on a limit, done — with the rules that matter
  (do not trust the report; compare the diff to the plan). Existing commands stay;
  their command lines keep passing the C12 check.
- **OpenCode plugin** (`internal/harness/opencodeplugin/`): `tui.tsx` call sites are
  updated only where a shape or flag changes; the `status --line --json`,
  `show --json`, `history --json` reads are already the contract.
- **Guide and definitions**: the guide gains the `--json` rule, the error
  code/`next` shape, and `help --json`. The shipped definitions stay free of CLI
  prose; the C12 command-line scan still covers them.

## 7. Measurement

The canonical planner loop, on a scratch repo with a cheap builder and a small
plan: `bind -> send -> wait -> show --report -> show --diff --stat -> done`.

- Record per step and in total: number of CLI invocations and stdout bytes; plus
  the token count of the driving session when its transcript exposes usage.
- Capture the baseline **before** the first output change (wave W0) and repeat with
  the same script after W4. Paste both into #505 as the before/after record.
- The script is a dev tool, not CI: it starts a real round, so it is never wired
  into `make check`.

## 8. Waves

Each wave becomes one lite-planner round (plan artifact), then a builder round; the
plan names the exact files, and no wave changes a golden without the owner's
approval recorded in its plan.

- **W0 — baseline.** Capture the §7 numbers as they are today. No code.
- **W1 — the frame.** Error catalog, the human/`--json` error helper, the registry,
  `help --json`, and the contract test that keeps the registry and the dispatcher in
  step. No verb output changes yet.
- **W2 — read verbs.** The hot loop first (`status`, `show`, `wait`), then
  `history`, `doctor`, `config`, `mastermind`, `serve`, `gate`, `version`. Human
  renderings derived from the documents where practical.
- **W3 — write verbs.** Result documents and error codes on the write paths; exit
  code audit across every verb.
- **W4 — integration.** MCP `show`/`gate`, instructions, the plugin skill, the
  guide, `tui.tsx` call sites.
- **W5 — after.** Repeat the §7 script, record on #505, and close any output docs
  that drifted.

## 9. Contract impact (C1-C12)

Unchanged, and provably so by their goldens: C1, C2, C3, C4, C5, C9, C10, C11;
C6 `config log` and C8 `wait` exit codes; C8's report payload text. Deliberately
changed: C7 (two new MCP tools, instructions), C12 (a plugin skill joins the
commands), and additive `--json` documents where none existed. Any change beyond
this list is a separate issue.

## 10. Out of scope

- The cockpit, the web GUI, and every human rendering's design.
- The cleanup's phase-3 finish for `cmd/relevo` (runs after this work).
- The wire protocol, the database schema, and the config document format.
- New verbs or behaviour changes; anything not in §3 is a follow-up issue.
- `relevo ask` (removed; do not resurrect it here).

## 11. Acceptance

- Every dispatched verb appears in `help --json`; the both-ways contract test fails
  when the registry and the dispatcher drift.
- Every read verb's `--json` is one document (or NDJSON stream) with a golden, and
  every human default in §3 still prints what it prints today.
- A forced failure on a converted verb prints the code and the next command, in both
  the human and `--json` shapes; an unlisted code fails the catalog test.
- `wait --json` exists and its exit codes are unchanged; `serve status --json` is
  byte-identical for burst; `status --line` is byte-identical.
- MCP exposes five tools, with goldens; the plugin skill passes the C12 scan.
- #505 carries the before/after numbers from §7.
