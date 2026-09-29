# Plan: bug sweep, batch 5 — #657, #659, #660

One builder round, one worktree, no push. Three commits, one per issue, in the seed's order; the plan itself is committed verbatim as `docs/plans/2026-09-29-bug-sweep-tail.md` **inside the first (`#657`) commit** (repo convention: plan docs ride with their implementing commit, e.g. `71731989`). Commit bodies carry `Fixes #657`, `Fixes #659`, `Fixes #660`. Proposed subjects: `fix(limit): gate only on harness-authored limit lines`, `fix(sanitize): strip terminal control bytes before storing and printing`, `fix(db): create the database and its backups owner-only`.

Reading of a seed point that the code contradicts: the seed asks the `#660` tests to assert "no world-readable file after a fresh open and after `BackupTo`". Post-hoc `stat` cannot fail when the fix is reverted, because `chmodPrivate`/`chmodIfExists` already run after the fact (`internal/db/db.go:110-117`, `:196`). The window is only observable at creation time, so the plan pins the creation mode through a test seam (below) **and** keeps the post-hoc mode assertions. This is the only place the plan deviates from the seed's letter; everything else follows the decisions.

## Behaviour and cases

### #657 — limit detection reads only harness-authored channels

A decision point may gate a provider only when a limit pattern matches a line the **harness itself wrote**, and the reset time is parsed from that same line (already `MatchLimit`'s rule, `internal/availability/limit.go:79`). Per kind, one raw stream line is limit evidence only when:

| kind | channels kept | dropped |
|---|---|---|
| claude | `type=="error"` → its `message`/`error.message`; `type=="result"` with `is_error==true` → its `result` text, else its `subtype` | assistant text blocks, `tool_result` content (including `is_error` results), a successful result's `result` text, `rate_limit_event`, `system` |
| agy | `event=="result"` with `status` set and ≠ `SUCCESS` → `result.error` (string or `{message}`) | `result.response`, `step_update` outputs and errors, `init` |
| opencode | `type=="error"` → `error.message` | `text`, `reasoning`, `tool_use` (calls and their results) |
| codex | `type=="error"` → `message`; `type=="turn.failed"` → `error.message` | `item.completed` of every item type including `error` (the model-visible warning), `thread.*`, `turn.*` |
| any | a non-JSON line that is not a supervisor trailer (`stderr`) | blank lines, `relevo-exit:`/`relevo-rusage:` trailers |

Consequences, stated as cases: a limit-shaped sentence in model text, in a tool result, in a successful result's text, or in a thinking block does not gate; a limit-shaped structured error line does; a reset phrase on a different line than the matched structured line leaves `Parsed=false` and gates only until the policy default. A round whose `LogPath` is a legacy `NNN-builder.log` keeps today's rendered-tail scan: such a round writes stderr to the log, so the stream carries no limit text, and the residual risk is recorded in the report. `rate_limit_event` is deliberately not read (the renderer drops it today, no fixture shape exists, and no shipped pattern could match a field of it) — reported as a deliberate leave.

Codex patterns are narrowed: `(?i)quota` becomes `(?i)quota (exceeded|reached|exhausted)` (`internal/harness/harness.go:199`). `usage limit`, `rate limit`, `"status": 429` and `too many requests` stay: two-word phrases, and only structured lines reach them now.

### #659 — one shared sanitiser

New leaf package `internal/sanitize`, `func Text(string) string`, semantics fixed by the TUI's existing tests (`internal/ui/detail_test.go:372-397`): `\n` kept; `\r` dropped (CRLF → LF); `\t` → four spaces; every other C0, `0x7f`, C1 `0x80–0x9f` and invalid UTF-8 (`utf8.RuneError`) → `U+FFFD`; everything else unchanged. No internal imports, so no cycle; every listed caller may import it.

Applied **at the source**: `transcript.Render` and `RenderRecord` sanitize every line they return (so stored logs, rendered stream tails and `show --transcript` are safe); `reporttail.ParseWithReason` sanitizes `HaltedAt` at `reporttail.go:74`; `availability.capLine` sanitizes the matched line before the 200-rune cap (`limit.go:99`), so gate/switch notes are stored clean; `availability.Unavailable` sanitizes `reason` before storing (`gates.go:155`); `availability.gatedNote` sanitizes the note it splices into its advisory line (`gates.go:440`). `POST /v1/unavailable` rejects a `reason` longer than 512 bytes with 400 and stores nothing (`internal/serve/bindings.go:539-549`).

Applied **at the print sites** for data that predates the fix: `relevo.LogLine` sanitizes the whole composed line (`internal/relevo/logline.go:20`), `serve.RenderGates` sanitizes each note (`internal/serve/admin.go:426`), `printShow` sanitizes `res.Text` before printing it (`cmd/relevo/show.go:307-313`), and the cockpit log view sanitizes every `Detail` in one pass at the end of `buildLogEntries` (`internal/ui/view_log.go:449-453`). `internal/ui`'s `sanitizeText` becomes a one-line delegate so its callers and tests are untouched. `printShow`'s `--artifact` path (`show.go:275-283`) stays raw byte-for-byte: it is a file-copy contract, not a display path — reported.

### #660 — owner-only database and state root

`internal/db`: before `sql.Open`, when the database path is absent, create it `O_CREATE|O_EXCL|O_RDWR, 0o600`; `EEXIST` from a concurrent first open is not an error. SQLite then derives `-wal`/`-shm` from the file mode, and the existing post-hoc chmods stay for pre-existing files. `BackupTo` pre-creates the target the same way but treats `EEXIST` as the existing refusal it already documents, then runs `VACUUM INTO` and the chmod: the copy never exists world-readable, and a concurrent appearance of the target is refused instead of overwritten. The state root is created `0o700`: a new `stateRootMode` in `internal/store` for the three root `MkdirAll`s, and `serve.EnsureStateRoot` (and `serve.New`'s `tmp` dir) at `0o700`. Existing roots keep their mode — mkdir never chmods — and that is reported, not silently tightened.

Verified in this tree with `modernc.org/sqlite v1.59.0` (throwaway program, deleted): a pre-created `0600` database yields `-wal`/`-shm` at `0600`; a pre-created empty `0600` backup target makes `VACUUM INTO` succeed, keeps `0600`, and the backup reads back with its rows.

## Seams (verified against this tree; all line numbers read from it)

| # | file:line | what |
|---|---|---|
| 657 | `internal/transcript/limit.go` (new) | `LimitLines(kind, line) []string`, the channel table above |
| 657 | `internal/transcript/claude.go:35-52`, `agy.go:26-50`, `opencode.go:26-27`, `codex.go:14-17` | per-kind shapes the filter reads |
| 657 | `internal/relevo/transcript.go:30-52`, `:88-103`, `:107-147` | parameterize `renderStreamFrom`/`diskStreamTail` with a line renderer; add a filtered tail beside `streamTail` |
| 657 | `internal/relevo/transcript.go:154-176` | `builderTail`/`currentBuilderTail` keep their signatures; new scan-text sibling honours `StreamStart` and the legacy log |
| 657 | `internal/relevo/switch.go:191-197` | `limitText` becomes the scan-text builder |
| 657 | `internal/relevo/headless.go:764`, `:962`; `reconcile.go:243-244` | the three decision points |
| 657 | `internal/harness/harness.go:196-202` | codex pattern list |
| 657 | `internal/relevo/limit_test.go`, `scan_scope_test.go:31-120`, `headless_test.go:4130-4145` | existing pins that must stay green |
| 659 | `internal/sanitize/text.go` (new), `text_test.go` (new) | the shared `Text` |
| 659 | `internal/ui/detail.go:103-124` | `sanitizeText` becomes `return sanitize.Text(s)` |
| 659 | `internal/transcript/transcript.go:23-46`, `record.go:14-27` | sanitize every returned line |
| 659 | `internal/reporttail/reporttail.go:74` | `HaltedAt` |
| 659 | `internal/availability/limit.go:99-105`; `gates.go:142-163`, `:433-452` | `capLine`, `Unavailable`, `gatedNote` |
| 659 | `internal/relevo/logline.go:20-57` | `LogLine` |
| 659 | `internal/serve/admin.go:415-429`; `bindings.go:511-555` | `RenderGates`; the 512-byte bound |
| 659 | `internal/ui/view_log.go:172`, `:449-453` | `buildLogEntries`' final `Detail` |
| 659 | `cmd/relevo/show.go:307-313` | `printShow` `res.Text` (`--artifact` at `:275-283` stays raw) |
| 659 | `internal/transcript/transcript_test.go:142`, `:231`; `testdata/agy.log:5` | the three tab expectations that move to four spaces |
| 660 | `internal/db/db.go:80-117` (open), `:186-200` (BackupTo), `:299-317` (chmods) | pre-creation; `open` must stay ≤70 counted lines for funlen, so the creation goes in a helper |
| 660 | `internal/store/store.go:29`, `:160`; `db.go:24`; `daemonlock.go:61` | `stateRootMode` and the three root creations |
| 660 | `internal/serve/serve.go:85-87`, `:99-100` | `EnsureStateRoot` and `tmp` |
| 660 | `internal/db/db_test.go:16-40`, `:440-496`; `internal/store` tests; `internal/serve/root_test.go:13-64` | where the new pins land |

## Steps

1. Copy this plan verbatim to `docs/plans/2026-09-29-bug-sweep-tail.md` (no edits); done when the file exists and `git diff --stat` shows no other path yet.
2. #657: add `internal/transcript/limit.go` + `internal/transcript/limit_test.go`; done when `go test ./internal/transcript/ -run TestLimitLines -count=1` passes.
3. #657: parameterize the tail reader and add the filtered scan text; switch `limitText` and the two `headless.go` call sites to it; done when `go test ./internal/relevo/ -run 'TestLimit|TestStderrLimit|TestScanScope' -count=1` passes and `go test ./internal/transcript/ -count=1` is green.
4. #657: add `internal/harness/harness_test.go`'s prose-ignore pin and narrow the codex quota pattern; done when `go test ./internal/harness/ ./internal/transcript/ -count=1` passes.
5. #657: run the mutations (list below), revert each, confirm `git diff --stat` matches the intended scope, then commit #1 (plan doc included) with `Fixes #657`; done when `git show --stat HEAD` lists exactly the plan, transcript and relevo files.
6. #659: add `internal/sanitize/text.go` + `text_test.go`; make `ui.sanitizeText` delegate; done when `go test ./internal/sanitize/ ./internal/ui/ -run 'TestText|TestSanitizeText|TestBodyOfControlBytes' -count=1` passes with the TUI tests unedited.
7. #659: sanitize the source sites (`Render`, `RenderRecord`, `reporttail`, `capLine`, `Unavailable`, `gatedNote`); done when `go test ./internal/transcript/ ./internal/reporttail/ ./internal/availability/ -count=1` passes.
8. #659: regenerate the fixture (`go test ./internal/transcript/ -run TestFixtures -update`) and hand-edit the two tab expectations; done when `go test ./internal/transcript/ -count=1` passes and `git diff` of `testdata/agy.log` shows only the tab line changed.
9. #659: sanitize the print sites and add the server bound; done when `go test ./internal/relevo/ -run TestLogLine ./internal/serve/ -run 'TestRenderGates|TestUnavailable|TestAdminGates' ./internal/ui/ -run 'TestLog|TestSanitizeText' ./cmd/relevo/ -run TestShow -count=1` passes (cmd/relevo tests stay on local fixtures, no harness, no network).
10. #659: run the mutations, revert each; produce `.coverage.txt` with `go test -cover ./...`, run `sh scripts/check-coverage.sh --write`, verify the baseline diff adds `internal/sanitize` and moves only `internal/ui` — any other lower line halts the round; commit #2 with `Fixes #659`.
11. #660: add the private-create helper and the seam, wire `open` and `BackupTo`, add `internal/db` tests; done when `go test ./internal/db/ -run 'TestOpen|TestBackupTo' -count=1` passes and the existing backup test is untouched.
12. #660: add `stateRootMode` to `internal/store` and use it at the three root sites; set `EnsureStateRoot`/`tmp` to `0o700`; add the store and serve root-mode tests; done when `go test ./internal/store/ ./internal/serve/ -count=1` passes.
13. #660: run the mutations, revert each; commit #3 with `Fixes #660`.
14. Full check once: `make check` (gofmt, vet, lint, comments, filesize, tidy, scripts, `go test -race -cover`, coverage guard); done when it exits 0 and the report records the coverage numbers for `internal/sanitize` (new) and `internal/ui`.

## Mutation checks (each mutation reverted before the next; the named test must fail)

- #657 — make `limitText` call `currentBuilderTail` again: `TestLimitScanReadsOnlyHarnessChannels` (`internal/relevo`) fails, because the model-text/tool-result stream now gates.
- #657 — let `LimitLines` pass assistant text for any kind: `TestLimitLinesByKind` (`internal/transcript`) fails.
- #657 — restore `(?i)quota`: `TestCodexLimitPatternsIgnoreProse` (`internal/harness`) fails.
- #659 — drop the wrap in `Render` / `RenderRecord`: `TestRenderSanitizesControlBytes` / `TestRenderRecordSanitizesControlBytes` fail.
- #659 — drop the sanitize in `reporttail`: `TestParseWithReasonSanitizesHaltedAt` fails.
- #659 — drop it in `capLine`: `TestMatchLimitSanitizesMatchedLine` fails.
- #659 — drop it in `Unavailable`: `TestUnavailableSanitizesReason` fails.
- #659 — drop it in `LogLine`: `TestLogLineStripsControlBytes` fails.
- #659 — drop it in `RenderGates`: `TestRenderGatesSanitizesNote` fails.
- #659 — drop the `buildLogEntries` pass: `TestLogViewDetailStripsControlBytes` (`internal/ui`) fails.
- #659 — drop it in `printShow`: `TestPrintShowSanitizesControlBytes` (`cmd/relevo`) fails.
- #659 — remove the 512-byte bound: `TestUnavailableRejectsAnOverlongReason` (`internal/serve`) fails.
- #660 — delete the create call in `open`: `TestOpenCreatesPrivateFiles` fails (the seam is never invoked).
- #660 — delete it in `BackupTo`: `TestBackupToCreatesPrivateTarget` fails.
- #660 — `stateRootMode` back to `0o755`: `TestStateRootIsOwnerOnly` (`internal/store`) and the root arm of `TestEnsureStateRoot` (`internal/serve`) fail.
- #660 — helper mode back to `0o644`: the recorded-mode assertion in both db tests fails.

The #660 db tests use a package-level `createFile` seam (same style as `busyTimeoutMS`/`freshTemplate`) that records `os.Stat(path).Mode()` immediately after creation and then delegates; that is the only way to see the mode before `chmodPrivate` runs. They also assert the final modes of the file and its siblings are `0600` after `Open`, and of the target after `BackupTo`, and they must not call `t.Parallel()`. The store/serve root tests compare against a same-umask control directory and `t.Skip` when the umask makes `0755` and `0700` indistinguishable, so they never pass vacuously.

## Deletions (closed list — everything else stays)

1. The loop body of `ui.sanitizeText` (`internal/ui/detail.go:107-123`) — replaced by a delegate to `sanitize.Text`.
2. The codex pattern string `(?i)quota` (`internal/harness/harness.go:199`) — replaced by the narrowed pattern.
3. The two `currentBuilderTail(rt, b, availability.LimitScanLines)` arguments in `internal/relevo/headless.go:764,962` — replaced by `limitText(ctx, rt, b)`.
4. Nothing else is deleted: no file, package, function, test, golden or lint exclusion is removed, and `currentBuilderTail`, `builderTail`, `streamTail`, `logTail`, `MatchLimit`, `matchDenial`, the raw `--artifact` print and the existing fixtures all survive. `internal/transcript/testdata/agy.log` is rewritten in place (one line changes), and `docs/plans/2026-09-29-bug-sweep-tail.md` plus `internal/transcript/limit.go`, `internal/sanitize/text.go` are added.

## Report must include

- The three commit SHAs and messages, each body naming its issue; "not pushed"; `git diff --stat` against this plan's declared scope and any path outside it.
- Every focused command run before `make check`, with its result, and the single `make check` result.
- Each mutation with the test that failed and confirmation the mutation was reverted (`git diff` clean at that path).
- The coverage statement: whether `--write` changed the baseline, `internal/sanitize`'s and `internal/ui`'s numbers, and that no other package's line dropped; and that no lint or filesize exclusion was added.
- The fixture regeneration line and the two hand-edited transcript expectations.
- Deliberate leaves and deviations: `rate_limit_event` unread; no explicit non-zero-exit condition (a structured harness error is required instead); legacy-log rounds keep the rendered-tail scan; `--artifact` and `FinalText` output stay raw; TUI stats/candidates gate reasons, doctor rows and `relevo status`'s gate text rely on source sanitization and were not re-sanitized; `db.Open`'s window is pinned by the creation-time seam, not a post-hoc stat; existing roots and pre-existing world-readable files are not chmod'd.
- Residual risks: non-JSON stream lines (stderr) and legacy logs remain trusted for limit text; `VACUUM INTO` failures leave a pre-created empty target that a retry refuses.
