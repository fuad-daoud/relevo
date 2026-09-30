# Plan: a verify question is a row-only round file, so the ask name is reserved

One change area: the staging of a verify consult's question, plus the reserved-name set it joins. If the tree contradicts anything below, halt and report.

## 1. Seed vs tree (checked at HEAD `5374819f`)

- `internal/consult/verify.go:337-350` — `stageVerifyQuestion`: the inline half rows the question with `Tx.PutRoundFile` (`:341`); the oversized half writes it with `os.WriteFile` (`:346`), which is the file's only `os` use.
- `internal/consult/verify.go:238-249` — `launch`: `inlinePrompt` decides (`:243`); `!inline` sets `prompt = render("Read: " + askPath)` (`:247-248`), a reference a harness resolves by reading the file.
- `internal/consult/prompt.go:18,22-25,31-37` — `InlineAskMax = 64 << 10`, `askInlineBlock`, `inlinePrompt`. The limit is argv's: the prompt is one argv element, and Linux caps one argument at 128 KiB, so half is the margin.
- `verifyQuestion` (`verify.go:67-73`) is a fixed template over four bounded paths; the rendered question is about 1 KiB. The `!inline` branch therefore cannot fire for a verify consult. It exists from the generic ask machinery, and `relevo ask` is gone (`consult.Request` has no callers).
- `internal/store/reserved.go:3-16` — `reservedRoundFileRe`, the row-only set: diff, drift, builder-segments, findings. `ReadFile`/`StatFile` (`seal.go:30`, `:66`), the round-file walk (`seal.go:471-476`) and `RoundFiles` (`seal.go:149-178`) all key off it.
- `internal/store/reserved_test.go:49-64` — `reservedKeyCases` names the four shapes; `:71-124` pins the read policy; `:215-280` the seal policy with diff as the representative.
- `internal/store/seal_test.go:209-233` — `writeSealFixtures` lists `003-aabbccdd-ask.md` in `sealed`: a planted ask file is sealed into a row today. After this round it must not be.
- The name itself does not change: `AskPath` (`paths.go:245-250`), `store_test.go:210` and `types_test.go:69` stay as they are.
- No harness binary and no network: everything here is pure store and consult code.

## 2. Behaviour and cases

Policy: **the ask is row-only; a question that cannot be inlined stops the verify.**

- `stageVerifyQuestion(tx, name, round, askPath, question string) error` records the question with `Tx.PutRoundFile` for every size. No disk write, no `inline` parameter (there is one caller).
- `launch`: the inline decision still picks the prompt. When it fits (always, in practice) nothing changes. When it does not, the verify is skipped with `fail("question over the inline prompt limit")` before anything is staged: relevo does not spawn a reviewer it cannot hand the question to. Nothing today reaches this.
- `reservedRoundFileRe` gains `|[0-9a-f]{8}-ask\.md`. A file with that name under a binding directory is a plant or a stale copy: `ReadFile` and `StatFile` answer from the row, live or archived, or miss with `fs.ErrNotExist`; the round-file walk skips it with one warning; it is never sealed, listed or removed. An oversized question written by an older relevo keeps sitting on disk, untouched and unread.
- The round view keeps resolving: `ReadFile(askPath)` returns the row, and `RoundFiles` lists the name from the row (`seal.go:170-176`), so a live ask appears in the round's file list with no file on disk.
- Unchanged: `AskPath`'s shape and the `Consult.AskPath` field; the ask log entry (path, direction, kind, note); the inline prompt's text and the argv it produces; the policy for the other four reserved keys; prompts, streams, reports, builder logs, gate logs, consult streams and `question` stay disk-first.

## 3. Seams (verified)

| Seam | Anchor |
| --- | --- |
| the staging to change | `internal/consult/verify.go:337-350` (`os.WriteFile` at `:346`) |
| the caller | `internal/consult/verify.go:238-249` |
| the guard to keep | `internal/consult/prompt.go:31-37` |
| the reserved set | `internal/store/reserved.go:7` |
| the read/stat/walk policy to inherit | `internal/store/seal.go:30-105`, `:149-178`, `:471-476` |
| fixtures to move | `internal/store/reserved_test.go:49-64`; `internal/store/seal_test.go:223` |
| test pattern for a live binding | `internal/store/reserved_test.go:16-24`; `store.New` `store.go:92`; required fields `lifecycle.go:207-212` |
| must not change | `paths.go:245-250`, `store_test.go:210`, `types_test.go:69`; `prompt.go`'s text; `.golangci.yml`; the coverage baseline |

## 4. Steps (each with its done-when)

1. **Store test, red half.** In `internal/store/reserved_test.go`, add the ask shape to `reservedKeyCases()`:
   ```go
   {"001-7f2a3c1d-ask.md", func(s *Store, b string) string { return s.AskPath(b, 1, "7f2a3c1d") }},
   ```
   and update the helper's comment: five shapes, not four.
   *Done when:* `go test ./internal/store/ -run TestReadFileRefusesAReservedRoundFileOnDisk -count=1` fails on the new subtests — today the plant's bytes come back and the row's do not.
2. **Reserve ask.** Add `|[0-9a-f]{8}-ask\.md` to `reservedRoundFileRe` and name the ask in the comment.
   *Done when:* step 1 is green and `go test ./internal/store/ -count=1` is green.
3. **Staging test, red half.** Add to `internal/consult/verify_test.go` (imports gain `io/fs`, `os`, `store`):
   `TestStageVerifyQuestionRecordsARowAndNoFile` —
   - `s := store.New(t.TempDir())`, then `s.Save(store.Binding{Name: "webshop", CWD: "/repo"})`;
   - `question := strings.Repeat("x", InlineAskMax+1)` on purpose: the function must be size-blind;
   - `askPath := s.AskPath("webshop", 1, "7f2a3c1d")`, staged inside `s.WithLock`;
   - assert `os.Stat(askPath)` is `fs.ErrNotExist`, and `s.ReadFile(askPath)` returns the question.
   *Done when:* it fails today: the file exists, and the read is the file's, not a row's.
4. **Stage as a row, always.** Rewrite `stageVerifyQuestion` per §2 and drop the `inline` argument at the call site; in `launch`, replace the `!inline` prompt line with `return v.fail("question over the inline prompt limit")`; drop the `os` import.
   *Done when:* step 3 is green, `go test ./internal/consult/ -count=1` is green, and `go test ./internal/relevo/ -run Verify -count=1` is green (the existing verify-flow tests already pin the inline row and no file).
5. **Seal fixture.** Remove `003-aabbccdd-ask.md` from `writeSealFixtures`' `sealed` map; leave the consult stream, and change nothing else in the test.
   *Done when:* `go test ./internal/store/ -count=1` is green.
6. **Mutations (§5), one at a time, restoring after each.**
   *Done when:* each mutation makes its named test fail for the stated reason, and the restore makes it pass.
7. **Wrap up.** `gofmt` the changed files; commit by name (never `git add -A`) with subject `fix(consult,store): a verify question is row-only, so the ask name is reserved` and a body ending `Fixes #708`; ride this plan verbatim as `docs/plans/2026-09-30-ask-row-only.md` in that commit. Do not push.
8. **Final verification.** `go test ./internal/store/ ./internal/consult/ -count=1`, then `make check` once.
   *Done when:* `make check` is green, the tree is clean, and the diff adds no exclusion, no allow-list entry and no coverage-baseline change.

Focused command: `go test ./internal/store/ ./internal/consult/ -count=1`. Full command: `make check`.

## 5. Mutation checks

| # | Mutation (revert the fix) | Test that must fail, and how |
| --- | --- | --- |
| M1 | drop `\|[0-9a-f]{8}-ask\.md` from `reservedRoundFileRe` | `TestReadFileRefusesAReservedRoundFileOnDisk/001-7f2a3c1d-ask.md`: the plant with no row is returned, and the row-plus-plant case returns the plant |
| M2 | `stageVerifyQuestion` writes the question with `os.WriteFile` again | `TestStageVerifyQuestionRecordsARowAndNoFile`: the file exists, and `ReadFile` misses (the name is reserved but no row was written) |

Both halves are independently pinned: M1 fails only the store read policy, M2 only the staging.

## 6. Deleted behaviour (closed list)

1. A verify question over `InlineAskMax` is no longer written to disk for the reviewer to read; the verify is skipped instead. Nothing today renders a question that large.
2. `stageVerifyQuestion` no longer takes an `inline` flag and no longer produces a file.
3. A file named `NNN-<8hex>-ask.md` under a binding directory is no longer read, sealed, listed or removed by relevo, whether planted or written by an older relevo.
4. Nothing else: the inline prompt, `AskPath`, the `Consult` record, the ask log entry, and the disk-first policy for every other key are unchanged.

## 7. Declared scope

- `internal/store/reserved.go` — the regex and its comment.
- `internal/consult/verify.go` — `stageVerifyQuestion`, the `launch` call site and the `!inline` branch, the `os` import.
- `internal/store/reserved_test.go` — the ask case and the helper's comment.
- `internal/store/seal_test.go` — one fixture line.
- `internal/consult/verify_test.go` — the new test.
- `docs/plans/2026-09-30-ask-row-only.md` — this plan, verbatim.

## 8. Not done (say it in the report)

- `consult.Request.Inline` and its comment (`internal/consult/ask.go:28-33`) describe a file fallback that no longer exists; `Request` has had no callers since `relevo ask` was removed. Dead code, left for a cleanup round.
- No change to `InlineAskMax`, `askInlineBlock`, `inlinePrompt` or the harness prompt text.
- No new consumer of the ask path is added; the change is the policy, not a surface.
