# Round tokens across builder switches

**Status:** approved 2026-09-24. Plan: `docs/plans/2026-09-24-round-tokens-segments.md`.
**Depends on:** #430 (round clock and round tokens on the status line), #440 (remote live block).

## 1. Problem

The status line's round clock covers the whole round, from the round's first
plan entry. Its token cell only covers the builder running *now*. On
2026-09-24, `rl-live` r1 ran 18 minutes across three builders (agy gemini,
then agy sonnet, then opencode). The row showed 171k tok, which was opencode's
last 47 s alone. Three causes:

1. **A switch records nothing.** `switchBuilder` (internal/relevo/switch.go)
   writes a switch entry without usage, so the outgoing builder's tokens are
   never recorded anywhere. Not for the live figure, not for the closed round,
   not for `Spend`.
2. **The stream has no per-process boundary.** Every builder in a round
   appends to the same `NNN-builder.jsonl`. `StreamOffset` is the log render
   cursor, which a switch keeps. The usage reader parses the *whole* file with
   the *current* harness's parser. A switch between two builders of the same
   harness would double-count, and a switch across harnesses silently drops
   the earlier builder.
3. **`RoundStartedAt` restarts on a switch**, so the usage window and
   `DurationMS` cover the current builder only.

## 2. Design

- **Per-process stream start.** `store.Endpoint.StreamStart int64` is the
  byte length of the round's stream when this process was spawned. It is set in
  `startProcess`. `usage.Source.StreamFrom` makes the reader parse only bytes
  at or after it. Every read for "this builder" uses it: live peek, the close,
  and the switch record.
- **The outgoing builder's usage goes on the switch entry.** Before the
  replacement starts, relevo reads the outgoing builder's segment (a peek,
  without waiting) and stores it as `Usage` on the `KindSwitch` entry. The
  daemon-restart relaunch (headless.go) does the same for the process it lost.
- **The round's tokens are the sum of its segments.** `BindingStatus` gains
  `RoundPriorTokens usage.Tokens`: the sum over the current round's switch
  entries (and, for a remote round, the prior tokens the server reported). The
  status line shows `(live or closing-report tokens) + RoundPriorTokens`.
- **`Spend` counts segments without counting rounds.** Switch-entry usage
  adds tokens, steps, tool calls and measured/estimated dollars. It never adds
  to `Rounds`, `Plan` or `Unknown`, which count rounds.
- **Remote.** The server computes the same `RoundPriorTokens` for its own row.
  `LiveView` carries `prior_tokens` while the round runs. `BindingView`
  carries `prior_tokens` for the closed round. The client stores the closed
  figure on the report entry it writes (`LogEntry.PriorTokens`), so a closed
  remote round keeps its whole-round total. The client cannot price another
  machine's earlier builders, so remote dollars stay last-builder-only. Tokens
  are complete.
- **Store format.** `BindingFormat` 3 → 4 for `builder.stream_start`, and it is
  never stamped, the same rule as `remote_live`. An older relevo that drops it
  on a rewrite makes the current process's reads start at byte 0. That is an
  over-count on a same-harness switch in that one round, never lost data.

## 3. Not changed

The round clock, `RoundStartedAt`'s budget and grace semantics, consult usage,
and pane-mode sources.
