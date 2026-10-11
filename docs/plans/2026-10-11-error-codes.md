# User errors reported as `internal` (slug: error-codes)

Base: `origin/main 19e25095`.

A user who mistypes a candidate, a ref or a provider, a provider whose monthly
limit resets weeks out, or a `db sync` verb fired during a breaker backoff, is
told `internal` and sent to `relevo bugreport`. None of those is a defect in
this build: each is either the caller's own argument or a condition the code
already knows how to describe. This round gives each one its own class.

## 1. `--clear` accepts a token with or without `#effort`; unknown subjects are usage

`Set.Resolve` and `ResolveClearSubject` did exact-map lookups on the string as
typed, so a token carrying the `#effort` suffix a builder's own settings print
resolved to nothing, and an unknown subject fell through `writeError` to
`internal`.

- `candidate.StripEffort` removes a trailing `#...`, leaving `:effort` alone
  (the same rule `internal/ingest` already applies), and both token readers use
  it.
- `availability.UsageText` names the accepted forms; `writeError` maps
  `ErrUnknownCandidate`, `ErrBadRef` and `ErrUnknownProvider` onto `usage`.

## 2. `gate` accepts the forms the halt texts tell the reader to type

A halt says `clear a gate with relevo gate --clear <provider>`, but
`gate <provider>` went through `Candidates.Resolve`, which only knows tokens.
Both verbs now resolve a subject by one rule: a candidate name, a full token
(with or without `#effort`), a bare provider or group, or `group@account`.

## 3. A reset duration is trusted up to the dated window

`parseDurationReset` summed its components correctly and then bounded them by
the 7-day window a vaguer form earns, so `16d 5h` was parsed and discarded and
the gate fell back to one hour. The overflow guard beside it already used the
31-day window a dated reset earns; the duration form now uses it too, and the
overflow refusals stay.

## 4. `db sync push|pull` during a backoff is a refusal

`verbClassify` had no case for `ErrBackingOff`, so the closed wire set answered
`internal` for a delay that ends on its own. The new code is conflict-shaped:
the reader's way out is `relevo db sync retry`.

## 5. A missing `--file` is a usage error

`relevo send --file /no/such/file` read as `internal: read prompt ...`. The path
is the caller's own argument, so it is refused as usage naming the path.

## Commits

| commit | message | issue |
| --- | --- | --- |
| 1 | `Fixes #1115` effort-tolerant clear, usage for an unknown subject | #1115 |
| 2 | `Refs #1101` gate takes a provider or group | #1101 |
| 3 | `Refs #1101` trust a duration reset up to the dated window | #1101 |
| 4 | `Fixes #1119` a backoff is a refusal, not an internal failure | #1119 |
| 5 | `send: a missing --file is a usage error` | none |