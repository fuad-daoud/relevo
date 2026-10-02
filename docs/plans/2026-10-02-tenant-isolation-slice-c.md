# Plan: #204 slice C — container mode (rootless `podman` per spawn)

Branch `relevo/iso-c`, cut from `origin/main` (`3d22ab1b`, slice B `ba059569`). New commits only; never amend or rebase a pushed commit. Client fetches each closed round as an incremental bundle based on the last commit it absorbed, so a rewrite strands that base.

## 0. Seed vs tree (read first)

- `docs/plans/2026-10-02-slice-c-handoff.md` is **untracked and not present in this worktree**; it exists at `~/projects/relevo/docs/plans/2026-10-02-slice-c-handoff.md`. Its decisions are carried into §1 and §3 here, so the builder does not need it.
- Every spec line anchor was re-checked at HEAD `3d22ab1b`. Key drift: S0's `out/` layout is in `internal/store/out.go`, so a round's stream is `<binding>/NNN-runner.jsonl` (not under `out/`) and `out/` is `<binding>/out`. The stream's parent is the binding dir, which is how `ContainerArgv` finds `out/` without a new `ProcSpec` field.
- `internal/proc` is `//go:build unix`; `internal/isolate` is cross-platform and must stay free of `syscall`. The supervisor script therefore moves into `spawn` (step 1) so the pure core can name it.
- Container mode is **not root**: it keeps today's user unit (`dist/relevo-serve.service`) and the default serve root; only `user` uses `/var/lib/relevo` and the system unit. `serve.isolation_image` is already required for container by `internal/policy/validate.go:301`, and `internal/serve/admin.go:isolationView` already reports the image. Neither changes.
- §5.6 holds: `podman` is not installed here. Every flag below is proven by reasoning about the `podman run` contract, never observed. `make check` must not need podman or network.

## 1. Behaviour and cases

- `serve.isolation: "container"` starts under the user unit; no root, no systemd scope. Startup refuses, with `codeNotAvailable`, when `podman` is not on `PATH` (fail closed). A round whose podman or image is missing halts **NEEDS YOU** naming the missing piece; it does not gate the candidate.
- A container round: `Start` pids podman (base handle), `Alive`/`ExitCode` stay base, the stream's last line is the builder's `relevo-exit:` code, `Rusage` is `ok=false`.
- Mounts are host absolute paths, so `-C` and `CLAUDE_CONFIG_DIR`/`CODEX_HOME` are not rewritten: round tree rw, the binding's `out/` rw, the owner's `repos/<hex>` rw (a linked worktree's `.git` needs `<repo>.git/worktrees/<name>` to commit), each owner harness home per its mode, `/tmp` tmpfs.
- `serve.scope` renders as `--memory`/`--cpus`/`--pids-limit`; an empty field is omitted; `Slice`/`CPUWeight`/`AllowedCPUs` have no podman flag and are dropped.
- Kill reads `<stream>.cid` and runs `podman rm -f`, then base `Kill`; a missing, blank or stale cidfile is not an error. The cidfile lives in the binding dir and is removed with the binding, like the kill record.
- Doctor: container row fails on absent podman or absent image with the exact fix; `none`/`user` rows unchanged.
- `none` and `user` are byte-identical to today. `container` stops being refused.

## 2. Seams (current file:line)

- `internal/isolate/isolate.go`: `Available` 57-63; `Wrap` 240-249; `boundary` 253-262; `Start` 275-291; `Kill` 325-327; `Rusage` 330-332; `translate` 364-371; `Boundary` interface 214-221.
- New `internal/isolate/container.go` (pure core) and `container_podman_test.go` (skips).
- `internal/proc/proc.go`: `ReapFragment` 31-56, `supervisorScript` 58-84, `buildArgv` 174-184, `Kill` 419-448, `Rusage` 453-464. New `internal/spawn/supervisor.go`.
- `internal/spawn/spawn.go`: `ProcSpec` 17-26, `ScopeSpec` 38-57.
- `internal/serve/serve.go`: `Config` 35-95, `repoRoot` 262-268, `runtimeAt` 303-331, `applyTenant` 340-365, `UserModeRoot` 132.
- `cmd/relevo/serve_isolation.go`: `resolveIsolation` 20-33. `cmd/relevo/serve.go`: `cmdServeRun` 389-537, scope block 474-492, cfg 499-526.
- `internal/doctor/serve.go`: `ServeChecks` 66-82, `serveIsolationCheck` 204-240, injectable seams 20-44.
- `dist/relevo-serve.service`; `README.md` 784-820; spec `docs/specs/2026-10-01-tenant-isolation-design.md` §5.6.

## 3. Ordered steps (deliverable → check)

1. **Share the supervisor script.** Move `ReapFragment` and `supervisorScript` to `internal/spawn/supervisor.go` as exported `spawn.ReapFragment`/`spawn.SupervisorScript`; add `spawn.ContainerSupervisorScript = SupervisorScript + "\nexit \"$rc\""`. Alias the old names in `proc` so `internal/proc/proc_test.go` is untouched. The inner supervisor must exit with the builder's code so `podman run` returns it and the outer supervisor's last-line trailer (base `ExitCode`) is the builder's, not `sh`'s 0. → `go test ./internal/proc ./internal/spawn`.
2. **Lift the refusal.** `Available` returns nil for container; `Wrap` returns its boundary. Update `isolate_test.go:TestAvailable`; delete `TestWrapRefusesUnavailableModes`'s container loop. → `go test ./internal/isolate`.
3. **Pure core.** `Mount{Path, ReadOnly}`, `ContainerSpec{Image, RepoRoot, Homes []Mount}`; `ContainerArgv(spec, c) spawn.ProcSpec` returning the podman render with `Scope=nil`, `Credential=nil`, and `Dir/Env/LogPath/StreamPath/DenyEnv` unchanged; `Bounds(scope) []string`. Mounts: `spec.Dir`, `filepath.Join(filepath.Dir(spec.StreamPath),"out")`, `c.RepoRoot`, `c.Homes`, `--tmpfs /tmp`. Env entries become `--env NAME=VALUE`. Name from `spec.Scope.Unit`, else sanitized stream base. cidfile `<StreamPath>.cid`. Tables: `TestContainerArgv` (argv order exactly §5.2), `TestBounds`, `TestContainerArgvNameFallback`, `TestContainerArgvNoScopeNoBounds`. → `go test ./internal/isolate`.
4. **Boundary wiring.** `Boundary` gains `ForContainer(ContainerSpec) spawn.Runner`; `boundary` gains the container spec and podman seams (`PodmanBin`, `LookPath`, `PodmanRm` options defaulting to `exec`). `Start` container branch probes podman and `podman image exists`, refusing with `spawn.ErrBoundarySetup` naming the missing piece, then starts `ContainerArgv`; unbound refuses like an unbound tenant. `Kill` container branch reads the cidfile, `podman rm -f`, removes the cidfile, then base `Kill`. `Rusage` container branch returns `ok=false`. Tables: `TestContainerStartRefusesMissingPodman`, `TestContainerStartRefusesMissingImage`, `TestContainerKillRemovesContainerFromCidfile`, `TestContainerKillToleratesStaleCidfile`, `TestContainerRusageReportsUnavailable`, `TestContainerBoundaryRefusesUnbound`. → `go test ./internal/isolate`.
5. **Serve per-owner binding.** `runtimeAt` calls `applyContainer(root, &rt)` when `cfg.Isolation == ModeContainer`; `applyContainer` binds `ForContainer(ContainerSpec{Image: s.cfg.IsolationImage, RepoRoot: <root>/repos/<hex>})`, or `isolate.Refuse` when the runner is not a `Boundary`. No tenant chown, no `ensureTenantRoots`. Tests: `TestServedRunnerRunsInContainer` (round, gate, consult: podman argv[0], mounts, `Scope==nil`, `Credential==nil`, rusage false), `TestContainerModeRefusesWithoutBoundary`. → `go test -race ./internal/serve`.
6. **cmd/relevo.** `resolveIsolation` refuses container at startup when podman is absent, through a `containerLookPath` package seam so tests are deterministic, naming podman and the fix. `cmdServeRun` in container mode keeps `scopeFromPolicy` as the bounds/name carrier, skips `proc.ProbeScopes`, and sets `scopesStatus = "off (isolation=container)"`. Tests: `TestServeRunRefusesContainerModeWithoutPodman` (seam override, bounded wait like the user test). → `go test ./cmd/relevo`.
7. **Doctor.** Add `containerPodmanLookPath`/`containerImageExists` vars and `serveContainerIsolationCheck`, reached from `serveIsolationCheck` for container: podman absent, image absent, else OK with `isolation=container`. `TestServeChecksIsolationContainer` one row per prerequisite. → `go test ./internal/doctor`.
8. **dist and docs.** New `dist/Containerfile` reference build (git, POSIX `sh`, document that the admin adds harness binaries on `PATH`, `WORKDIR /`). README container section replacing "container isolation is tracked in #204". Spec §5.6 note. Save this plan as `docs/plans/2026-10-02-tenant-isolation-slice-c.md`. → `make check`.
9. **Local podman test.** `container_podman_test.go:TestContainerBoundaryUnderPodman` skips when `exec.LookPath("podman")` fails or `RELEVO_TEST_PODMAN_IMAGE` is unset; otherwise runs the boundary over the real `proc.Runner` and asserts the stream trailer. → `go test ./internal/isolate` (skips here).
10. **Full check.** `gofmt -l $(git ls-files '*.go')`, then `make check`, after `git add -A` (untracked new files escape `scripts/check-comments.sh`). Do not touch `make e2e`; do not regenerate the coverage baseline (add tests).

## 4. The podman contract — each flag, unproven

- `--rm`: removes the container on exit, so names and ids do not accumulate; a stale name collision surfaces as a Start error.
- `--userns=keep-id`: maps the invoking uid/gid to the same ids inside the user namespace, so bind-mounted ownership and git's file checks match the host serve uid.
- `--name <unit>`: stable operator-visible name, unique per round.
- `--cidfile <stream>.cid`: podman writes the container id after creation; Kill reads it. Podman may or may not remove the file with `--rm`, so Kill treats absence as "no container".
- `-v <host>:<host>`: identical in/out paths mean no argv rewriting. `--tmpfs /tmp`: private scratch.
- `--memory`/`--cpus`/`--pids-limit`: the `serve.scope` bounds; `--cpus` takes a decimal, so a `200%` quota becomes `2`.
- `<image> /bin/sh -c <ContainerSupervisorScript> relevo-supervisor "" <argv>`: the container command; `podman run` returns the container command's exit status, which the appended `exit "$rc"` propagates to the outer trailer.
- `--env NAME=VALUE`: the round's environment additions reach the container.

## 5. What is deleted (closed numbered list)

1. `ModeContainer.Available`'s "not available in this build" refusal and the `Wrap` error it returned.
2. `serveIsolationCheck`'s container branch that fails with `Available`'s sentence.
3. `internal/isolate/isolate_test.go:TestAvailable`'s assertion that container is refused.
4. `TestWrapRefusesUnavailableModes`'s container iteration (the loop becomes empty).
5. `internal/doctor/serve_test.go`'s expectation that container fails with `Available`'s sentence.
6. `internal/proc/proc.go`'s own `supervisorScript`/`ReapFragment` definitions (now aliases of the `spawn` exports).
7. `translate`'s comment and path that container never reaches it; container is handled before `translate`.

## 6. Commands

- Focused per step: `go test ./internal/spawn ./internal/proc ./internal/isolate`, then `go test -race ./internal/serve ./cmd/relevo`, then `go test ./internal/doctor`.
- Full, once: `gofmt -l $(git ls-files '*.go')`, then `make check`.

## 7. The report must include

- Commands run and results; `git diff --stat` against `origin/main` versus the files named above, including docs.
- Every flag justified by the `podman run` contract, with the explicit statement that none was observed.
- Mutation checks (break the condition, named test fails), at least: `Available` re-refuses container → `TestAvailable`/`TestContainerArgv`; `ContainerArgv` drops a mount → `TestContainerArgv`; `Kill` skips `podman rm` → `TestContainerKillRemovesContainerFromCidfile`; the readiness probe removed → `TestContainerStartRefusesMissingPodman`; `Bounds` emits `--cpus` from `CPUWeight` → `TestBounds`.
- Any golden moved (expected none) and coverage for `internal/isolate` (baseline not lowered; tests added).
- The residuals of §8, each confirmed or corrected.

## 8. Residuals (deliberately not done)

- **Owner-path git in container mode** stays on the host as the serve uid. §6.3's "inside the container" is deferred: it is outside the issue's §5 scope and the named seams (`internal/git/client.go:run`, `snapshot.go:diffPatch`), and in container mode the container also runs as the serve uid, so there is no privilege crossing. State it plainly.
- `AllowedCPUs`/`Slice`/`CPUWeight` have no podman equivalent and are dropped.
- `Rusage` is unavailable in container mode; no measurement is faked.
- The podman integration test is local-only and skips.
