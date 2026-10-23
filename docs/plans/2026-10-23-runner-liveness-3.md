# Runner liveness, plan 3 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** A pure classification, no daemon change. Given the current
opencode process's own error events, say whether the last one is a
transport-class error, separately from a limit, an outage and a missing report.
Transport class: `ECONNRESET`, `socket connection was closed`, `stream ended
without finish_reason`, `Connection lost while reading the response`, `Stream
error occurred`, and an `error.type` of `provider.transport`. An outage line
(anything `availability.MatchOutage` matches) or a limit/credit line is never
transport, whichever pattern it also hits.

**Seams.**
- `internal/transcript/limit.go`: add `TransportLines(kind string, line []byte)
  []string` beside `LimitLines`: for kind `opencode`, an `error` event yields
  one line `"<error.type>: <error.message>"` (or just the message when the type
  is empty); every other kind and event yields nothing (model text and tool
  output must never classify). Reuse `str`/`asMap`/`nonEmptyLine`. A non-JSON
  stderr line is also returned, as `LimitLines` does for its channel, because
  the harness writes some of these on stderr.
- `internal/availability/transport.go` (new): `MatchTransport(text string)
  (line string, ok bool)`: scan lines last to first like `MatchOutage`
  (`internal/availability/outage.go:67`), skip `transcript.IsThinking` lines, a
  fixed case-insensitive pattern table compiled by the same helper style as
  `compileOutage`, return the first (most recent) match capped with `capLine`.
  It returns false if `MatchOutage(text, …)`, `MatchCredit(text)` or a limit
  pattern match the same line. Take the limit patterns through a parameter or
  leave limit exclusion to the caller (plan 4 orders limit first); pick the
  smaller seam and say which.
- `internal/relevo/transcript.go:229` `currentBuilderScanText`: add the sibling
  `currentBuilderTransportText(rt, b, n)` that calls `scanTail` with
  `transcript.TransportLines` instead of `LimitLines` (same bytes, same
  `StreamStart` cut, legacy-log branch unchanged).
- Tests: `internal/transcript/limit_test.go` (or a new
  `transport_lines_test.go`), `internal/availability/transport_test.go`.

**Steps.**
1. `TransportLines` plus tests: `TestTransportLinesReadsOpencodeErrorEvents`
   (one fixture line per error in #1330: `Transport: ECONNRESET…`, the
   deepseek `Transport: The socket connection was closed unexpectedly`,
   `provider.invalid-output: OpenAI Chat stream ended without finish_reason`,
   `provider.transport: Connection lost while reading the response`,
   `provider.unknown: Stream error occurred`), `TestTransportLinesIgnoresModelText`
   (assistant text and a `tool_use` output containing "ECONNRESET" yield nothing),
   `TestTransportLinesIgnoresOtherKinds`. Done when they pass.
2. `MatchTransport` plus tests: one positive per error above;
   `TestMatchTransportLeavesRateLimitAlone` (the `provider.rate-limit` and
   `provider.quota` lines from `testdata/opencode-errors/results.jsonl` do not
   match); `TestMatchTransportLeavesOutageAlone` (a line carrying both
   `ECONNRESET` and `status 503` is an outage); `TestMatchTransportTakesLastLine`.
3. `currentBuilderTransportText` plus one test on a stream file with a
   mid-round switch (`StreamStart` cuts the outgoing process's error), modelled
   on the existing `currentBuilderScanText` test (`grep -n
   currentBuilderScanText internal/relevo/*_test.go`).
4. Run `make check-affected`.
5. Commit.

**Report must include:** which fixtures were recorded from a real stream and
which were reconstructed; where limit exclusion lives.


