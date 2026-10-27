# Plan: muse allowance as a pick signal (issue #1358 item 1)

> Builder override (MasterMind, 2026-10-26): do NOT run `make check` or `make check-affected` in this round. Run only the targeted tests named in the plan. The MasterMind runs `make check-affected` after the round closes.

Destination: `docs/plans/2026-10-26-muse-allowance-pick.md`.

## Behaviour

- Every local muse round close parses the last `run.usage` allowance in its
  stream and records, per provider account, the last-seen
  `{window_used, weekly_used, window_resets_at_ms, weekly_resets_at_ms}`.
- A pre-round pick skips a candidate whose account's stored allowance shows
  window `used_percent >= 85` or weekly `used_percent >= 85`, counting only
  the dimension whose `resets_at_ms` is still in the future. The skip flows
  through the existing `Skip`/`GateText` refusal shape, indistinguishable
  from the `at concurrency limit` family.
- Threshold is one constant, 85, for both dimensions (defended below).
- Absence never gates: no stored record, no key for the account, a
  non-muse harness, or a dimension with no usable `resets_at_ms` all read
  as ungated.
- Staleness is tied to `resets_at_ms`: a stored generation gates only while
  its reset is in the future; once `now >= resets_at`, that dimension
  stops gating without any write.
- Within one reset generation (equal `resets_at_ms`) concurrent rounds
  closing out of order keep the maximum `used_percent`; a newer
  `resets_at_ms` starts a new generation and replaces the old record.
- An explicitly named `--candidate` bypasses the allowance skip exactly like
  every other gate except roles-missing/skill-missing: the pick proceeds
  and the skip is recorded as advisory. No new bypass rule is invented.
- The record is machine-local with no sync and no wire change: the machine
  that ran the round is the machine whose picks it informs, matching where
  rounds run and picks resolve.

## Cases

- Window at/above 85 with a future window reset: candidate skipped, refusal
  names the allowance reason through the existing skip text.
- Weekly at/above 85 with a future weekly reset (window below): skipped.
- Both below 85: picked normally, pick note byte-identical to today.
- No stored record for the account (never saw muse allowance): no gate.
- Stored window reset in the past, weekly fresh and high: window
  dimension ignored, weekly still gates, and vice versa.
- `used_percent` present but `resets_at_ms` missing/unparseable on that
  dimension: that dimension never gates (fail open, staleness unbounded).
- New `resets_at_ms` observed: old generation discarded, new values stored.
- Same generation, older round closing later with a lower percent: stored
  maximum kept, never regressed.
- Non-muse candidate (no allowance events exist): unaffected, never gated.
- Explicit `--candidate` on a gated account: proceeds with the skip
  recorded as advisory, per the existing explicit-pick rule.
- Two bindings sharing one account pool: a gate on one login leaves the
  other login pickable, per the existing group@account partial-gating rule.

## Seed-vs-code decision

- The seed fixes the transcript seam
  (`internal/transcript/muse.go` ~:162 renders allowance only) and demands
  the plan settle threshold, absence, staleness and keying from incident
  data. The incident comment (`gh issue view 1358`) corroborates and
  sharpens the numbers: one 10-minute standard-rates round burned window
  ~10% -> 102% (92 points) and week 3% -> 34% (31 points) on 19.9M
  tokens; a contributor-mode round used ~4% of window per 9.3M tokens; the
  transcript example shows a typical 8%.
- Threshold 85 (headroom 15 points) because: 15 points cover ~2-4 normal
  contributor-mode rounds (4% each; 8% typical observed), i.e. one heavy
  round plus margin, so a start at 85% completes ordinary work without
  tripping; the 92-point runaway exceeds *any* usable static threshold (it
  would require <=8%, gating accounts after two normal rounds, which is
  perverse), so the runaway stays caught by the existing mid-round
  `gateOnLimit` path -- proven in the incident itself, which detected the
  429, switched the builder, and finished the round. Weekly burns at ~1/3
  window speed (31 vs 92 observed), so a uniform 85 is conservative in the
  same direction with one constant and one code path. A weekly gate idles
  until a distant reset, but its all-gated refusal already names
  wait/clear, and a skip is cheap wherever the pool has depth.
- The seed forbids inventing a store when one fits. Finding: no
  per-account allowance table exists anywhere (per-account state today is
  `BuilderAccount` on the binding plus `group@account` ledger subjects).
  The fitting medium is the same `db.KV` store database that holds the
  ledger document (`ledgerKey "ledger"`,
  `internal/availability/ledger.go`:109-110) as a *sibling document*, not
  a new store and not a ledger entry (a sub-threshold observation must
  never look like a gate, and ledger clears must never erase observations).
  Absent row reads as empty per the `LoadLedger` pattern
  (`ledger.go`:122-130), so no migration exists or is needed.
- `internal/usage` owns shapes only ("knows harness record shapes and
  nothing else", `internal/usage/usage.go`:1-4): the allowance parser is a
  pure function there, while persistence lives in `internal/relevo`
  round-close and the read lives in `internal/availability`. Allowance
  must not ride `Sample`/`Fold` (`internal/usage/usage.go`:82-147),
  which sums tokens and costs; last-seen is not summable.
- Item 8 (RFC3339 reset parsing out of the 429 reason) is a different
  item and is not implemented here; `resets_at_ms` is used only to bound
  staleness. Contributor-mode launch (item 3) is untouched.

## Seams

- `internal/transcript/muse.go`: `museUsageLine` (:153-168) is the
  render-only parse to mirror for field paths; the allowance object is
  `{window: {used_percent, resets_at_ms}, weekly: {...}}` (:163-166).
- `internal/usage/muse.go`: `museEvent` (:14-19) and `museStream` (:27-29)
  are the stream parse that drops allowance today; the new pure parser
  (last `run.usage` allowance from stream bytes) lives beside them.
- `internal/usage/carry.go`: `newCarry` (:21-36) registers `museCarry`;
  evidence the reader path already drains muse streams at close.
- `internal/usage/source.go`: `Source` (:24-36) is what the close path
  already threads; it needs the round's account alongside
  provider/model/plan.
- `internal/relevo/usage.go`: `recordUsage` (:123-164) is the single
  choke point already draining the stream at close (steps read,
  :156-162); the record helper sits next to it, and `fillCandidate`
  (:86-100) is where the account is filled from `b.BuilderAccount`
  (`internal/store/binding.go`:237-246).
- Round-close call sites (local-read branch only; the remote branch ships
  server-measured usage and has no local stream): `queueReport` in
  `internal/relevo/reconcile.go` (~:965, the `else` calling
  `recordUsage`), `internal/relevo/nudge.go`:174,
  `internal/relevo/resume_same.go`:71. Parse/record on muse harness
  only.
- Storage medium: `LoadLedger` absent-row-is-empty (`ledger.go`:122-130)
  and `SaveLedger` (`ledger.go`:175-196) are the patterns the sibling
  allowance document mirrors (own key, own type, `knownKind`
  (`ledger.go`:98-100) untouched). `Deps` carries the kv plus clock
  (`internal/availability/deps.go`), bound by `AvailabilityDeps`
  (`internal/relevo/runtime.go`:440-462); `gateUnionKV` passes
  non-`"ledger"` keys straight to primary
  (`internal/relevo/gateunion.go`:94-96,138-141), so one sibling key
  works for local and served runtimes alike.
- Read path template: `concurrencyGates`
  (`internal/availability/concurrency.go`:14-63), the synthesised
  in-memory gate precedent, appended in `GatesFrom`
  (`internal/availability/gates_projection.go`:75-84). New `Kind` plus
  `GateKindText` entry (`internal/availability/gates.go`:464-486) with
  an explicit `noteIsReason`/`GateText` membership decision
  (`gates.go`:529-538) so refusal renders through `skipText`
  (`internal/relevo/candidate.go`:152-160), `allGated`
  (`candidate.go`:394-409) and `ExplainResolution`/`pickEntry`
  (`candidate.go`:513-539) with zero new render code.
- Pick rule: `resolveRoleBase` (`candidate.go`:267-362) over
  `gatesForRole` (:142-150) and `skipsFor` (:126-136); explicit-pick
  bypass is the existing rule at :267-301 (only roles/skill-missing
  refuse). Per-account key is `account.GateKey(provider, BuilderAccount)`
  with bare provider when no account, matching `gateOnLimit`
  (`internal/relevo/switch.go`:496-511) and `gateOnOutage` (:570-576);
  key helpers in `internal/account/gate.go`:10-32. Partial-pool
  behaviour reuses the existing group@account rule, not new logic.
- `gatedNote` (`gates.go`:552-567) deliberately covers only
  ledger+roles+window gates (concurrency excluded); the allowance gate
  follows the concurrency precedent there. Status/doctor/candidates
  views pick the new kind up through `GateKindText` with no view
  changes.

## Steps

1. Deliverable: pure `ParseMuseAllowance`-style parser in
   `internal/usage` beside `museStream` returning last-seen
   window/weekly percents and resets, plus unit tests on recorded
   `run.usage` lines. Check: `go test ./internal/usage/` green; test
   fails if the parser reads the first instead of the last event.
2. Deliverable: account on `usage.Source`, filled from
   `b.BuilderAccount`, plus a record helper beside `recordUsage` wired
   into the three local close sites, writing the sibling kv document
   with the generation/max rule under the store lock. Check: targeted
   `go test ./internal/relevo/` for the close-path tests green; no
   write on remote-shipped or non-muse paths.
3. Deliverable: sibling-document load/save in `internal/availability`
   mirroring the ledger patterns (absent reads empty, no migration),
   with unit tests. Check: `go test ./internal/availability/` green.
4. Deliverable: synthesised allowance gates (new `Kind`, kind text,
   `GatesFrom` append) so at/above-threshold skips with the named reason
   while below-threshold, stale, and absent all pick normally. Check:
   new pick/availability tests in existing style (cf.
   `internal/availability/concurrency_test.go`,
   `internal/relevo/inflight_test.go`) green.
5. Deliverable: threshold constant 85 with the incident numbers in the
   plan doc (not code comments), then the mutation check: drop the gate
   condition and confirm the skip test fails. Check: mutation fails the
   skip test; restore green.
6. Deliverable: this plan saved as
   `docs/plans/2026-10-26-muse-allowance-pick.md`, `gofmt -l` clean on
   changed files, targeted `go test` on the three touched packages
   green, one new commit, then a report. Check: `git diff --stat`
   shows only the files named above plus the plan. Do NOT run `make
   check` or `make check-affected`; the MasterMind gates afterwards.

## Must NOT change

- `internal/transcript/muse.go` rendering or any transcript test/golden.
- `Sample`, `Fold`, cost basis, or any price/token semantics in
  `internal/usage`.
- The ledger schema, `knownKind`/`knownSource`, clear semantics, and
  `relevo gate --clear` behaviour.
- Item 8's 429 reset parsing, item 3's contributor-mode launch, and any
  harness beyond muse.
- The explicit-`--candidate` bypass rule; the wire protocol, sync, and
  outbox (no allowance is shipped or synced).
- No new exclusion in `.golangci.yml`, `scripts/check-comments.sh`,
  `scripts/check-filesize.sh`, or `testdata/coverage-baseline.txt`; no
  issue numbers in code comments; test names say what they pin; no
  commit amended or rebased; CI (`-race`, coverage) stays the judge.

## Deleted behaviour

1. None: the change is purely additive. No existing pick, gate, sync, or
   render behaviour is removed; the pick previously ignored allowance
   entirely, and under-threshold picks resolve exactly as today.

## Report must include

- Files changed and the commit hash.
- The threshold (85/85) with the incident numbers it rests on (92-point
  window / 31-point weekly runaway vs ~4% normal contributor round),
  confirming no re-measurement beyond the cited issue data.
- The targeted `go test` invocations and their output, `gofmt -l`
  output, and which test was mutation-checked with how it failed with
  the gate condition dropped.
- Anything left unverified and why; the full `make check-affected` /
  `make check` runs stay with the gate.
