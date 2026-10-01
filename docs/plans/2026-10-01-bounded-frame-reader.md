# Bound the owner frame reader: no allocation from a declared length, a 64 MiB cap, and a live-connection bound (#789)

The owner protocol reads length-prefixed frames through one shared
`(*Conn).Read` in `internal/db/wire`, used by both the owner and the client.
Two properties of that path were unbounded, and both are fixed here without
changing the frame shape or any client.

## 1. The bug, confirmed in the code

1. `(*Conn).Read` read the 4-byte length, checked it, and then did
   `make([]byte, n)` before the first payload byte arrived. A peer sending one
   header declaring a large length made the daemon allocate that much and hold
   it while the connection stayed open.
2. `maxConns` bounded only the *pinned* slot taken in `pin`. Every accepted,
   uid-matching connection was tracked in `s.conns` with no bound, and the
   handshake never takes a pinned slot, so idle or handshaken connections could
   be opened without limit.
3. Both roles read through the same `(*Conn).Read`, so the read fix lands for
   both roles without touching client code.

## 2. Decisions

**D1. `(*Conn).Read` never allocates from the declared length.** After the
unchanged `n == 0 || n > maxFrameLen` refusal, a frame at or below one chunk
(`readChunk = 64 KiB`) keeps today's exact-`n` fast path. Above one chunk the
payload is read in bounded pieces: no single read request handed to the
underlying `io.ReadWriter` exceeds the chunk, and the result grows only with
bytes that actually arrived. Any error mid-payload returns immediately. This is
the same code for client and owner.

**D2. `maxFrameLen` is 64 MiB (`64 << 20`), a deliberate protocol bound.** A rows
batch aims at `BatchBudget` (1 MiB) and appends whole values, so the batch is
bounded only by the largest value in it; the largest documented per-value caps
in the tree are the 16 MiB JSON-RPC line (`internal/mcp/server.go`) and the
16 MiB usage line (`internal/usage/claude.go`). 64 MiB is a 4x margin on those.
Session-journal transcript lines have no smaller bound -- the ingest tail is
read whole (`internal/ingest/cursor.go`) and the line is stored whole
(`internal/ingest/transcript.go`) -- so one value above the cap, or a rows batch
that contains one, is now refused by the protocol rather than carried. `Write`
and `Encode` keep refusing above the cap and inherit the lower number.

**D3. The owner caps live connections, not only pinned ones.**
`maxLiveConns = 256` (4x the pinned cap) covers every connection in `s.conns`:
handshaken, idle, pinned, or still waiting for hello. The check and the
insertion happen in one `s.mu` section, so N concurrent accepts cannot exceed
it. Over the bound the accepted connection is closed without any frame; an
existing connection keeps working, and the client's existing open/retry path
copes. The pinned cap is unchanged: the handshake still never waits on it, and
an over-pinned-cap request still waits. The bound is captured in `New` into a
`Server` field, exactly as `maxConns` fills `sem`, so a test can shrink the
package var before `startServer` without racing the accept goroutines.

Frame shape, `Version`, `Proto`, message types, client behaviour, and
`Write`/`Encode` semantics beyond the shared constant are unchanged. No read
deadlines, no serve/HTTP/TLS change.

## 3. Live-cap arithmetic

- `maxConns = 64` pinned slots: the real database work a set of clients can
  hold in flight at once.
- `maxLiveConns = 256` live connections: 4x the pinned cap, so a burst of
  clients can connect and handshake while the pinned slots serialise the work.
- Worst case of *arrived* bytes: 256 live connections x 64 MiB per declared
  frame is ~16 GiB, but the chunked read makes the cost proportional to bytes
  actually sent rather than bytes declared. A peer that declares and sends
  nothing costs no more than one chunk.

## 4. Tests

`internal/db/wire/wire_test.go`:

| test | pins |
|---|---|
| `TestReadOfADeclaredHugeFrameBoundsEveryReadRequest` (header then EOF; header, one byte, then blocked) | Every read request the fake `io.ReadWriter` sees is at most one chunk, and a peer that goes away or blocks makes `Read` return an error without failing the blocked peer early. The fake generates payload bytes into the caller's buffer and holds no source buffer proportional to the declared length. |
| `TestFrameAtTheCapIsAccepted` | A declared frame exactly at `maxFrameLen` is accepted with payload length exactly `maxFrameLen`, every request at most one chunk. |
| `TestFrameOverTheCapIsRefusedFromTheHeaderAlone` | A declared length of `0` and of `maxFrameLen + 1` is refused from the 4-byte header alone: exactly one read request (the header), never a payload read. |
| `TestFrameAcrossTheChunkBoundaryKeepsTheStreamAligned` | A `readChunk + 1` frame returns exactly its bytes and leaves the stream aligned for the next frame. |

`internal/db/wire/owner/owner_test.go`:

| test | pins |
|---|---|
| `TestLiveConnectionCapDropsTheExtra` | With `maxLiveConns` shrunk to 1, an idle handshaken connection holds the slot, the next dial is dropped (its hello is never answered), and the holder is still served its `done`. No `t.Parallel`: the test mutates a package var that `New` has already captured. |

Existing tests stay green and are complemented, not replaced:
`TestFrameRejectsMalformedLength`, `TestBlobOfFourAndAHalfMegabytes`,
`TestBatchBuilderCursorBoundaryKeepsEveryValueWhole`, the client's
`TestDriverRoundTripsAValueLargerThanTheBatchBudget`, the owner's
`TestConnectionCapWaitsInsteadOfRefusing`, `TestFiveMegabyteBlobRoundTrips` and
`TestFiftyMegabyteResultStreamsInBatches`.

## 5. Mutations

| # | condition broken | named failing test | result |
|---|---|---|---|
| M1 | `make([]byte, n)` before reading the payload | `TestReadOfADeclaredHugeFrameBoundsEveryReadRequest` | fails: `largest read request = 67108864, want at most the 65536-byte chunk` |
| M2 | the `n > maxFrameLen` refusal dropped | `TestFrameOverTheCapIsRefusedFromTheHeaderAlone` | fails: `Read accepted declared length 0` and `2 read requests, want only the header` |
| M3 | the live-conn bound dropped in `addConn` | `TestLiveConnectionCapDropsTheExtra` | fails: `owner served a connection over the live bound` |

Each mutation was applied alone, the named test run, the mutation reverted, and
the suite re-run green.

## 6. Commands run

```
go test -race -count=1 ./internal/db/wire/
go build ./...
go test -race -count=1 -v ./internal/db/wire/ -run '<the four wire tests>'
go test -race -count=1 -run TestReadOfADeclaredHugeFrameBoundsEveryReadRequest ./internal/db/wire/   # M1, fails
go test -race -count=1 -run TestFrameOverTheCapIsRefusedFromTheHeaderAlone ./internal/db/wire/       # M2, fails
go test -race -count=1 ./internal/db/wire/owner/
go test -race -count=1 ./internal/db/wire/owner/ -run TestLiveConnectionCapDropsTheExtra
go test -race -count=1 ./internal/db/wire/owner/ -run TestLiveConnectionCapDropsTheExtra             # M3, fails
go test -race -count=1 ./internal/db/wire/...
go test -race -count=1 ./internal/db/...
make check
```

## 7. Risks and knowingly not done

- **Values above 64 MiB are refused by the protocol.** Rows batches append whole
  values and session-journal transcript lines have no smaller bound, so a single
  transcript value above the cap (or a batch containing one) can no longer cross
  the wire. This is the deliberate cost of D2. No smaller transcript bound is
  invented this round.
- **Residual DoS arithmetic.** 256 live connections x 64 MiB per declared frame
  is still ~16 GiB of *arrived* bytes at the worst case; the chunked read makes
  the cost proportional to bytes actually sent rather than declared, and read
  deadlines are deliberately out of scope.
- **The request-size test pins requests, not allocations.** A contrived mutant
  that pre-allocates `n` but still reads in chunks would pass; the named mutation
  (the naive `make([]byte, n)` plus one `io.ReadFull`) is the one pinned.
- **A burst above 256 concurrent live clients is dropped instead of served.**
  The pinned cap already serialises real database work, so 256 is 4x headroom.
  If a test or deployment exceeds it, that is a signal, not a reason to raise the
  cap silently.
- **Not done:** read deadlines; serve/HTTP/TLS; a protocol version bump;
  anything under `internal/db/` outside `wire/`; client behaviour; any
  `make`/encode semantics beyond the shared constant.

## 8. Correction round: pin the allocation, not only the read-request size

Section 7 named the gap and it was real: the request-size tests pin the sizes
handed to the stream, not what the reader allocates. A mutant that keeps the
chunked reads but reserves the declared length up front in `(*Conn).Read` --
changing `payload := make([]byte, 0, readChunk)` to
`payload := make([]byte, 0, int(n))` -- passed every check, because a peer that
declares 64 MiB and sends nothing then reserves 64 MiB on that connection, and
the live cap allows 256 of them.

`TestReadOfADeclaredHugeFrameDoesNotAllocateIt` closes it by measuring memory
instead of request sizes. A fake source declares `maxFrameLen` and serves a
1 KiB prefix before EOF, so the measured window is one short failing `Read`;
`runtime.ReadMemStats` is sampled before and after and the `TotalAlloc` delta
must stay under 8 MiB. The shipped reader allocates about one chunk for this
frame; the mutant adds the 64 MiB declared length. The budget is an order below
that 64 MiB and far above the delta a single-goroutine, non-parallel package
sees, so the test fails on the regression and not on the allocator.

`wire.go` is unchanged: the fix is a test that fails when the allocation
regresses, not a change to the reader.
