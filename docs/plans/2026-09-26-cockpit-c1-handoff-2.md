# Cockpit C1 handoff 2 (2026-09-26, end of session 6)

Read this first. It continues `docs/plans/2026-09-26-cockpit-c1-handoff.md`. That file's rules still hold, and so do
handoff-2 §1 (how this user works: canvas first, real data only, no captions, no dollars, 3-cell margins, every table
lists everything) and §4 (operations).

## 1. What landed

| PR | what |
|---|---|
| #511 | stale builder token: gate checks use the running triple; the next round re-picks when the candidate was edited or deleted |
| #524 | `:settings`: 17 settings in 6 groups, group forms, reset checked before the confirm, and the scope, serve.scope and classify forms |
| #520 | `:audit`: revisions by day, changes by name, `enter` for all changes, `R` rollback behind a red confirm, checked like an edit (`CheckDoc`) |
| #536 | #530: `db.Open` takes no write lock when the schema is current (this ended the `begin migration 001_initial.sql: busy` failures) |
| #553 | #540: limit and denial scans read only the current builder's output (`currentBuilderTail` from `StreamStart`); **merging on green** |
| #554 | `:settings` › notify.webhooks: list, add, edit and delete; **merging on green** |

- **`scan_patterns` gets no editor** (the user's decision); its notice points at `relevo config set policy`.
- **Canvas** (https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA): the rows "Settings in D2" (y=14030) and "Audit in D2"
  (y=15100) hold the approved boards. The generators live in the session scratchpad and are not kept.

## 2. State the user set

- **anthropic is gated until Oct 26 14:04, locally and on contabo's serve daemon** (`relevo gate sonnet --for 720h`;
  `relevo gate --serve …` on contabo). The user said: no claude/sonnet builders.
  - The gate is per provider, so `haiku` (the researcher's only candidate) is gated too.
  - The reviewer's list (sonnet, gpt-5.6-terra) has nothing open.
  - Tell the user if a `--verify` round or `relevo ask` fails to pick.
- **Contabo's cline-pass gate was cleared by the user.** It was the #540 misattribution.

## 3. Open items

1. **#535 step 2:** move `observeRemote`'s network I/O (`GetBinding`, log and drift mirroring, catch-up downloads,
   `Ack`) out of the state lock.
   - Step 1 landed independently as #538 (`SyncRemoteUnlessDaemon`), from the other planner.
   - The daemon still holds the machine-wide state lock across HTTP calls.
2. **Contabo redeploy:** it runs `v0.13.0-48`, with no C1 views and none of today's fixes. Use the deploying-to-contabo
   skill once the other planners are idle. After it, the user wants to turn `sonnet` off in `:actors` there.
3. **The small C1 gaps from handoff 1 §3**, still open:
   - the footer cannot grey out a key;
   - other views keep the startup config until restart;
   - no view lists stale gates;
   - agent "your edit + newer copy" is not shown.
4. **The rest of the roadmap:** A2 round 4, A3b, A4, A5, C3.

## 4. Operational lessons from this session

- **Remote bindings (contabo):**
  - **Never amend on a remote binding.** relevo only absorbs a fast-forward ("ref update is not a fast-forward"), and
    `send` refuses a rebased branch ("moved past your copy"). Remote rounds make a **new** commit; the squash-merge
    folds them.
  - To rebase between rounds, bind a fresh remote binding from the rebased commit (`--base <sha>`).
- **The server's gofmt and comment checks can be vacuous.** On contabo, `git ls-files` fails in some worktrees, so the
  checks see nothing. A remote builder once passed `check-comments.sh` with two violations. Always rerun gofmt,
  `check-comments.sh`, `check-filesize.sh` and `golangci-lint` locally.
- **Contabo's `/tmp` is a 3.9 GB tmpfs.** Run the race suite with
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`. Without it, SQLite reports `disk I/O error`, or the
  go-build cache loses files mid-run.
- **Other planners are active on `main`:** the cleanup P3 rounds, CI speed, the statusline.
  - Before a plan that fixes shared machinery, check `gh pr list` and recent `main`. #535 step 1 was duplicated by
    #538.
  - Before merging, check the PR's files against the commits merged since its CI ran (`comm` on the name lists, plus
    `git merge-tree`).
- **Merges need the user's explicit OK.** The classifier blocks `gh pr merge` otherwise, and it blocked killing
  another planner's process, too.
- **`:settings` opens after the first status arrives.** The first screen of `relevo ui :settings` is `:fleet`
  ("loading…"). Wait for `SETTING` before sending keys, or keys land on the fleet.
