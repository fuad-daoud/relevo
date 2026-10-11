# Plan: reader writes its output file; last message is only the missing-file fallback (slug: reader-output-file)

## Premise holds on current main

A reader round today saves the stream's last assistant text as its output file. `writeReaderSummary` (`internal/relevo/summary.go:48-95`) reads the stream (`readerOutputText` at `summary.go:138-150` via `transcript.LastWithBlock` / `transcript.FinalText` in `internal/transcript/final.go:15-81`) and writes it to `reportPathFor` (`summary.go:21-26`), which is `Store.OutputPath` (`internal/store/paths.go:64-66`) = `ArtifactDir` (`paths.go:39-51`) + `<label>.md`, label from `readerOutputLabel` (`internal/relevo/actoroutput.go:47-54`) via `ActorOutput` (`actoroutput.go:14-28`) and the shipped table (`internal/roles/actors_shipped.go:18-25`). It is called on every close path (`internal/relevo/headless.go:990`, `internal/relevo/reconcile.go:732,769`, `internal/relevo/stop.go:266` via `queueReport`). A runner-written regular file is left alone (`summary.go:62-67`); anything else there is refused. `relevo show --output` reads that same file through `showSections` (`internal/relevo/show.go:347-360`) via `readOutput` (`internal/relevo/artifacts.go:100-119`) and `OutputFile` (`artifacts.go:93-95`).

The prompt and the agents contradict the decided behaviour. `readerPrompt` (`internal/relevo/send.go:75-93`) says "Your final message is your %s: it is saved as %s" with no file write; `chainReaderPrompt` (`send.go:105-117`) the same with marker-before-message. Both render the same `OutputPath` `reportPathFor` records (`readerPromptFor` at `send.go:929-933`, `chainReaderPromptFor` at `send.go:916-920`), so prompt and close cannot drift today. The four architect definitions say the final message is the only output and to write no file (`architect.claude.md:24-25`, `architect.opencode.md:25-26`, `architect.agy.md:34-35`, `architect.codex.toml:17-18`); the other reader definitions say the same for their labels (`reviewer.claude.md:14-15,24-26,36`, `reviewer.agy.md:25-26,35-37`, `researcher.claude.md:13-14`, `security-reviewer.claude.md:13-14`, plus the `.opencode.md` / `.agy.md` / `.codex.toml` twins). The fallback is silent: nothing in the round output or status says the file was missing and the last message was used.

## Behaviour and cases

- Relevo names one output path for every reader round: the existing `OutputPath` (`out/NNN-<actor>/<label>.md`, `internal/store/paths.go:64-66`, `internal/store/out.go:18-20`). The reader prompt and every reader agent definition name that path and tell the reader to write the full artifact there and to keep the final message as the short close (status block last, marker ordering unchanged).
- The close prefers the file: a regular file at the output path with non-empty trimmed content is the saved artifact verbatim; the stream is not parsed and not rewritten for non-chain readers. Chain reviewer/security members keep their current relevo-owned rewrite (`summary.go:66-74`, `readerOutputText` chain branch `summary.go:140-144`); this lane does not change chain semantics, only the prompt wording that names the path.
- Fallback only when the file is missing or empty (absent, zero bytes, or whitespace-only after trim): the close saves the stream's block-carrying message else last assistant text (today's `readerOutputText` rule, unchanged) and records a visible fallback marker in the round's log entry note and in the `show --output` / status answer, so `relevo show <n> --output` output tells the MasterMind the file was missing and the last message was used.
- Kept refusals: symlink, directory, fifo or any non-regular file at the output path is refused and never followed (`summary.go:62-65,75-77`, `writeReaderOutput` `summary.go:184-194` O_EXCL/O_NOFOLLOW, `replaceReaderOutput` `summary.go:203-220`, `readerOutputRoot` `summary.go:101-121`); a refusal closes without an output as today, not as a silent fallback. A runner-written `summary.md` stays an extra artifact (`summary.go:34-35`, `artifacts.go:100-119`).
- The state-dir write must not count as a repo edit: the path lives under `OutDir` (`internal/store/out.go:18-20`), the only directory passed as writable root (`spawn.HeadlessLaunch` in `internal/spawn/launch.go:16-28`, called with `roundTree` at `internal/relevo/scratch.go:97-102` and `OutDir` at `headless.go:288`, `send.go:414`, resumed at `headless.go:476`). The plan verifies the write is allowed on every harness tier (claude `tier.go:66-74`, agy `tier.go:75-85`, opencode read/edit refusal `tier.go:87-96`, codex read refusal + edit `writableRootsArg` `tier.go:97-107` / `harness.go:344-372`) without lifting repo read-only-ness.
- Remote/server readers use the same path: the server closes from `OutputPath` and the client fetches the output at `reportPathFor` (`internal/relevo/remote_artifacts.go:146-174`); the e2e helper already assumes this (`internal/e2e/chain_server_test.go:495-532`). No divergent server path; verify, do not fork.

## Seams

- Prompt: `internal/relevo/send.go:66-117` (`readerPrompt`, `chainReaderPrompt`), `send.go:914-933` (`readerPromptFor`, `chainReaderPromptFor`), `send.go:883-912` (`composePrompt`, `roundPrompt`).
- Close/save + fallback flag: `internal/relevo/summary.go:19-95` (`reportPathFor`, `writeReaderSummary`), `summary.go:138-150` (`readerOutputText`), `summary.go:157-163` (`readerHasReport`); callers `internal/relevo/headless.go:985-1024`, `internal/relevo/reconcile.go:721-773`, `internal/relevo/stop.go:264-284`, `internal/relevo/reader_deliverable.go:13-70` (`readerDeliverablePresent`, `readerCandidateBodies`).
- Readback + visibility: `internal/relevo/show.go:342-360` (`showSections`), `internal/relevo/artifacts.go:90-119` (`OutputFile`, `readOutput`), `cmd/relevo/show.go:133` (`--output` flag text); status/log note where the fallback surfaces (one of `internal/relevo/status.go`, log `Note` via `queueReport` in `reconcile.go:754-`, `internal/store` log entry — builder picks the existing note/status seam that `show --output` and fleet status both read).
- Labels: `internal/relevo/actoroutput.go:14-54`, `internal/roles/actors_shipped.go:18-25`, `internal/roles/outputs_default.go:5-20`, `internal/store/paths.go:39-66`, `internal/store/out.go:14-49`.
- Stream decode (read-only, not changed): `internal/transcript/final.go:15-81`.
- Permissions: `internal/harness/tier.go:59-111`, `internal/harness/harness.go:285-372` (`Launch`, `PrintArgs`, `writableRootsArg`), `internal/spawn/launch.go:16-28`, `internal/relevo/headless.go:280-292`.
- Agents: `internal/harness/agents/architect.{claude.md,opencode.md,agy.md,codex.toml}` + reader agents `reviewer.*`, `researcher.*`, `security-reviewer.*` across the same four kinds (`.claude.md` / `.opencode.md` / `.agy.md` / `.codex.toml`); pin via `scripts/agents-shipped.sh --write` into `internal/harness/agents/shipped.sha256`.
- Investigated, not touched: `internal/ingest/dedupe.go:50-79`, `internal/ui/actions.go:437-449`, `internal/transcript/*`, `internal/workflow/outputs.go` (`Footer`).

## Steps

1. Reword `readerPrompt` + `chainReaderPrompt` to order file-write to the named output path plus the unchanged final-message/marker ordering — deliverable: updated prompt templates; worked when `go test ./internal/relevo -run 'TestReaderPrompt|TestComposePrompt|TestReaderRound' -count=1` pins the path and the write instruction.
2. Add file-first save with empty-aware check + fallback-visible marker on the close path — deliverable: `writeReaderSummary` (or one ≤70-line helper it calls) preferring the regular non-empty file and returning/reporting whether the fallback fired; worked when the focused `internal/relevo` summary/close tests pin file-wins and missing/empty-falls-back with marker.
3. Surface the fallback in round output/status via the existing log-note/status seam — deliverable: one note/status field the `show --output` header and fleet row read; worked when a fallback close asserts the marker in `Show` and status test fixtures.
4. Allow the state-dir output write on every harness without allowing repo edits — deliverable: tier/launch adjustment (allowlist, writable-root, or reader-tier floor) confined to `OutDir`; worked when harness tier/launch tests pin read-tier file-write to `OutDir` still refuses repo writes on claude, agy, opencode, codex.
5. Reword all reader agent definitions to write the full artifact to the given path (repo stays read-only; state-dir path is the exception) — deliverable: updated `architect.*` + `reviewer.*` + `researcher.*` + `security-reviewer.*` across four harnesses + `sh scripts/agents-shipped.sh --write`; worked when `sh scripts/agents-shipped.sh --check` and `go test ./internal/harness -run 'TestAgent|TestShipped|TestArchitect' -count=1` pass.
6. Cover remote/server readers by assertion only — deliverable: test pinning server close + client fetch use the same `OutputPath`/`reportPathFor`; worked when `go test ./internal/relevo -run 'TestRemoteArtifact|TestCatchUp' -count=1` passes with no new server path.
7. Save this plan as `docs/plans/2026-10-11-reader-output-file.md` and commit code + plan as one commit with message naming the issue (`Fixes #N`) as new commits only — deliverable: committed plan doc + green `make check`.

## What is deleted

1. The "write no file / final message is the whole deliverable" instruction in every reader agent definition (replaced by "write the full artifact to the given state-dir path; the repo stays read-only").
2. The silent fallback that saved the stream's last message with no marker (replaced by missing/empty-only fallback with a visible round output/status marker).
3. Nothing else: no prompt path, label, artifact-dir layout, symlink refusals, chain-rewrite rule, or `show --output` section is removed.

## Scope (builder may touch only)

- `internal/relevo/send.go`, `internal/relevo/summary.go`, `internal/relevo/reader_deliverable.go`, `internal/relevo/show.go`, `internal/relevo/artifacts.go`, `internal/relevo/status.go` (or the one log-note/status file the fallback marker lands in — if a second status file is needed say why), `internal/relevo/remote_artifacts.go` (assert-only unless server path diverges, then say why).
- `internal/harness/tier.go`, `internal/harness/harness.go`, `internal/spawn/launch.go` (only if the permission probe in step 4 requires it).
- `internal/harness/agents/architect.claude.md`, `architect.opencode.md`, `architect.agy.md`, `architect.codex.toml`, and the `reviewer.*` / `researcher.*` / `security-reviewer.*` twins across the same four kinds, plus regenerated `internal/harness/agents/shipped.sha256` via the script (no hand edits).
- Tests only in `internal/relevo/*_test.go`, `internal/harness/*_test.go`, `internal/store/*_test.go` (for path pins), `internal/spawn/*_test.go` (if step 4 changes launch); pure-function CLI coverage in `internal/relevo`, never a `cmd/relevo` harness-spawning or network test.
- `docs/plans/2026-10-11-reader-output-file.md` (new).
- Must not touch: `internal/reporttail`, `internal/relevo/chain_*`, `served_check.go`, `internal/serve/checks.go`, `internal/remote/chain.go`, `internal/db/chain.go`, `internal/sync`, `cmd/relevo/daemon.go`, `cmd/relevo/gate.go`, `internal/bugreport`, unless the permission or server-path probe forces it, stated in the report.

## Rules the builder honours

- Base `origin/main 19e25095`; comments say why only; no issue numbers in code/tests; functions ≤70 lines, non-test files ≤600 lines (`scripts/check-comments.sh`, `scripts/check-filesize.sh`); no new lint exclusions (`.golangci.yml`); coverage baseline via `sh scripts/check-coverage.sh --write` only if code moves packages, never lowered to green.
- CI has no harness binary and no network: any CLI-level test is a pure function in `internal/relevo`.
- Every fix gets a failing-without-it test, each naming its mutation: (a) runner file present but close saved stream text — kill by making file content differ from stream text; (b) empty/whitespace file treated as delivered — kill by writing empty file and asserting fallback fires; (c) fallback without visible marker — kill by removing the marker assertion; (d) prompt/agent text missing the path-write order — kill by deleting the path sentence; (e) read-tier refusing the `OutDir` write — kill by denying `OutDir` in the fake harness; (f) server/client path drift — kill by pointing the fake server at a different rel.
- Focused command (iterate until green): `go test ./internal/relevo -run 'TestWriteReader|TestReader|TestShowOutput' -count=1` plus `go test ./internal/harness -count=1` after agent edits; final command once: `make check`.

## Report must include

- Which close paths now prefer the file and the exact empty definition used (byte length vs trimmed).
- Where the fallback marker surfaces (log note text, `show --output` header, status cell) with one quoted line.
- Per-harness permission finding (claude, agy, opencode, codex): what allowed the `OutDir` write and what still refuses repo writes, with the tier/flag evidence.
- Server-reader finding: same path confirmed or the divergence with file:line.
- `scripts/agents-shipped.sh --write` run + `--check` result; coverage baseline regenerated or not; `make check` result; commit hash and message.
