# Plan: read a report's relevo block even when text follows it

## Behaviour

A report whose fenced `relevo` block parses, followed only by trailing text (for example `git diff --stat` output), is read as that block's status. The trailing text stays in the report body. There is no nudge round and no halt.

### Why the chain halts today

1. `reporttail.FindRelevoBlock` (internal/reporttail/reporttail.go, ~L207-238) returns the reason `tail: prose after closing fence` for any non-blank line after the closing fence.
2. `ParseWithReason` (same file, L46-59) propagates that reason and returns `ok == false`.
3. The round close, `reconcile` in internal/relevo/reconcile.go (~L808-833), keeps the outcome `unstructured` and appends the reason to `note`.
4. `chainEventFromCloseWF` (internal/relevo/chain_close.go, L88-100) sees `wf.Outcome != done`. It builds the halt "builder halted on plan N: note: tail: prose after closing fence" through `chain.BuilderHaltReason`.

### Fix site

The reason is produced in one place, so the fix is there. `FindRelevoBlock` stops treating text after the closing fence as a reason. The closing fence still ends the block, so the block grammar is otherwise unchanged. The callers of that reason need these changes:

- **`ParseWithReason` / `Parse`** (reconcile.go L810, chain_close.go L89, reader_deliverable.go L195 via `ParseWithReason`; `Parse` itself): they now return ok and the block's status. This is the actual fix for chains, plain rounds and readers.
- **`StripTail`** (reporttail.go L295-312): it keys on the reason to keep a body whole when prose follows. Once the reason is gone it would cut the block and eat the prose. It must keep that behaviour by checking for trailing non-blank lines after `closeIdx` itself. Its callers (plan_steps.go L28, summary.go L121 and L224, reconcile.go L1022) see no change.
- **`BlockValue` and `HasFencelessBlock`** (internal/reporttail/blockvalue.go L16, L36): they used `reason != ""` to send a "block followed by prose" body to the fenceless fallback, where it missed. They now read the fenced block's values (reader outcomes and status), the same as `consult/verify.go`, which already ignored the reason and read such a block. The chain's reader path (`chainReaderStatus`, `chainOutcomeBodies`, `reader_deliverable.go`) therefore no longer halts on trailing text either. This is a deliberate, small widening of what a reader's block may be followed by. Say so in the report.
- **`consult/verify.go` L87**: it ignores the reason. Only the call signature changes.

## Seams and steps

1. **`internal/reporttail/reporttail.go`**
   - Change `FindRelevoBlock` to return `(openIdx, closeIdx int)`: drop the `reason` result and the trailing-text loop (L229-235). Update its doc comment (L198-206) to say the block ends at its closing fence and anything after is not the block's concern; callers that must not cut text check for it themselves.
   - Add a small unexported helper, e.g. `textAfter(lines []string, closeIdx int) bool`: true when any line from `closeIdx+1` is non-blank. It is false for an unclosed fence, where `closeIdx == len(lines)`.
   - `ParseWithReason`: drop the `reason` plumbing from the `FindRelevoBlock` call (L53-59). `reject`, `missing status` and `unknown status` stay. Keep the function's doc accurate: the reason now names only a block that was found but unreadable.
   - `StripTail`: replace `reason == "tail: prose after closing fence"` with `textAfter(lines, closeIdx)`, and update its doc bullet. Behaviour is byte-identical to before.
   - Done when: `go build ./...` fails only at the callers listed in step 2.

2. **Callers of the signature**
   - `internal/reporttail/blockvalue.go` L16: `openIdx, _ := FindRelevoBlock(lines)`; the fallback condition becomes `openIdx < 0`. L36 becomes `openIdx >= 0` alone.
   - Update the `BlockValue` doc comment so it no longer implies a block followed by text misses.
   - `internal/consult/verify.go` L87: `openIdx, closeIdx := ...`.
   - Done when: `go build ./... && go vet ./...` pass.

3. **Update the tests that pinned the old rule** (rename and flip them; the names say what they pin, with no history):
   - `internal/reporttail/reporttail_test.go` ~L92: "prose after the closing fence -> false" becomes "a parsed block followed by prose reads as the block's status". Assert `Parse` returns ok with `Status == "done"`.
   - `internal/reporttail/blockvalue_test.go` L67: remove the "prose after the closing fence" row from `TestBlockValueMissesWithoutABlock`, and add a hit case elsewhere in that file. A fenced block with `verdict: pass` followed by `trailing prose` returns `("pass", true)`.
   - `StripTail`'s existing case at reporttail_test.go L641 stays unchanged. It proves the body is still kept whole.
   - Done when: `go test ./internal/reporttail/...` is green.

4. **New unit tests in `internal/reporttail/reporttail_test.go`**, table-style and `t.Parallel`:
   - `ParseWithReason` on a done block followed by `git diff --stat`-style lines returns ok, status done, reason "".
   - The same holds for a halted block followed by text (status halted, `HaltedAt` kept), and for text separated by a blank line and with none.
   - A block with a rejectable body followed by text still returns its own reject reason (`tail: line N has no ':'`).
   - `StripTail` of block plus text is byte-identical to its input.
   - Mutation check: restore the old reason and confirm the new ok-case fails.

5. **Chain decision test in `internal/relevo/chain_test.go`**, next to `TestChainBuilderCloseSeedsReviewer` (L239). Reuse `chainRuntime`, `startedChain`, `chainBuilderClose` and `chainDoneBody`; no harness and no network, so it is CI-safe.
   - Name it something like `TestChainBuilderCloseWithTextAfterTheBlockAdvances`.
   - Close the builder with `chainDoneBody()` plus a trailing ` file.go | 2 +-\n 1 file changed` block.
   - Assert the chain row is not halted, `Step == reviewing` and `AwaitingMember == reviewer`.
   - Mutation check: with `FindRelevoBlock` restored to return the reason, it fails with "builder halted on plan 1: note: tail: prose after closing fence".

6. **Round-close test in `internal/relevo/reconcile_test.go`**, a subtest next to "rejected block notes why on the entry" (~L1002). It writes `"```relevo\nstatus: done\n```\ntrailing text\n"` to the report, reconciles, and reads the log. Assert `Outcome == done`, `Note` empty, and the saved report body still contains `trailing text`. Reuse `sentBinding` and the same pattern as the neighbours.

7. **Reader coverage**
   - Add one reader-close case, with `chainReaderClose` or `reader_deliverable` tests, only if an existing helper makes it a few lines. Its body is a block carrying a declared outcome followed by prose, and the chain reads it rather than halting. It is optional.
   - Report that `BlockValue` now reads a block followed by prose, as `consult/verify.go` already did.

8. **Docs and comments check**
   - Grep docs, the guide and agent prompts for any statement that the block must be the last thing, or that trailing prose voids it: `rg -n "prose after|last thing|nothing after" internal/mastermind docs README.md internal/agents`. docs/design.md L205 tells builders to put the block last; leave that instruction, since it is guidance and not a rule the reader enforces.
   - New or edited comments say why and carry no issue numbers or history.
   - Keep `reporttail.go` under 600 lines (it is 513 now) and every function at or under 70 lines.

9. **Gate and commit**
   - Focused loop: `go test ./internal/reporttail/... ./internal/consult/... && go test ./internal/relevo/ -run 'Chain|Reconcile|Reader' -count=1`. Fix every error before the next run.
   - Gate once at the end: `make check-affected`. The whole suite, `-race` and coverage run in CI after the round.
   - If coverage in `internal/reporttail` moved, do not edit the baseline; state it in the report.
   - Commit as new commits only. Never amend or rebase, never push, never use `gh`.

10. **Save this plan** to `docs/plans/2026-10-24-report-tail-prose.md` as the last step, committed with the code.

## Deleted

1. The `reason` return value of `reporttail.FindRelevoBlock`.
2. The "prose after closing fence" rejection and its string.
3. The `reason != ""` branches in `BlockValue` and `HasFencelessBlock`, and the `reason == "tail: prose..."` string match in `StripTail`.

## Report must include

- Each caller of the old reason and what it does now: `ParseWithReason`, `Parse`, `StripTail`, `BlockValue`, `HasFencelessBlock`, `consult/verify.go`, and the chain, reconcile and reader paths.
- The reader widening: `BlockValue` now reads a fenced block that is followed by prose.
- The names of the tests that fail under the mutation (the restored reason), and the `make check-affected` result.
- `git diff --stat` against the scope above: reporttail.go, blockvalue.go, consult/verify.go, the four test files and the plan doc.
- Anything that contradicted this plan, as a halt.
