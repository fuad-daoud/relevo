# Plan: every round process is marked a runner, and a runner is never asked to be, or made, a MasterMind

Fixes the halt in #640: an opencode (or Claude) session relevo spawned for a round is asked the repository consent question at its first turn, and on a `yes` repository is registered as a MasterMind.

## Behaviour

**A. The marker (round spawns).** Every process relevo starts for a round runs with exactly one `RELEVO_RUNNER=<binding name>` in its environment, and *without* `RELEVO_MASTERMIND` / `RELEVO_PLANNER`, even when the daemon's own environment carries them. "Every round process" means: a writer builder, a reader round (a planner actor *is* a reader round — `store.ShapeReader`), the fresh spawn, the resume after a daemon restart, a mid-round switch, a repair round, the admit of a queued round, local daemon and `relevo serve` alike. A stale `RELEVO_RUNNER` inherited from the daemon is replaced, so exactly one entry reaches the child.

**B. The silence.** In a process whose `RELEVO_RUNNER` is set and non-empty:
- `relevo mastermind guide` prints nothing (text form); `--json` prints `{"state":"disabled","text":""}` and stops before it opens the database: no record, no consent answer, no repo read.
- `relevo mastermind init --hook claude` writes `{}` to stdout and nothing else: no record, no session consent, no told baseline, nothing appended to `$CLAUDE_ENV_FILE`. It answers `{}` for **any** payload, including a malformed one — a runner never gets the failure note injected as context.
- `relevo mastermind notice --hook claude` writes `{}` and writes no told baseline.

The JSON state stays `disabled` (not a new `runner` state): the plugin is silent on it already, `docs/specs/2026-09-27-mastermind-consent-design.md` §6.2 documents exactly `enabled|ask|disabled`, and it is byte-identical to the existing non-repo answer. No plugin change, no shipped-copy change.

**C. The deny.** `RELEVO_MASTERMIND`, `RELEVO_PLANNER` **and** `RELEVO_RUNNER` join the deny list the spawn filters with, so no spawned child inherits a MasterMind identity — the round's own marker is appended *after* the filter and therefore survives, while a parent's stale one cannot shadow it (Go's `os.Getenv` returns the first match, and extras are appended last).

**D. Unchanged.** A session without the marker behaves exactly as today (ask on unset, guide on yes, registration, told baseline). `mastermind enable|disable|reset|list|rename|forget`, the plain (non-hook) `init`, `mcp`, `claude-plugin/hooks/hooks.json`, `server.ts`, `tui.tsx` and `internal/harness/files.go` are untouched.

**E. Cases the tests pin.** Marker present/absent (`nil` env, unset, empty value → not a runner; set → runner). Writer, writer-with-author and reader bindings all get the marker; the spawn's `spec.Env` carries it. A child of `proc.spawnEnv` with a parent that sets all three variables sees `RELEVO_MASTERMIND`/`RELEVO_PLANNER` absent and `RELEVO_RUNNER` equal to the new value, exactly once. The three commands are silent in an **unset** repo *and* in a **yes** repo, and create no record, no session consent and no told baseline in either. A human session's existing tests pass unmodified.

## Seams

| Seam | Where |
| --- | --- |
| Predicate + wire name | `internal/mastermind/ident.go`, after `Detect` (line 63): `RunnerEnv = "RELEVO_RUNNER"`, `IsRunner(env func(string) string) bool` (nil env → false; non-empty value → true) |
| Marker writer | `internal/relevo/headless.go`: new pure `roundEnv(b store.Binding) []string` next to `builderEnv` (lines 152–170) — `builderEnv(b)` plus the marker; used at `startProcess`'s `spec.Env` (lines 299–304). `builderEnv` itself is not changed (its `TestBuilderEnv` contract stays) |
| Deny list | `internal/proc/env.go:10–14` (`DeniedEnv`), consumed by `spawnEnv` at `internal/proc/proc.go:293–303` |
| Guide | `cmd/relevo/mastermind_consent.go:384–397`: early return after `parseFlags`, before `newRuntime()` (line 394) |
| init hook / notice hook | `cmd/relevo/mastermind_hook.go:68–72` and `:179–193`: check right after the stdin read, *before* the parse-error branch |
| Serve path (confirmation) | `internal/serve/admit.go:109` → `relevo.Admit` (`internal/relevo/queue.go:23`, `startRound` at :68) → `startProcess`; the server's runtime is `proc.New()` (`cmd/relevo/serve.go:299,488`, `internal/serve/serve.go:227`). One spawn site, no second |
| README | `### Headless builders`, the "What this means in practice" bullet list (README.md ~633–685) |

**Seed-vs-code note (decision 5).** README ~2088–2096 (`### Hook execution & environment`) lists the variables injected into *lifecycle hook scripts* (`RELEVO_EVENT`, `RELEVO_BINDING`, …); `RELEVO_RUNNER` is not injected there, so the conditional in decision 5 is not met and the variable is documented in Headless builders instead.

**Second spawn sites, reported not changed.** A verify consult (`internal/consult/verify.go:288`) and an availability latency probe (`cmd/relevo/probe_exec.go:72`) are harness sessions relevo spawns too; they are not round processes (a probe has no binding name to carry), so they stay unmarked. Both still shed `RELEVO_MASTERMIND` through the deny list, but their own harness hooks can still ask them (unset repo) or register them (`yes` repo). Follow-up decision, not this round.

## Steps

1. `internal/mastermind/ident.go`: add `RunnerEnv` and `IsRunner` — new `internal/mastermind/ident_test.go::TestIsRunner` (nil/unset/empty/set) pins it; `go test ./internal/mastermind/ -run TestIsRunner -count=1` passes.
2. `internal/proc/env.go`: add the three names to `DeniedEnv` and rewrite its comment to say why relevo's own identity is denied (not just secrets, and not because the harness needs it); new `internal/proc/proc_test.go::TestStartStripsMasterMindIdentityEnv` starts `sh -c 'env'` with the parent setting all three and `spec.Env` setting the round's marker — the child sees the marker once and neither identity variable; `go test ./internal/proc/ -run 'MasterMind|Denied' -count=1` passes.
3. `internal/relevo/headless.go`: add `roundEnv` and use it for `spec.Env`; `TestRoundEnvMarksTheRunner` (no author, author, reader shape) and `TestStartProcessMarksTheRunner` (fake runner's recorded spec, literal `RELEVO_RUNNER=<name>`) in `internal/relevo/headless_test.go` pass; `go test ./internal/relevo/ -run 'RoundEnv|StartProcessMarks' -count=1`.
4. `internal/serve/admit_test.go:72–84`: the no-author case now expects exactly `[]string{"RELEVO_RUNNER=api"}` and the author case also expects the marker — the served-round proof; `go test ./internal/serve/ -run TestRoundSpawnCarriesAuthorEnv -count=1` passes.
5. `cmd/relevo/mastermind_consent.go` and `cmd/relevo/mastermind_hook.go`: the three early returns; `TestMasterMindGuideStaysSilentForARunner`, `TestMasterMindInitHookStaysSilentForARunner`, `TestMasterMindNoticeHookStaysSilentForARunner` in `cmd/relevo/mastermind_test.go` (each: unset repo and yes repo, `t.Setenv("RELEVO_RUNNER", …)`, assert exact stdout, empty records, no told baseline, `$CLAUDE_ENV_FILE` untouched; init also with a malformed payload) pass; `go test ./cmd/relevo/ -run 'StaysSilentForARunner' -count=1`.
6. Mutation checks, each reverted after it fails: drop the marker from `roundEnv` → `TestStartProcessMarksTheRunner` and the no-author serve case fail; drop the guide check → guide test fails; drop init's → init test fails; drop notice's → notice test fails; drop a name from `DeniedEnv` → the proc test fails.
7. README: one bullet under Headless builders naming `RELEVO_RUNNER=<binding name>` and the stripped identity variables, and why a round's session is never asked or registered.
8. `make check` green (gofmt, vet, lint, comment/filesize checks, `go mod tidy`, plugin-version, name, scripts, coverage baseline not lowered, no new exclusions); `make e2e` if the round has time (CI's, not part of `make check`).
9. Copy this plan to `docs/plans/2026-09-28-runner-not-mastermind.md`, `git add` code + tests + README + plan, one commit; do not push. Expected scope: the 9 paths in the Seams table, nothing else.

Style for every edit: comments say why, no `#NNN`/`§` in new code or test names, functions under 70 lines, no file over 600.

## Deleted behaviour

1. The consent ask-note / guide text reaching a round's harness session through `mastermind guide` — a builder or reader session is no longer told anything.
2. A round's session being registered as a MasterMind by the Claude SessionStart hook, and its told baseline / consent answer being written.
3. A spawned child's inheritance of `RELEVO_MASTERMIND` and `RELEVO_PLANNER` from the daemon environment, and of a stale `RELEVO_RUNNER`.
4. Nothing else: no verb, flag, file, test, golden or plugin copy is removed, weakened or re-ordered.

## Deliberately not done

- `internal/consult/verify.go:288` (verify consult) and `cmd/relevo/probe_exec.go:72` (latency probe) left unmarked, per decision 1's "builder and reader rounds"; reported above.
- Stale MasterMind records a runner session may already have created on a server are not cleaned (`relevo mastermind prune` handles dead ones); no spec edit (`docs/specs/2026-09-27-mastermind-consent-design.md` §6.1's "every SessionStart" now has a runner exemption that lives in code) and no migration.
- A runner's own `relevo bind/send/wait/done` now resolves no MasterMind (the stripped identity): intended by decision 2, listed here because it is a behaviour change beyond the three hooks.

## The report must include

The marker's name/value and the one spawn site it is set at; the deny list chosen (`DeniedEnv`) and why not a runner-specific one; the serve-path confirmation with `internal/serve/admit.go:109` → `startProcess` and the updated admit test as evidence; the chosen `--json` state (`disabled`) and why no plugin change; the test names added/updated, and for each mutation check the named test that failed and that the mutation was reverted; `make check` output (and whether `make e2e` ran); the README placement and the reason it is not in the lifecycle-hook env list; the residual second spawn sites verbatim; commit hash, one commit, not pushed, `git status` clean.
