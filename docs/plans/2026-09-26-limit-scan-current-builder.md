# Limit and denial scans read only the current builder's output (#540) (2026-09-26)

## 1. Overview

Every process of a round appends to one stream file, `rt.Store.BuilderStreamPath(name, round)`, including a builder
switched in mid-round:
- `startProcess` in `internal/relevo/headless.go` keeps the stream on a same-round switch;
- `switchBuilder` in `internal/relevo/switch.go` carries it with `carryStream`.

`startProcess` records where the new process's bytes begin: `b.Builder.StreamStart = <file size at start>`, with
`b.Builder.StreamRound = round`.

The rate-limit and permission-denial scans read `builderTail(rt, b, limitScanLines)` (`internal/relevo/transcript.go`,
~line 144), which is the last 40 rendered lines of the **whole round**. A 429 line written by the previous builder is
therefore matched again and gated against the **current** builder's provider (`providerOf(b.BuilderCandidate)` in
`gateOnLimit`, `internal/relevo/limit.go`). On contabo this wrongly gated cline-pass and openrouter with Google's quota
text.

**Fix:** a tail reader for the current builder's bytes only, used by exactly these four scans (closed list):
1. `limitText` (`internal/relevo/limit.go`, ~line 327), which `reconcile.go` (~line 232) uses;
2. `headless.go` ~line 744: `gateOnLimit(ctx, rt, tx, b, builderTail(rt, b, limitScanLines), false)`;
3. `headless.go` ~line 783: `matchDenial(builderTail(rt, b, limitScanLines), denialPatterns(c, h))`;
4. `headless.go` ~line 923: `gateOnLimit(ctx, rt, tx, b, builderTail(rt, b, limitScanLines), false)`.

Two other callers stay on `builderTail`, because they are for humans and should show the whole round:
- `headless.go` ~789 (the exit entry's tail);
- `headless.go` ~1073 (the status tail).

## 2. Files

```
internal/relevo/transcript.go   + currentBuilderTail; streamTail / diskStreamTail gain a from offset
internal/relevo/limit.go        limitText uses currentBuilderTail
internal/relevo/headless.go     the three scan call sites of §1 use currentBuilderTail
internal/relevo/limit_test.go (or a new scan_scope_test.go)  + tests of §4 step 3
docs/plans/2026-09-26-limit-scan-current-builder.md
```

## 3. Contracts

```
// currentBuilderTail is builderTail limited to what the current builder process wrote: a mid-round switch appends
// the new process to the same stream, so scanning the whole round would charge the old builder's lines to the new one.
func currentBuilderTail(rt Runtime, b store.Binding, n int) string
```
- **Legacy log round** (the same condition `builderTail` checks: `b.Builder.LogPath` is this round's
  `BuilderLogPath`): return `logTail(b.Builder.LogPath, n)`, unchanged.
- **Otherwise**, let `from := b.Builder.StreamStart` when `b.Builder.StreamRound == b.Round`, else `0`. Return the
  last `n` rendered lines of the stream at byte offset `from` or later, through `streamTail` with that offset.

**`streamTail(path, read, segs, fallback, n)` gains an offset:** `streamTail(path, read, segs, fallback, n, from int64)`.
Update every existing caller to pass `0`, so their behaviour is unchanged.
- **In `diskStreamTail`:**
  - the window's `start` is never below `from`;
  - when `start == from`, the first line is complete: treat it like `start == 0` and do not drop a partial first line;
  - the loop ends when `start == from` as it does at `0` today.
- **In the `read` fallback:** render `data[from:]` with `renderStreamFrom(data[from:], from, segs, fallback)`, so segment
  offsets stay absolute. Use whichever of `renderStream` / `renderStreamFrom` fits; the offsets must be the file's
  absolute ones.
- **A `from` past the end of the data** renders nothing.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/relevo/transcript.go` (whole);
  - `internal/relevo/limit.go:1-120,300-400`;
  - `internal/relevo/headless.go:240-310,730-800,910-930`;
  - `internal/relevo/switch.go:95-175`;
  - `internal/relevo/limit_test.go:190-400`;
  - `internal/relevo/headless_test.go:1960-2060` (`TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted`)
    and `:3330-3460` (the `StreamStart` / `carryStream` tests).
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
- Focused: `go test ./internal/relevo/ -run 'Limit|Denial|Tail|Stream|Switch|ExitOnLimit' -count=1 -race`
- Final:
  - `go test ./internal/relevo/ -count=1 -race`
  - `go vet ./internal/relevo/`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
- If `git ls-files` fails in this worktree, check the touched files by hand, and say so.
- Skip `make check`. **No test in `cmd/relevo`.**
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin.

### 1. `streamTail`'s offset and `currentBuilderTail` (§3)

### 2. The four scan call sites (§1)

### 3. Tests (`internal/relevo`)

Build a stream file the way `TestAgyLimitDetectedInRenderedStream` does: the rendered agy stream whose limit line is
detected. Then:

- **a. `TestLimitScanIgnoresThePreviousBuildersLines`:**
  - The stream holds an earlier builder's line that matches the current harness's limit patterns (e.g.
    `RESOURCE_EXHAUSTED (code 429): Individual quota reached …`), followed by the current process's lines, which hold
    no limit line.
  - `StreamStart` is the byte offset where the current process begins, `StreamRound == Round`, and `BuilderCandidate`
    is on a **different** provider.
  - `limitText` contains no limit line, and `gateOnLimit(...)` with `currentBuilderTail` returns `handled == false`.
    The ledger has **no** entry for the current provider.
- **b. `TestLimitScanStillSeesTheCurrentBuildersLimit`:** the same file, but the current process's own lines (after
  `StreamStart`) include a limit line. It is matched and gated against the current provider.
- **c. `TestDenialScanIgnoresThePreviousBuildersLines`:** as a, with a denial line before `StreamStart`. `matchDenial`
  over `currentBuilderTail` finds nothing.
- **d. `TestCurrentBuilderTailFromStaleRoundIsWholeStream`:** with `StreamRound != Round`, `from` is 0, and the output
  equals `builderTail`'s.
- **e. The read fallback:** exercise `streamTail` with a `read` func and a path that is not on disk, `from > 0`. Only
  the bytes after `from` appear.

**Required mutation:** make `currentBuilderTail` pass `from = 0` always. Tests a and c must fail. Report the failing
lines, then revert.

Existing tests must pass unchanged, in particular:
- `TestReconcileHeadlessExitOnLimitGatesAndSwitchesUncounted`;
- `TestAgyLimitDetectedInRenderedStream`;
- `TestSwitchKeepsTheStreamCursor`.

If one of them fails because its fixture put the limit line before `StreamStart`, that is a halt: report it.

### 4. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-limit-scan-current-builder.md`.
3. `git add -A && git commit -m "fix(relevo): limit and denial scans read only the current builder's output (#540)"`.
   A new commit.

## 5. Deletions

None. `builderTail` stays for its human-facing callers.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- `StreamStart` is not set by `startProcess` as §1 says;
- a scan call site is somewhere §1 does not list;
- an existing test's fixture depends on scanning bytes before `StreamStart`.
