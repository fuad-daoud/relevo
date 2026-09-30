# Round detail: a live headless round's transcript tab shows the rendered log, not the raw stream

Date: 2026-09-25. Worktree: this binding's, on origin/main. This is one round. Commit once at the end. Do not push.

**Stop rather than improvise.** If the code differs from what is quoted here, halt and report. Change only:
- `internal/ui/fetch.go`
- its test file: `internal/ui/fetch_test.go`, or whichever file tests `fetchTerminal` (find it with
  `grep -ln fetchTerminal internal/ui/*_test.go`)
- `docs/plans/`

## 1. The bug

In the cockpit's round detail, the transcript tab of a **live** headless round (the binding's current round, still
running) shows raw JSON lines:

```
headless · 003-builder.jsonl · 256 lines · following
{"event":"step_update","step_update":{"conversation_id":…
```

A finished round of the same kind shows the rendered transcript (`● edit(…)`, `⎿ ok: …`) from `NNN-builder.log`.

**The cause:** `fetchTerminal` (`internal/ui/fetch.go`, the headless branch).
- **Past rounds** read `rt.Store.BuilderLogPath(name, round)`, the rendered `.log`.
- **The current round** reads `b.Builder.LogPath`, which names the raw stream `NNN-builder.jsonl`. It falls back to
  `BuilderLogPath(name, b.Builder.StreamRound)` only when `LogPath` is empty.

The daemon renders the stream into the `.log` on every tick (`relevo.drainStream`, `internal/relevo/headless.go:359-395`), so
the rendered log exists and grows while the round runs. `relevo show oc-ui5 --round 3 --transcript` prints it for a live
round today. `rt.Store.ReadFile` reads a sealed log from the database when the file is gone (`internal/store/seal.go:32`), so
reading it through `logTab` is correct in both cases.

## 2. The fix (in `fetchTerminal`'s current-round path)

1. **Pick the round:** `r := b.Builder.StreamRound`, or `round` when `StreamRound` is 0.
2. **Try the rendered log first:** `logTab(key, name, rt.Store.ReadFile, rt.Store.BuilderLogPath(name, r))`. If it
   succeeds, set `msg.round = round` and return it.
3. **Otherwise**, keep today's behaviour exactly:
   - `b.Builder.LogPath` (the raw stream);
   - then the `"headless builder; no round has run yet…"` and `"log not written yet: …"` prose.

   A round in its first seconds, before the daemon's first drain, still shows the raw stream. That is acceptable.

Update the function's doc comment to say that the current round prefers the rendered log.

## 3. Tests

1. `TestFetchTerminalLiveRoundPrefersRenderedLog`:
   - **Setup:** a store with a headless binding at round 3, `Builder.LogPath` = the round's `.jsonl` path and
     `Builder.StreamRound = 3`. Write both files:
     - the `.jsonl` holds `{"event":"x"}`;
     - the `.log` holds `● edit(a.go)\n  ⎿ ok`.
   - **Assert:** run the returned command. The `tabMsg`'s `content.body` contains `● edit(a.go)`, and its `logName` is
     `003-builder.log`.
   - Build the binding and the source the way the existing `fetchTerminal` tests do: read them first, and copy their setup.
2. `TestFetchTerminalLiveRoundFallsBackToStream`:
   - The same setup without the `.log`: the body is the `.jsonl` line, as today.
3. **Mutation check (report the result):** swap the order of the two reads, confirm test 1 fails, then restore.

These are pure tests: no harness and no network.

## 4. Steps

1. The fix and the tests.
   - **Done when:**
     `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run 'FetchTerminal|Terminal' -count=1` passes.
2. The full check:
   - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1 && gofmt -l internal/ cmd/ && go vet ./internal/ui/`
   - Everything green, `gofmt -l` empty.
3. Copy this plan, `/home/fuad/projects/relevo/docs/plans/2026-09-25-live-transcript-rendered.md`, into `docs/plans/`, and
   commit:
   - `fix(cockpit): a live headless round's transcript tab shows the rendered log`

## 5. Report

Include:
- the tests;
- the mutation check's result;
- `git diff --stat origin/main HEAD`.
