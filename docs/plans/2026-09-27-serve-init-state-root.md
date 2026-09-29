# Plan: `serve init` leaves a usable state root (#635)

Facts below were read on the round's base, `a1ff0348`. Every location is named; if a quoted range does not match the tree, halt rather than improvise.

## 1. Today (the reference) and the sharing decision

- `relevo doctor`'s serve/state check is `serveStateCheck` (`internal/doctor/serve.go:114-126`): it fails on `Stat(root)` or on `Probe(<root>/bindings)`, with `fix: relevo serve init`. Both the check and its fix text stay exactly as they are.
- `cmdServeInit` (`cmd/relevo/serve_admin.go:23-64`) calls `serve.InitTLS` (line 48); on `ErrTLSExists` it prints `already initialised; fingerprint <fp>` and returns at line 55 without touching the filesystem. `InitTLS` (`internal/serve/tls.go:186-226`) writes only the two secrets. So neither path today leaves a root, and a second run is a no-op.
- **The daemon's root creation today (the reference):** there is no helper; the only root-creating code in `internal/serve` is inline in `serve.New` (`internal/serve/serve.go:81-117`), lines 91-94: `os.MkdirAll(filepath.Join(cfg.Root, "tmp"), 0o755)`. `<root>/bindings` otherwise appears only when an owner store first saves, via `store.Store.WithLock` (`internal/store/store.go:169`). **Init shares none of it today.**
- **Decision:** init gets one new helper, `serve.EnsureStateRoot(root)`, creating `<root>/bindings` (MkdirAll, `0o755`) — the exact directory the doctor probes. `serve.New`'s start path is the reference for the contract (MkdirAll, `0o755`, idempotent, raw error), not a shared call: sharing it verbatim would create only `tmp`, which does not meet "at least `<root>/bindings`"; and moving `bindings` into `serve.New` would make every root the daemon starts read as initialised (`Initialised`, `internal/serve/pointer.go:85-118`) and turn `TestAdminGatesAvailableUnavailable` (`internal/serve/admin_test.go:458-522`) into a comparison that is always equal. `serve.New`, `InitTLS`, `Fingerprint` and `Initialised` are not touched.

## 2. Behaviour, as a closed list

1. Fresh (no TLS secrets, no root): `<root>/bindings` is created before the secrets; stdout prints `fingerprint <fp>` (existing enroll hint unchanged); exit 0.
2. Already initialised (TLS present, root missing — the laptop's case): the TLS identity is untouched, `<root>/bindings` is created/repaired, stdout prints `already initialised; fingerprint <fp>` for the same identity; exit 0.
3. Second run with the root present: nothing changes (secrets byte-identical, existing files intact; MkdirAll is a no-op); exit 0.
4. Root that cannot be created: `cmdServeInit` returns the error before `InitTLS`; no success line (`fingerprint …` or `already initialised…`); on the fresh path no secrets are written.

## 3. Cases and where each is pinned

| Case | Test | Pins |
|---|---|---|
| fresh with no root | `cmd/relevo/serve_test.go`: `TestServeInitFreshLeavesARoot` | stdout `fingerprint sha256:<64 hex>`; `<state>/serve/bindings` is a directory; the machine DB holds `serve.tls.key` + `serve.tls.cert` |
| already initialised, root missing | `TestServeInitRepairsAMissingRoot` | after `RemoveAll(<state>/serve)`, the next run prints `already initialised; fingerprint <fp1>`, recreates `bindings`, and the key/cert bytes are unchanged |
| already initialised, root present | `TestServeInitSecondRunChangesNothing` | the second run exits 0 with the same line; a sentinel file inside `bindings` and the key/cert bytes are unchanged |
| root cannot be created | `TestServeInitRootFailureIsAnError` | fresh: a regular file at `<state>/serve` → error, stdout without `fingerprint`; already initialised: a regular file at `<root>/bindings` → error, stdout without `already initialised` |
| helper contract | `internal/serve/root_test.go`: `TestEnsureStateRoot` | creates `<root>/bindings` (and the root) from nothing; a second call is nil and leaves a sentinel file alone; a file at `<root>/bindings` is an error |

Test rules: pure/fixture only — `t.TempDir()` for `--state` and for `t.Setenv("XDG_STATE_HOME", …)` (so the machine DB is private), local key generation. No network, no server, no harness, no subprocess; `serve init` spawns nothing and reaches nothing. `internal/doctor` is not imported.

## 4. Seams

```
internal/serve/serve.go                        NEW  EnsureStateRoot(root string) error, inserted just above New (before line 81)
cmd/relevo/serve_admin.go                      ~    cmdServeInit: one call inserted after line 39 (root resolution), before openMachineDB (line 41)
internal/serve/root_test.go                    NEW  TestEnsureStateRoot (three arms)
cmd/relevo/serve_test.go                       ~    append the four tests and two small helpers: runServeInit (stdout + err) and one that reads a secret through openDB
docs/plans/2026-09-27-serve-init-state-root.md NEW  this plan, saved verbatim
```

`EnsureStateRoot` contract: `os.MkdirAll(filepath.Join(root, "bindings"), 0o755)`, raw error returned (as `serve.New` does today); one doc line saying why — the doctor's serve/state check probes exactly this directory — and that MkdirAll also creates the root and repairs a removed directory.

`cmdServeInit` shape after the step (all other lines unchanged):

```
root, err := serveRoot(fs)                                          // 36-39
if err != nil { return err }
if err := serve.EnsureStateRoot(root); err != nil { return err }    // NEW
d, _, err := openMachineDB()                                        // 41
```

## 5. Ordered steps

1. `internal/serve/serve.go`: add `EnsureStateRoot` above `New`. Deliverable: helper + doc line. Verify: `go build ./...` clean.
2. `internal/serve/root_test.go` (NEW): `TestEnsureStateRoot`, three arms of §3. Verify: `go test -count=1 -run TestEnsureStateRoot ./internal/serve/` passes.
3. `cmd/relevo/serve_admin.go`: insert the call in `cmdServeInit`. Verify: `go build ./...` clean; `git diff` shows only that four-line insert in the file.
4. `cmd/relevo/serve_test.go`: append the four tests of §3. Verify: `go test -count=1 -run 'TestServeInit' ./cmd/relevo/` passes.
5. Mutations, each reverted before the next: (a) make `EnsureStateRoot` return nil without the MkdirAll → the focused run must fail (at `TestEnsureStateRoot` and the fresh, repair and failure tests; name the exact ones); (b) delete the `cmdServeInit` call → the four CLI tests must fail. Verify: after both reverts `git diff` matches step 4 and the focused run is green.
6. Full check, once: `make check`, then `make e2e`. If coverage dips, add the missing assertion — never edit `testdata/coverage-baseline.txt` or an exclusion.
7. Write this plan verbatim as `docs/plans/2026-09-27-serve-init-state-root.md`, then one commit — `fix(serve): serve init leaves a usable state root (#635)` — carrying code, tests and the plan. Do not push.

Focused command throughout: `go test -count=1 -run 'TestEnsureStateRoot|TestServeInit' ./internal/serve/ ./cmd/relevo/`.

Working efficiently: read each named range once; one edit call per file (the two code files are one insert each; append tests at the end of the existing file); no searching.

## 6. Deleted (closed list)

1. Nothing. No flag, verb, check, test or file is removed. `cmdServeInit`'s already-initialised path stops returning before touching the filesystem — a changed path, not a deletion, and its printed line is unchanged. The doctor's `serve/state` check and its `relevo serve init` fix text are untouched, and no package moves, so the coverage baseline does not move.

## 7. The report must include

- Each case with its test name and the observed output: the fresh fingerprint line, the repair run's `already initialised; fingerprint …` line, and the failure run's error text.
- The mutation results: which named tests failed for (a) and (b), and that both were reverted.
- `make check` and `make e2e` results.
- `git diff --stat` against the base — exactly the five paths of §4 — and the commit sha plus subject.
- The reference answer of §1: the daemon's root creation is `serve.New` lines 91-94; init does not share it, and why.
- Confirmations: `internal/doctor` untouched; `testdata/coverage-baseline.txt` untouched; no test reached a harness, a network or a server.

## 8. Halt rather than improvise

- If `cmdServeInit`'s root resolution or `serve.New`'s lines 91-94 do not match §1/§4; if a case can only pass by changing the doctor check or its fix text; if a cmd test would need a harness, a network or a server; if coverage would need a baseline edit or a new exclusion; if the diff would touch a file outside §4.
