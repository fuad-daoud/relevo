# Plan T1: tmpdir-precedence

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

**Behaviour.** A process can carry its own temp directory. When it does, that directory beats an inherited `TMPDIR`/`GOTMPDIR`. Only an entry in the spec's own `Env` (a candidate's configured env) still beats it. A process without one behaves exactly as today.

This plan is process-layer only. It creates nothing on disk and nothing sets the field yet.

**Seams.**
- `internal/spawn/spawn.go`, `ProcSpec` (~17): add `TmpDir string`, documented as the per-process directory that wins over an inherited value.
- `internal/proc/env.go`:
  - `tmpDirEnv` (~63) gets the spec's dir as a second source. Per name, the order is: an `extra` entry, then `spec.TmpDir`, then (old rule) a non-empty inherited value, then the runner's `TmpDir` default.
  - Rewrite the comments on `TmpDirEnv` and `tmpDirEnv`, which state the old rule.
- `internal/proc/proc.go`: thread `spec.TmpDir` through `buildCmd` and `spawnEnv` (~340). Also update the `Runner.TmpDir` comment (~56).
- `internal/proc/proc_other.go`: keep the non-unix build compiling.
- `internal/serve/tenant_tmp.go`:
  - `tmpDirRunner.Start` (~55): when `spec.TmpDir != ""`, hand the spec to the base untouched. The base proc runner applies it, and it must not also append the owner default.
  - When `spec.TmpDir == ""`, keep the per-owner default and the inherited-wins rule. Consults and git still start through it.
  - Update its comments.
- `internal/isolate/container.go`: confirm `ContainerArgv` forwards only `Env`. A `TmpDir` on a container spec is then inert. Add a one-line comment saying why, and a test.

**Steps.**
1. Add `ProcSpec.TmpDir` and the `proc` precedence change. Deliverable: `go test ./internal/proc/...` is green.
2. Change `tmpDirRunner.Start` and the comments. Deliverable: `go test ./internal/serve/... ./internal/isolate/...` is green.
3. Run `GOOS=darwin go vet ./internal/proc/...` to check the non-unix build.
4. Run `make check-affected`.
5. Save the plan to `docs/plans/2026-10-23-tmpdir-precedence.md` in the same commit.

**Tests (named by what they pin).**
- `TestTmpDirEnv` (existing, `internal/proc/env_test.go`) gains cases for each rule:
  - A spec dir beats a non-empty inherited value.
  - A spec dir beats the runner default.
  - An `extra` entry beats the spec dir.
  - An empty spec dir changes nothing.
  - GOTMPDIR is decided on its own, apart from TMPDIR.
- `TestStartSpecTmpDirBeatsInheritedTmpDir` is a real-process test in `internal/proc/proc_test.go`, modelled on `TestStartKeepsTheUsersTmpDir` (~603).
- In `internal/serve/tenant_tmp_test.go`:
  - A spec with `TmpDir` reaches the base with `Env` unchanged.
  - A spec without it still gets the owner default, and an inherited value still wins there.
- In the `isolate` tests: `ContainerArgv` output does not mention `spec.TmpDir`.

**Mutation targets.**
- Drop the `spec.TmpDir` branch in `tmpDirEnv`: the "beats inherited" tests must fail.
- Let `spec.TmpDir` beat `extra`: the "extra wins" case must fail.
- Make `tmpDirRunner` also append when `spec.TmpDir` is set: the serve passthrough test must fail.

**Deleted.** None. The old-rule comment wording is replaced.

**Report must include.**
- The final precedence order, as a table.
- Which tests carry each mutation.
- Confirmation that the container path ignores `TmpDir`.

## Standing constraints (every plan carries them)

- CLAUDE.md applies.
  - Comments say *why* and never restate code.
  - No issue or PR numbers, and no "used to", in code or tests.
  - Functions are at most 70 lines; non-test files at most 600.
  - `headless.go` is over the limit and on the allow-list: do not grow it. New code goes in a new file, and a call-site addition there is paid for by moving the same number of lines out.
  - Add no lint or allow-list exclusion.
- Tests in `cmd/relevo` must not spawn a harness or reach the network, because CI has neither. Test rules as pure functions in `internal/relevo` or the owning package.
- Run only the focused tests of the packages you touched, fixing every error before the next run. Then run `make check-affected` once at the end. The chain runs the full gate (`make check`) after the last plan.
- A step that moves code between packages regenerates `testdata/coverage-baseline.txt` with `sh scripts/check-coverage.sh --write` and says so. No step lowers a baseline.
- Every plan's last step is to save the plan to `docs/plans/2026-10-23-<slug>.md` in the same commit.
- Never amend or rebase a pushed commit. Fix forward with new commits.
- If a premise below contradicts the code, halt and report. Do not improvise.
