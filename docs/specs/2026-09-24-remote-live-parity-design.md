# Remote builder parity: see a remote round the way you see a local one

**Status:** approved 2026-09-24. Round 1 plan is `docs/plans/2026-09-24-remote-live-r1.md`; the round 2 plan follows.
**Depends on:** #430 (the status line redesign; `BindingStatus.Server`).

## 1. Problem

While a local headless round runs, `relevo status`, the status line and
`relevo ui` show live tokens, a live diff stat, progress (quiet for,
exploring), the process (pid, since, exit code, log tail) and a builder word
(working, exploring, gating, exited N). A remote round shows only the server's
round state (running, queued, stalled) and a log mirror that is re-downloaded
whole on every daemon tick.

The server already computes all of it. For the server, a remote round is a
local headless round. `AdminStatus` (`internal/serve/admin.go`) runs the
ordinary `relevo.Status` on a runtime with Git, Runner and Usage wired in. What
reaches the client is only `remote.BindingView` (`GET /v1/bindings/{name}`).
The client then points the local readers at laptop state: `peekUsage` at a
stream path it does not have, `liveStat` at the laptop's repo with no baseline.

## 2. Principle

**The server's own row is the source of truth for a running remote round.**
The client does not reconstruct it. The server sends the live facts of its
row, and the client row shows them through the same fields a local row uses.
Every surface that renders those fields works unchanged: status line, rail,
pane header, `relevo status`.

## 3. Round 1: the live block

- **Wire.** `remote.BindingView` gains `Live *remote.LiveView`
  (`json:"live,omitempty"`). The server fills it only when the round state is
  `running`. It holds pid, started_at, exit_code, the log tail, the live
  usage, the live diff stat, last_progress_at, exploring_since, gating_since
  and the time it was built (`at`). Server paths never cross the wire.
- **Server cost.** The block is built *after* the handler releases `s.mu`,
  because it runs git and the usage reader. It is cached per (caller, binding,
  round) for 2 s, which is the client daemon's poll interval.
- **Client store.** `store.Endpoint` gains `RemoteLive *store.LiveFacts`,
  copied from the view on each poll in the running case. Every other round
  state clears it, the same way `RemoteQueue` is cleared. An unreachable server
  leaves the last facts in place; the row already says `unreachable`.
- **Client row.** The remote branch of `statusRow` fills `Headless`,
  `LiveUsage`, `Live`, `LastProgressAt` and `Exploring` from `RemoteLive`. It
  derives the builder word by the same precedence `headlessStatus` uses:
  stalled, then exploring, then gating, then exited, then working. For a
  remote row `statusRow` no longer calls `peekUsage` or `liveStat`: those read
  laptop state and could match an unrelated local session.
- **Labels.** A row with `Headless` set now means a builder *process*, local
  or remote. Where a renderer prints the word `headless`, it prints `remote`
  when `Server != ""`: `relevo status`, the rail tag, and the pane footer.
- **Compatibility.** An older server omits `live`, and the client shows what
  it shows today. An older client ignores the field. No feature flag is
  needed, because the client makes no new request.

## 4. Round 2: the live transcript

- The log file route takes `?from=<byte offset>` for a running round and
  returns only the bytes after it. A new `whoami` feature,
  `log_offset`, advertises this, because an older server would ignore the
  parameter and return the whole file. The client keeps an offset per
  (binding, round) and appends to the mirror instead of rewriting it.
- The server serves `drift` as a file kind, and the client fetches it once
  after the round starts.

Mirroring the server's log entries is dropped: the server already writes
relevo's own events (exit, switch, gate) into the builder log as
`--- relevo HH:MM:SS: … ---` markers (`appendLogMarker`), so the tailed log
shows them live. Injecting server entries into the client's own log would feed
reconcile logic that keys on log entries (`HasEntry`, switch counting).

## 5. Not changed

Closed-round catch-up (`catchUp`), `BindingView`'s closed-round fields, the
queue view, stall copying (`StalledSince`), and every local-builder code path.
