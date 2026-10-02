# Plan: `relevo wait` survives a transient db error, and relevo's db stops writing temp files to `/tmp`

Base is origin/main. This is one builder round, committed as new commits.

## Findings

**Which path `relevo wait` reads through.**
- `cmdWait` calls `relevo.Wait` (`internal/relevo/wait.go:161`). Every poll runs `rt.Store.Load` and `rt.Store.ReadLog`, and a failure returns straight away (`return n, WaitResult{}, err`). `cmd/relevo/wait.go:~103` turns that into `internal: …`, exit 1.
- `wait` is not a peek verb. `routeForArgs` (`cmd/relevo/machinedb.go`) gives it `routeOwner`, so the store dials the daemon's owner socket and does not open `relevo.db` itself.
- The `pwrite` therefore happened inside the daemon process. The error crossed the wire as a `wire.Error` and was printed as `db: record get /"f861": turso: error: I/O error (pwrite): quota exceeded`.

**Why a read can pwrite.**
- The turso native library is `~/.local/state/relevo/turso-go/*/libturso_sync_sdk_kit.so`. Its strings show:
  - it reads `TURSO_TMPDIR`, `SQLITE_TMPDIR` and `TMPDIR`;
  - it implements `PRAGMA temp_store` (`0|1|2|DEFAULT|FILE|MEMORY`);
  - it has ephemeral-table and sorter file I/O, hash-join and hash-table spill ("spilling to disk", "too many temporary files exist") and `cache_spill`;
  - `quota exceeded` is the plain `io::Error` text for EDQUOT, reached through `core/io/unix.rs` `pwrite`.
- So a read query that needs an ephemeral table writes a temp file under `$TMPDIR`. The daemon's `/tmp` is a tmpfs with `usrquota`, and other tools fill it, which gives EDQUOT.
- Not verified: which statement or temp file it was, and whether the default `temp_store` is FILE. Step 1 pins that.

**What the driver gives us for classifying the error.**
- `tursogo`'s `statusToError` (`bindings_db.go:~240`) has typed sentinels for busy, readonly, constraint and database-full.
- An I/O failure arrives as the untyped `ErrTursoGeneric` with the message `I/O error (…): …`. No typed I/O kind exists in the driver.
- `internal/db/engine_turso.go` `engineCode` maps only busy, constraint and readonly. The owner server's `codeOf` hook (`internal/db/wire/owner/owner.go:99`) therefore sends no SQLite code for this error. The client sees an error with a message and no kind.

**Seam for the fake store.** `relevo.Wait` takes `rt Runtime` with a concrete `*store.Store`, so a fake store needs a narrow interface at the poll loop.

## Behaviour

1. **Classification.**
   - A new `db.IsTransient(err) bool` is true for busy (`ErrBusy`, or code 5) and for SQLite I/O error (primary code 10, `sqliteIOErr`).
   - It is false for not-found, constraint, corrupt, not-a-db, misuse and `ErrNewerSchema`. A refusal from the owner (`errOwnerUnavailable`, a dial failure) also stays non-transient.
   - To make I/O a kind, extend `engineCode` in `engine_turso.go`: an `ErrTursoGeneric` whose text starts with `I/O error` maps to code 10. This is the single, documented string match, at the engine seam. Add the matching constant beside `tursoBusy`.
   - Check `engine_modernc.go` and `errCode` (`db.go:421`) so a modernc error carrying code 10 also classifies. Check that `wire.Error` round-trips the code, so the client sees the same kind over the socket.
2. **Wait retry.**
   - The poll loop's two store reads (`Load` and `ReadLog` per name) go through a helper. If the helper gets an `IsTransient` error it:
     - prints one stderr line, "relevo: db read failed (<err>); retrying", only on the first failure of a streak;
     - sleeps with backoff (250 ms doubling to 2 s, capped, abortable by `ctx.Done()`);
     - retries the same read.
   - The streak is consecutive failures, bounded at 60 s measured from the first failure of the streak. A success resets the streak, and a later streak logs once again.
   - When the 60 s is spent, or the error is not transient, the original error is returned unchanged, so exit 1 and the message are as today.
   - Only the poll loop is covered. The up-front `Load`/`ReadLog` of names before the loop keeps today's behaviour, because a bad name or db must fail fast.
   - The delivery call `PullPendingThrough` already has its own busy retry, so leave it.
   - Clock and sleep come from `rt.Now` and an injectable sleep, so the test needs no real waiting. Wait's overall `--timeout` still applies. A retry streak does not extend it, and the existing timeout check runs after the helper gives up on that pass.
3. **Chain wait.** `WaitChain` and `chainServerWait` share the daemon read path and the same failure. The decision names only the wait poll loop, so do not change them. Say in the report whether `WaitChain` has the same hazard.
4. **Temp files out of `/tmp`.**
   - In `prepareEngine` / `extractAndLoad` (`engine_turso.go:~200-236`), set `TURSO_TMPDIR` and `SQLITE_TMPDIR` to `<dir>/tmp` (the state dir beside the database), created `0700`. Do this only when not already set by the user, and before the library loads, since the library may cache the value at load time. Do not set `TMPDIR`, which would redirect every other `os.TempDir` user in the process.
   - Add `PRAGMA temp_store = MEMORY` to `openPragmas` for both modes. A relevo db is small, so the memory cost is negligible, and it removes the temp file outright.
   - Step 1 decides which of the two is needed. If the pragma alone eliminates the files, keep the env var as a fallback only if the library needs it for hash-join spill, which the pragma may not cover. Do not ship either one without the evidence.
   - The modernc build (`engine_modernc.go`) needs no change.

## Steps

1. **Pin the cause.**
   - Deliverable: a short finding in the report, naming which statement produced a temp file, with no repo change.
   - Method: in a scratch dir, open a throwaway db with `db.OpenRaw` or the turso driver. Run a query shaped like the failing one with `ORDER BY`/`DISTINCT`/`GROUP BY` over a few thousand rows. Run it under `strace -f -e trace=openat,pwrite64 -o /tmp/x` with `TMPDIR` pointed at a small scratch tmpfs, and check whether files appear there. Repeat with `PRAGMA temp_store=MEMORY`, and with `TURSO_TMPDIR=<other dir>`.
   - Look up the failing `record get` query and read `internal/db/record.go` to see how it is shaped.
   - Done when the builder can say whether the pragma, the env var, or both remove the `/tmp` writes. If neither does, halt and report: the premise is wrong and the fix needs a redesign.
2. **Add the I/O kind.**
   - Deliverable: the `engineCode` mapping and the `sqliteIOErr` constant in `internal/db/engine_turso.go`, plus a `db.IsTransient` in `internal/db/errors.go` or `db.go`.
   - Done when a unit test shows an engine generic I/O error and a wire-rebuilt error with code 10 both classify as transient, and constraint, not-found and plain-generic errors do not. Run it with `go test ./internal/db -run Transient`.
3. **Move the temp files.**
   - Deliverable: the env setup and the pragma from Behaviour 4, in `engine_turso.go`, with the same cleanup rules as the cache dir.
   - Done when `go test ./internal/db` passes on the default build, and also with `-tags modernc` (`go vet -tags modernc ./internal/db`, and the modernc tests under `internal/db`).
4. **The wait retry seam.**
   - Deliverable: a small interface in `internal/relevo/wait.go` covering `Load` and `ReadLog`, satisfied by `*store.Store`, and a retry helper. Keep `wait.go` under 600 lines; if it would exceed that, put the helper in a new `internal/relevo/wait_retry.go`. Keep every function at 70 lines or fewer.
   - Wire the poll loop to the helper, and leave the signature of `Wait` and the CLI untouched.
   - Done when `go build ./...` passes and the existing wait tests are green.
5. **Tests (pure function, no network, no harness).**
   - Fake store: `Load` and `ReadLog` fail N times with a transient error, then return a closed round. Assert `Wait` returns the round's outcome and the one-line notice is printed exactly once.
   - Persistent failure: the fake always fails transiently. Assert `Wait` returns the error after the bounded time, using a fake clock, with no real sleep.
   - Non-transient failure on the first poll: assert `Wait` returns immediately with no retry.
   - Context cancelled mid-backoff: assert it returns `ctx.Err()`.
   - These tests live in `internal/relevo`. A `cmd/relevo` test must not run a subcommand that spawns a harness or reaches the network, so none is added there.
   - Focused run: `go test ./internal/relevo -run 'Wait' -count=1` and `go test ./internal/db -count=1`.
6. **Mutation check (report it).**
   - Mutation A: make `IsTransient` return false for code 10. The "fails N reads then succeeds" test must fail.
   - Mutation B: remove the 60 s cap, or make the retry unbounded. The persistent-failure test must fail.
   - Mutation C: delete the `temp_store` pragma and the `TURSO_TMPDIR` setup. The step 3 test must fail, so that test must assert the connection reports MEMORY and `TURSO_TMPDIR` is set to the state-dir path.
7. **Full check.** Run `make check` once at the end, fix everything it reports, then run `gofmt -l` over changed files. Do not add an exclusion to `.golangci.yml`, `scripts/check-comments.sh` or `scripts/check-filesize.sh`. If package coverage moves more than a point, say so; do not lower `testdata/coverage-baseline.txt`.
8. **Save this plan** to `docs/plans/2026-10-02-wait-transient-db.md` as the last step, in the same commit series as the code.

## Constraints

- Comments say why and carry no history: no issue numbers, no "round N", no "used to".
- No amend and no rebase of any commit already on a remote branch. Add new commits only.
- A test that sets env (`TURSO_TMPDIR`) uses `t.Setenv`. In `internal/db`, the engine prepare runs once per process (`enginePrepareOnce`), so the env test must call the extracted helper directly rather than relying on a second `prepareEngine`.

## Halt conditions

- Step 1 shows the temp file is not what produced the `pwrite`, for example it was the WAL on `/home`. Then drop step 3, keep steps 2 and 4 to 7, and say so.
- `wire.Error` cannot carry a code for this error without a protocol change. Report it; do not bump the wire version silently.
- The turso library ignores both the env var and the pragma for the failing file. Report with the strace evidence.

## What the report must include

- The step 1 evidence: which file or statement, and what the pragma and the env var each changed.
- The final `engineCode` and `IsTransient` rules, and the one place a message string is matched.
- The mutation results (A, B, C), each with the failing test name.
- `make check` output summary, and `git diff --stat` against this plan's scope: `internal/db/engine_turso.go`, `internal/db/errors.go` or `db.go`, `internal/relevo/wait.go` (or `wait_retry.go`), their tests, and the plan doc.
- Whether `WaitChain` and `chainServerWait` have the same hazard, left unchanged.

## MasterMind amendment (binding on this round)

A second relevo write to `/tmp` hit the same quota on 2026-10-02:

    relevo: internal: snapshot refs/relevo/fb-reader/out: git bundle create /tmp/relevo-bundle-3505865424.bundle ...: fatal: sha1 file '<stdout>' write error: Disk quota exceeded

`relevo send` to a remote binding builds its git bundle under `os.TempDir()`. Move relevo's
own scratch files (that bundle, and any other `os.CreateTemp("", ...)` /
`os.MkdirTemp("", ...)` in non-test relevo code -- grep for them) under a
`tmp` directory in the relevo state root, created 0700, the same directory the
db temp files go to. A test pins that the bundle path is under the state root.
List every call site you moved in the report.
