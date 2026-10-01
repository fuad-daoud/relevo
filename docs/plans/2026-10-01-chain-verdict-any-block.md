# Chains — the verdict is read from any relevo block, not only the last

Base: main at `118adba5`. One-shot builder round, new commits only (no amend, no rebase of anything on the branch), `make check` and `make e2e` green, and this plan committed as `docs/plans/2026-10-01-chain-verdict-any-block.md` in the same round.

## Seed vs code (said plainly, not guessed around)

The seed matches the tree:

- `blockValue` (`internal/chain/verdict.go:40-63`) bounds its key scan by `FindRelevoBlock`'s `openIdx`/`closeIdx`, and `FindRelevoBlock` (`internal/reporttail/reporttail.go:184-213`) returns the LAST ```relevo fence pair. So a final message that ends with the reader's own status block makes `ParseVerdict` return `""` and `ParseFindings` return `ok == false`.
- The shape is structural, not an accident: the reviewer and security seeds ask for the verdict/findings block at the end (`internal/chain/seeds/reviewer.md`, `seeds/security.md`), and every reader round gets `readerPrompt`, which asks for the status block last (`internal/relevo/send.go:70-87`), composed for a chain member by `composePrompt`/`readerPromptFor` (`send.go:849-869`) and sent by `sendChainRound` (`internal/relevo/chain_send.go:25-118`, which stages the seed as the plan and composes the reader prompt).
- The e2e fake writes one block per member (`internal/e2e/headless_test.go:433` reviewer, `:442` security), which is why `TestChainE2E` passed.

Three readings the seed leaves open, decided here and pinned by tests:

1. The body's tail rule stays: the last ```relevo fence must be closed and followed by nothing but blank lines, or there is no verdict. That is `FindRelevoBlock`'s `reason`, and it is exactly what the existing bodies in `TestParseVerdictRejectsMissingBlock` pin (unclosed fence; prose after the closing fence). The fix reads any *earlier* block; a body whose tail is unreadable stays refused. Prose *between* two blocks does not disqualify an earlier block.
2. The LAST block that carries the key decides, with no fallback: `verdict: pass` followed by a later `verdict: maybe` is no verdict, never the earlier pass.
3. `reporttail.StripTail` keeps its last-block rule (the seed forbids changing the builder tail's rule). Consequence, accepted and reported: a two-block reader output keeps its first (verdict/findings) block in the saved output file; only the trailing status block is stripped.

## Deliverable

- `chain.ParseVerdict` / `chain.ParseFindings` read their key from the LAST fenced relevo block of the body that carries it; a body with no block carrying the key is still "no verdict" / not given; an unknown value is still "no verdict".
- New `reporttail.RelevoBlocks`, the smallest helper that enumerates fence pairs, so the earlier-block scan never re-implements fence rules.
- A two-block final message for the e2e fake's reviewer (and its security member), with `TestChainE2E` still green.
- This plan as `docs/plans/2026-10-01-chain-verdict-any-block.md`.

## Scope

Changed: `internal/reporttail/reporttail.go`, `internal/reporttail/reporttail_test.go`, `internal/chain/verdict.go`, `internal/chain/verdict_test.go`, `internal/e2e/headless_test.go` (only the fake script's two message literals).

Created: `docs/plans/2026-10-01-chain-verdict-any-block.md`.

Untouched, deliberately: `reporttail.FindRelevoBlock`, `Parse`/`ParseWithReason`, `StripTail` and the builder report tail's rule; `internal/relevo/{chain.go,reconcile.go,chain_send.go,send.go}` (call sites and prompts stay); the chain seeds; `internal/consult/verify.go`'s `parseVerdict` (its own prompt asks for exactly one block, not the reader prompt); `cmd/relevo` (no CLI behaviour changes; CI has no harness or network); `docs/specs`.

## Pinned behaviour and seams

### 1. `internal/reporttail/reporttail.go` — the fence pairs

Insert after `FindRelevoBlock` (`:184-213`), before `SplitFenceLines` (`:215-225`):

```go
// RelevoBlock is one fenced relevo block: the line indices of its opening and
// closing fences.
type RelevoBlock struct{ Open, Close int }

// RelevoBlocks returns every fenced relevo block in lines, in file order:
// each ```relevo opening fence paired with the next ``` closing fence. An
// opening fence with no closing fence after it yields no pair and ends the
// scan; no block returns nil.
func RelevoBlocks(lines []string) []RelevoBlock
```

- Fence matching must be byte-for-byte `FindRelevoBlock`'s (`TrimRight(line, " \t") == "```relevo"` at `:187`; `== "```"` at `:197`), so the gate and the scan cannot disagree about what a block is.
- Why a new helper and not repeated `FindRelevoBlock` calls: `FindRelevoBlock` returns only the last pair and its `reason` is non-empty whenever that pair is followed by non-blank lines — true for every earlier block of a multi-block body, because the next block follows it. Enumerating through it would refuse exactly the bodies this round must read.
- Nothing existing changes: the builder tail's last-block rule is untouched.

### 2. `internal/chain/verdict.go` — the scan

- `blockValue` (`:40-63`): keep the gate — `openIdx, _, reason := reporttail.FindRelevoBlock(lines)`; `openIdx < 0 || reason != ""` → `("", false)` — then walk `reporttail.RelevoBlocks(lines)` in order and run today's per-line key scan unchanged (`TrimSpace(StripComment(line))`, first `:`, key equal after trim, `UnquoteScalar`) over `lines[b.Open+1 : b.Close]`. Every hit overwrites the previous, so the last block carrying the key wins. Extracting the per-line scan as `blockKey(lines []string, from, to int, key string) (string, bool)` is fine if it keeps `blockValue` small; the behaviour above is the contract.
- Doc comments: `ParseVerdict` (`:10-12`), `ParseFindings` (`:26-27`) and `blockValue` (`:40-42`) say what now holds — the key is read from the last block that carries it; a missing block, an unreadable tail or an unknown value is still no.

### 3. `internal/e2e/headless_test.go` — the fake's two-block messages

- The chain-seed branch (`:417-444`) builds each member's final message as one JSON string by hand (printf into `"result":"…"`), with `\n` written as literal backslash-n; the escapes are the whole trick. Reviewer case (`:420-434`): after the verdict block, append relevo's status block (`status: done`, `halted_at: ""`, `changed_paths: []`, `commands_run: []`, `not_done: []`), with `halted_at`'s quotes written `\"\"` so the JSON line stays valid — `transcript.FinalText` drops an unparsable line and the round would close with no summary. Security case (`:441-443`): the same status block after `findings: 1`, so the e2e pins `ParseFindings` on the live shape too.
- Change nothing else in the harness: the reviewer round counter (`:421-428`) and every seed first line stay, or the fake stops recognising the seeds.

## Tests (new, by name, and what each pins)

`internal/reporttail`:

- `TestRelevoBlocksReturnsEveryBlockInOrder` — two blocks with prose between → both pairs with exact indices; one block → one pair; no fence → nil; a closed block followed by an unclosed open → only the closed pair.

`internal/chain`:

- `TestParseVerdictReadsAVerdictBeforeTheStatusBlock` — `verdict: pass` followed by a full status block → pass; the same with `changes`.
- `TestParseVerdictLastVerdictBlockWins` — pass then changes → changes; changes then pass → pass; verdict, status, verdict → the last.
- `TestParseFindingsReadsACountBeforeAStatusBlock` — `findings: 1` then status → (1, true); `findings: 0` then status → (0, true).
- `TestParseFindingsLastFindingsBlockWins` — `findings: 3` then `findings: 0` → (0, true).
- `TestParseVerdictRejectsMissingKey` gains a full status-block-only body → no verdict.
- `TestParseVerdictRejectsUnknownValue` gains a pass-then-maybe body → no verdict.
- The existing unclosed-fence and prose-after bodies stay as they are and must keep passing; they are the tail rule's pin.

`internal/e2e`: no new name; `TestChainE2E` is the pin.

## Ordered steps

1. reporttail: add `RelevoBlock`/`RelevoBlocks` and `TestRelevoBlocksReturnsEveryBlockInOrder` — works when `go test ./internal/reporttail -count=1` is green.
2. chain: rewrite `blockValue`'s scan and its docs, add the four new tests and the two extended bodies — works when `go test ./internal/chain -count=1` is green.
3. e2e fake: the reviewer and security messages gain the status block — works when `go test ./internal/e2e -run TestChainE2E -count=1` is green.
4. Mutation checks (table below), each mutation reverted before the next — works when every named test fails under its mutation and passes again after the revert.
5. Full: `make check`, then `make e2e`; write `docs/plans/2026-10-01-chain-verdict-any-block.md` with this plan's text and commit the code and the doc together, as new commits.

## Deletions (closed list)

1. The last-block-only bound of `blockValue`'s key scan (`internal/chain/verdict.go:50-62`): `closeIdx` no longer bounds the loop; the loop walks every enumerated block. Nothing else in the function goes.
2. Nothing else is deleted. No test is deleted; the existing reject bodies are kept.

## Mutation checks (each names the test that must fail)

| # | behaviour | mutation | test that must fail |
|---|---|---|---|
| 1 | a verdict before the status block is read | restore last-block-only `blockValue` | `TestParseVerdictReadsAVerdictBeforeTheStatusBlock`; `TestChainE2E` |
| 2 | a findings count before the status block is read | leave `ParseFindings` last-block-only (verdict fixed) | `TestParseFindingsReadsACountBeforeAStatusBlock`; `TestChainE2E` halting at the security step |
| 3 | the last carrying block wins | keep the first hit instead of overwriting | `TestParseVerdictLastVerdictBlockWins` |
| 4 | an unknown verdict is never a pass | `ParseVerdict`'s default returns `VerdictPass` | `TestParseVerdictRejectsUnknownValue` |
| 5 | an unreadable tail is never a verdict | drop the `reason` check on `FindRelevoBlock` | `TestParseVerdictRejectsMissingBlock` |
| 6 | earlier blocks are paired by reporttail | make `RelevoBlocks` return only the last pair | `TestRelevoBlocksReturnsEveryBlockInOrder` |

## Halt conditions (unverified premises)

1. If the status block cannot ride the fake's JSON line without touching `transcript.FinalText` (the line stops parsing, the summary comes out empty), halt and report; the e2e must pin the parse, not a workaround.
2. If `TestChainE2E` fails for anything but the verdict/findings parse — the close's outcome moving from `unstructured` to `done`, the strip leaving the earlier block, an injection note, timing — halt and report the failing assertion; do not bend the e2e or the close.
3. If enumerating blocks needs a change to `FindRelevoBlock`, `Parse`/`ParseWithReason` or `StripTail`, halt: the builder tail's rule is out of scope.
4. If any existing test beyond the two extended bodies pins last-block-only for these two parsers (none was found in this tree), halt rather than edit it.
5. If `make check` fails on coverage (reporttail's baseline is 96.9 in `testdata/coverage-baseline.txt`) or on lint (`internal/chain` and `internal/reporttail` are not excluded in `.golangci.yml`), fix the new code; editing the baseline or adding an exclusion is a halt.
6. If the seed's "any fenced relevo block" is read to drop the unreadable-tail refusal (the prose-after body becoming a pass), halt and report: that contradicts `TestParseVerdictRejectsMissingBlock` and this plan's reading.

## The report must include

- The `relevo` tail (`status`, `changed_paths`, `commands_run`, `not_done`) and the commit(s) made — new, never amended.
- The focused commands per step and their results, plus the full `make check` and `make e2e` output.
- The mutation table filled with what actually failed for each mutation.
- The three readings above: the tail rule kept (with the existing bodies that pin it), last-carrying-unknown-wins, and the `StripTail` consequence (a two-block reader output keeps the verdict/findings block in the saved file).
- The e2e escaping detail (`\n` and `\"\"` in the fake's JSON line) and confirmation the reviewer's round-1 `changes` counter is untouched.
- Coverage: `check-coverage.sh` ok, baseline untouched; lint ok.
- Deliberately not done: `readerPrompt`, the chain seeds, the builder tail, `consult.parseVerdict` (its prompt asks for one block), `cmd/relevo`, `docs/specs`; no new CLI test (CI has no harness or network).
