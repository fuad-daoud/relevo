# Reader output: every reader saves its block-carrying message (corrected round)

Base: your worktree HEAD (a1c6b084), which includes bc4de8cc (#809). Your round-1 halt was right: #809 already made a CHAIN reviewer/security member save its block-carrying message (`transcript.LastWithBlock`, block stripped, a runner-written file replaced). This round extends the selection -- not the replace -- to every other reader. The round-1 plan is withdrawn; this file replaces it. New commits only.

## The remaining bug

A plain reader (not a chain member) that writes its findings with a relevo block, touches the marker, then writes a recap ("Done.") still has the recap saved as `<label>.md`: `writeReaderSummary` uses `transcript.FinalText` for every reader that is not a chain reviewer/security member. Its status then reads "unstructured". Seen live today on plain security and planner readers.

## The change (internal/relevo/summary.go, writeReaderSummary)

For a reader that is NOT a chain reader part, when relevo writes the output itself (no file at the path):
- `text` = `transcript.LastWithBlock(kind, stream)` when non-empty, else `transcript.FinalText(kind, stream)` exactly as today.
- Keep the block in the text. Do NOT strip it here: queueReport parses the saved body's tail and strips the trailing block later, so the status parse still works.

Unchanged:
- The chain-reader path from #809 (strip and replace).
- A runner-written regular file for a non-chain reader is still left alone.
- Writers.
- `send.go` readerPrompt, byte for byte.

Keep the function within 70 lines; split a small helper (e.g. `readerOutputText(kind, stream, chainReader)`) if needed. Update the doc comment so it says what the code does now, with no history and no issue numbers.

## Tests (internal/relevo/summary_test.go, beside #809's test)

- `TestWriteReaderSummarySavesAPlainReadersBlockMessageOverARecap`: a plain reader (not a chain member), a stream of block message then "Done.", and no file at the path. The saved file is the block message, and it still carries the relevo block.
- `TestWriteReaderSummarySavesTheFinalTextWithoutABlock`: a plain reader with two block-free messages. The last one is saved, as today.
- `TestWriteReaderSummaryLeavesAPlainReadersOwnFileAlone`: a plain reader whose regular file is already at the path. The file is byte-identical afterwards.
- `TestReaderCloseOfARecapRoundIsNotUnstructured` (in reader_close_test.go, through the close): the same incident shape gives a report entry whose Outcome is the reader's own status, not "unstructured".
- `TestWriteReaderSummaryPrefersTheChainBlockMessage` (#809) must pass unchanged.

## Mutation (run it, report it, restore it)

Make the non-chain path use FinalText again. `TestWriteReaderSummarySavesAPlainReadersBlockMessageOverARecap` and `TestReaderCloseOfARecapRoundIsNotUnstructured` must both fail.

## Then

- `make check` and `make e2e` green.
- If `TestChainE2E` asserts the old recap behaviour for a reader this round now changes, report it. Do not soften the assertion: halt instead.
- Save this plan as `docs/plans/2026-10-01-reader-output-block-message.md` and commit it with the code.

## Halt if

- Making it green needs a change to the chain-reader path, to `queueReport`, or to `StripTail`.
- Any test not named here has to change.
