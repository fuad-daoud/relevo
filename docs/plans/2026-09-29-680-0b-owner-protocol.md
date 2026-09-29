# Plan: #680 stage 0b — the owner protocol in `internal/db` (no caller yet)

Spec: `docs/specs/2026-09-29-db-owner-design.md` §4/§5/§7/§8.
Built in two rounds on `relevo/db-owner-0b`: round 1 shipped steps 1-9, round 2
shipped steps 10-16. This document ships with round 2's code.

## Amendments applied with this plan

Four amendments override the plan and the spec wherever they disagree. A
shipped in round 1; B, C and D ship in round 2.

**Amendment A — the schema answer mirrors `db.Open` exactly; the owner never refuses on schema.**

- `db.Dial` sets `have` from `welcome.schema_have` and `know` from **this
  binary's own embedded maximum** (the value `db.Open` uses), and
  `newer = have > know` — never `know` from `welcome`. A client older than the
  owner's database therefore opens with `Newer() == true`, exactly as a direct
  open does today; callers keep deciding (the store refuses with
  `ErrNewerSchema`, the daemon pauses ingest).
- Drop the `schema_newer` refusal from the owner. `hello.schema_know` may stay
  as information. The refusal codes are `wrong_proto`, `shutting_down`,
  `restarting`.
- Replace the planned `TestOwnerRefusesNewerClientSchema` with
  `TestDialOfANewerDatabaseReportsNewer`: an owner whose database is ahead of
  the client's embedded maximum welcomes it, and the dialled handle reports
  `Newer() == true` with the same `have`/`know` a direct `Open` of that file
  reports.
- The spec's §5 carries the matching sentence: `welcome` carries `origin`, the
  refusal list loses `schema_newer`, and the schema answer mirrors `db.Open`.

**Amendment B — the cap limits pinned SQLite connections, and an over-cap client truly waits.**

- The handshake never waits on the cap: it is not taken before the handshake, so
  a client over the cap does not block in the handshake, hit the 2 s
  `handshakeTimeout`, and fail (a refusal in practice, which would break D6).
- The slot is acquired in `pin` — the first request that needs an owner
  connection — on the request's context, with no deadline of its own, and
  released in `cleanup` when the pinned connection is discarded. Idle handshaken
  connections never hold a slot.
- Pin: a test in package `owner` shrinks `maxConns`, holds the cap for longer
  than `handshakeTimeout`, and requires the over-cap client's request to succeed
  once a slot frees. Mutation: acquire the slot at accept again → that test
  fails.

**Amendment C — never report `driver.ErrBadConn` after a request frame has been sent.**

- `ErrBadConn` only before any byte of the request has been written (the `dead`
  check, or a failed `send`).
- After a request is sent, a lost connection returns a distinct error
  (`wire.ErrConnLost` wrapping the I/O error) that does **not** match
  `driver.ErrBadConn`, and marks the connection dead. This covers exec, query,
  `next`, and the transaction frames.
- Implement `driver.Validator` (`IsValid` returns `!dead`) and
  `driver.SessionResetter` (`ResetSession` returns `ErrBadConn` when dead), so
  the pool discards a dead connection before reuse.
- Pin: a fake owner (a test listener that speaks the handshake and executes a
  real `INSERT` on a real database, then closes without answering); a pool-level
  `Exec` through `db.Dial` returns an error, and the table holds exactly one
  row. Mutation: make the post-send path return `ErrBadConn` again → the row
  count is 2 and the test fails.

**Amendment D — spec corrections in `docs/specs/2026-09-29-db-owner-design.md`.**

- §5 Transport: macOS uses `LOCAL_PEERCRED` (`unix.GetsockoptXucred`), not
  `getpeereid`.
- §5 Connections: the cap counts pinned owner connections; the handshake never
  waits on it.
- §6 Re-exec: the sentence claiming `database/sql` retries a statement on
  `ErrBadConn` is replaced by the rule from amendment C: a request that never
  reached the owner is retried on a fresh connection; one already sent fails
  with a connection-lost error and is never retried automatically.

## Round 2 seam notes

Three facts the plan did not specify, resolved while making the suites pass
unchanged through the switch:

- The wire client now sends `close` on a clean close and waits for the owner to
  finish its rollback and discard before returning (bounded by a 2 s drain). The
  `close` kind was defined and handled but never sent on a clean close, and
  without the wait a dialled handle's `Close` raced the owner's write-back to
  the file.
- `dbtest.Main` installs `OwnerMode` only after the template is migrated, for
  the reason the plan's seam table gives.
- A `-1.0` point coverage tolerance is unchanged; the baseline records the new
  packages.

---

# Plan — #680 stage 0b: the owner protocol in `internal/db` (no caller yet)

Base: this worktree, detached at `401961a` (`docs(specs): the daemon owns relevo.db (#680)`), clean tree.
Read for this plan: `docs/specs/2026-09-29-db-owner-design.md` §4/§5/§7/§8, `docs/specs/probes/2026-09-29-db-owner/{usage,protocol,lifecycle}.md`, `CLAUDE.md`, `Makefile`, `scripts/check-coverage.sh`, `.github/workflows/ci.yml`, `internal/db/*`, `internal/db/dbtest`, `internal/store/{db,store,daemoninfo}.go`, `go.mod`.
`gh issue view 680` is **not readable here** ("no git remotes found" — the worktree has no remote); the spec's §10 is the authoritative correction of the issue body and is treated as the issue. That is recorded as a gap, not silently worked around.

## 1. What ships and what does not

Ships, all inside `internal/db` and its new subpackages:

- `internal/db/wire` — the protocol: framing, control messages, the five SQLite storage classes in binary, the rebuilt error type, refusal codes. Platform-neutral, no sockets.
- `internal/db/wire/client` — the `database/sql` driver: one driver connection pins one owner connection; handshake, exec, query, `next` streaming, `cancel`, `close`.
- `internal/db/wire/owner` — the owner server: serves one directly-opened `*db.DB` on a `net.Listener`, peer-uid check, connection cap that waits, pinned connection per client, rollback and discard on disconnect.
- `internal/db` — `Dial`, `NewOwner`, the `Code() int`-based matchers, and one test-only hook point for the transparency switch.
- `internal/db/dbtest` — the switch that routes every test database through an in-process owner.
- `.github/workflows/ci.yml` — one new job running the switch; `internal/db/main_test.go` (external test package) so `internal/db`'s own suite hops too.
- `testdata/coverage-baseline.txt` — regenerated; `docs/plans/2026-09-29-680-0b-owner-protocol.md` — this plan.

Does **not** ship: any change under `cmd/relevo`, `internal/store` non-test code, `internal/relevo`, `internal/serve`, `dist/`; no auto-start, no re-exec, no fd handoff, no `relevo.sock` under the state root, no `doctor` row, no `RELEVO_DB_DIRECT`, no `OpenReadOnly` over the wire. Stage 0b leaves a protocol nobody calls in production.

## 2. Behaviour and the cases (the contract)

**Transport.** A unix socket somewhere the caller chooses; the owner takes a `net.Listener` and checks the peer uid on each accepted connection (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED`/`Xucred` on macOS, via `golang.org/x/sys/unix`). Everything socket-shaped is `//go:build unix`; other platforms get refusing stubs, as `internal/store/lock_unsupported.go` does today. No TCP, no network, no harness.

**Frames.** 4-byte little-endian length, then a payload; a payload begins with a kind byte: control frames are JSON objects with `type` and `id`, value frames are a JSON header plus raw bytes. Types: `hello`, `welcome`, `refuse`, `exec`, `query`, `rows`, `next`, `done`, `close`, `cancel`, `error`.

**Values.** SQLite's five storage classes, one type byte each: NULL, int64, float64, text (UTF-8), blob; text and blob are length-prefixed raw bytes. No base64, no message cap. A Go `bool` argument is encoded as int64 0/1; a text column returns as `string`, a blob as `[]byte`, so `Scan` into `sql.Null[string]`, `[]byte`, `int64`, `float64` and `nil` keeps working.

**Rows.** Streamed in batches of about 1 MiB of encoded values; a value is never split across batches, so a single 5 MB blob arrives in one batch. The client asks for the next batch with `next`; the last batch is followed by `done`. No-limit queries stay possible (`lookup.go`'s callers materialise whole sets).

**Exec.** `exec` answers `done` carrying `rows_affected` and `last_insert_id`, because production callers read both (`internal/db/revision.go:52`, `consent.go:146`, `origin.go:43`, `write.go:370,433,461`).

**Connections.** Each client driver connection pins one owner connection: a `*sql.Conn` from the owner's own `*sql.DB`, so it opens with today's DSN pragmas (`db.go:99`: `busy_timeout`, `journal_mode(WAL)`, `foreign_keys(ON)`, `journal_size_limit`) and per-connection behaviour is unchanged. The owner caps pinned connections, starting at 64 (an unexported var, the `busyTimeoutMS`/`beginRetryFor` precedent, so a test can shrink it); a client over the cap **waits** — never refused (D6). On disconnect the owner cancels its in-flight request, issues `ROLLBACK`, and discards the pinned connection rather than returning it to the pool (`*sql.Conn.Close` only returns it; the discard is `Raw` returning `driver.ErrBadConn`). Idle rollback on a connection with no transaction is ignored.

**Cancellation.** `cancel{id}` cancels the running request's context on the owner, which interrupts the statement; the owner answers `error{id}`. Client-side, a cancelled Go context sends `cancel`, drains the answer, and returns `ctx.Err()` so `database/sql` discards that driver connection.

**Errors.** `error{id, code, extended_code, message}`. The client rebuilds an error whose `Code() int` returns exactly what modernc's `*sqlite.Error.Code()` returns on the owner, so today's masks keep working byte for byte (`db.go:312` `== 5`, `write.go:220-221` `&0xff == 19`), and the text fallbacks ("database is locked", "UNIQUE constraint failed") stay. Owner refusals are a separate type with a string code: `wrong_proto`, `schema_newer`, `shutting_down`, `restarting` — deliberately **not** `Code() int`, so `mapBusy` can never swallow one.

**Handshake.** `hello{proto, version, exe_id, schema_know}` → `welcome{proto, min_client, version, schema_have, schema_know, origin, features}` or `refuse{code, message}`. The client answers `Newer()` from `welcome`, never from the file; `welcome.schema_have` is also what `hasConfig()` needs (`config.go:24`). The owner refuses `wrong_proto` (proto mismatch), `schema_newer` (the client's advertised `schema_know` is below the owner's database), `shutting_down` (owner closed); `restarting` is produced by nothing in 0b and is pinned client-side. Dial plus handshake is bounded at 2 s; requests have no deadline.

**`db.Dial(sock)`** returns the same `*DB`: `sqlDB` over the wire driver, `have`/`know` from `welcome`, `newer = have > know`, `origin` from `welcome`, `beginRetry` default. `db.NewOwner(d)` serves a directly-opened handle. Owner-only work (migrations, `installation.json`, chmod, startup passes) stays where it is; none of it changes in 0b.

**A hole in §5 that 0b must close:** `welcome` has to carry the installation id (`origin`). `record.go:58` builds every scoped query from the handle's origin and every write stamps it; a Dialled handle with an empty origin would silently read and write only empty-origin rows, and the spec's own rule is that clients must not read `installation.json`. So `origin` is added to `welcome`; `db.NewOwner` supplies it from the handle it serves, `Dial` adopts it, and the test hop passes the caller's `Options` verbatim (see §4).

**Test sockets.** Every socket a test binds lives directly under `/tmp` (`/tmp/rvo-…/o-n.sock`), never under `t.TempDir()`, and the full path is asserted shorter than 104 bytes (macOS `sun_path`); the directory is removed on cleanup. Linux uses `unix.GetsockoptUcred` (`SOL_SOCKET`,`SO_PEERCRED`); darwin uses `unix.GetsockoptXucred` (`SOL_LOCAL`,`LOCAL_PEERCRED`) — `x/sys@v0.47.0` has no `Getpeereid`, so these are two small per-OS files; `golang.org/x/sys` becomes a direct dependency (`go mod tidy`, the `Makefile:40-46` guard must stay green).

## 3. Seams — existing anchors and new files

Existing code this touches (line numbers at `401961a`):

- `internal/db/db.go:79` `Open`, `:85` `OpenWith`, `:89` `open`, `:99` DSN, `:193` `Close` (per-handle, not idempotent — keep), `:243`/`:255`/`:262` `Tx`, `:282` `BEGIN IMMEDIATE`, `:300` `COMMIT`, `:307-319` `mapBusy`.
- `internal/db/write.go:214` `sqliteConstraint`, `:220` `mapMasterMindKey`.
- `internal/db/errors.go:5-17` sentinels (`ErrBusy`, `ErrInvalid`, `ErrNewerSchema`).
- `internal/db/record.go:58` `originScope` — the reason `welcome.origin` exists.
- `internal/db/config.go:188` `OpenReadOnly` — untouched; test callers (`helpers_test.go:87`, `compress_test.go:242`) keep opening the file directly.
- `internal/db/dbtest/dbtest.go` `Install`/`Main` — the switch joins here; `Install`'s template copy must run **before** the hop is installed (a hop-opened template would be left with a live owner and a non-empty `-wal`, and `Install` refuses exactly that).
- `internal/store/db.go:20` `dbForWrite`, `:36` `db.OpenWith(…, Options{Origin})`, `:54` `dbForRead` (`os.Stat` existence rule — unchanged; the owner creates the file, the same path, so the rule still holds); `internal/store/main_test.go:10` TestMain.
- `internal/db`'s test layout: 21 test files, all `package db`, **no TestMain**; `dbtest` imports `db`, so package `db` can never import `dbtest` (cycle). That is why the switch needs a hook with a `direct` closure and why `internal/db` gets a `package db_test` TestMain.
- `internal/db/db_test.go:401-432` (a short `BeginRetry` must bound a busy `Tx`) and `internal/db/origin_test.go:51-63,210` (one path, two origins, then a plain handle) — the two tests that force the hop to carry the caller's `Options` exactly.

New files (non-test, ≤600 lines each, ≤70-line functions, no new `.golangci.yml`/allow-list exclusions, no `#NNN`/`§` in comments):

| file | owns | rough size |
|---|---|---|
| `internal/db/wire/wire.go` | frame read/write, kind byte, length bound, `Conn`, `Version`, batch budget | ~250 |
| `internal/db/wire/msg.go` | the eleven message structs and their JSON encoding | ~200 |
| `internal/db/wire/value.go` | the five storage classes; batch builder and cursor (shared by both roles) | ~200 |
| `internal/db/wire/errors.go` | rebuilt sqlite error (`Code() int`, `ExtendedCode()`), refusal error + codes, the `Code() int` extractor the owner uses | ~100 |
| `internal/db/wire/client/client.go` | driver registration, `driver.Conn`, `Prepare` unsupported, `Ping` | ~200 |
| `internal/db/wire/client/session.go` | handshake, request loop, rows batches, cancel, bad-conn discipline | ~300 |
| `internal/db/wire/client/unsupported.go` | non-unix: registers nothing, refuses | ~25 |
| `internal/db/wire/owner/owner.go` | `Server`, accept loop, cap semaphore, `Close` | ~200 |
| `internal/db/wire/owner/conn.go` | per-client session: handshake, pinned conn, exec/query/next, rollback+discard on disconnect | ~300 |
| `internal/db/wire/owner/peer_linux.go`, `peer_darwin.go` | the uid check | ~30 each |
| `internal/db/wire/owner/unsupported.go` | non-unix refusal | ~25 |
| `internal/db/dial.go` | `Dial`, unexported `dial(sock, o)`, `NewOwner` wrapper | ~100 |
| `internal/db/hop.go` | `SetOwnerHop` (test-only hook point) | ~30 |
| `internal/db/dial_unsupported.go` | non-unix `Dial`/`NewOwner` that refuse | ~25 |

The owner needs a pinned `*sql.Conn` from the handle it serves; `db.NewOwner(d)` is in package `db` and passes `d.sqlDB`, `d.have`, `d.know`, `d.origin` into `owner.New(*sql.DB, have, know int, origin string)`, so `wire/owner` never imports `db` and no package imports upward (`db → wire, wire/client, wire/owner`; `wire/{client,owner} → wire`).

## 4. Exported surface (kept minimal)

- `db.Dial(sock string) (*DB, error)`
- `db.NewOwner(d *DB) *owner.Server`
- `db.SetOwnerHop(start func(path string, o Options, direct func() (*DB, error)) (sock string, err error))` — documented test-only, the `SetFreshTemplate` precedent; cleanup passes nil.
- `owner.Server` with `Serve(net.Listener) error` and `Close() error`; `owner.New(*sql.DB, have, know int, origin string) *Server`.
- `client.DriverName`, `client.Info(ctx, sock) (Info, error)` (`Info{Have, Know int; Origin string}`), used by `db.Dial` for the handshake answer; the driver registers itself in `init`.
- `wire.Version`, `wire.Conn` (`NewConn`, `Read`, `Write`), the message types the two roles exchange, the value kind constants plus the batch builder/cursor, `wire.Error` + constructor, `wire.Refusal` + the four code constants.

Nothing else is exported; anything a test needs beyond this list stays in `_test.go` files.

## 5. The transparency switch, and how CI runs both modes

- **Selection.** The environment variable `RELEVO_DBTEST_OWNER` (non-empty ⇒ on), read only by `dbtest` — in `Main` and in a new `dbtest.OwnerMode() (cleanup func(), err error)` entry point. `make check` and every CI shard run with it unset, i.e. direct mode.
- **The hop.** `OwnerMode` installs `db.SetOwnerHop`: on the first `Open`/`OpenWith` for a path it calls the supplied `direct` closure (a genuinely direct open, no recursion), starts an owner for that handle on a `/tmp` listener, records the socket, and returns it; every later open of the same path reuses the socket (mutex-guarded, so `db_test.go`'s concurrent opens get one opener). `internal/db`'s `OpenWith` dials the returned socket with the caller's `Options` **verbatim** — origin included, so `origin_test.go`'s plain handle stays unscoped and `db_test.go`'s 1 ms `BeginRetry` still bounds its `Tx`; production `db.Dial(sock)` takes the origin from `welcome`. Cleanup closes every owner and removes the socket directory.
- **`internal/db`'s own suite** joins via a new `internal/db/main_test.go` in `package db_test` (allowed alongside 21 `package db` test files; the cycle forbids anything else) calling `dbtest.OwnerMode()` and `m.Run` — not `dbtest.Main`, so `internal/db` keeps migrating its own fixtures instead of seeding from the template.
- **`dbtest`'s own test** pins both modes (`OwnerMode` with the variable set routes an `Open` through a socket; unset does nothing) so the package's coverage baseline holds in the default run.
- **CI.** New job `owner-mode` in `.github/workflows/ci.yml`, `needs: changes`, `if: needs.changes.outputs.code == 'true'`, `ubuntu-latest`, `go: stable`, env `RELEVO_DBTEST_OWNER: '1'`, run `go test -race -count=1 ./internal/db/... ./internal/store/...`. The three existing shards stay unchanged, so every PR runs both modes. It is deliberately not a macOS leg: the free-plan 5-job macOS cap, and owner-per-path fd cost, while the 104-byte rule is pinned by a test that runs everywhere. The job uploads no artifact, so the coverage job's `shard-*` download is unaffected.

## 6. Ordered steps

**Round 1 — the protocol (ends with a green `make check`).** Leaves: `wire`, `wire/client`, `wire/owner` and the `db` seam working and tested; nothing calls `Dial`; the switch does not exist yet.

1. Add `golang.org/x/sys/unix` as a direct dependency and touch nothing else — `make check-static` (the `go mod tidy` guard) green.
2. Write `internal/db/wire`: framing, messages, values, `Error`/`Refusal` — `go test ./internal/db/wire/` covers every value class, a 4.5 MB blob, a batch boundary, and a malformed length.
3. Write `internal/db/wire/client`: driver + session — `go test ./internal/db/wire/client/` has a compile-and-refuse test on non-unix and the shared owner fixture on unix.
4. Write `internal/db/wire/owner`: accept, uid check, cap, handshake, pinned conn, rollback, cancel — `go test ./internal/db/wire/owner/`.
5. Write the seam in `internal/db`: `Dial`, `NewOwner`, the hop point, unix/stub split — `go test ./internal/db/ -run 'TestDial|TestNewOwner|TestMap'`.
6. Switch `mapBusy` (`db.go:307`) and `mapMasterMindKey` (`write.go:220`) to any error with `Code() int`, keeping today's masks and text fallbacks — a test feeds a wire error with code 5 → `ErrBusy`, code 19(+2067) → `ErrInvalid`.
7. Add the protocol tests, each named for what it pins: `TestClientDisconnectRollsBackAndKeepsSeq`, `TestPinnedTransactionDoesNotBlockOtherConnections` (three or more live connections while one is pinned), `TestCancelInterruptsARunningStatement`, `TestFiveMegabyteBlobRoundTrips`, `TestFiftyMegabyteResultStreamsInBatches` (asserts more than one `next`), `TestBusyMapsToErrBusy`, `TestConstraintMapsToErrInvalid`, `TestOwnerRefusesWrongProto`, `TestOwnerRefusesNewerClientSchema`, `TestOwnerRefusesWhenShuttingDown`, `TestClientMapsRestartingRefusal`, `TestConnectionCapWaitsInsteadOfRefusing`, `TestOwnerDropsADifferentUid`, `TestTestSocketPathFitsSunPath` — `go test -race -count=1 ./internal/db/...` green.
8. Regenerate `testdata/coverage-baseline.txt` on this machine's Go (`go test -race -count=1 -cover ./... > .coverage.txt` then `sh scripts/check-coverage.sh --write`) so `internal/db/wire*` are recorded; never lower a line.
9. Run the whole gate plus the two cross checks — `make check` green, `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` green, `CGO_ENABLED=0 GOOS=darwin go vet ./...` green.

**Round 2 — the switch, CI, verification, the plan (ends with a green `make check`).** Leaves: the transparency proof running in CI and locally, the mutation checks reported, the plan committed with the code; still no production caller.

10. Add `dbtest.OwnerMode` + registry + `Main` wiring + the `/tmp` short-path rule — `go test ./internal/db/dbtest/` pins both modes.
11. Add `internal/db/main_test.go` (package `db_test`) calling `dbtest.OwnerMode()` — `go test ./internal/db/` still green in direct mode.
12. Run the transparency proof: `RELEVO_DBTEST_OWNER=1 go test -race -count=1 ./internal/db/... ./internal/store/...` — passing **unchanged**. Any failure is a seam defect to fix; a suite that cannot pass unchanged is a halt-and-report, never a test edit.
13. Add the `owner-mode` CI job — `sh -n`/the workflow passes `make check-scripts`' shell checks and `scripts/ci-code-changed.sh` still classifies the PR as code.
14. Mutation checks, one at a time, each reverted: pinning broken (owner serves from the pool) → step 7's concurrency test fails; rollback removed on disconnect → the rollback test fails; the owner sends code 0 (or the client drops the `Code()` match) → `TestBusyMapsToErrBusy` fails; the owner refuses at the cap → `TestConnectionCapWaitsInsteadOfRefusing` fails; uid compared against a wrong uid → the uid test fails. Record each edit and the failing name.
15. Re-run `sh scripts/check-coverage.sh --write` if step 12/14 moved coverage, and say so in the report.
16. Save this plan to `docs/plans/2026-09-29-680-0b-owner-protocol.md`, commit it with the code on a branch carrying the spec commit, `Refs #680`, one PR — final `make check` green before the report.

## 7. What is deleted

1. Nothing. This stage adds code; no function, flag, test call site, file, or behaviour is removed or repurposed. `cmd/relevo`, `internal/store`'s production code and `internal/relevo` keep every current call; `Open`/`OpenWith`/`OpenReadOnly`/`Close`/`Tx` keep their contracts, and `mapBusy`/`mapMasterMindKey` keep every match they have today while adding the `Code() int` one. No exclusions, no allow-list entries, no `//nolint`, no baseline lowered.

## 8. The report must include

- Commands run, in order, with results: the focused `go test -race -count=1 ./internal/db/...`, the owner-mode run (with the package list), `make check`, the Windows build, the darwin vet.
- That the coverage baseline was regenerated, with the exact `--write` command and the lines the new packages added.
- The mutation checks: condition, the edit, the named test that failed, and that it was reverted.
- The transparency proof result, plus every test or seam that had to change to pass, or a halt with the failing test named.
- What is knowingly not done: auto-start, re-exec/fd handoff, `relevo.sock`, `doctor`, `RELEVO_DB_DIRECT`, typed endpoints, any production caller.
- The CI job name and the two modes; the plan-doc path and the commit/PR with `Refs #680`; and any deviation from this plan with its reason.

## 9. Flags on the seed (points the plan resolves rather than guesses)

1. `welcome` must carry the installation id, or Dialled handles silently scope to empty origin (`record.go:58`); §5's field list omits it.
2. The switch cannot live only in `dbtest`: `internal/db`'s tests are `package db` with no TestMain, and `dbtest` imports `db`, so the hop needs the `direct`-closure hook plus a `package db_test` TestMain; the hook must carry the caller's `Options` verbatim because two existing tests pin `BeginRetry` and origin-per-handle on one path.
3. `golang.org/x/sys@v0.47.0` has `GetsockoptUcred` (linux) and `GetsockoptXucred` (darwin) but no `Getpeereid`, so the uid check is two per-OS files.
4. `exec` must carry `rows_affected` and `last_insert_id`; four production call sites read them.
5. Discarding a pinned connection is `Raw` returning `driver.ErrBadConn`; `*sql.Conn.Close` only returns it to the pool.