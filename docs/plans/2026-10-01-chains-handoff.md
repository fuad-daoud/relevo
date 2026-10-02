# Chains: handoff (2026-10-01, session "chaining-2", MasterMind claude-4)

Untracked on purpose. Read this first, then memory `chains-2026-09-30.md`.

## State: slices 1-3 DONE, merged, installed, deployed

- Laptop, zen and contabo all run v0.15.0-20-g5fb13dc6 (relevo-serve redeployed on both servers).
- Merged today in this session:
  - #785 slice 2 (a chain places its builder on a server);
  - #786 (#782): OOM detection reads the user journal (`--collect` scopes made the systemctl probe always say success), an OOM no longer gates the candidate, the seed cap is `usage` and an open member round is `conflict`;
  - #787: the security/fixes seeds name the chain's whole diff (`NNN-chain-diff.patch`, base -> builder closed tree) plus every plan copy;
  - #788: the turso UX fixes (resume refuses an open member round before writing, halt reason once, no step on a done chain, `manual round N running`, inputs swept at the end, resume re-sends the stopped round's own prompt, `bind --server --gate` carries the gate);
  - #790 slice 3: `relevo chain --server <s>` (server drives everything; client pulls rounds in order, absorbs commits, one delivery).
- Live test passed: `live-s3` (scratch repo `~/.cache/chain-live`) ran 2 plans on zen with `--server zen`, then was pulled back: the trace, the branch commits and the single delivery were all correct.

## Open / next

1. **#754 sub-chains** (designed on the issue): a parent's `forked` step spawns child chains, which join afterwards. Not started; plan it with the `planner` (opus) actor.
2. **Small, observed in the live run:** a reviewer that recaps after its verdict leaves a pulled findings file holding only the recap ("Done."). The verdict is still parsed from the stream. Consider making `findings.md` hold the message that carried the verdict.
3. **Reported, not fixed:**
   - a resume can still halt on the `ErrReportPending` path;
   - `--regate` has no wire field on a plain remote bind;
   - the peak is not stored on `OOMRequeue`.
4. **Housekeeping:** today's bindings are DONE and need `relevo unbind --done`. Remove `~/.cache/chain-live` when you no longer need it.

## How this session worked

Same method as the previous handoff:
- lite-planner, or the `planner` actor for heavy plans;
- builders on zen;
- verify in `~/.cache/relevo-chains/clone`: static checks locally, tests with `dev run` on contabo, plus one mutation of my own per round;
- merge on green.

Parallel rounds ran on separate zen builders and were merged in the clone. Watch for semantic conflicts after a merge (the build broke on a signature change). contabo is shared with other sessions: a 600 s test timeout there means load, not a hang.
