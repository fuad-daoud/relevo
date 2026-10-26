# Nudge-resume scope fix — implementation plan

## 1. Problem

A reviewer (reader-shaped) round ended its turn with code 0 and no output, was nudged,
and the resumed builder process ran outside its own systemd scope — in the owner's login
scope (`session-2.scope`) — so `relevo doctor` warns a daemon restart may kill it.
Fresh spawns on the same bind paths land in their own scopes. The defect is specific to
the resume path, not to spawning in general.

## 2. Current behaviour (verified from code, read-only)

- `nudgeResume` (`internal/relevo/nudge.go:156`) resumes via `resumeRound` (`internal/relevo/headless.go:381),
  which shares `startProcess` (`internal/relevo/headless.go:248`) with a fresh `startRound`
  (`internal/relevo/headless.go:186`). Both set `spec.Scope = scopeFor(rt, scopeRound, scopeUnitName(b),
  cpuPinText(b))` (`internal/relevo/headless.go:336`).
- `scopeFor` (`internal/relevo/headless.go:124`) returns nil if and only if `rt.Scope` is nil.
  So the resume path requests a scope exactly when the reconciling runtime carries a template.
  A missing scope in the resume spawn means the spawn ran under a scope-less runtime or the
  scope was dropped below — not a missing line in the resume path. The MasterMind's evidence is confirmed.
- The issue's "wait client reconciled the round" hypothesis is refuted from code:
  `cmdWait` (`cmd/relevo/wait.go:66`) calls `relevo.Wait` (`internal/relevo/wait.go:381`), whose poll
  loop only loads bindings, reads logs, classifies `WaitOutcome`, and pulls deliveries; it never calls
  `Reconcile` and never spawns (imports are `delivery`, `reporttail`, `store`, `view` only).
  No file under `cmd/relevo` calls `Reconcile`, `reconcileHeadless`, `nudgeResume`, or `resumeRound`.

### (a) Where `Runtime.Scope` is populated

- `Runtime.Scope` field and its nil-means-off contract: `internal/relevo/runtime.go:332`.
- Daemon and every CLI one-shot share one constructor: `newRuntimeOn`/`newRuntime`
  → `newRuntimeWith` → `buildRuntime`, which sets `Scope: scopeFromPolicy(pol.ScopeFor(false))`
  (`cmd/relevo/wire.go:422`; constructors at `cmd/relevo/wire.go:160,176,197`).
- `scopeFromPolicy` (`cmd/relevo/serve_isolation.go:109`): nil only when explicitly disabled;
  otherwise on by default (`CPUWeight: 100`, other fields passed through).
  `ScopeFor(served)` (`internal/policy/policy.go:473`): served rounds take `serve.scope` when set,
  else the top-level `scope`.
- Serve daemon: `scope := scopeFromPolicy(pol.ScopeFor(true))`, probed at startup, nilled on
  user/container isolation or probe failure (`cmd/relevo/serve.go:482`); per-owner `runtimeAt`
  carries `Scope: cfg.Scope` (`internal/serve/serve.go:404`).
- Daemon-only runtime extras (CLI one-shots lack them): `StartedAt`, `Watched`, `AuthGrace`
  (`cmd/relevo/daemon.go:230`); `lostToRestart` (`internal/relevo/watched.go:75`) is false without them.
- Consequence: with default policy, daemon, CLI, and serve runtimes all carry a template.
  A scope-less spawn therefore needs either a disabled scope block or a per-spawn drop (see (d)).

### (b) Every caller that can reach `nudgeResume` / `resumeRound`, and in which process each runs

- `nudgeResume` has exactly two callers, both inside the daemon/serve tick:
  `reconcileHeadless` exited-round branch (`internal/relevo/headless.go:1184`) and
  `nudgePlanOnMarker` (`internal/relevo/headless.go:1253`) via `markerClose`
  (`internal/relevo/headless.go:1374`, reader-marker path).
- `resumeRound` additionally via the lost-to-restart branch (`internal/relevo/headless.go:1108`)
  and via `resumeSameSession` (`internal/relevo/resume_same.go:72`) ← `sameSessionRecovery`
  (`internal/relevo/transport_resume.go:17`) ← `reconcileHeadless` (`internal/relevo/headless.go:1199`).
- All of the above are reachable only through `reconcileHeadless` (`internal/relevo/headless.go:740`)
  ← `Reconcile`/`reconcileWith` (`internal/relevo/reconcile.go:149,155,238`)
  ← local `Daemon.tickOne` (`internal/relevo/daemon.go:479`)
  or serve `Server.Tick` → per-owner tick (`internal/serve/daemon.go:29`).
- Fresh sends spawn in the CLI process instead: `cmdSend` → `relevo.Send`
  (`cmd/relevo/send.go:106`, `internal/relevo/send.go:485`) → `startRound` (`internal/relevo/send.go:631`).
  Fresh-send-scoped plus resume-unscoped under one policy points at a per-spawn drop at resume
  time (see (d)), not at a permanently scope-less reconciler.
- Adjacent fact the fix must respect: `nudgeResume` never calls `freeRoundScope`
  (`internal/relevo/roundscope.go:127`) before reusing the round's unit name; a still-loaded old
  scope surfaces as an `isScopeBusy` Start error (`internal/relevo/headless.go:59,356`), which
  `nudgeResume` already converts into fall-through to the exit path (`internal/relevo/nudge.go:177`).
  That path fails loudly — it cannot produce the observed silently-unscoped process.

### (c) How the doctor's restart check detects an out-of-scope builder

- Collector: `RunningProcs` (`internal/relevo/runningprocs.go:22`) — local bindings only, with a PID:
  headless builder, current gate, running headless consults.
- Wiring shared by `doctor` and `status`: `restartCheck` → `toRunningProcs` → `doctor.RestartSafety`
  (`cmd/relevo/restart_notice.go`), reading `/proc/<pid>/cgroup` per PID via `readCgroup`.
- Verdict: `RestartSafety` (`internal/doctor/restart.go:44`) parses the `0::` unified line
  (`ParseUnifiedCgroup`) and `inOwnScope` accepts only a last element of `relevo-*.scope`;
  anything else is an offender → Warn "`N` of `M` running outside their own scope (`...`): a daemon
  restart may kill them", `Unsafe = N`, surfaced as the `restart unsafe` status line (`restartNotice`).
- Pin the fix the same way: a regression test asserting the resumed PID's cgroup last element equals
  the round's `scopeUnitName` unit file, mirroring `inOwnScope`, not merely that `Runner.Start` was called.

### (d) What a scope-less `Runner.Start` does

- `Start` (`internal/proc/proc.go:207`) calls `resolveScope` (`internal/proc/proc.go:303`):
  `Scope == nil` passes through unchanged; `buildArgv` (`internal/proc/proc.go:162`) then execs
  without the `systemd-run --user --scope` wrapper (`ScopeArgv`, `internal/proc/scope.go:28`),
  so the child inherits the spawner's cgroup — the login `session-*.scope` when the spawner is a
  login-shell child, `relevo.service` when it is the service daemon.
- A failed lazy probe drops the scope the same silent way, with only
  "scopes unavailable; builders will run in this process's cgroup" (`internal/proc/proc.go:121`):
  first scoped `Start` probes (`scopesUsable`, `internal/proc/proc.go:107`), success is sticky,
  failure is trusted for `ScopeReprobeAfter = 5m` (`internal/proc/proc.go:49`). One transient
  `systemd-run` failure therefore costs exactly the spawns in its window their scopes — matching
  "resume unscoped, fresh spawns scoped" with no code difference between the two paths.

## 3. Steps

1. Diagnose first: reproduce which scope-less cause fired — nil `rt.Scope` in the reconciling process
   vs transient `resolveScope` probe drop — with named observable `scope-drop-source`. Deliverable: a
   scratch probe (under `/tmp`, not committed) plus a log/journal read; done when it names one cause
   with its line (nil template at `headless.go:125` branch, or the `proc.go:121` warning at resume time)
   and refutes the other; the wait-client theory stays refuted per §2 and is not re-investigated.
2. Add the failing regression test at the layer step 1 implicates. If the drop is a nil template or a
   `scopeFor` result, pin it in `internal/relevo/nudge_test.go`: seed a headless binding with `rt.Scope`
   set and a fake runner, run `nudgeResume`, assert the recorded `ProcSpec.Scope.Unit` equals
   `scopeUnitName(b)` and the scope carries the template fields; making `scopeFor` return nil must fail it.
   If step 1 instead implicates the lazy probe drop inside `Runner.Start` (`internal/proc`), the test
   belongs in `internal/proc` (a failed probe must not silently un-scope a resume spawn) — a
   `ProcSpec.Scope`-only assertion in `internal/relevo` cannot see that drop and is not sufficient.
   Either way the fix must make the drop observable to `startProcess` (an error it can refuse on), not
   merely assert the spec field. Done when the test passes with the fix and fails under the stated mutation.
3. Implement the fix at the shared seam (`startProcess`/`scopeFor` area, `internal/relevo/headless.go:124,248`):
   chosen remedy — a resume (`resumeRound`, including `nudgeResume` and `resumeSameSession`) refuses
   instead of silently spawning unscoped when a scope was configured but is missing at spawn time, so it
   falls through to the existing exit/switch path exactly like a resume Start error does today
   (`nudge.go:177`); scopes-off/policy-disabled hosts keep today's behaviour. Rejected alternative —
   per-command template construction in `cmd/relevo` (duplicates `buildRuntime`'s single seam at
   `wire.go:422` and drifts) and keeping the silent `resolveScope` downgrade for resumes. Done when the
   step-2 test passes and no fresh-send Fill behaviour changes.
4. Extend the doctor-side pin only if step 1 implicates the probe: assert `inOwnScope`-equivalence for
   the resumed unit in `internal/doctor/restart_test.go` style; otherwise leave `internal/doctor`
   untouched. Done when `git diff --stat` shows only the declared files (step 2–3 files plus this plan doc).
5. Run only the targeted tests for the packages you touched — e.g.
   `go test ./internal/relevo/ ./internal/proc/` (plus `./internal/doctor/` only if step 4 touched it) —
   and `gofmt -l` on the files you changed. Done when those pass in-session.
   Do NOT run `make check` or `make check-affected`: the MasterMind runs the gate after the round
   closes and will bounce failures back. A full-suite run by the builder wastes the round.
6. Place this plan at `docs/plans/2026-10-26-nudge-resume-scope.md` unchanged apart from path headers.
   Done when the file exists at that path with no code or test commentary added.

## 4. Behaviour deleted by this fix (closed list)

1. A nudge/same-session resume no longer starts a builder outside its own systemd scope when a scope
   was configured but dropped at spawn time; the resume is refused and the existing exit/switch path runs.
2. Nothing else is removed: fresh sends, scopes-off hosts, and the `isScopeBusy`/`ErrScopeActive`
   refusal paths keep their exact behaviour.

## 5. Constraints

- Style per `CLAUDE.md`: no issue/PR numbers or history in code or test comments; test names say what
  they pin; functions ≤ 70 lines, non-test files ≤ 600 lines; no new linter exclusions.
- CLI tests must not spawn a harness or reach the network: test the rule as a pure function in
  `internal/relevo` (fake runner + `scopeFor`/`startProcess` seam), not via `cmd/relevo`.
- Coverage: `internal/relevo` is held by `testdata/coverage-baseline.txt` on PRs touching its files;
  regenerate with `sh scripts/check-coverage.sh --write` only if code moves between packages, never lower it.

## 6. Existing tests that pin the surrounding behaviour

- `internal/relevo/headless_test.go:530` `TestStartRoundSetsScope` (template fields flow into `ProcSpec.Scope`);
  `internal/relevo/headless_test.go:608` `TestScopeFor` incl. nil-template and no-mutation cases;
  `internal/relevo/headless_test.go:3058` (nil `rt.Scope` performs no scope queries).
- `internal/relevo/gate_test.go:135,177,186` (gate scope from template / nil stays nil);
  `internal/relevo/cpus_test.go` (AllowedCPUs pin handling through the same template).
- `internal/relevo/nudge_test.go:476` `TestNudgeRecordsTheOutgoingSegment` (nudge resume shape; extend beside it).
- `internal/proc/scope_accept_test.go`, `scope_expand_test.go`, `proc_test.go` scope cases (runner-side argv/probe).
- `internal/doctor/restart_test.go:68,75` (in-scope OK vs outside-scope Warn pinning the user-visible symptom).

## 7. Report must include

- The step-1 verdict: which cause from §2(d) vs §2(a) fired, with the log/journal line or probe output,
  and confirmation the wait-client theory stayed refuted.
- Files changed vs the §3 declared set (`git diff --stat`), the exact targeted-test commands run and their results,
  the mutation check outcome (which condition was broken, which test failed without the fix),
  and whether the coverage baseline was regenerated.
