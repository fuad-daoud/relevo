# The wire protocol for #680: a survey, and one recommendation

Status: research report, 2026-09-29. Read-only round: no code, no `go.mod`/`go.sum`
change, no `go get`. Written as a prose and citation survey of #680's five
questions; the deliverable is this file.

## Header

- **Checked out HEAD:** `f74996fa5f6b62ccf58297224695cdcd7e6f829a` (`f74996fa`,
  "fix: harness-only limit lines, one terminal sanitiser, owner-only db files
  (#692)"). `git status --porcelain` was empty before and after this round.
- **`origin/main`:** the tree has **no git remote configured**, so
  `git rev-parse origin/main` fails (`fatal: ambiguous argument 'origin/main'`).
  The planning pass recorded `origin/main` as `f74996fa`; the branch
  `relevo/db-owner-r1-build` has since been advanced onto the same commit, so
  **HEAD is now `f74996fa` itself** — the three commits the plan named as ahead
  (`e7b2485` (#689), `8e0b0ed` (#693), `f74996fa` (#692)) are now *below* HEAD,
  and `442fc598` (the hash the plan's seam table was verified at) is an ancestor:
  `git log --oneline 442fc598..HEAD` → `e7b2485`, `8e0b0ed`, `f74996fa`.
  **Every `path:line` below is resolved at `f74996fa`, not at `442fc598`.**
- **Date:** 2026-09-29. All external facts below were fetched today with the
  command printed beside them; all repo facts are quoted from the worktree.
- **Citation rule:** repo facts are `path:line` at `f74996fa`; external facts
  carry the exact command and the date 2026-09-29; anything not directly checked
  is marked `(inferred)`.
- **Round override (the one deviation authorised):** the plan said the report is
  the runner's final message and nothing is written to the tree. Instead this
  file is committed at `docs/specs/probes/2026-09-29-db-owner/protocol.md`; it is
  the only file this round adds or changes. Everything else in the plan stands.

---

## Q1 — Is there a maintained, pure-Go `database/sql` client for Hrana?

**Verdict: no. The only pure-Go client is copied from a repository its owner
deprecated, and the copy keeps the same closed transport; the official successor
requires cgo.**

| candidate | status (2026-09-29) | licence | cgo | dialer / unix socket? | deciding evidence |
|---|---|---|---|---|---|
| `tursodatabase/libsql-client-go` | not archived, pushed `2026-05-28`, **0 tags, 0 releases**; README banner `> **This repository is deprecated.**` at `README.md@2` | MIT | pure Go | **No.** The option list is closed (`WithAuthToken`, `WithTls`, `WithProxy`, `WithSchemaDb`, `WithRemoteEncryptionKey`, `WithRequestHeaders`: `lcgo-sql.go@35-112`) and none takes a dialer, `net.Conn`, `*http.Client` or `Transport`. The HTTP transport executes on the package-global client verbatim: `resp, err := http.DefaultClient.Do(req)` (`hranaV2.go@261`). The WebSocket transport dials with a literal options struct: `websocket.Dial(ctx, url, &websocket.DialOptions{` (`websockets.go@201`) with no `NetDial`. | `gh api` commands in Sources |
| `WillowWorks-io/libsql-client-go` | the "maintained fork" the plan called a new lead: created `2026-08-16`, pushed `2026-08-17`, 0 stars, **not a GitHub fork**, pure-Go ("builds as a static binary on distroless") | MIT | pure Go | **No — same gap.** Its `libsql/internal/ws/websockets.go` is byte-identical to upstream (322 lines; `Dial` at :201, `Subprotocols: []string{"hrana1"}` at :202, `SetReadLimit(1024 * 1024 * 16) // 16MB` at :208 — `diff` empty). Its `hranaV2.go` is longer (671 vs 616 lines) but only adds `driver.Validator`/`driver.Pinger`; the transport line is the same: `http.DefaultClient.Do(req)` at `ww-hranaV2.go@316`, `/v2/pipeline` at :296. | Sources |
| `tursodatabase/go-libsql` | the successor the deprecation points at | MIT | **cgo required**: `//go:build cgo` at `golibsql.go@1`, `#cgo darwin,amd64 LDFLAGS: -L${SRCDIR}/lib/darwin_amd64` … `#cgo LDFLAGS: -lsql_experimental` at :8-12 | n/a (embedded, not a wire client) | Sources |
| `tursodatabase/turso-go` | **archived** `2026-01-06`, description "Moved back to https://github.com/tursodatabase/turso" | MIT | no cgo but `dlopen`s a Rust library (per #466) | n/a (embedded/partial-sync, not a wire server) | Sources |

Two details worth naming:

- `WithProxy` is **not** a transport hook. It rewrites the URL host and scheme for
  `http`/`https` only and explicitly refuses WebSockets: `return nil, fmt.Errorf("proxying of ws:// and wss:// URLs is not supported")` (`lcgo-sql.go@185`). A unix socket needs `http.Transport.DialContext`, which the option list does not expose.
- The driver *does* understand `file:` URLs (`lcgo-sql.go@119-132`), but only by delegating to a separately imported `sqlite`/`sqlite3` driver — that is the embedded path, not Hrana.

**Q1 verdict:** no maintained, pure-Go Hrana client exists that can be pointed at
a unix socket or given a custom transport; the maintained pure-Go copy shares the
deprecated client's hard-coded `http.DefaultClient` and `websocket.Dial`.

---

## Q2 — How do streams and batons map onto `database/sql`, and is there a Go server?

**Verdict: the written spec exists and is complete (1704 lines, versions 1-3,
both transports); an existing Go Hrana server does exist (`cornejong/hrana`) but
carries no licence, so it cannot be reused; the minimum server request set per
variant is small and is listed below.**

### The written specs (all fetched today)

| spec | path in `tursodatabase/libsql` | lines |
|---|---|---|
| Hrana 1 | `docs/HRANA_1_SPEC.md` | 452 |
| Hrana 2 | `docs/HRANA_2_SPEC.md` | 219 |
| Hrana 3 | `docs/HRANA_3_SPEC.md` | 1704 |
| HTTP v1 | `docs/HTTP_V1_SPEC.md` | 70 |
| HTTP v2 | `docs/HTTP_V2_SPEC.md` | 236 |

A root `proto/` directory does **not** exist (`contents/proto` → HTTP 404); the
Hrana message definitions live in `libsql-hrana/src/proto.rs` (659 lines;
`PipelineReqBody` at :8, `StreamRequest` at :56, `StreamResponse` at :72 — decoded
copy in Sources) and `libsql-hrana/src/protobuf.rs` (496 lines). The only `.proto`
files in the repository are replication/admin ones
(`libsql-replication/proto/proxy.proto`, `metadata.proto`, `replication_log.proto`,
`libsql-server/proto/admin_shell.proto`).

### Streams and batons

- **WebSocket (versions 1-3).** Streams are connection-scoped: "A single
  connection can host an arbitrary number of streams. In effect, one Hrana
  connection works as a 'connection pool' in traditional SQL servers."
  (`spec-HRANA_3_SPEC.md@84-101`). The client opens one with
  `open_stream`/`stream_id` (`:251-266`).
- **HTTP (v2 and v3).** There is no connection, so a **baton** stands in for one:
  "The server returns a baton in every response to a request on the stream, and
  the client then needs to include the baton in the subsequent request… the client
  must serialize the requests on a stream" (`spec-HRANA_3_SPEC.md@593-595`;
  `spec-HTTP_V2_SPEC.md@12-21`). The server must make batons unforgeable
  (`spec-HRANA_3_SPEC.md@686`) and closes abandoned streams
  (`spec-HTTP_V2_SPEC.md@20-22`).

### Mapping onto `database/sql` and onto relevo's `Tx`

- one **stream** → one pinned `*sql.Conn` (Hrana's "connection pool" comment,
  `spec-HRANA_3_SPEC.md@84-101`, is exactly `database/sql`'s pool model), and
- one **baton** → one stream id the server keeps in a side table (`spec-HTTP_V2_SPEC.md@37-97`).
- relevo's own pinned-connection unit is `Tx.conn *sql.Conn` (`internal/db/db.go:244`), taken once per transaction and held for its life (`internal/db/db.go:263-271`). A Hrana stream maps onto that unit **1:1**, which is why the plan's "SQL-level proxy" shape is the right level: everything above `internal/db` sees no change.
- A `BEGIN IMMEDIATE` is just SQL the client sends (there is no transaction
  concept in Hrana), so the server must run relevo's exact begin loop
  (`internal/db/db.go:281-291`) or pinned-connection semantics are lost.

### Minimum request set, per variant

- **hrana1 over WebSocket** (`spec-HRANA_1_SPEC.md@210-221`): `Hello`
  (`:101`), `RequestMsg`/`ResponseOkMsg` (`:141-165`), and
  `OpenStreamReq`, `CloseStreamReq`, `ExecuteReq`, `BatchReq`. That is the whole
  request surface of version 1: **so a usable server is `hello` + `open_stream` +
  `execute` + `close_stream`** (`batch` only for multi-statement atomic work).
- **HTTP v2 `/v2/pipeline`** (`spec-HTTP_V2_SPEC.md@37-97`, request union `:98-122`):
  `CloseStream`, `ExecuteStream`, `BatchStream`, `SequenceStream`, `DescribeStream`,
  `StoreSqlStream`, `CloseSqlStream`. **Minimum for relevo: `ExecuteStream` (and
  `CloseStream`).** The client this repo ships speaks exactly this endpoint
  (`/v2/pipeline`, `hranaV2.go@241`).
- **Hrana 3 over WebSocket** (`spec-HRANA_3_SPEC.md@213-250`): adds
  `OpenCursor`, `CloseCursor`, `FetchCursor`, `Sequence`, `Describe`, `StoreSql`,
  `CloseSql`, `GetAutocommit` on top of hrana1. Version 3 "is designed to be a
  strict superset of versions 1 and 2" (`:80-83`), so a v3 server must accept
  `hrana1`/`hrana2` subprotocols.

**Cancellation is absent from the protocol.** `grep -in cancel` returns **0
matches** in `HRANA_1_SPEC.md`, `HRANA_2_SPEC.md`, `HRANA_3_SPEC.md` and
`HTTP_V2_SPEC.md`. This is a *new* client requirement, exactly as the plan's
Seams A say, and no Hrana variant can express it.

### Existing Go Hrana **servers**

`cornejong/hrana` is a real server: "A Go package that serves the Hrana protocol
(V1, V2, V3) over both HTTP and WebSockets. It wraps a standard `*sql.DB`
connection pool and implements the full specification including batches, stored
SQL, sequences, cursors, and JWT authentication." Its tree carries
`hrana.go` (226 lines; `New(db *sql.DB, conf *Config) *Server` at :103),
`executor.go` (487 lines; `executeStmt` at :13, `rowsToStmtResult` at :130,
`executeBatch` at :214), `baton.go`, `stream.go`, `websocket.go`, `http.go`,
`codec.go`, `cmd/stresser`, `clients/` and a copy of the five specs.

But: **`gh api repos/cornejong/hrana` returns `"license": null`** (0 stars,
created `2026-04-28`, pushed `2026-08-19`, description "hrana server
implementation for golang"), and its README's quick-start imports
`github.com/mattn/go-sqlite3` with "Requires CGo". An unlicensed package cannot
be copied into relevo at all, and its documented path is cgo. It is a useful
**reference reading** of the spec and nothing more.

**Q2 verdict:** the spec is implementable in pure Go and the minimum server
surface is small (`hello` + `open_stream` + `execute` + `close_stream`, or one
HTTP pipeline endpoint), but the only existing Go server is unlicensed, and no
Hrana variant offers cancellation.

---

## Q3 — Alternatives: existing Go `database/sql`-over-network proxies

**Verdict: none is a drop-in. Only rqlite/gorqlite ships a `database/sql` driver
over a network with an injectable `http.Client`, and it drags in a whole
distributed database; the rest are full servers, archived, or a CGo-adjacent
example. A small custom protocol is the smallest thing that meets the five Q4
requirements.**

| option | source (2026-09-29) | licence | cgo | `database/sql` story | custom transport | fit for relevo |
|---|---|---|---|---|---|---|
| **postlite** `benbjohnson/postlite` | **archived**, pushed `2023-10-02`, 1227 stars, "Postgres wire compatible SQLite proxy" | Apache-2.0 | pure Go | none shipped (it is a Postgres-wire **server/proxy**: "translating Postgres frontend wire messages into SQLite transactions") | n/a | Archived; README states it "is no longer maintained" — read for ideas only |
| **psql-wire** `jeroenrinzema/psql-wire` | not archived, pushed `2026-09-24`, 242 stars | Apache-2.0 | pure Go | **server library**: "Build your own PostgreSQL server"; you write the handler (`wire.ListenAndServe(addr, handler)`), and **there is no SQL parser** ("This project does not include a PSQL parser") | n/a | Needs a Postgres parser + executor written by us to expose relevo's `*sql.DB`; far more than Hrana's minimum |
| **go-mysql-server** `dolthub/go-mysql-server` | not archived, pushed `2026-09-29`, 2658 stars | Apache-2.0 | pure Go | MySQL-wire **server** with a storage-agnostic engine; you implement a database provider | n/a | A full SQL engine to sit in front of relevo's single connection; wrong layer and wrong protocol |
| **marmot** `maxpert/marmot` | not archived, pushed `2026-09-29`, 2822 stars | MIT | pure Go | MySQL-protocol**-compatible distributed SQLite** replication ("leaderless… gossip-based") | n/a | Brings a cluster and its own conflict model; not a local endpoint |
| **rqlite / gorqlite** `rqlite/rqlite` + `rqlite/gorqlite` | not archived; rqlite pushed `2026-09-29` (17778 stars); gorqlite pushed `2026-05-04` (187 stars) | MIT / MIT | pure Go | **yes**: `gorqlite/stdlib` registers a `database/sql` driver — `sql.Register("rqlite", &Driver{})` and `Driver.Open` → `gorqlite.Open(name)` (`gorqlite-sql.go@14-23`). The wire is rqlite's **HTTP/JSON API** (`/db/execute`, `/db/query`; `README.md@28-36`). | **partly**: `gorqlite.OpenWithClient(connURL string, client *http.Client)` (`gorqlite-main.go@69`) and the package var `DefaultHTTPClient` (`gorqlite-conn.go@20`) let a caller install an `*http.Client` whose `Transport.DialContext` dials a unix socket; DSN options are `level`, `disableClusterDiscovery`, `timeout` (`gorqlite-conn.go@221-224`). The stdlib driver itself only ever calls `gorqlite.Open`, so the hook is the package var. | The closest reuse, but it embeds rqlite's cluster/raft/consensus model and its own transaction semantics — no `BEGIN IMMEDIATE`, and BLOB fidelity through JSON is unverified. |
| **fork/patch the deprecated client** | the `WillowWorks-io` copy is exactly that | MIT | pure Go | Hrana client (see Q1) | **no** — the fork did not add a dialer | Cloning the fork does not fix the transport, the 16 MB cap or the missing cancellation |
| **loopback TCP + token** | n/a | n/a | n/a | a local TCP listener with a shared token over relevo's own protocol | full, but on TCP not a socket | A weaker variant of the custom option: TCP is reachable by other users' processes on the host; the plan's own security note favours a 0600 unix socket in a 0700 dir |
| **custom: length-prefixed JSON/gob over a unix socket** | this round's proposal | (project licence) | pure Go | ships its own `database/sql` driver or, at stage 0, an in-memory pipe | full | Meets every Q4 requirement by construction; small enough to test |

**Custom vs reuse.** Reuse buys a protocol we did not write and did not test;
against that, every reuse candidate either carries a licence problem
(cornejong/hrana), a cgo problem (go-libsql), an archive (postlite), a whole
engine (go-mysql-server, marmot), a cluster (rqlite) or a transport we cannot
re-point (the libSQL clients). The custom option's cost is a protocol to version
and torture-test — which #680's "Risks" section already budgets for ("it needs
its own torture tests").

**Q3 verdict:** no existing proxy is reusable as-is; rqlite/gorqlite is the only
one with a `database/sql` driver and a transport hook, and it brings a cluster
relevo does not want.

---

## Q4 — What each viable option cannot do (the requirement matrix)

### The five requirements, anchored in relevo's own code

| # | requirement | anchor (at `f74996fa`) |
|---|---|---|
| R1 | **`BEGIN IMMEDIATE` semantics** | `internal/db/db.go:255` (`Tx`), `internal/db/db.go:282` (`conn.ExecContext(ctx, "BEGIN IMMEDIATE")`), retry loop `internal/db/db.go:286-291`, `COMMIT` not retried `internal/db/db.go:300-302`; the contract is stated at `internal/db/db.go:251-254` |
| R2 | **context cancellation of a running query** | `internal/db/db.go:259` passes `context.Background()` into `db.tx`; `db.tx` accepts one (`internal/db/db.go:262`) but **no exported `*DB` method takes a ctx** (`grep` result: none). There are 38 `context.Background()` calls in `internal/db` non-test. Ctx exists only *above* the seam: `internal/delivery/pull.go:34-48` |
| R3 | **BLOB values** (per-row zstd frames up to tens of MB) | codec: `internal/db/codec.go:12-15`, `:29-38`, `:59-65`; the only bulk blob column is `round_file.body`, written at `internal/db/roundfile.go:23-41` and read back as bytes at `internal/db/roundfile.go:46-71`; the movable list names `round_file.body` and `transcript` as the conversion scope (`internal/db/compress.go:57-64`) |
| R4 | **SQLite error codes (`SQLITE_BUSY`) surfacing to the client** | `internal/db/db.go:31` (`const sqliteBusy = 5`), `internal/db/db.go:307-319` (`mapBusy`, code 5 at `:312`, message fallback at `:315`); constraint code 19 at `internal/db/write.go:217` and `mapMasterMindKey` at `internal/db/write.go:220`; sentinels `ErrBusy`/`ErrInvalid`/`ErrNewerSchema` at `internal/db/errors.go:7,13,17`; the consumer that must still see busy is the ctx-aware retry at `internal/delivery/pull.go:24-27` |
| R5 | **version handshake** | `internal/remote/proto.go:16` (`const Version = 1`), `internal/remote/proto.go:24` (`HeaderClientVersion`); the HTTP shape is a 426 upgrade-required carrying `remote.CodeVersion` at `internal/serve/routes.go:82`; the schema answer is `have > know` at `internal/db/db.go:143-149` and `internal/db/config.go:223` |

### The matrix

Columns are the viable options; rows are R1-R5.

| | **A. Hrana client + Hrana server** | **B. reuse `cornejong/hrana`** | **C. rqlite/gorqlite** | **D. custom length-prefixed JSON over a unix socket** |
|---|---|---|---|---|
| **R1 BEGIN IMMEDIATE** | **needs work** — Hrana has streams, not transactions; the server must pin a `*sql.Conn` per stream and run relevo's begin loop, because a client can only send `BEGIN IMMEDIATE` as SQL text | **needs work** — it wraps `*sql.DB` and `executeStmt` runs on the pool (`cornejong-executor.go@13`); per-stream pinned connections are its `Stream`'s business, not relevo's `Tx` (`inferred` — `stream.go` was not read) | **needs work** — rqlite's own transaction/consistency model, not `BEGIN IMMEDIATE` | **supported by construction** — the owner runs `internal/db/db.go:255-303` unchanged |
| **R2 cancellation** | **impossible** — no Hrana variant names cancellation, and HTTP streams are explicitly serialized (`spec-HRANA_3_SPEC.md@593-595`), so a running query cannot be interrupted out of band | **needs work / (inferred)** — its executor takes a `ctx` (`cornejong-executor.go@13`), so it depends on the wrapped driver honouring it | **needs work** — depends on rqlite's HTTP handler; no cancel verb in the client | **supported** — a cancel frame plus the ctx `db.tx` already accepts (`internal/db/db.go:262`); this is the one requirement that is *new* everywhere (no exported ctx method today) |
| **R3 BLOB values** | **needs work, and capped** — values travel as base64 blobs (`hrana/value.go@11-14`), +33% on the wire (stated in `docs/specs/2026-09-25-compression-spike.md:117`), and the WebSocket client hard-caps a message at 16 MB (`websockets.go@208`) | **needs work / (inferred)** — same Hrana value encoding | **needs work** — blobs go through JSON; gorqlite has no `base64` handling in `write.go`/`query.go`/`request.go` (`grep` empty), so `[]byte` fidelity is unverified | **supported** — a length-prefixed byte frame has no message cap and no encoding tax |
| **R4 SQLite error codes** | **needs work** — Hrana has typed errors but no SQLite code; the server must map `db.ErrBusy`/`ErrInvalid` into a Hrana error, and the client surfaces only a string | **needs work / (inferred)** — same mapping problem, in code we do not control | **needs work** — rqlite returns its own error shape | **supported** — the error frame carries the sentinel (`internal/db/errors.go:7,13,17`) mapped by `mapBusy` (`internal/db/db.go:307`) and `mapMasterMindKey` (`internal/db/write.go:220`) |
| **R5 version handshake** | **supported** — subprotocol negotiation (`spec-HRANA_3_SPEC.md@55-83`) and `GET v3` (`spec-HRANA_3_SPEC.md@621`); but the client only offers `hrana1` (`websockets.go@202`) and `/v2/pipeline` (`hranaV2.go@241`), so the handshake is the protocol's, not relevo's | **supported** — the server already negotiates subprotocols (`cornejong-hrana.go@187-206`) | **needs work** — rqlite has no relevo schema handshake | **supported** — mirror `remote.Version` and the 426 pattern (`internal/remote/proto.go:16`, `internal/serve/routes.go:82`), and carry the schema answer (`internal/db/db.go:143-149`) |

### The two explicit disqualifier tests

1. **No cgo.** relevo is `CGO_ENABLED=0` and fully static: `go 1.25.0`
   (`go.mod:3`), CI builds every target with `CGO_ENABLED: '0'`
   (`.github/workflows/ci.yml:300`) including a Windows compile-only target
   (`.github/workflows/ci.yml:302`), and releases ship linux/darwin amd64/arm64
   only (`.github/workflows/release.yml:36`); the README states the file is
   `mode 0600` and "no verb needs it closed" (`README.md:963`, `README.md:967`).
   **`tursodatabase/go-libsql` is disqualified** (`golibsql.go@1`, `:8-12`).
   **`cornejong/hrana` is disqualified on its licence**, not on cgo — the library
   takes any `*sql.DB`, but `license: null` makes it uncopyable regardless.
2. **The 16 MB WebSocket read limit vs a 157 MB row.** The client caps a message
   at `c.SetReadLimit(1024 * 1024 * 16) // 16MB` (`websockets.go@208`). The
   repo's bulk figure is `round_file` `builder.jsonl` **157 MB across 233 rows**
   (`docs/specs/2026-09-25-compression-spike.md:31`), with `diff.patch` at
   21.5 MB across 407 rows — i.e. an average of about 674 KB per body, **not** a
   single 157 MB row. **No single-row maximum is recorded anywhere in the repo**
   (`grep` for largest/biggest/max body finds nothing but the comment at
   `docs/specs/2026-09-25-compression-spike.md:176`). So the plan's
   "16 MB limit against a 157 MB row" premise is **not supported as written**;
   the honest statement is: the limit bites when one encoded body exceeds 16 MB
   (roughly 12 MB of raw bytes after the base64 +33% tax,
   `docs/specs/2026-09-25-compression-spike.md:117`), which the repo neither
   records nor rules out. Marked **`(inferred)`**, and it is a cap the custom
   option does not have.

**Q4 verdict:** the Hrana column is blocked on cancellation (impossible), a
16 MB message cap, and a value encoding that taxes blobs; rqlite is blocked on
`BEGIN IMMEDIATE` and unverified BLOBs; only the custom option supports all five
requirements, and cancellation is new work in every option.

---

## Q5 — Recommendation

**Pick (D): a small custom protocol — length-prefixed JSON frames over a unix
socket under `store.DefaultRoot()` — owned by the single process that opens
`relevo.db`.**

### Why

1. **It is the only option that supports all five Q4 requirements at once**, and
   it supports R1 by *reusing* `internal/db/db.go:255-303` verbatim rather than
   re-deriving `BEGIN IMMEDIATE` and the busy retry. The proxy lives at the
   `database/sql` seam (`internal/db/db.go:41`, the only importer of a driver:
   `internal/db/db.go:15`), which is exactly the seam #680 names and the smallest
   blast radius above it.
2. **`CGO_ENABLED=0` and the release matrix hold.** Everything is stdlib; the
   release targets are linux/darwin amd64/arm64 (`.github/workflows/release.yml:36`),
   so a unix-domain socket is sufficient. There are **no unix-socket syscalls
   anywhere in the tree today** and the only listener is the TCP one at
   `internal/serve/listen.go:41` — both confirmed by `grep` — so the plan's "this
   is building relevo's local server tier" is accurate.
3. **The one-owner rule already has its building blocks**: `AcquireDaemonLock`
   (`internal/store/daemonlock.go:24`), the re-exec machinery
   (`internal/relevo/daemon.go:41`), and the peek runtime that must keep never
   opening the database (`cmd/relevo/wire.go:239-271`, `cmd/relevo/daemon.go:46`).
4. **The version handshake and the schema answer have precedent to copy**:
   `remote.Version` / `HeaderClientVersion` (`internal/remote/proto.go:16`,
   `:24`) and the 426+code shape (`internal/serve/routes.go:82`), plus the
   `have > know` answer (`internal/db/db.go:143-149`, `internal/db/config.go:223`).
5. **"Typed endpoint later" stays open.** A length-prefixed framed protocol can
   carry a typed request later without changing the socket, the handshake or the
   error frame; a Hrana server would freeze us into the Hrana message set.

### Design skeleten (protocol shape only — no code in this round)

- Frame: `[4-byte little-endian length][payload]`; JSON payloads for control
  (`hello`, `open`, `exec`, `query`, `begin`, `commit`, `rollback`, `cancel`,
  `close`), raw length-prefixed bytes for parameter and result blobs (no base64,
  no per-message cap — the point the 16 MB limit at `websockets.go@208` fails).
- Handshake: a `hello` carrying `remote.Version`'s peer version and receiving the
  owner's schema `have`/`know`, so a client can answer `Newer()` without opening
  the file (`internal/db/db.go:143-149`).
- Errors: an error frame carrying the wire code for `ErrBusy` / `ErrInvalid` /
  `ErrNewerSchema` (`internal/db/errors.go:7,13,17`), produced by `mapBusy`
  (`internal/db/db.go:307`) — so `internal/delivery/pull.go:24-27`'s retry keeps
  working unchanged.
- Socket: `<state root>/relevo.sock`, 0600 in a 0700 root (the directory already
  is: `internal/store/db.go:25`), peer-uid checked.

### Risks

1. **A protocol we own.** Version skew across a daemon re-exec
   (`internal/relevo/daemon.go:41`) and the upgrade window must be handled by the
   handshake plus "restart the daemon", exactly as #680's Risk 2 says.
2. **Cancellation is genuinely new.** No exported `*DB` method takes a ctx today
   (`internal/db/db.go:259` is the only `Tx` path and it passes
   `context.Background()`); adding interrupt of a running modernc statement is
   the least-charted part of the work.
3. **Blob streaming correctness.** `round_file.body` is read as one `[]byte`
   (`internal/db/roundfile.go:46-71`); the frame must not need the whole body in
   memory twice.
4. **Availability becomes mandatory** once the CLI defaults to the endpoint
   (#680's Risk 1): auto-start, retry and an actionable failure line are part of
   the work, and `AcquireDaemonLock` (`internal/store/daemonlock.go:24`)
   serializes the cold-start race.
5. **Platform detail.** A unix socket path is length-limited on some systems;
   `store.DefaultRoot()` is short, but it must be checked on macOS.

### What would overturn it

- A maintained pure-Go Hrana **client** gains a `NetDial`/`http.Client` hook and
  the 16 MB read limit is lifted (`websockets.go@208`) — then option A becomes
  viable.
- `cornejong/hrana` gains an OSI licence — option B (reuse the server) becomes
  viable, though it still leaves R2 open.
- Turso's `multiprocess_wal` stabilises: #1853 closes, #9222 merges, #9362 is
  fixed (**all three are open as of 2026-09-29**, see Sources) — then #466's
  option (a) returns and the endpoint is unnecessary.
- A measurement shows gob materially beats JSON at relevo's body sizes — that
  swaps the frame codec, not the design.

**Q5 verdict:** build the custom minimal protocol (D); it is the only option that
satisfies all five requirements or preserves `BEGIN IMMEDIATE` for free, and its
risks are the ones #680 already lists.

---

## Premise audit

### #680 (checked at `dd2abcb6` by the issue, re-checked today)

`git merge-base --is-ancestor dd2abcb6 HEAD` → **yes**; `dd2abcb6` is an ancestor
of `f74996fa` (7 commits back). Every citation is checked twice: against
`git show dd2abcb6:<path>` and against the worktree.

| # | sentence of #680 | verdict | evidence |
|---|---|---|---|
| 1 | "#466 … is blocked on challenge 1: several OS processes open `relevo.db` at once" | **verified** | #466's "Challenges Part 1" §1 (fetched today) |
| 2 | "the daemon, `relevo mcp`, `relevo wait` and every CLI one-shot" | **verified** | `cmd/relevo/mcp.go:62` and `cmd/relevo/wait.go:35` both call `newRuntime()`; 53 non-test `newRuntime(` call sites at HEAD |
| 3 | "10 processes held the file at one random moment during #466's research" | **stale / not re-verifiable** | rests on #466's body alone; a snapshot of a laptop, not reproducible now |
| 4 | "Turso's default mode rejects a second opener, read-only included" | **external, partly verified** | #1853 open ("Multi-process support", `2025-06-27`); the *default-mode rejection* itself was not re-fetched, and #9362 describes the flag-set case instead. Marked **`(inferred)`** for today |
| 5 | "its multi-process mode is experimental with no schedule" | **verified** | #1853 open, no milestone; #466 quotes `docs/manual.md@156` "not production ready" |
| 6 | "#1853 sits in Backlog with no ETA" | **verified (open); "Backlog" `(inferred)`** | `gh api repos/tursodatabase/turso/issues/1853` → `"state":"open"`; the board column was not fetched |
| 7 | "the killed-writer fix is an unmerged PR (#9222)" | **verified** | `#9222` → `"state":"open"`, `"pull_request":true` |
| 8 | "a data-loss bug is open (#9362)" | **verified** | `#9362` → `"state":"open"`, title "…a process opening without the flag is not rejected, and its committed writes are lost" |
| 9 | "it is mutually exclusive with MVCC" | **verified against #466** | #466: "it can't be combined with MVCC" |
| 10 | "Checked 2026-09-29 at `dd2abcb6`" | **verified** | `dd2abcb6` is an ancestor of HEAD |
| 11 | "no local endpoint exists today" | **verified** | `grep`: no `net.Dial`/`net.Listen` under `cmd/relevo` or `internal/relevo`; no `unix.Socket`/`syscall.Socket` anywhere non-test; the only listener is `internal/serve/listen.go:41` |
| 12 | "`relevo serve`'s listener is TCP … (`internal/serve/listen.go:41`)" | **verified, citation correct at both** | `net.Listen("tcp", addr)` at `internal/serve/listen.go:41` at `dd2abcb6` and HEAD |
| 13 | "`db.Open` (`cmd/relevo/wire.go:225`, `internal/store/db.go:28`)" | **verified at `dd2abcb6`, drifted at HEAD** | at `dd2abcb6`: `wire.go@225` = `return db.Open(path)`, `store/db.go@28` = `db.Open(s.DBPath())`; at HEAD the same statements are `cmd/relevo/wire.go` line 232 and `internal/store/db.go` line 36 |
| 14 | "`db.OpenReadOnly` (`cmd/relevo/wire.go@249`)" | **verified at `dd2abcb6`, drifted at HEAD** | `dd2abcb6` `wire.go@249` = `d, err = db.OpenReadOnly(...)`; at HEAD the statement is `cmd/relevo/wire.go` line 256 |
| 15 | "`db.DB` wraps `*sql.DB` (`internal/db/db.go:41`)" | **verified, correct at both** | `type DB struct {` at `internal/db/db.go:41` |
| 16 | "`store.Store`/`store.NewShared` hold a `*db.DB`" | **verified** | `internal/store/store.go:99-101` stores `d` as `shared`; the only production caller is `internal/serve/serve.go:146` |
| 17 | "53 `newRuntime()` call sites" | **verified** | `grep -rn 'newRuntime(' --include='*.go' \| grep -v _test.go \| wc -l` = **53** at HEAD (and per the planning pass, at `dd2abcb6`) |
| 18 | "41 direct `rt.DB` uses" | **verified with drift** | rule `grep -rnE '\brt\.DB\b' --include='*.go' \| grep -v _test.go \| wc -l` = **44** at HEAD (planning pass: 41 at `dd2abcb6`, 44 at its HEAD) |
| 19 | "321 `rt.Store` call sites" | **not reproduced** | same rule with `rt.Store` = **327** at HEAD. The planning pass also recorded 321/323. The counting rule is unstated; the figure is flagged, not confirmed |
| 20 | "`store.Store` **119 methods**" | **refuted** | at HEAD: 84 `func (s *Store)`, 103 `Store`+`Tx`, 111 all receiver methods in `internal/store` non-test. No rule tried reaches 119 (planning pass reached 83/102/110 at `dd2abcb6`). **Not reproduced under any rule tried** |
| 21 | "`db.DB` **65 methods**" | **verified** | rule "all methods with a `*DB` receiver in `internal/db` non-test" = **65** (61 exported). Reproduces exactly |
| 22 | "the db-backed runtime fields `Store`, `DB`, `Gates`, `Latency`, `Config`" | **verified** | `DB *db.DB` at `internal/relevo/runtime.go:146`; `Store`/`Gates`/`Latency`/`Config` set in `cmd/relevo/wire.go:354-383` |
| 23 | "`relevo mcp` and `relevo wait` … both call `newRuntime()` and hold the database for their lifetime" | **verified** | `cmd/relevo/mcp.go:62`, `cmd/relevo/wait.go:35` |
| 24 | "`AcquireDaemonLock` (`internal/store/daemonlock.go:24`)" | **verified, correct at both** | `func (s *Store) AcquireDaemonLock()` at `internal/store/daemonlock.go:24` |
| 25 | "the daemon's upgrade/re-exec machinery (#371)" | **verified** | `ErrReexec` and `WithUpgrade` at `internal/relevo/daemon.go:41` and `:97` |
| 26 | "`--check`/`--preflight` (`cmd/relevo/daemon.go:46`)" | **verified at HEAD; one line low at `dd2abcb6`** | at HEAD `cmd/relevo/daemon.go:46` = `if *preflight || *check {`; at `dd2abcb6` the statement is line 47 and line 46 is its comment |
| 27 | "`relevo serve` embeds a daemon … `cmd/relevo/serve.go:98-113, 303-352`" | **verified, drifted** | at HEAD `openMachineDB` is `cmd/relevo/serve.go:102`, `loadConfig` is `:306`, `loadServeConfig` is `:343`; the spans are right, the numbers moved |
| 28 | "releases are linux/darwin on amd64/arm64 only (`release.yml@36`)" | **verified, correct** | `.github/workflows/release.yml:36` = `for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do` |
| 29 | Design shape, Risks, Size, Chain, Out of scope | **design text, not auditable** | one countable claim inside: "a typed `store`/`db` proxy (**184+ methods**)". 84+65 = 149, 103+65 = 168, 111+65 = 176 — **184 does not reproduce** under the rules above. Flagged |

### #466 (base date 2026-09-25) — citation drift

`#466` was checked 2026-09-25; the tree's HEAD on that date was `6677854`
(`git log --before='2026-09-26' -1`). None of its `internal/db` line citations
resolve at `6677854`, at `dd2abcb6`, or at HEAD:

| #466 citation | what is there at `dd2abcb6` | at HEAD |
|---|---|---|
| `db.Open` "`internal/db/db.go:54`" | line 54 is a comment; `func Open` is line 70 | `func Open` at `internal/db/db.go:79` |
| `db.OpenReadOnly` "`internal/db/config.go:217`" | line 217 is `}`; `func OpenReadOnly` is line 188 | `func OpenReadOnly` at `internal/db/config.go:188` |
| `mapBusy` "`db.go@246`" | `func mapBusy` is line 285 | `internal/db/db.go:307` |
| `mapPlannerKey` "`write.go@235`" | **`mapPlannerKey` does not exist at `dd2abcb6`**; `func mapMasterMindKey` is line 217 | `internal/db/write.go:220` |
| `DB.Vacuum` "`db.go@180`" | `func (d *DB) Vacuum` is line 204 | `internal/db/db.go:223` |
| `BEGIN IMMEDIATE` "`db.go@221-227`" | `type Tx struct` is `dd2abcb6` lines 221-227 | `internal/db/db.go:243-249` |

**Stale name (`mapPlannerKey`).** It is gone from code — `grep -rn mapPlannerKey`
over `*.go`/`*.md` finds **no Go file** and exactly two docs:
`docs/specs/2026-09-20-persistence-design.md:57` and
`internal/db/migrations/README.md:37`. The plan named only the first; the second
is the file that *binds* migrations, so it matters more. `#466`'s body also uses
it. The rename to `mapMasterMindKey` landed in `72a90e3` (#633); `dd2abcb6`
(#679) touched the name only in text.

**The 10-process note** rests on `#466`'s body alone — historic, not
re-verifiable, exactly as the plan says.

---

## Sources

External facts, each with the exact command and the date **2026-09-29**. Scratch
copies live under `/tmp/opencode/db-owner-r1-protocol/`.

```
gh api repos/tursodatabase/libsql-client-go --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description,default_branch}'
gh api repos/tursodatabase/libsql-client-go/tags --jq 'length'
gh api repos/tursodatabase/libsql-client-go/releases --jq 'length'
gh api repos/tursodatabase/libsql-client-go/contents/README.md --jq '.content' | base64 -d | grep -n -i deprecat
gh api repos/tursodatabase/libsql-client-go/contents/libsql/sql.go --jq '.content' | base64 -d | nl -ba | sed -n '25,230p'
gh api repos/tursodatabase/libsql-client-go/contents/libsql/internal/http/hranaV2/hranaV2.go --jq '.content' | base64 -d | grep -n 'pipeline\|DefaultClient\|baton'
gh api repos/tursodatabase/libsql-client-go/contents/libsql/internal/ws/websockets.go --jq '.content' | base64 -d | grep -n 'Dial\|Subprotocol\|ReadLimit'
gh api repos/tursodatabase/libsql-client-go/contents/libsql/internal/hrana/value.go --jq '.content' | base64 -d | nl -ba | sed -n '1,25p'
gh api repos/WillowWorks-io/libsql-client-go --jq '{archived,license:.license.spdx_id,pushed_at,created_at,stars:.stargazers_count,fork,description}'
gh api repos/WillowWorks-io/libsql-client-go/contents/libsql/internal/ws/websockets.go --jq '.content' | base64 -d | grep -n 'Dial\|Subprotocol\|ReadLimit'
gh api repos/WillowWorks-io/libsql-client-go/contents/libsql/internal/http/hranaV2/hranaV2.go --jq '.content' | base64 -d | grep -n 'pipeline\|DefaultClient\|baton'
gh api repos/tursodatabase/go-libsql/contents/libsql.go --jq '.content' | base64 -d | nl -ba | sed -n '1,30p'
gh api repos/tursodatabase/turso-go --jq '{archived,license:.license.spdx_id,pushed_at,description,stars:.stargazers_count}'
gh api repos/tursodatabase/turso/contents/bindings --jq '.[].name'
gh api repos/cornejong/hrana --jq '{license,created_at,pushed_at,stars:.stargazers_count,description,archived}'
gh api repos/cornejong/hrana/contents --jq '.[].name'
gh api repos/cornejong/hrana/contents/go.mod --jq '.content' | base64 -d
gh api repos/cornejong/hrana/contents/readme.md --jq '.content' | base64 -d | head -40
gh api repos/cornejong/hrana/contents/hrana.go --jq '.content' | base64 -d | grep -n 'func'
gh api repos/cornejong/hrana/contents/executor.go --jq '.content' | base64 -d | grep -n 'func'
gh api repos/tursodatabase/libsql/contents/docs/HRANA_3_SPEC.md --jq '.content' | base64 -d > f ; wc -l f ; grep -n -i cancel f
gh api repos/tursodatabase/libsql/contents/docs/HRANA_1_SPEC.md --jq '.content' | base64 -d | wc -l
gh api repos/tursodatabase/libsql/contents/docs/HRANA_2_SPEC.md --jq '.content' | base64 -d | wc -l
gh api repos/tursodatabase/libsql/contents/docs/HTTP_V1_SPEC.md --jq '.content' | base64 -d | wc -l
gh api repos/tursodatabase/libsql/contents/docs/HTTP_V2_SPEC.md --jq '.content' | base64 -d | wc -l
gh api repos/tursodatabase/libsql/contents/proto            # → HTTP 404
gh api repos/tursodatabase/libsql/contents/libsql-hrana/src --jq '.[].name'
gh api repos/tursodatabase/libsql/contents/libsql-hrana/src/proto.rs --jq '.content' | base64 -d | wc -l
gh api 'search/code?q=extension:proto+repo:tursodatabase/libsql' --jq '.items[].path'
gh api repos/tursodatabase/turso/issues/1853 --jq '{number,title,state,created_at,pull_request:(.pull_request!=null)}'
gh api repos/tursodatabase/turso/issues/9222 --jq '{number,title,state,created_at,pull_request:(.pull_request!=null)}'
gh api repos/tursodatabase/turso/issues/9362 --jq '{number,title,state,created_at,pull_request:(.pull_request!=null)}'
gh api repos/benbjohnson/postlite --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/jeroenrinzema/psql-wire --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/dolthub/go-mysql-server --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/maxpert/marmot --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/rqlite/rqlite --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/rqlite/gorqlite --jq '{archived,license:.license.spdx_id,pushed_at,stars:.stargazers_count,description}'
gh api repos/rqlite/gorqlite/contents/stdlib/sql.go --jq '.content' | base64 -d | sed -n '1,45p'
gh api repos/rqlite/gorqlite/contents/gorqlite.go --jq '.content' | base64 -d | grep -n 'func Open'
gh api repos/rqlite/gorqlite/contents/conn.go --jq '.content' | base64 -d | sed -n '218,300p'
gh issue view 680 -R fuad-daoud/relevo --json number,title,state,createdAt,body
gh issue view 466 -R fuad-daoud/relevo --json number,title,state,createdAt,body
```

Repo facts are `path:line` in the sections above and were resolved with
`sed -n '<line>p' <path>` from the worktree at `f74996fa`.

---

## Honest gaps

- **`origin/main` is not resolvable locally** — the tree has no git remote, so
  the plan's "3 commits ahead" was verified by commit walk
  (`git log --oneline 442fc598..HEAD`), not by a ref.
- **The round-1 prompt could not be re-read.**
  `relevo show db-owner-r1-protocol --round 1 --prompt` returns
  `relevo show: binding "db-owner-r1-protocol" not found (live or in the
  database)`; no `003-prompt.md` exists on disk. The five questions were taken
  verbatim from the plan handed to this runner. The plan's provenance claim for
  them is therefore **not independently verified**.
- **Two counts did not reproduce**: `store.Store` "119 methods" (84/103/111 at
  HEAD) and "184+ methods" in #680's design shape (149/168/176). The `rt.Store`
  figure differs from the plan's (327 vs 323) because HEAD moved on and the rule
  is unstated.
- **No single-row maximum body size is recorded**, so the plan's "157 MB row"
  premise is refuted as written; the disqualifier test is conditional and marked
  `(inferred)`.
- **Default-mode Turso rejection** ("read-only included") was not re-fetched
  today; only #1853/#9222/#9362 were.
- **`cornejong/hrana`'s internals were read shallowly** (`hrana.go`,
  `executor.go`, README, go.mod); its `stream.go`/`baton.go` were not read, so
  its R1/R2/R3 cells are marked `(inferred)`. It is anyway disqualified on
  licence.
- **Turso release tags** (`tursodatabase/turso` version numbers) were not
  re-fetched; the codecs and dialect claims come from #466 and
  `internal/db/migrations/README.md`.
