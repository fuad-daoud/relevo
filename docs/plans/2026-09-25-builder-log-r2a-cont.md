# builder.log round 2a, continued: the UI current-round rule, then finish 2a

Date: 2026-09-25. Binding blog2, branch relevo/blog2.
Plan being continued: docs/plans/2026-09-25-builder-log-r2a.md. Your round 1 halted at step
5, correctly. Steps 1-4 are done, and the step 5 edits to show, serve, escape and ui/fetch.go
are **uncommitted in the worktree**. Keep them and continue from there. Do not reset.

**Stop rather than improvise.** Any other existing test failing means halt and report.

## Decision on the halt (replaces the UI bullet of §4.7 for the current round only)

The current-round branch of `fetchTerminal` (ui/fetch.go, headless branch) reads, in order:

1. **The endpoint's own log.** Use it when `b.Builder.LogPath != ""` **and**
   `b.Builder.LogPath != rt.Store.BuilderStreamPath(name, b.Builder.StreamRound)`.
   - Read `b.Builder.LogPath` exactly as today (`logTab(key, name, rt.Store.ReadFile, b.Builder.LogPath)`).
   - If that read fails, the prose is today's `"log not written yet: " + b.Builder.LogPath`.
   - This is today's behaviour. In 2a it always applies to a live process. In 2b `LogPath`
     becomes the stream path, so this branch stops applying.
2. **Otherwise, RoundTranscript.** Use
   `relevo.RoundTranscript(rt.Store, name, r, b.Builder, <live read adapter>)`, with
   `r = b.Builder.StreamRound` when non-zero, else `round`. This is the path your edit
   already has.
   - The prose on a miss is `"log not written yet: " + rt.Store.BuilderStreamPath(name, r)`.
   - The "no round has run yet" prose is unchanged: when LogPath is "" and StreamRound
     is 0.

The past-round branch stays as you implemented it (RoundTranscript). The three tests you
named must now pass **unchanged**:

- `TestFetchTerminalHeadlessReadsTheLogNotThePane`
- `TestFetchTerminalHeadlessReturnsWholeLog`
- `TestFetchTerminalHeadlessIdleAndMissingLog`

N10 must use a binding whose `LogPath` is "" (between rounds, `StreamRound` = the round) or
equal to the stream path, and whose round has a stream and no log. Then the tab renders the
stream, with `logName` `NNN-builder.jsonl (rendered)`.

Add one more assertion to N10: with `LogPath` set to a readable non-stream file, the tab
reads that file even when a stream exists. This pins rule 1.

## Continue

Carry out the rest of the original plan exactly:

- step 5's remaining tests: N8, N10 (as amended above) and N11;
- step 6, the #452 fix, with N9;
- step 7, the full check and the §7.4 mutations, plus one more:

  | # | Break | Must fail |
  |---|---|---|
  | M10 | ui/fetch.go: drop rule 1, so the current round always uses RoundTranscript | `TestFetchTerminalHeadlessReadsTheLogNotThePane` and the N10 rule-1 assertion |

- step 8: save **both** plans verbatim. Copy `docs/plans/2026-09-25-builder-log-r2a.md`
  from `/home/fuad/projects/relevo/docs/plans/` and this file as
  `docs/plans/2026-09-25-builder-log-r2a-cont.md`.
- step 9: one commit, push with `-u`, **no PR**.

Report the per-step status from round 1 plus this round, and every §7.4 result (M1-M10).
