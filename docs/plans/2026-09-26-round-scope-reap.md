# Plan: end a round's systemd scope when its runner goes, and never claim a kill that did not happen

Issue #619. Base: `origin/main` at `89278bce` (facts below verified against it).

## 0. What this round changes

A round's scope unit (`relevo-round-<owner8>-<name>-<round>.scope`) is ended by the
supervisor's own `relevo_reap_scope` when the supervisor lives to run it. When the
supervisor dies first (provider transport error, `kill -9`, daemon restart, OOM) the
scope keeps whatever the builder abandoned, `relevo stop` says "killed" while killing
nothing, and the next send starts round N+1 beside round N's scope. This round adds an
optional `spawn.ScopeStopper`, a `systemctl --user stop` implementation behind it, and
four call sites: the daemon's observed-exit close, the marker close with a dead runner,
`relevo stop`, and the send-time guard for the round before the one being sent. `relevo
stop` then reports what it actually did: `killed`, `reaped` (scope only) or `gone`
(nothing was left running).

Smallest-change rule: only these four call sites, plus the wording and the interface.
Anything else the code suggests (switch, done, unbind, Admit, gate and consult scopes)
is listed in §10 and deliberately untouched.

## 1. Facts, verified against `89278bce`

### 1.1 The only reap is the supervisor's, and only while it lives

`internal/proc/proc.go:35-56` — `ReapFragment` is the POSIX sh function
`relevo_reap_scope <procs_file> <self_pid>`: TERM every other pid in the scope's
cgroup, wait at most 20×0.1 s, then KILL.

`internal/proc/proc.go:68-84` — `supervisorScript` runs it in exactly one place:

```sh
if [ -n "$want" ]; then
  cg=$(cut -d: -f3 /proc/self/cgroup 2>/dev/null | head -1)
  case "$cg" in */"$want")
    ...
    read -r self _ </proc/self/stat
    [ -n "$self" ] && relevo_reap_scope "/sys/fs/cgroup$cg/cgroup.procs" "$self"
    ;;
  esac
fi
printf '\nrelevo-exit:%s\n' "$rc"
```

A supervisor that is killed (or killed by its own harness) never reaches this: the
stream then has no `relevo-exit:` trailer, which is why the daemon logs `code=unknown`.

There is no other place that ends a scope. `internal/proc/scope.go` only probes:
`ScopeActive` (`:106-124`, `systemctl --user show --property=ActiveState --value
<unit>.scope`; a missing `systemctl` is `(false, nil)`) and `ScopeResult` (`:138-151`).
The tree's only other `systemctl` callers are the service-file migration helpers in
`internal/migrate/services_os.go` and `cmd/relevo/migrate.go`; nothing runs
`systemctl stop`/`kill` on a scope unit.

### 1.2 Scope unit names are per round, and mirrored in relevo

`internal/relevo/headless.go:84-90` — `scopeUnitNameFor(kind, owner, name, round, id)`
builds `"relevo-" + kind + "-" + scopeOwner8(owner) + "-" + safeUnitPart(name) + "-" +
strconv.Itoa(round)` (plus `-id` when non-empty).
`headless.go:95-97` — `scopeUnitName(b)` is that with `scopeRound, b.Owner, b.Name,
b.Round, ""`.
`headless.go:105-119` — `scopeFor` returns nil when `rt.Scope == nil` (scopes off) and
otherwise copies the template with `Unit` set.
`headless.go:305` — the round spawn: `spec.Scope = scopeFor(rt, scopeRound,
scopeUnitName(b), cpuPinText(b))`.
`internal/proc/scope.go:21-23` — `ScopeUnitFileName(unit)` appends `".scope"`.
`internal/proc/scope.go:28-49` — `ScopeArgv` launches under `systemd-run --user --scope
--quiet --collect --unit=<unit>.scope`; `--collect` is what frees the name once the unit
is gone.

`internal/relevo/runtime.go:237-241` — `Runtime.Scope`: "Nil means no scopes".

### 1.3 The daemon observes the round's exit in one place

`internal/relevo/headless.go:589-711` — `reconcileHeadless` in order: `drainStream`
(598), `roundOpen` (604), the no-round stray kill (607-628), the queued check (634),
`markerClose` (652), `PID == 0` (660), the gated switch (671-680), then
`rt.Runner.Alive` (682) with the alive branch (694-711).

`headless.go:713-717` — the exited region begins:

```go
	// Exited. The exit code is read once, from the stream's trailer.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.BuilderStreamPath(b.Name, b.Round)); ok {
```

Every route that ends this round's process life is below that line: the marker re-check
(723), the unmarked close (743-771), `exitEntry` (796), the stop-requested close (806),
the OOM requeue (822), the lost-to-restart relaunch/switch (840-933), the escape halt
(938), `gateOnLimit` (942), the denial halt (947), `nudgeResume` (956) and the final
halt or `switchBuilder` (963-971). A relaunch (`startRound`/`resumeRound`), a switch and
a nudge all spawn again under this same round's unit name, which an active scope keeps
busy.

`headless.go:842` — this branch clears the endpoint: `b.Builder.PID, b.Builder.StartedAt
= 0, 0`. That is the state the issue's step 3 was in: round open, pid 0.

### 1.4 The marker route closes a round without ever checking liveness

`headless.go:638-652` — the marker is read and `markerClose` is called *before* the
liveness check, so a builder that wrote its marker and then died inside the same tick
closes here. `headless.go:1034-1066` — `markerClose`: `closedRound := b.Round` (1061),
`closeOnMarker` (1053), the served close (1062-1064), `next.Builder =
clearProcess(next.Builder)` (1065).

### 1.5 `stop` claims a kill it did not do, and touches no scope

`headless.go:1126-1142` — `stopProcess`:

```go
func stopProcess(ctx context.Context, rt Runtime, e store.Endpoint, why string) (int, error) {
	if !e.Headless() || e.PID == 0 {
		return 0, nil
	}
	...
}
```

`internal/relevo/stop.go:53-61` — `stopDecision(b, now)` returns `stopKill` for any
round with `RoundStartedAt` set and no queue stamp. `:139-157` — the switch in `Stop`;
its `stopKill` case calls `stopProcess`, ignores the returned pid, and sets `how =
"killed"` unconditionally:

```go
		case stopKill:
			// No stdin to type into: kill now, then close the round without a
			// report. Ordered so a failed kill leaves the round open and nothing
			// recorded, exactly as `done` does.
			if _, err := stopProcess(ctx, rt, b.Builder, "stop"); err != nil {
				return err
			}
			b.Builder = clearProcess(b.Builder)
			b = abandonSession(b)
			how = "killed"
```

`internal/relevo/text.go:65-75` — `StopText` prints that word: `"... stopped: process
killed; round closed without a report unless one was on disk"`.

### 1.6 The send guard sees only the round being sent

`internal/relevo/send.go:493-510`, in `Send`, inside the state lock, after the plan is
staged:

```go
			if rt.Scope != nil {
				if p, ok := rt.Runner.(spawn.ScopeProber); ok {
					unit := scopeUnitName(b)
					active, perr := p.ScopeActive(ctx, unit)
					if perr != nil {
						slog.Debug("scope probe", "unit", unit, "err", perr)
					}
					if active {
						_ = os.Remove(planPath)
						return fmt.Errorf("binding %q round %d: scope %s.scope is still running -- a builder for this round is already alive (an earlier send may have started it); inspect it with systemctl --user status %s.scope, and relevo stop %s ends it: %w",
							name, b.Round, unit, unit, name, ErrScopeActive)
					}
				}
			}
```

`slog` is used nowhere else in `send.go` (line 502 is the only `slog.` call), so the
`slog` import at `send.go:7` goes with it.

### 1.7 The fakes and the host fallbacks

`internal/relevo/fake_test.go:619-660` — `fakeRunner`: `scopeActive map[string]bool`,
`scopeQueries []string`, `scopeResults`, `scopeResultQueries`. `:736-749` —
`ScopeActive` and `ScopeResult`. It is the runner every test in `internal/relevo` uses;
no test starts a real unit.

`internal/proc/proc_other.go:17-43` — the `!unix` `Runner`: `Start`/`Alive`/`Kill`
return `spawn.ErrRunnerUnavailable`; it implements neither `ScopeProber` nor the new
`ScopeStopper`. On macOS the unix `Runner` exists but `ProbeScopes` fails
(`proc.go:263-270` drops the scope in `resolveScope`), so no scope name is ever created.

`internal/proc/scope_test.go:150-183` — the PATH-stub pattern: write a fake `systemctl`
into a temp dir and set PATH. `internal/proc/helpers_test.go:44-51` — `writeStub` writes
a fake `systemd-run`; add a `systemctl` sibling next to it.

## 2. Design

1. **`spawn.ScopeStopper`** — an optional half of `spawn.Runner`, beside
   `ScopeProber`, with one method `StopScope(ctx, unit) error`. `proc.Runner` (unix)
   implements it with `systemctl --user`; a runner without it is treated as "can see a
   scope, cannot end one".
2. **`internal/relevo/roundscope.go`** — one home for "is this scope loaded", "end it",
   "the round's scope is over, best effort" and "end the previous round's scope before
   the next one starts".
3. **The daemon reaps on an observed exit** — one call at the top of the exited region
   (§1.3), before anything below relaunches, switches or nudges.
4. **The marker route reaps when the runner is already dead** — one call in
   `markerClose` after the process is cleared; a live runner is left alone, because its
   own supervisor reaps.
5. **`stop` stops what is there** — a new `stopOpenRound` kills the recorded process
   only when it is alive and then ends the scope, and the close says `killed`, `reaped`
   or `gone` from what happened.
6. **The send guard ends, or refuses over, the previous round's scope** — before the
   plan is staged, so a refusal leaves nothing behind.

Every new path is gated on `rt.Scope != nil` and on a scope actually being loaded, so a
host with scopes off or no systemd behaves exactly as today (§7).

## 3. Files

New:

- `internal/relevo/roundscope.go` — the five scope helpers (§4.3).
- `docs/plans/2026-09-26-round-scope-reap.md` — this plan, committed verbatim.

Changed (production):

- `internal/spawn/spawn.go` — add `ScopeStopper` after `ScopeProber` (`:149-159`),
  before `ScopeResultProber` (`:161`).
- `internal/proc/scope.go` — add `(*Runner).StopScope` after `ScopeActive`
  (`:102-124`), before `firstNonEmptyLine` (`:126`).
- `internal/relevo/headless.go` — two insertions (`:712`, `:1065`).
- `internal/relevo/send.go` — one insertion (`:476`), one replaced block (`:493-510`),
  drop the `log/slog` import (`:7`).
- `internal/relevo/stop.go` — add `stopOpenRound`, replace the `stopKill` case
  (`:142-151`), update the doc comments at `:35`, `:181`, `:194`.
- `internal/relevo/text.go` — add `StopText` cases for `reaped` and `gone` (`:65-75`).
- `internal/remote/proto.go` — the `Stopped` doc comment (`:137-139`).
- `internal/relevo/served.go` — the `stopped` doc comment (`:122-125`).

Changed (tests):

- `internal/relevo/fake_test.go` — `StopScope` plus two fields (`:649-660`, `:736-749`).
- `internal/relevo/headless_test.go` — three reconcile tests.
- `internal/relevo/send_test.go` — three send tests.
- `internal/relevo/stop_test.go` — four stop tests, one `TestStopPayload` row.
- `internal/relevo/text_test.go` — `TestStopText`.
- `internal/proc/scope_test.go` — `TestStopScope`; `internal/proc/helpers_test.go` — a
  `systemctl` stub helper.

Expected diff: 17 files (16 code/doc files and `docs/plans/2026-09-26-round-scope-reap.md`),
none deleted, no new dependency, no lint/size/coverage exclusion, no change under
`cmd/`. Anything else: halt and report.

## 4. Interfaces and contracts

### 4.1 `internal/spawn` — `ScopeStopper`

```go
// ScopeStopper is the optional half of a Runner that can end a systemd scope and
// everything still in it. Callers type-assert Runtime.Runner to it; a Runner that
// lacks it can see a scope but not end one.
type ScopeStopper interface {
	// StopScope ends the scope unit <unit>.scope and every process still in its
	// cgroup, so a straggler a harness abandoned cannot hold the unit open. It
	// signals first and kills after the runner's own grace, and returns only once
	// the unit is gone; an error means it may still be there. unit is the base name
	// scopeUnitName returns (no ".scope"). A unit that is not loaded, and a host
	// with no systemctl, are not errors: there is nothing to end.
	StopScope(ctx context.Context, unit string) error
}
```

No comment may cite an issue number or `§`: `internal/spawn/spawn.go` is not on
`scripts/check-comments.allow`.

### 4.2 `internal/proc` — `(*Runner).StopScope`

Precondition: `unit` is a base name. Postcondition: nil means `<unit>.scope` is not
loaded (or the host has no `systemctl`); an error means it may still be loaded.

Behaviour, bounded and TERM-before-KILL:

1. `systemctl --user stop --no-block <unit>.scope`. On success, go to 2. On an error:
   a missing `systemctl` (`errors.Is(err, exec.ErrNotFound)`) is nil -- no systemd, no
   scope; otherwise re-probe with `ScopeActive` -- not active is nil (the unit went away
   by itself), still active (or the probe itself failed) is the stop's error, wrapped as
   `proc: stop <file>: ...`.
2. Poll `ScopeActive` every 200 ms until it reports false, or `r.grace()` (KillGrace,
   default `DefaultKillGrace` 5 s) expires.
3. Still active: `systemctl --user kill --kill-whom=all --signal=SIGKILL <unit>.scope`.
   `--kill-whom=all` is required: the scope's main process is the (dead) supervisor, so
   the default `main` would signal nothing. An error here is returned wrapped.
4. One last `ScopeActive`: false → nil, true → `fmt.Errorf("proc: scope %s is still
   running after SIGKILL", file)`.

Never `systemctl --user stop` without `--no-block`: a stop job waits out systemd's own
stop timeout (90 s), and the daemon holds the store lock across reconcile.

### 4.3 `internal/relevo/roundscope.go`

All five helpers live in one new file, package `relevo`, clean comments (the file is not
on the comments allow-list), each function under 70 lines.

- `scopeRunning(ctx, rt, unit) bool` — false when `rt.Scope == nil`, when `rt.Runner`
  is not a `spawn.ScopeProber`, and when the probe errors (the probe error is logged at
  Debug). One gate for "scopes are off", so no caller repeats it.
- `endScope(ctx, rt, unit) (bool, error)` — `(false, nil)` when the scope is not
  loaded; otherwise assert `spawn.ScopeStopper`; a runner that cannot stop returns
  `fmt.Errorf("scope %s.scope is still running and this runner cannot end it", unit)`;
  `StopScope`'s error is wrapped with the unit; success is `(true, nil)`.
- `endRoundScope(ctx, rt, b, round)` — `scopeUnitNameFor(scopeRound, b.Owner, b.Name,
  round, "")`, `endScope`, and on error a `slog.Warn("round scope not ended", ...)`.
  Best effort: the round is already over.
- `roundRunnerAlive(ctx, rt, b) bool` — false for `PID == 0` or a nil runner; `Alive`'s
  answer otherwise; a probe error counts as alive (the same rule `reconcileHeadless`
  uses at `:682-688`), so relevo never ends a scope that might hold a working builder.
- `endEarlierRoundScope(ctx, rt, b) error` — nil for `b.Round <= 1`; otherwise
  `scopeUnitNameFor(..., b.Round-1, "")` through `endScope`, and on error
  `ErrScopeActive` wrapped with the unit and the manual command:

  `"binding %q round %d: the previous round's scope %s.scope is still running and could
  not be ended; stop it with systemctl --user stop %s.scope, then send again: %w"`.

  A successful reap logs `slog.Info("ended the previous round's scope before starting a
  new round", ...)`.

### 4.4 `stopOpenRound` in `internal/relevo/stop.go`

```go
// stopOpenRound stops what an open headless round still has running: its recorded
// process when that is alive, then its scope when that is still loaded, which is
// where a straggler the runner abandoned holds on. It reports what it did -- killed
// for a live process signalled, reaped for a scope ended -- so the close can say
// what happened instead of claiming a kill that never was. An error means the round
// may still have something running and the caller leaves the round open.
func stopOpenRound(ctx context.Context, rt Runtime, b store.Binding) (killed, reaped bool, err error)
```

- `b.Builder.PID == 0` → skip the process half.
- `rt.Runner == nil` with a pid → return `spawn.ErrRunnerUnavailable` unchanged.
- `Alive` error → `fmt.Errorf("binding %q: check previous process %d: %w", ...)`.
- `stopProcess(ctx, rt, b.Builder, "stop")` error → return it; `killed = alive`.
- Then `reaped, err = endScope(ctx, rt, scopeUnitName(b))`; return both.

`stop.go` gains the `internal/spawn` import for `ErrRunnerUnavailable`.

### 4.5 `Stop`'s reported action

`StopResult.Action` gains `"reaped"` and `"gone"`:

- `"killed"` — a live process was signalled (a scope ended too, when one was loaded).
- `"reaped"` — the runner was already gone; its scope was loaded and was ended.
- `"gone"` — nothing was left: no live process, no loaded scope.
- `"dequeued"`, `"nothing"` — unchanged.

`text.go`:

```go
	case "reaped":
		return fmt.Sprintf("%s round %d stopped: reaped the round's scope (its runner was already gone); round closed without a report unless one was on disk", name, res.Round)
	case "gone":
		return fmt.Sprintf("%s round %d stopped: its runner was already gone and nothing was left running; round closed without a report unless one was on disk", name, res.Round)
```

The stopped payload (`stopPayload`) and the log note (`"stopped/" + how`) interpolate
`how` and need no change; both doc comments (`stop.go:181`, `:194`) name the new words.
`ServedView` (`served.go:118-135`) derives its `stopped` field from any
`"stopped/<word>"` KindStop note, so the new words cross the wire unchanged.

## 5. Pseudocode

### 5.1 Daemon, on an observed exit (`reconcileHeadless`)

```
after Alive says not alive (headless.go:711) and before codeText (713):
    endRoundScope(ctx, rt, b, b.Round)      # best effort, warns on failure
    ... the existing exited region, unchanged ...
```

### 5.2 Daemon, on a marker close (`markerClose`)

```
after next.Builder = clearProcess(next.Builder)   # headless.go:1065
    if rt.Scope != nil and not roundRunnerAlive(ctx, rt, b):
        endRoundScope(ctx, rt, b, closedRound)
```

`rt.Scope != nil` first keeps the extra liveness read out of every scopes-off close.

### 5.3 `relevo stop` (local, headless)

```
case stopKill:
    killed, reaped, err = stopOpenRound(ctx, rt, b)
    if err: return err                     # round stays open, nothing recorded
    b.Builder = clearProcess(b.Builder)
    b = abandonSession(b)
    how = if killed: "killed" elif reaped: "reaped" else "gone"
    ... closeStopped(ctx, rt, tx, b, how) unchanged ...
```

### 5.4 `Send`, under the lock

```
after the tier override (send.go:473-475), before the plan is written (477):
    if err := endEarlierRoundScope(ctx, rt, b); err != nil: return err
    # nothing staged yet, so a refusal leaves no plan file and no NEEDS YOU

at the existing guard (send.go:493-510):
    if unit := scopeUnitName(b); scopeRunning(ctx, rt, unit):
        remove the staged plan; return ErrScopeActive with the existing message
```

## 6. Error handling

- **Best effort, logged:** the two daemon reaps (`endRoundScope`). The round's close
  must not fail because a scope would not die; the send guard is the backstop.
- **Refusals, nothing changed:** `endEarlierRoundScope` (before staging) and the
  existing current-round guard (removes the staged plan). Both wrap `ErrScopeActive`, so
  `errors.Is` and the CLI message are unchanged in shape.
- **Round stays open:** `stopOpenRound`'s errors — a failed process kill, an unreadable
  liveness check, or a scope that cannot be ended. Same rule the existing `stopKill`
  comment states: a failed stop leaves the round open and nothing recorded.
- **Not errors:** a scope that is not loaded, and a host without `systemctl`.
- **Unchanged:** `ErrNothingToStop`, the remote stop path, `ErrStopFailed` in
  `done`/`unbind`, and every existing error string except the two new `StopText` lines.

## 7. The non-systemd fallback (keeps working)

- Scopes off (`rt.Scope == nil`): `scopeRunning` returns false before any assertion or
  probe, so no new systemctl call, no refusal and no new error path. The existing
  `TestSendWithScopesOffNeverProbesAScope` already pins "no probe" for the round being
  sent; this round adds the same pin for the previous-round path, for `Stop`, and for
  reconcile.
- macOS / no user systemd: `systemd-run` fails the probe, `resolveScope`
  (`proc.go:263-270`) drops the scope at spawn, so the unit name is never created;
  `ScopeActive` returns `(false, nil)` with no `systemctl` on PATH, so nothing is
  reaped and nothing is refused. `StopScope` is never reached.
- A runner with `ScopeProber` but no `ScopeStopper` (the `!unix` stub and any future
  remote runner): a loaded scope refuses the send with a message naming the unit;
  `endRoundScope` logs and moves on. No panic, no type assertion without `ok`.

## 8. Working efficiently

- Run every command in the foreground and wait for it. Never background a task or end
  the turn before the report and the done marker exist: the process exits when the turn
  ends and the round is lost.
- One test file per behaviour, edits in one call per file. Run the focused test between
  edits, not the whole suite:
  - `go test ./internal/proc/ -run 'TestStopScope' -count=1`
  - `go test ./internal/relevo/ -run 'TestStop|TestStopText|TestSendReaps|TestSendRefuses|TestReconcileHeadlessReaps|TestReconcileHeadlessMarkerClose|TestReconcileWithScopesOff' -count=1`
- Then the package pass, then the full check:
  - `go test ./internal/relevo/... ./internal/proc/... ./internal/spawn/... -count=1`
  - `gofmt -l .` (must print nothing), `sh scripts/check-comments.sh`,
    `sh scripts/check-filesize.sh`, then `make check` (it runs gofmt over every tracked
    file, `go vet`, the linter when installed, both check scripts, tidiness and the
    coverage baseline). If `golangci-lint` is installed, also `golangci-lint run
    --allow-parallel-runners ./internal/proc/... ./internal/spawn/...`: those two
    packages are not lint-excluded.
- Fix every reported error before the next run. Never add a lint, size or coverage
  exclusion, never lower `testdata/coverage-baseline.txt` (a compile-time interface
  adds no statements; the new code is covered by the new tests).
- If a step is impossible as written, or the code contradicts this plan, halt and
  report instead of improvising.

## 9. Steps

**Step 1 — `spawn.ScopeStopper`.** In `internal/spawn/spawn.go`, insert the interface of
§4.1 between `ScopeProber` (ends `:159`) and `ScopeResultProber` (`:161`). Build:
`go build ./internal/spawn/...`.

**Step 2 — `proc.StopScope`.** In `internal/proc/scope.go`, add `(*Runner).StopScope`
after `ScopeActive` (ends `:124`), per §4.2. Keep it under 70 lines and low-complexity
(`internal/proc` is lint-checked): if it grows, split the probe-poll into a small
`scopeGone(ctx, unit) bool` helper. Build: `go build ./internal/proc/...`.

**Step 3 — the proc test.** In `internal/proc/helpers_test.go`, add a `writeSystemctlStub`
next to `writeStub` (`:44-51`); in `internal/proc/scope_test.go`, add `TestStopScope`
with subtests:
- "already gone" — the stub answers `show` with `inactive` and fails `stop`: nil, and the
  argv log has no `kill`.
- "stop ends the unit" — the stub starts `active` and flips the state on `stop`: nil, the
  argv log shows `--user stop --no-block <unit>.scope` exactly once and no `kill`.
- "stubborn unit" — the stub keeps answering `active` after both signals, `KillGrace`
  50 ms: an error naming the unit, and the argv log shows a `kill` with
  `--kill-whom=all --signal=SIGKILL`.
- "no systemctl" — `PATH` is an empty dir: nil.
The stub appends its `$@` to a log file, answers `--user show --property=ActiveState
--value <unit>.scope` from a state file, fails `stop` while the state is `inactive`, and
flips the state to `inactive` on `stop` or `kill` unless a "stubborn" file exists. No real
unit is ever started.
Run: `go test ./internal/proc/ -run TestStopScope -count=1`.

**Step 4 — `internal/relevo/roundscope.go`.** Create the file with the five helpers of
§4.3. Clean comments: no `#NNN`, no `§`, why only. Build: `go build ./...`.

**Step 5 — the fake.** In `internal/relevo/fake_test.go`: two fields beside the scope
fields (`:649-660`) — `scopeStops []string` ("every unit StopScope was asked to end")
and `scopeStopErr error`; and the method after `ScopeResult` (`:749`): record the unit,
return `scopeStopErr` when set, else `delete(f.scopeActive, unit)` and nil. Add
`var _ spawn.ScopeStopper = (*fakeRunner)(nil)` beside the existing assertion at `:781`.

**Step 6 — the daemon reaps.** `internal/relevo/headless.go`: insert §5.1 at `:712`
(between `	}` on `:711` and the `// Exited.` comment on `:713`) and §5.2 at `:1065`
(between `next.Builder = clearProcess(next.Builder)` and `next.StalledSince =
time.Time{}`). Build.

**Step 7 — the send guard.** `internal/relevo/send.go`: insert §5.4's first block
between the tier override (`:473-475`) and `planPath := rt.Store.PlanPath(name,
b.Round)` (`:477`); replace `:493-510` with §5.4's second block (same `os.Remove`, same
error string); delete the `"log/slog"` import (`:7`) — line 502 was its only use.
Build.

**Step 8 — `stop`.** `internal/relevo/stop.go`: add `stopOpenRound` (§4.4) next to
`Stop`; replace the `stopKill` case body (`:146-151`) with §5.3; add the `internal/spawn`
import; update `StopResult.Action`'s comment (`:35`), `stopPayload`'s (`:181`) and
`closeStopped`'s (`:194`) to name `reaped` and `gone`. Build.

**Step 9 — the wording.** `internal/relevo/text.go`: add the two `StopText` cases of
§4.5 (`:65-75`). `internal/remote/proto.go:137-139` and `internal/relevo/served.go:122-125`:
extend the `Stopped`/`stopped` comment word lists to `killed`, `reaped`, `gone`,
`dequeued`. Comments only, no behaviour.

**Step 10 — the relevo tests, one file per behaviour.**

`internal/relevo/headless_test.go` (fixtures: `sentHeadless` `:31-42`, `reconcile`
`fixture_test.go:176-185`; set `rt.Scope = &spawn.ScopeSpec{}` and script
`fr.scopeActive[scopeUnitName(b)] = true`):
- `TestReconcileHeadlessReapsTheRoundsScopeWhenTheRunnerExited` — report on disk, no
  marker, runner scripted dead: `scopeStops` is exactly the round's unit, the round
  closes (`Round == 2`), the pid is cleared.
- `TestReconcileHeadlessReapsTheRoundsScopeOnAMarkerCloseWithADeadRunner` — marker and
  report on disk, runner dead: same asserts. Pins the §5.2 route.
- `TestReconcileHeadlessMarkerCloseWithALiveRunnerReapsNothing` — marker and report,
  runner alive, scope scripted active: `scopeStops` is empty. Pins the liveness guard.
- `TestReconcileWithScopesOffNeverProbesAScope` — `rt.Scope` nil, runner dead: no
  `scopeQueries`, no `scopeStops`.

`internal/relevo/stop_test.go` (fixture `sentHeadless`, then `rt.Scope = &spawn.ScopeSpec{}`
before each Stop; for the dead-runner cases load the binding and `b.Builder =
clearProcess(b.Builder)` then save):
- `TestStopReapsTheScopeWhenTheRunnerIsGone` — pid 0, the round's unit scripted active:
  `Action == "reaped"`, no `Kill`, `scopeStops` is the round's unit, the round closes, the
  log entry is `stopped/reaped`.
- `TestStopReportsGoneWhenNothingIsLeftToStop` — pid 0, no unit scripted active:
  `Action == "gone"`, no `Kill`, no `scopeStops`, log entry `stopped/gone`.
- `TestStopKillsTheProcessAndReapsTheScope` — live pid, scope active: `Action ==
  "killed"`, one `Kill` of the round's handle, `scopeStops` is the round's unit.
- `TestStopWithScopesOffNeverProbesAScope` — `rt.Scope` nil, pid 0: `Action == "gone"`,
  no `scopeQueries`, no `scopeStops`.
- `TestStopPayload` (`:62-112`): one added row, `how: "reaped"` → `"The runner was
  stopped (reaped) for round 2; no report was written."`, note `noreport stopped`.

`internal/relevo/text_test.go`:
- `TestStopText` — the `killed`, `reaped`, `gone`, `dequeued` and default lines
  verbatim.

`internal/relevo/send_test.go` (setup for rounds ≥ 2: `sentHeadless`, write the done
marker and the report, `reconcile`, then `rt.Scope = &spawn.ScopeSpec{}` and script the
previous round's unit active):
- `TestSendReapsAnEarlierRoundsScopeBeforeItStarts` — `Send` of round 2 succeeds,
  `scopeStops` is round 1's unit, one new `Start`, and the plan for round 2 exists.
- `TestSendRefusesWhenAnEarlierRoundsScopeCannotBeEnded` — `fr.scopeStopErr` set:
  `errors.Is(err, ErrScopeActive)`, no new `Start`, no plan file for round 2.
- `TestSendRefusesAScopeItsRunnerCannotEnd` — a test-only runner that implements
  `spawn.ScopeProber` but not `spawn.ScopeStopper` (an embedded interface type satisfied
  only for `ScopeActive`), previous scope active: `ErrScopeActive`, no `Start`.
- Keep `TestSendRefusesWhileTheRoundsScopeIsActive` (`:565-598`) and
  `TestSendWithScopesOffNeverProbesAScope` (`:600-615`) passing; add to the latter's
  sibling a scopes-off round-2 send that still records no query.

Run: `go test ./internal/relevo/ -run 'TestStop|TestStopText|TestSendReaps|TestSendRefuses|TestReconcileHeadlessReaps|TestReconcileHeadlessMarkerClose|TestReconcileWithScopesOff' -count=1`, then the whole package.

**Step 11 — mutation test.** Two mutations, each restored and re-run:
1. In `roundscope.go`, make `scopeRunning` return false before its first check. Expect
   failures in `TestReconcileHeadlessReapsTheRoundsScopeWhenTheRunnerExited`,
   `TestReconcileHeadlessReapsTheRoundsScopeOnAMarkerCloseWithADeadRunner`,
   `TestStopReapsTheScopeWhenTheRunnerIsGone`,
   `TestStopKillsTheProcessAndReapsTheScope` and
   `TestSendReapsAnEarlierRoundsScopeBeforeItStarts`. Restore.
2. In `stopOpenRound`, set `killed = true` unconditionally (keep the kill). Expect
   `TestStopReportsGoneWhenNothingIsLeftToStop` to fail. Restore.
Report the mutation, the test names, and `git diff` being clean after the restore.

**Step 12 — full check.** `gofmt -l .` prints nothing; `sh scripts/check-comments.sh`
and `sh scripts/check-filesize.sh` are ok; `make check` passes in the foreground. Do not
touch the coverage baseline or any allow-list.

**Step 13 — the plan and the commit.** Copy the plan file you were given verbatim to
`docs/plans/2026-09-26-round-scope-reap.md`, `git add -A`, and commit once, e.g.
`fix(headless): a round's scope is ended when its runner goes, and stop says what it
did (#619)`.

## 10. Deliberately not covered (say so in the report)

- `relevo done` and `relevo unbind` still kill only the recorded process. A scope they
  leave behind is caught by the send guard when the binding's name is reused (the new
  round 1 probes the same unit name and refuses), not by a reap of their own.
- A deferred round (`Send{Defer}`) is still started by `Admit` without a scope guard.
- A mid-round switch (`gateOnLimit` with `closeOld`) still spawns under the same unit
  name while the old scope is tearing down; that race predates this round.
- `relevo gate`/`consult`/`verify` scopes are untouched: only `scopeRound` names are
  ended.
- The stray-process branch (`headless.go:607-628`) does not reap the last closed
  round's scope; every close that clears a pid now reaps at the close itself, and this
  branch is only reached for a pid a close did not clear.

## 11. Report

Name: the files touched; each new test and what it pins; the two mutations and the
tests that failed under them; that `make check`, `gofmt -l .` and
`sh scripts/check-comments.sh` pass; that no lint/size/coverage exclusion was added;
the deliberately-not-covered list of §10; and any place the code contradicted this plan.
