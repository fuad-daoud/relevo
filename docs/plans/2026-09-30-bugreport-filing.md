# Plan: `bugreport` files a real title and description, caps the file it writes to gh's limit, and carries the round's note (#732)

Base: `40d749b4` (current main, this worktree's HEAD). One builder round, one commit, do not push.
The four base decisions (D1–D4) stand as written. One note where a decision passes the
issue: D2 makes `--out` capped too, though #732 proposed `--out` stay uncapped — `--out` prints
the same `gh` line, so the file it names is the filing artifact and must fit. No other disagreement.

## Behaviour

**A. `--title T` overrides the generated title (D1).** Seams: `cmd/relevo/bugreport.go:19-28` (flag values), `:32-43` (flag set), `:54-63` (options), `:87-101` (parse), `:126-131` (the title handed to `Collect`); `internal/bugreport/render.go:12-17` (`bugreport.Title` stays the generator).
- A non-empty T replaces the generated title in the markdown header, in the `--json` document's `title`, in the `gh issue create --title` argv and in the printed line — one value, four renderings.
- An empty `--title ""`, or the flag absent, keeps today's generated title; no new refusal.
- T combines with every mode (`--stdout`, `--json`, `--gh`, `--out`) and with `--logs`, `--raw`, `--name`/`--round`.
- T passes the redaction pass exactly as the generated title does (it is the same `Bundle.Title`); no `sanitize.Text` is added to it — D1 gives sanitize to the body only.

**B. `--body FILE` becomes the bundle's first section (D1).** Seams: `cmd/relevo/bugreport.go:32-43` (flag), `:54-63` (options), `:87-101` (`cmdBugreport` reads the file before `runBugreport`); `cmd/relevo/bugreport_sources.go:101-117` (source wiring) and `:442-447` (`bodyLines`); `internal/bugreport/bundle.go:13-24` (a new `SectionDescription = "description"`); `internal/bugreport/project.go` (a new `DescriptionSection`).
- The file's text becomes the first section, `## Description`, ahead of `## Environment`, in the markdown and in the `--json` document alike; with `--logs` it is still first.
- The text passes `sanitize.Text` (tabs become four spaces, `\r\n` becomes `\n`, control bytes become `U+FFFD`) and then the bundle-wide redaction pass, like every other section.
- A missing or unreadable FILE is `usage`, exit 2: the message names the path and the OS error, no runtime is built, nothing is written or printed.
- A file with no text renders the `## Description` heading with no lines (the flag was given, so the section is there).
- Absent `--body` keeps today's output byte-for-byte; the two flags need no `validate()` combination rule.

**C. The markdown written to a file is capped to gh's body limit (D2).** Seams: `internal/bugreport/gh.go:1-16` (a new `GhBodyLimit = 65536`), `internal/bugreport/render.go:49-69` (a new capped render), `cmd/relevo/bugreport.go:136-158` (the file branch).
- Under or at the limit the capped render is byte-identical to `Markdown`.
- Over the limit the render is cut on a line boundary so the file's bytes are ≤ 65,536, and one final line names the limit and `relevo bugreport --stdout` as the full render (for example `[body cut at 65536 bytes to fit GitHub's issue-body limit; run 'relevo bugreport --stdout' for the full render]`). The test asserts the two facts — `65536` and `--stdout` — not the sentence.
- The cap counts bytes, which is conservative against GitHub's 65,536-character limit; it applies to the default file, `--out` and the file `--gh` files, and changes none of those paths' printed lines or argv.
- `--stdout` and `--json` stay uncapped; `--stdout` is where the full render lives.
- A bundle that cannot be cut — its first line alone does not fit beside the marker — fails `usage`, exit 2, with the bundle's size and the limit, before a file is written or a line is printed; the same bundle still renders under `--stdout`.

**D. The rounds table carries the entry's note (D3).** Seams: `internal/bugreport/project.go:19-24` (`noteRunes` already exists), `:169-187` (columns), `:205-220` (`roundRow` and its comment). `internal/relevo/reconcile.go:408-411` is where the reporttail reject reason lands in that note.
- A `note` column sits directly after `halted_at`, before the token columns; its value is `truncateRunes(e.Note, noteRunes)` (200 runes, cut marked with `…`).
- An `unstructured` report row carries its reason in that column — the issue's `tail: line 267: commands_run list is not closed` is the recorded example.
- Notes pass redaction like every other cell (the fixture's token-bearing note must render `<redacted>`); `Payload`, `Path`, `ChangedPaths`, `CommandsRun` and `NotDone` stay excluded.

**E. Docs, registry and goldens (D4).** Seams: `docs/specs/2026-09-30-bugreport-design.md` (header `:1-10`, decisions `:32-53`, shape `:55-80`, sections `:82-116`, acceptance `:163-172`); `cmd/relevo/registry_rows.go:22-30`; `internal/mastermind/guide.md:48-57`; `claude-plugin/skills/planner-loop/SKILL.md:41-47`; `CONTRIBUTING.md:83-89`.
- The spec gains one amendment line naming #732 (the spec's header already carries its issue; no code or test comment gains a number), decision 6 gains the `--title` override, a new decision states the cap, §3's synopsis and bullets gain `[--title T] [--body FILE]` and say the file render is the capped one, §4 gains the description section as its first item, item 5 drops `Note` from its exclusion list and adds `note` to its kept list, and §8 gains acceptance bullets for one command filing title + description + diagnostics, the capped file with `--stdout` as the full render, the uncuttable-bundle `usage`, and the unstructured report's reason.
- The guide and skill bullets gain one clause for `--title`/`--body` (they are injected into every session: one clause, no growth); `CONTRIBUTING.md` gains one line.
- The registry entry gains `--body` and `--title` in `Flags` (sorted) and in `Args`, so `help --json` spells them; `cmd/relevo/registry_test.go:173-205` needs no edit — its parity test is the check.
- Regenerated goldens: `internal/bugreport/testdata/bundle.golden.md`, `internal/mcp/testdata/contract/instructions.golden`, `cmd/relevo/testdata/contract/help-json.golden`.

## Steps

1. Tests first, red for the right reason: add `TestMarkdownCappedFitsTheLimit` to `internal/bugreport/render_test.go` (small bundle byte-identical; a small limit cuts whole lines and leaves a marked final line naming the limit and `--stdout`; an uncuttable first line errors with the size and the limit), `TestRoundsSectionCarriesTheNote` to `internal/bugreport/project_test.go` (the column after `halted_at`, an `unstructured` row's reason, the 200-rune cut), and to `cmd/relevo/bugreport_test.go` `TestBugreportTitleOverridesTheGeneratedTitle` (header, `--json` title, printed line, `--gh` argv; empty title keeps the generated one), `TestBugreportBodyBecomesTheFirstSection` (first section in markdown and JSON; tab/control sanitize; a seeded `ghp_` token renders `<redacted>`), `TestBugreportBodyUnreadableIsUsage` (path and OS error named, exit 2), `TestBugreportBodyAndTitleCombineWithEveryMode` (`--stdout`, `--json`, `--gh`, `--out`, `--logs`, `--raw`), `TestBugreportFileRenderIsCappedToGhLimit` (a `--body` file of ~70 KiB: the written file is ≤ 65,536 bytes, its last line names 65536 and `--stdout`, and the `--stdout` render is the uncapped full text), `TestBugreportUncuttableBundleIsUsage` (a `--title` of 70,000 bytes: `--out` is `usage` naming size and limit and writes no file; `--stdout` still prints). Focused command: `go test -count=1 ./internal/bugreport/ -run 'Markdown|RoundsSection|GhBody'` and `go test -count=1 ./cmd/relevo/ -run 'TestBugreport'`.
2. A in `cmd/relevo/bugreport.go` (`:19-43`, `:54-63`, `:87-101`, `:126-131`): the two flags, the options fields, the non-empty override; step 1's title tests green.
3. B: read FILE in `cmdBugreport` (`usage` on error), add `SectionDescription` (`internal/bugreport/bundle.go:13-24`), `DescriptionSection` (`internal/bugreport/project.go`), wire a `descriptionSource` first in `bugreportSources` (`cmd/relevo/bugreport_sources.go:101-117`); step 1's body tests green.
4. C: `GhBodyLimit` in `internal/bugreport/gh.go`, the capped render in `internal/bugreport/render.go`, and the file branch of `runBugreport` (`cmd/relevo/bugreport.go:136-158`) switched to it, mapping its error to `fail(codeUsage, …)`. `runBugreport` is 68 lines today: extract the file/print/gh half into one helper so both stay ≤ 70 lines, per CLAUDE.md. Step 1's cap tests green.
5. D: the `note` column (`internal/bugreport/project.go:169-187`, `:205-220`; the function comments updated to the new allow-list) and regenerate `internal/bugreport/testdata/bundle.golden.md` with `go test -count=1 ./internal/bugreport/ -run TestMarkdownGolden -update`; the golden's diff is exactly one `note` column, with the seeded token rendered `<redacted>`.
6. E: the spec, `cmd/relevo/registry_rows.go:22-30`, the guide bullet, the skill bullet, `CONTRIBUTING.md`, then `go test -count=1 ./internal/mcp/ -run 'TestContractInstructions' -update` and `go test -count=1 ./cmd/relevo/ -run 'TestHelpJSON' -update`; each golden diff carries only the new clause or flags, and `TestContractCommandLineReferences` still resolves every backticked `relevo bugreport --title/--body` example.
7. Package sweep: `go test -count=1 ./internal/bugreport/ ./cmd/relevo/ ./internal/mcp/` green; `go test -race -count=1 ./...` green; no new exclusion in `.golangci.yml` or `scripts/check-filesize.allow`, and `testdata/coverage-baseline.txt` untouched (more tests, never a lower baseline). The new `cmd/relevo` tests keep the `bugreportExec` fake (no gh, no journalctl) and use only `t.TempDir()` under the TestMain sandbox.
8. `make check` on the working tree while fixing; then write `docs/plans/2026-09-30-bugreport-filing.md` (this plan, in the repo's shape) and make the one commit: code, tests, spec, docs and plan, no push.
9. From the committed tree: `make check` exit 0 and `gofmt -l $(git ls-files '*.go')` empty; `git show --stat HEAD`.

## Deleted behaviour

1. The rounds projection no longer excludes the log entry's `Note`: the table carries a `note` column between `halted_at` and the token columns, truncated to 200 runes. `Payload`, `Path`, `ChangedPaths`, `CommandsRun` and `NotDone` stay excluded.
2. The default, `--out` and `--gh` markdown file is no longer always the full render: over 65,536 bytes it is cut on a line boundary with a marked final line, and the full render moves to `--stdout`/`--json`. Bundles at or under the limit are byte-identical to today.
3. `--gh` no longer hands gh a body over the limit; gh files the capped file, so GitHub's raw refusal is no longer reachable from `bugreport --gh`.
4. Nothing else: no flag, output line, argv entry or code path is removed or reordered; with no `--title`/`--body` and a small bundle, today's stdout and file bytes stand.

## The report must include

- The focused test output for each named test above, and the changed/added test names.
- The capped body's size evidence from a real run: `wc -c` of the file the default/`--out` run wrote (≤ 65,536) and its final line, beside `wc -c` of the same bundle's `--stdout` render showing the larger uncapped size.
- The uncuttable case's message, verbatim, with its size and the limit, and its `usage` exit code.
- The regenerated goldens' diffs (`bundle.golden.md`, `instructions.golden`, `help-json.golden`) and the spec, guide, skill and CONTRIBUTING diffs.
- The full `make check` output, exit 0, from the committed tree; `gofmt` clean; the commit hash and `git diff --stat`.
- One line each: the coverage baseline and the exclusion lists are untouched; `runBugreport` and its new helper are within 70 lines and no touched file is near 600.
