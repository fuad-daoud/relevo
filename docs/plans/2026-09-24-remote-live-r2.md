# Plan: remote builder parity, round 2 (live transcript: log tailing, drift)

Spec: `docs/specs/2026-09-24-remote-live-parity-design.md` §4 (in this tree, merged in #440). Read it first.
Round 1 (#440) is merged into the base of this branch.

**If a step is impossible as written or contradicts the code, stop and report.
Do not bend a test to fit.** In particular, stop if any of these is true:
(a) the server's builder log for a running round is not append-only (it is
    written by `drainFile`/`appendLogMarker` in internal/relevo/headless.go,
    both appending; verify);
(b) the request signing in `internal/remote/client` does not cover the query
    string. `RoundBundle` already sends `?since=`, so it should.

## 0. Scope change from the spec (planner's decision)

Spec §4's second bullet ("mirror the server's log entries") is **dropped**. The
server already writes relevo's own events (exit, switch, gate) into the builder
log as `--- relevo HH:MM:SS: … ---` markers (`appendLogMarker`), so the tailed
log shows them live. Injecting server entries into the client's own log would
feed reconcile logic that keys on log entries (`HasEntry`, switch counting).
Update the spec's §4 to say this, in one short paragraph, as part of step 5.

## 1. Overview

Today, while a remote round runs, the client re-downloads the whole builder log
on every daemon tick (`observeRemote`, `case remote.RoundRunning`,
internal/relevo/remote.go around line 800–820) and writes it with
`writeTempAndRename`. Drift is never fetched for a remote round. This round
adds three things:
- **Offset tailing.** The server honours `?from=<bytes>` on the log file route
  and says so in response headers. The client appends only the new bytes to
  its mirror.
- **Old servers.** A server that ignores `from` (it sends no headers) gets
  today's whole-file replacement.
- **Drift.** The server serves `drift` for a running round, and the client
  fetches it once per round into its own drift round file.

## 2. Files

```
internal/remote/proto.go            MODIFY  header name constants
internal/serve/rounds.go            MODIFY  handleRoundFile: ?from= for log; drift allowed for running rounds
internal/remote/client/client.go    MODIFY  RoundFileFrom
internal/relevo/runtime.go          MODIFY  Remote interface gains RoundFileFrom (line ~315)
internal/relevo/remote.go           MODIFY  mirrorLog + mirrorDriftOnce, used in the running case
docs/specs/2026-09-24-remote-live-parity-design.md  MODIFY  §4 per §0
tests: internal/serve (rounds or serve_test), internal/remote/client (client tests), internal/relevo/remote_test.go (+ every fake Remote in the repo gains the method)
```
Nothing else changes. `catchUp` (the closed-round fetch) is untouched: at close
it still rewrites the whole log, which also repairs any mirror drift.

## 3. Data / wire

- `remote.HeaderFileSize = "X-Relevo-Size"`: the file's total byte length on the server.
- `remote.HeaderFileFrom = "X-Relevo-From"`: the offset the server honoured.
- `remote.FileRange struct { Honored bool; From int64; Size int64 }`. `Honored`
  is true only when both headers are present and parse as non-negative ints.

## 4. Contracts

### Server: `handleRoundFile` (internal/serve/rounds.go; the switch on `kind` is at ~line 362, the closed-round gate at ~379)
- **`from` on `log`.** For `kind == "log"`, an optional query `from`. When
  present it must parse as an int64 ≥ 0; otherwise the response is
  `400 remote.CodeInvalid "invalid from"`. After `ReadFile` succeeds:
  - `size := len(data)`, and both headers are always set on a `log` response
    (with or without `from`).
  - With `from`: the body is `data[from:]` when `from <= size`, else an empty
    body. `X-Relevo-From` is set to `from`.
  - Without `from`: the body is the whole file, as today, and `X-Relevo-From`
    is `0`.
- **Drift for a running round.** The closed-round gate
  (`if kind != "log" { … n > ClosedRound → 404 }`) now also exempts `"drift"`.
  Add `case "drift": path = rt.Store.DriftPath(name, n)`. Drift is written once
  at send and never changes. Verify that a served round's `relevo.Send` writes
  it (`CaptureDrift`, called from send.go). If the server path never writes
  drift, still add the kind (a 404 is harmless), but say so in the report.
- Every other kind's behaviour is unchanged.

### Client: `(*Client) RoundFileFrom(ctx, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error)`
Model it on `RoundFile` (client.go ~493), with the path plus `?from=<from>`. It
returns the body under the same deadline wrapper, plus the parsed `FileRange`.
Non-2xx statuses behave exactly as `RoundFile` treats them today.
Add the method to the `Remote` interface (internal/relevo/runtime.go ~315), and
to every fake that implements that interface. Find them with
`grep -rn 'func (f \*fakeRemote) RoundFile\|) RoundBundle(ctx' --include=*_test.go`.

### Client: `mirrorLog(ctx, rt Runtime, server, name string, round int)` (remote.go, new)
```
path  := rt.Store.BuilderLogPath(name, round)
local := size of path on disk (os.Stat; missing → 0)
rc, fr, err := rt.Remote.RoundFileFrom(ctx, server, name, round, "log", local)
err → slog.Warn as today, return
switch:
  fr.Honored && fr.From == local && fr.Size >= local:
      append rc's bytes to path (O_APPEND|O_CREATE, 0o644)       // the normal case
  fr.Honored && fr.Size < local:
      close rc; refetch with from=0; writeTempAndRename(path, body) // server file shrank or was replaced
  default (not honoured: an older server sent the whole file):
      writeTempAndRename(path, rc)                                  // today's behaviour
```
The `case remote.RoundRunning` code keeps everything before its log mirror
(StalledSince, the RemoteLive copy). It replaces the `RoundFile(... "log")` and
`writeTempAndRename` block with `mirrorLog(...)`, then calls `mirrorDriftOnce`,
then `return b, false, nil` as today.

### Client: `mirrorDriftOnce(ctx, rt Runtime, tx *store.Tx, server, name string, round int)` (remote.go, new)
- If `rt.Store.ReadFile(rt.Store.DriftPath(name, round))` succeeds, return.
- A package-level `sync.Map` keyed by `name + "/" + round` records attempts.
  If the key is already there, return. Otherwise store it before fetching.
  This makes it one attempt per round per daemon process, so a server with no
  drift is asked once, not every 2 s.
- `rc, err := rt.Remote.RoundFile(ctx, server, name, round, "drift")`. On an
  error (including a 404), `slog.Debug` and return.
- Read it all, then `tx.PutRoundFile(name, round, rt.Store.DriftPath(name, round), data)`.
  On an error, `slog.Warn`. This is the write `CaptureDrift` uses since #432
  (internal/relevo/drift.go).

## 5. Steps

1. Wire constants plus the server change. Tests next to the existing
   round-file tests in internal/serve (grep `handleRoundFile` or `files/log` in
   `*_test.go`), using `setupTestEnv` and `sendRound` for a running round:
   - `TestRoundFileLogFrom`: a GET of the log with no `from` gives the whole
     body, `X-Relevo-Size` = len and `X-Relevo-From` = 0. `?from=k`, with k
     inside the file, gives the suffix and From = k. `?from=` past the end
     gives an empty body with Size = len. `?from=-1` and `?from=x` give 400.
     (Write at least a few bytes to the server's log first if the fake
     builder writes none; `appendLogMarker` on the log path is fine.)
   - `TestRoundFileDriftRunning`: a running round with a drift file present
     gives 200 and its bytes. With none, 404 "file not found", not "round 1 is
     not closed". A running round's `report` is still 404 "not closed", which
     pins that the gate is only widened for drift.
2. Client `RoundFileFrom`, following the client package's existing
   httptest-based tests: headers present → `Honored`, with the From and Size
   values; headers absent → `Honored == false`.
3. `mirrorLog` and `mirrorDriftOnce`, wired into the running case. Tests in
   remote_test.go, modelled on `TestObserveRemoteCopiesStalledSince`. The
   fakeRemote gains a scriptable `RoundFileFrom` that records the `from`
   values it is asked for.
   - `TestMirrorLogAppends`: the local mirror holds "a\n"; the fake is honoured
     with From = 2, Size = 4 and body "b\n". The mirror becomes "a\nb\n" and the
     requested from was 2.
   - `TestMirrorLogOldServerReplaces`: not honoured, body "whole\n". The mirror
     becomes "whole\n".
   - `TestMirrorLogShrankRefetches`: the mirror holds 10 bytes; the first
     answer is honoured with Size = 4. It refetches from 0, and the mirror
     equals the second body.
   - `TestMirrorDriftOnce`: the first running poll fetches drift and it is
     readable via `rt.Store.ReadFile(DriftPath)`. A second poll makes no
     drift request. A fake answering 404 is asked once across two polls.
     Reset the package-level map between tests, or key the test bindings
     uniquely.
   - Existing running-case tests that script `roundFileResp` for the log:
     port them to the new method, name each one in the report, and keep their
     assertions.
4. Mutation checks (run each, report it, revert): (1) always replace instead
   of appending → `TestMirrorLogAppends` fails; (2) drop the `sync.Map` check →
   the 404-once assertion fails; (3) in the server, ignore `from` →
   `TestRoundFileLogFrom` fails.
5. Update spec §4 per §0. Then the full check by its parts:
   `gofmt -l $(git ls-files '*.go')` (it must print nothing), `go vet ./...`,
   and `go test -race -count=1 ./...`. `git diff --stat` must touch only the §2
   files. Make one commit:
   `feat(remote): tail a running remote round's log by offset; fetch its drift`.

## 6. Working efficiently

Read the §2 files once, in parallel, at the ranges given. Make each file's
edits in one call. Focused loop:
`go test ./internal/serve ./internal/remote/... ./internal/relevo -count=1 -run 'RoundFile|Mirror|ObserveRemote|Drift'`.
Fix everything it reports before the next run. Add no test in `cmd/relevo`,
because CI has no harness and no network.
