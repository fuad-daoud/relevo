# Status line redesign: a round clock, round tokens, no money, the server

**Amends:** docs/specs/2026-09-13-statusline-design.md, decision 4 ("age is
time since the last relayed message") and the row anatomy of §1; and
docs/specs/2026-09-14-statusline-edges-design.md wherever it pins the right
cell as `age · STATE`.
**Scope:** `relevo status --line` only. `relevo status` (human), `--json`
(except four additive fields), and the `relevo ui` rail are unchanged.
**Status:** approved 2026-09-24; plan at `docs/plans/2026-09-24-statusline-redesign.md`.

**Amended 2026-09-28 by `docs/plans/2026-09-27-round-vocabulary.md`:** a
round's input is now its prompt; the current words are `prompt sent` and
`no prompt yet`, and the log kind is `prompt`. The sections below stay as
written.
## 1. What was wrong

Observed on a planner with seven bindings:

```
○ ck-w1   r1 · agy · plan sent · live unknown · 11.1M tok       15m · ACTIVE
○ ck-b1   r4 · opencode · report in · $0.35 · 36.7M tok         22m · ACTIVE
```

1. **The clock kept ticking after the report came in.** It was
   `now − LastPayload.TS`: during a round that is the round's elapsed time,
   but after the report it becomes "how long the report has sat", which reads
   as a round that is still running.
2. **ACTIVE said nothing.** It is `displayState(store.StateActive)`: the
   binding is open. A working builder ("plan sent") and an idle one whose
   report is in ("report in") both read ACTIVE.
3. **Money is not wanted**, and for a harness with no price (agy) the live
   figure rendered as the word `unknown` ("live unknown").
4. **Tokens meant two things.** A running row showed this round's live
   tokens; a closed row showed `Spend`, the sum over every round.
5. **A remote builder's server was not shown.**

## 2. The row

```
○ planner-policy  r1 · builder on agy@contabo · 11.8M tok  plan sent             16m
○ ck-b1           r4 · builder on opencode · 8.2M tok      REPORT IN            22m
● ck-x            r2 · builder on opencode · builder gone  NEEDS YOU             4m
○ docs            r2 · builder on agy                      PAUSED               23s
```

The row has three parts: the middle is identity, the status column is the one
status, and the clock is last.

- **Middle: identity.** The shown round (`r` + `report_round` when it is > 0,
  else `round`), the actor (`row.Actor`: the binding's role, `builder` when it
  stores none), and the harness cell (`on` + the candidate's harness segment;
  for a remote binding, `harness@server`, where server is the binding's
  configured server name, `Builder.Server`). Then the reason, when there is
  one, and the token cell.
- **Reason:** `waiting()` when the row needs you — `b.Detail` when it is set
  (for example `round 1 was open …`), else `report in`, which is the why of a
  stalled report. Otherwise `b.Detail` when it is set, else no reason at all.
- **Token cell:** this round's tokens only, `N tok` via `usage.ShortTokens`.
  While the round is open: `LiveUsage` (when it has samples). Once it has
  closed: the usage on *that round's* report entry. No usage for the round →
  no cell; an older round's figure is never shown. No dollar figure, no
  `live` word.
- **Status column:** the row's one status, padded to the widest status among
  the rows so the column lines up. The vocabulary:
  - **Attention words** are uppercase and coloured: `NEEDS YOU`, `REPORT IN`,
    `QUESTION IN`. A delivered report's qualifiers follow it, each prefixed
    with ` · `: the note bare, then the outcome when it is set and is not
    done — `REPORT IN`, `REPORT IN · halted`, `REPORT IN · unmarked · halted`.
  - **relevo state words** (`PAUSED`, `HELD`, `DONE`) and **passive phases**
    (`plan sent`, `no plan yet`, `question in`, `report in`, `answered`) keep
    their case; the phases are lowercase and dim. `NEEDS YOU` (a stalled
    report), a relevo state word and `REPORT IN` outrank the phase.
- **Clock:** last, right-aligned in its own column, so it does not move when
  another row's status or name changes length.

## 3. The round clock

The current round is the round of the newest `plan` entry sent to the
builder in the binding's log.

- **start** = TS of the *earliest* `plan` → builder entry of that round. A
  nudge, a switch or a repair may add later plan entries or reset
  `RoundStartedAt`; neither moves the start, so a builder switch does not
  restart the clock.
- **end** = TS of the newest `report` → planner entry of that round, or zero
  when there is none (the round is open).
- clock = `end − start` when end is set (frozen), else `now − start`
  (ticking), formatted by `AgeText`. No plan entry → `--`.

The clock is derived from the log, not from `Usage.DurationMS`, so it works
for rounds whose report carries no usage and agrees across harnesses.

## 4. Data

`BindingStatus` gains four additive fields, filled in `buildRow`:

| field | JSON | meaning |
|---|---|---|
| `RoundStart time.Time` | `round_start,omitzero` | §3 start; zero when the log has no plan entry |
| `RoundEnd time.Time` | `round_end,omitzero` | §3 end; zero while the round is open |
| `RoundUsage *usage.Usage` | `round_usage,omitempty` | usage on the report that set RoundEnd; nil when open or when that report has none |
| `Server string` | `server,omitempty` | `Builder.Server` for a remote binding; "" otherwise |

`RenderStatusLine` stays a pure function of `Report` and `now`.

## 5. Not changed

The dot, the planner line, width and truncation rules, `RELEVO_STATUSLINE_MARGIN`,
row order, `waiting()`'s words, `LastUsage`/`Spend`/`LiveUsage` and every
other surface that reads them (rail card, `relevo status`).
