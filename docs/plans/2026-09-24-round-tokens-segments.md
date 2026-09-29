# Plan: round tokens across builder switches

Spec: `docs/specs/2026-09-24-round-tokens-across-switches-design.md` (in this
tree). Read it first. Line numbers are as of 2c6d5075, the tip of this branch.

**If a step is impossible as written or contradicts the code, stop and report.
Do not bend a test to fit.** Stop in particular if:
(a) the supervisor opens the round's stream in a mode other than append, so a
    second process in the same round overwrites instead of appending. Check
    how `ProcSpec`'s stream path is opened in internal/proc.
(b) a harness's usage parser (`newSourceCarry`, internal/usage) needs to see
    the stream from byte 0, for example a header line that later events depend
    on, in which case reading from an offset would lose events.

## 1. Overview

This round does three things:
- Each builder process gets a byte offset into the round's shared stream
  (`Endpoint.StreamStart`). The usage reader parses only that process's
  segment.
- A switch records the outgoing builder's segment usage on its log entry.
- The round's token figure becomes (current builder's live, or the closing
  report's) + the sum of earlier segments (`RoundPriorTokens`).

`Spend` includes segments without counting extra rounds. The same prior-token
figure crosses the wire for remote rounds, both running and closed.

## 2. Files

```
internal/usage/source.go          MODIFY  Source.StreamFrom; parseCached + readSealed honour it
internal/usage/spend.go           MODIFY  (Spend).AddSegment
internal/store/types.go           MODIFY  Endpoint.StreamStart; LiveFacts.PriorTokens; LogEntry.PriorTokens
internal/store/format.go          MODIFY  BindingFormat 3 -> 4 (never stamped) + comments
internal/store/testdata/binding-shape.golden  REGENERATE (sanctioned; must show only stream_start + remote_live.prior_tokens paths + the number)
internal/relevo/headless.go       MODIFY  startProcess sets StreamStart; restart relaunch records the lost segment's usage
internal/relevo/usage.go          MODIFY  roundSource sets StreamFrom
internal/relevo/switch.go         MODIFY  switchEntry takes usage; switchBuilder records the outgoing segment
internal/relevo/reconcile.go      MODIFY  queueReport gains prior *usage.Tokens -> LogEntry.PriorTokens
internal/relevo/{reconcile,headless,stop}.go  call sites pass nil (reconcile.go:360,368; headless.go:657; stop.go:215)
internal/relevo/remote.go         MODIFY  liveFactsOf copies PriorTokens; catchUp passes view.PriorTokens to queueReport (line ~1300)
internal/relevo/status.go         MODIFY  BindingStatus.RoundPriorTokens; priorTokensOf; Spend loop; remote override
internal/relevo/served.go         MODIFY  ServedView.PriorTokens for the closed round; liveViewOf copies RoundPriorTokens
internal/relevo/statusline.go     MODIFY  roundTokens adds RoundPriorTokens
internal/remote/proto.go          MODIFY  BindingView.PriorTokens; LiveView.PriorTokens
tests: internal/usage, internal/store, internal/relevo (status, statusline, switch, served, remote)
docs/specs/2026-09-24-round-tokens-across-switches-design.md, this plan  ALREADY PRESENT, commit them
```

## 3. Data

| Where | Field | Type | JSON | Meaning |
|---|---|---|---|---|
| store.Endpoint (types.go, after StreamOffset ~line 120) | StreamStart | int64 | `stream_start,omitempty` | Byte length of the round's stream (`Store.BuilderStreamPath(name, round)`) when this process was spawned. 0 for a round's first process. |
| usage.Source (source.go:29) | StreamFrom | int64 | (none) | Parse only bytes at or after this offset. 0 means the whole stream. |
| store.LogEntry | PriorTokens | *usage.Tokens | `prior_tokens,omitempty` | Only on a report entry that closed a *remote* round: the tokens the server's earlier builders in that round used. |
| store.LiveFacts (types.go:170) | PriorTokens | usage.Tokens | `prior_tokens,omitzero` | Copied from LiveView. |
| remote.LiveView (proto.go:165) | PriorTokens | usage.Tokens | `prior_tokens,omitzero` | The server row's RoundPriorTokens. |
| remote.BindingView | PriorTokens | *usage.Tokens | `prior_tokens,omitempty` | For ClosedRound, `priorTokensOf(serverEntries, ClosedRound)`. Nil when zero. |
| relevo.BindingStatus (after RoundUsage, status.go:166) | RoundPriorTokens | usage.Tokens | `round_prior_tokens,omitzero` | Tokens of the current round's earlier segments. |

## 4. Contracts

### usage
- `parseCached` (source.go:233). The cache key becomes path + harness + StreamFrom.
  A new cache entry starts at `offset = src.StreamFrom`. The existing
  "file shrank" reset (`info.Size() < e.offset`) also resets to StreamFrom,
  and it applies when the size falls below StreamFrom. Everything else is
  unchanged.
- `readSealed` (source.go:185): the whole-line data becomes `data[StreamFrom:]`
  when StreamFrom ≤ len(data), and empty otherwise. This happens before the
  whole-line trim.
- `func (s Spend) AddSegment(u Usage) Spend`: adds Steps, ToolCalls and Tokens,
  plus Measured or Estimated USD by `u.Cost.Basis`. It **never** changes
  Rounds, Consults, Plan or Unknown. A plan-lane segment (`u.Cost.Plan`) adds
  tokens, steps and calls only.

### store
- The fields in §3. BindingFormat 3 → 4. `recordFormat` is unchanged, and its
  comment says StreamStart (format 4) never raises a record's format, the same
  as RemoteLive. BindingFormat's doc says: "Format 4 adds
  `builder.stream_start`, never stamped: an older relevo that drops it makes
  the current process's usage reads start at byte 0 -- an over-count on a
  same-harness switch in that round, never lost data."
- Regenerate the golden with
  `go test ./internal/store -run TestBindingShapeMatchesFormat -count=1 -update`.
  Its diff must show only the new key paths and the number. Anything else →
  halt. The `TestRemoteLiveDoesNotRaiseRecordFormat` pattern gets a sibling
  test, `TestStreamStartDoesNotRaiseRecordFormat`.

### relevo
- `startProcess` (headless.go:233). After the StreamRound reset block
  (lines 234–238), set `b.Builder.StreamStart` to the current size of
  `rt.Store.BuilderStreamPath(b.Name, b.Round)` on disk. A missing file → 0.
- `roundSource` (usage.go ~53). For a headless builder, set
  `src.StreamFrom = b.Builder.StreamStart` next to StreamPath.
- `switchEntry(now, round, reason, res, u *usage.Usage)`: the entry gets
  `Usage: u`. Update every call site.
- `switchBuilder` (switch.go:93). Right after `old := b.BuilderCandidate` /
  `now := rt.Now().UTC()` (~lines 121–122) and before `resolveBuilder`,
  compute `prior := peekUsage(ctx, rt, b, now)`. `b` is still the outgoing
  binding: old Kind, candidate, StreamStart and RoundStartedAt. Pass `prior`
  to `switchEntry`. `peekUsage` never blocks past its 500 ms deadline and
  returns nil when it reads nothing, which gives an entry with no usage, as
  today.
- Daemon-restart relaunch (headless.go ~760–800). Before the `startRound` call,
  capture `prior := peekUsage(ctx, rt, b, now)` with the pre-relaunch
  binding, and set `Usage: prior` on the KindSwitch entry it appends (~line 793).
- `queueReport(..., usage *usage.Usage, rusage *store.Rusage, prior *usage.Tokens)`
  (reconcile.go:376): the report entry gets `PriorTokens: prior` (copied).
  Local call sites pass nil. `remote.go` ~1300 passes `view.PriorTokens`.
- `func priorTokensOf(entries []store.LogEntry, round int) usage.Tokens` (status.go, pure). It is the sum of `e.Usage.Tokens`
  over entries with `Kind == KindSwitch && Round == round && Usage != nil`, plus
  `*e.PriorTokens` of the newest `KindReport` + `DirToPlanner` entry with
  `Round == round`, when it has one.
- `statusRow`. Next to `row.RoundStart, row.RoundEnd, row.RoundUsage = roundFacts(entries)`
  (status.go:552), set `row.RoundPriorTokens = priorTokensOf(entries, row.PlanRound)`.
  `PlanRound` is already computed above it (~line 520); verify that. Then,
  when `b.Builder.Remote() && row.RoundEnd.IsZero() && b.Builder.RemoteLive != nil`,
  override it with `b.Builder.RemoteLive.PriorTokens`, because the server
  knows the running round's segments and the client does not.
- The Spend loop (status.go ~556). Add the `KindSwitch` entries that carry
  `Usage` through `AddSegment` after `usage.Sum`. For each report entry with
  `PriorTokens`, add those tokens to `Spend.Tokens` (tokens only). The Spend
  pointer stays nil when nothing carries usage at all.
- `liveViewOf` (served.go:244): `PriorTokens: row.RoundPriorTokens`.
- `ServedView` (served.go ~82–170): `PriorTokens` is
  `priorTokensOf(entries, b.Serve.ClosedRound)` when ClosedRound > 0 and its
  Total() > 0; otherwise nil.
- `liveFactsOf` (remote.go:645): copy PriorTokens.
- `roundTokens` (statusline.go:168). The base is the same as today: LiveUsage
  tokens while open with samples, RoundUsage tokens once closed. The result is
  `base + RoundPriorTokens.Total()`. When the sum is 0 it returns "" as today.
  A round whose current builder has no samples yet, but whose prior tokens are
  non-zero, shows the prior tokens.

## 5. Deletions (closed list)

None. Behaviour changes, each ported and not deleted:
- The usage reader no longer reads a stream from byte 0 when StreamStart > 0.
- `switchEntry`'s signature.
- `queueReport`'s signature.

## 6. Steps

1. **usage.** Add StreamFrom, update parseCached and readSealed, add AddSegment.
   Tests in internal/usage:
   - `TestStreamFromSkipsEarlierSegment`: write a stream holding two
     same-harness segments (use a harness whose test fixtures already exist
     in the package). Reading with StreamFrom = the second segment's offset
     gives only the second segment's tokens. StreamFrom = 0 gives both.
     Cover the on-disk (parseCached) path and the sealed (readSealed) path.
   - `TestSpendAddSegmentCountsNoRound`: Rounds, Plan and Unknown are
     unchanged, and tokens and USD are added.
2. **store.** Add the fields and the format change, regenerate the golden, and
   add `TestStreamStartDoesNotRaiseRecordFormat`.
3. **relevo, local.** startProcess, roundSource, switchEntry/switchBuilder,
   the restart relaunch, the queueReport signature, priorTokensOf, statusRow,
   Spend, and roundTokens. Tests:
   - `TestPriorTokensOf`, table-driven: no switches → zero; two switch
     entries in the round → their sum; a switch entry in another round is
     ignored; a report with PriorTokens is added; a switch entry with nil
     Usage is skipped.
   - `TestSwitchRecordsOutgoingUsage`. Follow the existing switchBuilder tests
     (grep `switchBuilder(` in `*_test.go`) and use `fakeUsage` with
     `peekSamples` set. The appended KindSwitch entry's Usage has those
     tokens, and the peek source's `StreamFrom` equals the outgoing
     endpoint's StreamStart.
   - `TestStartProcessSetsStreamStart`: a stream file of N bytes exists
     before the spawn → StreamStart == N. With no file → 0.
   - `TestRoundTokensAddsPrior` (statusline_test.go): an open round with live
     41k plus prior 100k gives "141k tok"; closed with 2.1M plus 0.4M gives
     "2.5M tok"; no live samples but prior 100k gives "100k tok".
   - `TestSpendIncludesSwitchSegments`: in a status row, Spend.Rounds equals
     the number of report entries, and Spend.Tokens includes the switch
     entries.
   - Port any existing test broken by the two signature changes, and name
     each one in the report.
4. **Remote.** Add the proto fields, liveViewOf, ServedView, liveFactsOf, the
   catchUp pass-through and the statusRow override. Tests:
   - Extend `TestLiveViewOf` to assert PriorTokens.
   - `TestServedViewPriorTokens`: server entries with two switches carrying
     usage in the closed round → view.PriorTokens is their sum. None → nil.
   - `TestObserveRemoteRunningStoresLive`: also assert PriorTokens.
   - `TestCatchUpRecordsPriorTokens`: model it on an existing catchUp test.
     The view has PriorTokens → the client's report entry has them, and
     `priorTokensOf` on the client log for that round returns them.
5. **Mutation checks** (run each, report it, revert):
   (1) ignore StreamFrom in parseCached → `TestStreamFromSkipsEarlierSegment` fails;
   (2) make AddSegment increment Rounds → `TestSpendAddSegmentCountsNoRound` fails;
   (3) drop `prior` from switchEntry → `TestSwitchRecordsOutgoingUsage` fails;
   (4) drop the remote override in statusRow → pick the test that fails and name it. If none fails, add one.
6. **Full check and commit.** Run the full check by its parts:
   `gofmt -l $(git ls-files '*.go')` (it must print nothing), `go vet ./...`, and
   `go test -race -count=1 ./...`. `git diff --stat` must touch only the §2 files.
   Make one commit:
   `feat(usage): a round's tokens include every builder it switched through`.

## 7. Working efficiently

Read the §2 files once, in parallel, at the lines given. Make each file's
edits in one call. The signature changes are mechanical: make them with one
scripted edit across the call sites, then fix whatever does not compile.
Focused loop:
`go test ./internal/usage ./internal/store ./internal/relevo -count=1 -run 'Stream|Segment|Prior|Switch|RoundTokens|Spend|LiveView|ServedView|ObserveRemote|CatchUp|Format'`.
Add no test in `cmd/relevo`, because CI has no harness and no network.
