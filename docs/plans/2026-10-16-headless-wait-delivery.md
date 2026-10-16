# MasterMind amendments (read first; these override the plan below)

**Step 1 is already done -- do not run `claude -p`.** The MasterMind probed
Claude Code 2026-10-16 by dumping the environment a child process inherits:

| variable | interactive | `claude -p` |
|---|---|---|
| `CLAUDE_CODE_ENTRYPOINT` | `cli` | `sdk-cli` |
| `CLAUDE_CODE_SESSION_ATTENDED` | `1` | `0` |

Decision: the Go server treats its process as non-interactive when
`CLAUDE_CODE_ENTRYPOINT` begins with `sdk-` (the Agent SDK entrypoints are
`sdk-*` too, and they are also headless). Unset, `cli` or anything else is
interactive -- today's behaviour. Do not key on SESSION_ATTENDED. Put the rule in
one small pure function taking a getenv `func(string) string`, and test it in a
table (`cli`, `sdk-cli`, `sdk-ts`, unset).

Keep the mod gate (step 2) as planned: skip spawning `relevo push` only when
`e.isInteractive === false`. If the real engine's typings (look for the mod
engine's .d.ts or types the plugin imports) do not declare `isInteractive` on
`session.start`, say so in the report but still keep the `=== false` guard.

CI has no `claude` binary: if `claude plugin test claude-plugin` cannot run in
your environment, say so in the report rather than skipping silently; `make
check` must pass. Commit as new commits on your branch; the plan file goes to
docs/plans/2026-10-16-headless-wait-delivery.md in the same commit series.

---

# Plan: `wait` delivers inline in headless `claude -p` (issue #1189)

## Behaviour and the cases

- In a non-interactive (`claude -p`) session, `wait` returns the report
  payload inline, exactly as `internal/mcp/instructions.go:52-53` already
  promises. Interactive sessions keep today's behaviour: while a live push
  claim covers the target, `wait` claims and confirms nothing and appends
  "delivered by the mod" (`internal/mcp/wait.go:309-352`).
- The claim is never taken in a non-interactive session: the mod does not
  spawn `relevo push` there, so no new claim is written.
- A stranded claim is not fatal: if a live claim already covers the
  target (concurrent interactive holder, version skew, unknown engine),
  a `-p` `wait` still delivers inline through the normal pull path
  instead of reporting "delivered by the mod" with no payload.
- Timeout / gone exits are unchanged: they carry no payload and no mod
  note in either mode (`wait.go:347-350`).

## Direction picked, with evidence

Two cooperating changes, one per process, because the signal lives in
two places:

1. Mod gate (primary). The `session.start` event carries `isInteractive`:
   `claude-plugin/hooks/register.test.ts:107` drives
   `$.session.start({ cwd: '/w', surface: null, isInteractive: false })`,
   and `register.ts:462` receives that event as `e`. The mod therefore can
   tell non-interactive itself, and per the seed's preference it simply
   does not spawn `relevo push` there, so no claim is ever written.
   Back-compat: only `e.isInteractive === false` skips; an engine that
   omits the field keeps today's spawn behaviour.
2. Server bypass (stranded claims). The Go MCP server never sees `e`; it
   is spawned by `claude-plugin/scripts/mcp.sh:8` (`exec relevo mcp`)
   inheriting Claude Code's environment, so env is its only `-p` signal.
   `pushClaimLive` (`wait.go:284-307`) therefore treats the claim as
   absent when the server process's own environment marks a
   non-interactive session. Which variable: **unverified**. A repo-wide
   search finds no reference to `CLAUDE_CODE_ENTRYPOINT` (or any `-p`
   marker) anywhere; the seed's example must not be assumed. Step 1
   below probes the real `-p` environment first and halts if nothing
   reliable exists.

## Seams: files, types, functions, line ranges

- `claude-plugin/hooks/register.ts:285-303` `supervise`, `:305-327`
  `start`, `:452-479` `register` / `on('session.start')` — gate the push
  supervisor on the start event's interactivity.
- `claude-plugin/hooks/register.test.ts:20-102` `fakes`, `:107`
  `startSession`, `:115-144` `identity` block — extend with a
  non-interactive start case.
- `internal/mcp/wait.go:238-280` `Wait`, `:284-307` `pushClaimLive` —
  bypass the claim when the server process is non-interactive; helper
  reads env (pure function of `func() string`, tested with `t.Setenv`).
- `internal/mcp/wait_push_test.go:19-71` `liveClaimStore`,
  `TestWaitUnderPushClaimDoesNotClaimOrConfirm` — headless-mode pin test
  lives beside these; helpers `waitVerbsFor`/`waitText`/`saveVerbBinding`
  in `wait_verb_test.go:27-48` and `verbs_test.go:78-83` are reused.
- Read-only refs, no edits: `internal/mcp/instructions.go:52-53`
  (promise already written; golden
  `internal/mcp/testdata/contract/instructions.golden` unchanged),
  `internal/delivery/deliver.go:88-96` (push-route guard stays as is),
  `cmd/relevo/push.go:59-115` (claim writer unchanged),
  `internal/relevo/wait.go:445-453` (pull delivery with route "wait").

## Ordered steps (each: deliverable + how to know it worked)

1. Probe the `-p` environment. Deliverable: a recorded finding of which
   process env (`claude -p` MCP server context) reliably marks a
   non-interactive session, or a halt. How to know: a probe (dump of the
   MCP server's env under `claude -p`, or Claude Code docs) names the
   variable and its `-p` vs interactive values; if no reliable signal
   exists, halt and report instead of guessing.
2. Mod gate in `register.ts`. Deliverable: `session.start` with
   `isInteractive === false` resolves identity but never spawns
   `relevo push` (no supervisor, no claim); any other value behaves as
   today. How to know: `claude plugin test claude-plugin` passes,
   including the new non-interactive-start test and the existing
   resolved-spawns test.
3. Server bypass in `internal/mcp/wait.go`. Deliverable: `pushClaimLive`
   returns false in a non-interactive server process, so `Wait` takes
   `waitOnTarget` and delivers inline; interactive behaviour byte-identical
   (existing tests set no such env and must pass untouched). How to know:
   `go test ./internal/mcp/ -run 'Wait' -count=1` passes, including the
   new headless-claim test.
4. Pin tests plus mutations. Deliverable: (a) Go test — live claim plus
   headless env yields the closed outcome line WITH the payload, no
   "delivered by the mod", entry confirmed with route "wait"; (b) TS test
   — `session.start` with `isInteractive: false` yields zero spawns after
   settle plus 10 s advance, while `true`/absent still spawns. How to
   know: each test fails when its logic is inverted (unset the env /
   force the old branch / drop the `=== false` check) and passes
   restored.
5. Full verification. Deliverable: green gate. How to know:
   `claude plugin test claude-plugin` (local only; CI has no `claude`
   binary) then `make check` both exit 0. Focused loop while iterating:
   `go test ./internal/mcp/ -count=1` and `claude plugin test
   claude-plugin`.
6. Save this plan verbatim to
   `docs/plans/2026-10-16-headless-wait-delivery.md` and commit plan +
   code as new commits (never amend or rebase a commit on a remote
   binding's branch). How to know: `git status --porcelain` clean and
   `git show --stat HEAD` lists only the declared scope plus the plan.

Style: comments say why, never restate code; no issue numbers in code
or tests; functions stay within 70 lines and Go files within 600
(`wait.go` is at 371, so the helper fits). No
`scripts/agents-shipped.sh --write`: it hashes only
`internal/harness/agents/*`, and this change touches no agent
definition. No coverage-baseline regen expected; if
`scripts/check-coverage.sh` trips, regenerate only via its `--write`
flag and say so, never lower a baseline.

## What is deleted (closed list)

1. Nothing. No behaviour is removed: the mod still spawns `relevo push`
   in interactive sessions, `waitDeliveredByMod` still serves them, and
   no file, command, or tool is deleted or re-routed.

## What the report must include

- `git diff --stat` against the declared scope; every file outside it
  named with why.
- The step-1 finding: the exact env variable and its `-p` vs
  interactive values, or the halt.
- Each new/changed test by name with its focused-command result; the
  `claude plugin test claude-plugin` and `make check` results.
- Both mutation checks: what was broken, the named test that failed,
  revert confirmation.
- Confirmation that no commit was amended/rebased/force-pushed and no
  lint/comment/filesize/coverage exclusion was added.
- Anything said here that the tree proved false (notably whether
  `e.isInteractive` exists on the real engine's `session.start` event
  as the test mock suggests).
