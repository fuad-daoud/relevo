# The client's mirror of a remote round's log is a round_file row, not a file

Date: 2026-09-25. Base: origin/main (d9b9c42 or later). One round, one PR.
Context: builder-log rounds 1-2 (#469, #478) stopped the local builder from writing
`NNN-builder.log`. The one place the client still writes that file is its mirror of a
**remote** round's log (#442). This round moves that mirror into `round_file`.

**Stop rather than improvise.** If a step contradicts the code, or an existing test outside
§7.3 fails, halt and report it. Do not bend a test.

## 1. Overview

In internal/relevo/remote.go today:

- **`mirrorLog`** (~634-684) runs on every tick of a running remote round (called from
  `observeRemote`).
  - It takes the local file's size as the offset.
  - It fetches the server's log from that offset (`RoundFileFrom(…, "log", local)`).
  - It appends to `Store.BuilderLogPath(name, round)`, or rewrites it with
    `writeTempAndRename` when the server shrank or ignored `from`.
- **`catchUp`** (~1212-1227) fetches the whole log at round close and overwrites the same
  file.

Only relevo reads that file (the UI terminal tab and `relevo show --transcript`), always
through `Store.ReadFile`, which falls back to `round_file`.

After this round:

- **A new remote round's mirror is a `round_file` row** named `NNN-builder.log`, written
  with `Tx.PutRoundFile`.
  - The offset is the row's current length.
  - An append writes the old bytes plus the new ones.
  - A rewrite writes the whole body.
- **The legacy rule from #478 decides**, via `legacyLog(rt, name, round)` in headless.go: a
  round that already has `NNN-builder.log` **on disk** keeps the file behaviour, unchanged.
  That covers a remote round in flight across the upgrade.
- **The stream fetch in catchUp stays a file.** The seal and usage code read it as they read
  every round's stream.

Logs are small (12 KB typical, 74 KB max, measured), so rewriting the row every tick is
cheap.

## 2. Files

```
internal/relevo/remote.go       mirrorLog takes tx and writes a row (or the legacy file); catchUp's log write likewise
internal/relevo/remote_test.go  N1-N5; fenced ports
docs/plans/2026-09-25-remote-log-mirror-row.md   this plan, verbatim
```

No store, JSON, golden or `BindingFormat` changes.

## 3. Contracts

### 3.1 `mirrorLog(ctx, rt, tx *store.Tx, server, name string, round int)`

- Add the `tx` parameter. Update its caller in `observeRemote`, which has `tx`.
- `path := rt.Store.BuilderLogPath(name, round)`.

**`legacyLog(rt, name, round)` is true:** keep today's body exactly (file size as the
offset, append or `writeTempAndRename`).

**Otherwise (the row path):**

- `local`:
  - `cur, err := rt.Store.ReadFile(path)`;
  - not-exist gives `cur = nil`, `local = 0`;
  - any other error: `slog.Warn` and return;
  - otherwise `local = int64(len(cur))`.
- Then `RoundFileFrom(…, "log", local)` as today, with the same three cases:
  - **Honored, `From == local`, `Size >= local`:** read the body, then
    `tx.PutRoundFile(name, round, path, append(cur, body...))`.
  - **Honored, `Size < local`:** refetch from 0, then `PutRoundFile(… , full body)`.
  - **Default (an old server that ignores `from`):** `PutRoundFile(…, full body)`.
- Every error is `slog.Warn("mirror builder log failed", …)` and a return, exactly as
  today. It never fails the tick.
- Bound each read with `io.ReadAll(io.LimitReader(rc, maxMirrorBytes+1))`.
  - `const maxMirrorBytes = 16 << 20`.
  - Over the limit: warn and write nothing. A log is far below this; the cap only guards
    memory.

### 3.2 catchUp's log write (~1221-1227)

- If `legacyLog(rt, name, n)`: today's `writeTempAndRename`, unchanged.
- Otherwise:
  - read the body bounded by `maxMirrorBytes` as above;
  - `tx.PutRoundFile(name, n, rt.Store.BuilderLogPath(name, n), body)`;
  - on error, `slog.Warn("write log failed", …)` and `return b, nil`, as today.
- This overwrite also repairs a mirror that missed its final lines.

### 3.3 Nothing else

Readers already use `Store.ReadFile` / `RoundTranscript`, which find the row. Do not change:

- the stream fetch;
- the report and diff handling;
- `mirrorDriftOnce`;
- the UI;
- `show`.

## 4. Steps

1. §3.1 (mirrorLog plus its caller), with N1-N3.
2. §3.2 (catchUp), with N4 and N5.
3. Full check:
   - `make check`. If a hook blocks it, run its steps directly;
   - paste `gofmt -l $(git ls-files '*.go')`'s empty output;
   - then `make e2e`.
4. Run the §6 mutations, one at a time, restoring after each.
5. Save this plan verbatim at `docs/plans/2026-09-25-remote-log-mirror-row.md`.
6. Commit and PR.
   - One commit: `fix(remote): the client's mirror of a remote round's log is a round_file
     row, not a file`.
   - Push with `git push -u origin <branch>`. Open a PR against main with the §6 results.

## 5. Tests

**Reads first:**

- remote.go 620-700 and 1200-1250;
- remote_test.go around `TestReconcileRemoteRunningMirrorsLog` (~2351), `TestMirrorLogAppends`
  (~2395), `TestMirrorLogOldServerReplaces` (~2433) and `TestMirrorLogShrankRefetches`
  (~2468), plus the catchUp tests that assert the local log (grep `BuilderLogPath` in
  remote_test.go);
- headless.go `legacyLog`.

Line numbers are from origin/main d9b9c42.

Focused run: `go test ./internal/relevo/ -run 'Mirror|CatchUp|Remote' -count=1`.

### New

- **N1 `TestMirrorLogWritesARow`:**
  - no local file; the fake server serves "abc" with `from` honored;
  - after mirrorLog: `Store.ReadFile(path) == "abc"`, and there is no file on disk at the
    path.
  - A second tick, with the server now "abcdef", requests `from=3`. It is served "def",
    and the row becomes "abcdef".
- **N2 `TestMirrorLogRowRewritesWhenServerShrank`:**
  - with row "abcdef" and a server of size 3 that serves "xyz" from 0, the row becomes
    "xyz".
  - With an old server (`from` not honored), the row becomes the full body.
- **N3 `TestMirrorLogKeepsALegacyFile`:**
  - a local file exists on disk at the path;
  - mirrorLog appends to the **file**, as today;
  - no row is written, i.e. the round_file listing has no `NNN-builder.log` row. Use
    `Store.RoundFiles` or the DB helper the other tests use.
- **N4 `TestCatchUpWritesTheLogAsARow`:** at catchUp with no local file, the fetched log is
  a row, with no file on disk, and it overwrites a stale mirror row.
- **N5 `TestCatchUpKeepsALegacyLogFile`:** with a local file present, catchUp overwrites
  the file, as today.

### Fenced: the only existing tests you may change

The tests named above, and any catchUp or remote test that reads the local log with
`os.ReadFile(BuilderLogPath(…))` **when no local file was seeded first**. They may read it
through `rt.Store.ReadFile(…)` instead and assert that it is not on disk. Their expected
content must not change. Report each change.

Tests that seed a local file first go down the legacy path and must pass **unchanged**.
Everything else (serve, UI, show, e2e) must pass unchanged. If one fails, halt.

A cmd/relevo test must not spawn a harness or reach the network. This round adds none.

## 6. Mutation checks (run each, report pass/fail, restore)

| # | Break | Must fail |
|---|---|---|
| M1 | mirrorLog: always the file path (ignore the row path) | N1 |
| M2 | mirrorLog: ignore legacyLog (always the row) | N3 |
| M3 | row path: offset always 0 | N1 (second tick requests from=3) |
| M4 | row path append: write only the new bytes, not old+new | N1 |
| M5 | catchUp: always the file | N4 |

If a mutation does not make its named test fail, report it. Do not strengthen tests
beyond this plan.

## 7. Scope check

- `git diff --stat` shows only remote.go, remote_test.go and this plan.
