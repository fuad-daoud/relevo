# Pin the daemon's idle tick to a budget

One guard, in one place, over the real `Daemon.Tick`: an idle tick makes **no
git call**, runs **no mirror run**, and reads **under 512 KiB**. Deterministic,
test-only, exercised by `make check` on the CI it already runs on.

## Why this guard, and not the other two

- **(a) rejected**: `go test ./...` never runs a `testing.B` benchmark, so a
  benchmark would need a new Makefile target *and* a new CI job, and its verdict
  on a shared runner is not deterministic -- the same idle tick measured
  **2.7 ms** in a plain run and **40 ms** under the `-race` build `check-test`
  uses. A flaky guard is worse than none.
- **(b) rejected**: a self-report is observability, not a guard -- nothing fails
  when the number crosses the line -- and it puts per-report work and log parsing
  into the daemon, against the "no runtime cost added" constraint. The `--pprof`
  hook already answers "what is it doing".
- **(c) chosen**: the budget is stated in units a test can read exactly: **bytes
  read per tick** (the kernel's own `rchar`) and **method calls through the `Git`
  interface**. Today's tick reads one bounded 64 KiB tail per live round and
  nothing else; every form of the regression costs megabytes.

## The bound, in words

- An idle tick -- a tick after the first, with every round's stream behind its
  cursor and the progress interval not due -- reads **under 512 KiB** and makes
  **zero git calls**, i.e. zero subprocesses, on a store where each live round
  has streamed 16 MiB.
- Under the 2 s interval (floored at 500 ms, `internal/relevo/daemon.go`), 10 %
  of a core is ~200 ms of CPU per tick; the measured idle tick is ~2 ms of wall
  time in a plain run (~42 ms instrumented under `-race`), and the pinned
  envelope is ~1 % of the read volume one stream holds -- so the bound sits far
  below the owner's 10 %/300 MB line while every regression that crossed it is
  red.

## What the fixture calls *idle*

`idleTickFixture` builds, in `internal/relevo`:

- two live headless rounds (`alpha`, `beta`), each with a **16 MiB** stream
  already behind the drain's cursor (`Builder.StreamOffset` at EOF), neither
  holding its completion marker, both escape-check applicable (`Repo` and
  `RoundBaselineTree` set), and both carrying a progress sample stamped at the
  package's fixed clock so the progress interval is not due;
- one DONE binding (`finished`);
- a real sqlite mirror (`db.Open` on a temp file, wired as `Runtime.DB`);
- the package's fixed clock (`newRuntime`), so no interval can become due
  mid-test.

A tick is **idle** when every live round's stream is behind its cursor, no
round's completion marker is on disk, the progress interval is not due, and the
mirror's revision gate holds. The guard warms the daemon with **one** tick
first -- the first tick after a start is allowed to open the mirror and write
each binding's cursor row once -- and measures the ticks after it.

## The three assertions, and exactly what fails

| idle tick | assertion | measured today | when the regression returns |
|---|---|---|---|
| git calls (`fakeGit.calls`, over 3 ticks) | `0` | 0 | ungating the escape check in `reconcileHeadless`: **12 calls over 3 ticks** (2 per live round per tick) -- `3 idle ticks made 12 git calls, want none` |
| bytes read (`/proc/self/io` `rchar`, per tick, over 3 ticks) | `< 512 KiB` | 131 107 B plain, 131 108 B under `-race` (2 × 64 KiB + ~35 B), stable over 5 plain runs | restoring the whole-stream read in `StreamDrained`: **33 554 468 B/tick** plain, **33 554 497 B/tick** under `-race` -- `an idle tick read 33554497 bytes; the budget is 524288, and one fixture round has streamed 16777216` |
| the ingest cursor row (`updated_at`) and the binding record (`updated_at`) | unchanged | unchanged | dropping the revision gate in `ingestLiveBindings`: the cursor moves on the first idle tick -- `an idle tick rewrote the mirror cursor at 2026-09-30 19:40:52.491 +0000 UTC, want it untouched at 2026-09-30 19:40:52.169 +0000 UTC` |

The read-volume assertion is the one nothing pins today: with the whole-file
read restored, the store-level `TestStreamDrainedReadsOnlyTheTail`
(`internal/store/seal_test.go`) **still passes** (measured) -- it pins the
*decision*, not the volume. The escape gate
(`TestReconcileHeadlessSkipsEscapeCheckWithoutAMarker`) and the mirror gate
(`TestIngestLiveBindingsSkipsUnchanged`) *are* pinned at their unit level (both
measured failing under their mutations); the guard keeps its copies because its
subject is the whole tick and the idle definition, not those two paths.

Positive control, so the zero is not a dead counter: in the same fixture,
touching the completion marker and ticking once advances the round and pays git
for the escape answer (`fg.calls` rises above the last idle tick's count).

## The three mutations, and their exact failures

Measured in this tree, each reverted before the next, with the clean run green:

| mutation | failing assertion | message |
|---|---|---|
| `internal/store/seal.go`: `readStreamTail(path, start, size)` → `readStreamTail(path, 0, size)` | bytes read | `an idle tick read 33554497 bytes; the budget is 524288, and one fixture round has streamed 16777216` |
| `internal/relevo/headless.go`: run `escapeCheck` unconditionally (drop the marker `os.Stat` gate) | git calls | `3 idle ticks made 12 git calls, want none` |
| `internal/relevo/daemon.go`: drop `ingestSeen[b.Name] == rev` from `ingestLiveBindings` | cursor / binding `updated_at` | `an idle tick rewrote the mirror cursor at …, want it untouched at …` |

## What the guard deliberately does not pin

Wall-clock CPU or RSS per tick (not asserted; the read test logs bytes and wall
time per tick and never asserts the time), the first tick after a daemon start
(it mirrors everything once, by design), and the non-Linux legs for the read
assertion (`/proc/self/io` is Linux-only, so that test skips there with its
reason; the git/mirror test runs on every leg). The RSS half of the budget is
covered only by the local recipe below, and the round's report says so.

## 6. Local re-measurement (the other half of the acceptance)

```
# the daemon's own numbers for one run, then reset the unit to its shipped ExecStart
systemctl --user edit relevo.service
#   [Service]
#   ExecStart=
#   ExecStart=%h/.local/bin/relevo daemon --interval 2s --pprof /run/user/%U/relevo-pprof.sock
systemctl --user restart relevo.service
curl --unix-socket /run/user/$UID/relevo-pprof.sock 'http://x/debug/pprof/profile?seconds=30' -o /tmp/cpu.pprof
curl --unix-socket /run/user/$UID/relevo-pprof.sock 'http://x/debug/pprof/heap' -o /tmp/heap.pprof
go tool pprof -top -nodecount=25 "$(command -v relevo)" /tmp/cpu.pprof
go tool pprof -sample_index=inuse_space -top -nodecount=25 "$(command -v relevo)" /tmp/heap.pprof
systemctl --user show relevo.service -p CPUUsageNSec -p MemoryCurrent -p MemoryPeak
# and the tick's own envelope in the rig
go test -race -count=1 ./internal/relevo/ -run TestIdleTick -v
```

Read it as: `CPUUsageNSec` over the run's ticks, against 200 ms/tick for 10 % of
a core at the 2 s interval; `MemoryCurrent` against the 300 MB line; the guard's
log line against 512 KiB.

## Deleted behaviour

Nothing. No production file is touched, no test is deleted or weakened, no lint
exclusion and no coverage baseline is added or changed, and no Makefile/CI
surface changes -- the guard rides the existing `make check` and the existing
shards.

## Risks, stated

- The fixture's clock is fixed, so the guard's git assertion is strict by
  design: a new git read that is meant to be interval-gated must extend the
  fixture deliberately, or the guard is right to fail.
- `/proc/self/io` is Linux-only; the read assertion skips on the macOS legs (the
  git/mirror test does not). Precedent for a Linux-gated resource guard:
  `countFDsOn` in `internal/relevo/daemon_test.go`.
- The budget is bytes read, not allocations: a regression that churns without
  reading (none is known in the idle path) would not show. The `--pprof` recipe
  covers what a test cannot.
