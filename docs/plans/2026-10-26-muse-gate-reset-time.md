# Plan: muse gate honors the reset instant

## Behaviour

- When a muse limit stop carries a reset instant, the gate expires at that
  instant, not at now plus the default.
- Precedence, settled: structured `resets_at_ms` when present and sane beats
  the RFC3339 instant parsed from the matched text, which beats the existing
  prose forms (duration, date, clock — unchanged relative order), which beat
  today's default (`LimitGateDefault`). The default applies only when nothing
  parses.
- A parsed reset at or before now means the limit already lifted: the gate
  expires immediately (`Until` = now), never now plus the fallback. The hand
  path's existing clamp (`internal/availability/gates.go`) already does this
  for whatever `ResetFromReason` returns, and the clock grace is the older
  precedent for the same reading.
- The new instant form is shape-guarded to muse-shaped lines, so another
  provider's prose timestamp cannot set a gate. The guard looks for the
  markers already in the muse rows (`internal/harness/harness.go`): "usage
  window", "quota", "rate_limit_error", "API error 429".
- The instant form is tried before the clock form. This ordering is required,
  not stylistic: `clockRe` reads the instant's year as an hour.
- Both gate paths inherit the new form through the shared parser: automatic
  gates (`gateOnLimit`, `internal/relevo/switch.go`) and hand-recorded gates
  (`handleUnavailable` via `ResetFromReason`, `internal/serve/unavailable.go`).
  The hand path keeps zero-`Until` (until cleared) when nothing parses.

## Incident mechanism

- On 2026-10-24 ~11:03:33Z `gateOnLimit` matched the muse 429 line and gated
  provider `meta` (source `relevo`). Ledger mirror: `availability_event`, `at_ms`
  1792839813021, kind `rate_limited`, with the full 429 note ending in the
  sentence "Your usage window resets at 2026-10-24T13:22:58Z.
  (rate_limit_error)". The same record's round exited 11:03:32Z and switched
  11:03:33Z.
- `parseReset` had no absolute-instant form. `clockRe` matches `resets at
  2026-...` as hour 20 (the "20" of "2026"), so the line parsed as clock-time
  20:00 local, i.e. `Until` = 17:00Z — the observed gate, about 3.6 h past the
  true 13:22:58Z reset. Detection worked; the reset was misread, not ignored.
- Corroboration reused as fixture values: the failed-event quota
  `resets_at_ms` 1792848178000 and the allowance `window.resets_at_ms` in the
  muse fixture stream both equal the sentence instant 13:22:58Z.

## Seams

1. `internal/availability/limit.go` — the instant regex and guard sit beside
   the duration/date/clock regexes; `parseReset` tries the instant first;
   `MatchLimitWithReset` carries the structured millisecond instant and
   prefers it over the text parse; the dated (31-day) window bounds both.
2. `internal/transcript/limit.go` — `LimitResetMS` reads one raw muse line:
   the failed run's quota instant (error, else reason), else the usage
   allowance window's. Zero or absent reads as no reset.
3. `internal/relevo/transcript.go` — `scanTailBoth` / `currentBuilderScan`
   collect the text and the structured instant in the one scan pass the
   decision already runs, so no new I/O; the failure's instant wins over the
   window's. The older text-only helpers delegate to them unchanged.
4. `internal/relevo/switch.go`, `internal/relevo/headless.go`,
   `internal/relevo/reconcile.go` — the four gate call sites scan once and
   pass both halves to `gateOnLimit`, which feeds the instant to
   `MatchLimitWithReset`.
5. `internal/serve/unavailable.go`, `cmd/relevo/gate.go` — unchanged; they
   inherit the instant form through `ResetFromReason`.
6. `internal/harness/harness.go` — the muse comment now records the captured
   429 sentence instead of claiming no evidence exists.
7. Tests in existing style (table-driven, names pin behaviour, no history in
   comments): `internal/availability/limit_instant_test.go`,
   `internal/availability/limit_reason_test.go`,
   `internal/transcript/limit_reset_test.go`,
   `internal/relevo/limit_test.go`.

## Cases

- Line with the reset sentence gates until that instant (`Parsed` true).
- Allowance-carrying stream (failed-event quota ms, or run.usage window ms)
  gates until the ms instant even when the matched text line names no reset;
  structured outranks text.
- Insane structured ms (zero, or outside the dated window) is ignored; falls
  through to text, then default.
- Garbage, past, or absent reset: default today. Parsed instant at or before
  now: `Until` = now (expires immediately).
- Non-muse prose containing an ISO timestamp: no gate change (guard holds).
- Existing duration, date, and clock lines: behaviour unchanged (regression).
- Mutation check: remove the new form and the new tests fail.

## Item 2 findings

- Searched `$XDG_STATE_HOME/relevo` excluding `.worktrees`/`.verify`: no
  standalone sealed runner stream contains a quota stop. The only
  `*-runner.jsonl` hit is this round's own `plan-1358-i8/001-runner.jsonl`
  (prompt echo).
- The sentence and its gate survive in-DB: `binding_event` record
  `01M5N7DW70QHK8TC5X89D4MD8` round 4; `availability_event`
  `rate_limited`/`meta`/`relevo`/`drop-snapshot` with the full 429 note. The
  backup's `transcript` table holds no rendered quota row, so there is still
  no fixture-grade raw stream; `harness.go` stays true for streams.
- The rows found belong to binding `drop-snapshot` round 4, not career#19:
  no career binding data exists on this machine, and zen was out of scope for
  this read-only round.
- Verdict: item 2 is partially unblocked — text-level fixture (sentence plus
  ledger provenance) exists and the builder adds it as a pattern/parse
  fixture; patterns tighten further only when a real stream surfaces.
