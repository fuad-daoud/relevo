# Runner liveness, plan 6 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** A live opencode builder whose process tree has emitted no stream
bytes for `silent_after` AND has used almost no CPU in that time is shown in
`relevo status` as `model silent for N min` in place of `working`, and the round
log gets one `provider stall` line per silence episode. No process is touched
yet. A builder running a long foreground command (CPU busy) is never labelled.

**Seams.**
- `internal/relevo/silence.go` (new), pure core:
  `silenceVerdict(lastEvent, now time.Time, after time.Duration, cpuBusy bool,
  cpuKnown bool) (silent bool, quiet time.Duration)` : silent only when the gap
  is `>= after`, CPU is known, and it is not busy. Unknown CPU never produces
  silent. Also `silenceKinds` (a one-line set containing `"opencode"`) and
  `silenceDue(b)` (kind in the set, process alive, round open).
- `internal/relevo/silence_cpu.go` (new, Linux only, guarded by a build tag or
  runtime.GOOS check like the other /proc reads in `internal/spawn`; on other
  platforms report "unknown"): `processTreeCPUTicks(pid int) (ticks uint64,
  ok bool)`: sum utime+stime from `/proc/<pid>/stat` for `pid` and all its
  descendants (walk `/proc/*/stat` ppid links). Read errors return `ok=false`.
- CPU delta state: two additive, omitempty fields on `store.Progress`
  (`internal/store/binding.go:84`): `CPUTicks uint64`, `CPUTicksAt time.Time`,
  refreshed by `progressStep` when the tick count moved by more than a floor
  (`silenceCPUFloor`, 2 CPU-seconds in ticks over the window; comment the
  number: opencode idles at under 1 percent of a core, a suite or `go build`
  burns whole cores). `CPUTicksAt` is "last time the tree burned CPU", which is
  what `silenceVerdict` compares against, so there is no stored busy flag.
  `sampleSignals` (`progress.go:25`) gains a `cpuTicks` field in `signals`.
  Decide on `BindingFormat` as in plan 2 and report it.
- The label: `headlessStatus` (`headless.go:1555-1595`) calls `labelsOf`
  (`progress.go:134`). Add the silence word there in a new helper in
  `silence.go` that takes `(rt, b, now)`: it returns `model silent for N min`
  (`view.AgeText` style: check what `AgeText` prints and use "N min" for whole
  minutes) when the verdict is silent; it is shown only when no `stalled` label
  is present (stalled outranks it) and replaces `exploring`.
- The log line: in the alive branch of `reconcileHeadless`, right after
  `progressStep` (`headless.go:~942-947`), a three-line call into a new
  `noteProviderStall(ctx, rt, tx, next, entries, now)` in `silence.go` that
  appends one `store.KindSwitch` entry (Confirmed, `DirToMasterMind`) with note
  prefix `provider stall:` and the gap, once per stall: skip when the round's
  entries already hold a `provider stall:` note newer than `b.Builder.StartedAt`
  (log-derived, so no stored marker). `slog.Warn("provider stall", ...)` beside
  it.
- Streams: the last-event time is `streamLastActivity(rt, b)`
  (`headless.go:737`), the stream file's mtime, which counts any bytes
  including stderr; say so in a comment.
- Tests in `internal/relevo/silence_test.go`: `TestSilenceVerdictNeedsGapAndIdleCPU`
  (table: gap under/over, busy, unknown CPU), `TestSilenceVerdictNeverFiresWithUnknownCPU`,
  `TestHeadlessStatusShowsModelSilentForMinutes`, `TestProviderStallNotedOncePerEpisode`,
  `TestProviderStallNotNotedWhileCPUBusy`, `TestProcessTreeCPUTicksSumsDescendants`
  (a fake /proc root passed in as a parameter, so no real process is read),
  `TestSilenceIgnoresOtherHarnesses`. Mutation check: drop the CPU guard; the
  busy-CPU test must fail. No test in `cmd/relevo`.

**Steps.**
1. `silence.go` pure core and `silence_cpu.go`, with their tests. Done when
   `go test ./internal/relevo -run Silen` passes.
2. `Progress` fields, `signals.cpuTicks`, `progressStep` CPU bookkeeping. Done
   when the CPU-busy and CPU-idle progress tests pass.
3. Status word and the stall note call site. Done when the status and
   once-per-episode tests pass.
4. Run `make check-affected`.
5. Commit.

**Report must include:** whether the CPU guard held on a real opencode process
if the builder can observe one in the sandbox (`scripts/relevo-sandbox.sh`), and
if not that this is unverified; the `BindingFormat` decision; the remote-parity
gap (item 9 above).


