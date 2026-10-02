# Plan: fix #862 — `relevo unbind <name> --dry-run` really unbinds

Base `origin/main` `3d22ab1b`. New commits only; nothing on a remote binding's branch is amended or rebased. Deliverable: the refusal fix, two pinned tests, and `docs/plans/2026-10-02-unbind-dry-run-refusal.md`, in one commit series.

## Root cause (verified, not re-derived)

`cmd/relevo/unbind.go:47 cmdUnbind` reads `dryRun` in exactly two branches: `--sweep` (`unbind.go:62`) and `--done` (`unbind.go:75`). The plain path (`unbind.go:91` → `relevo.Unbind`, `internal/relevo/bind.go:894`) and the `--pick` path (`unbind.go:78` → `runPick`) never look at it. `relevo.Unbind` on a remote binding POSTs `/v1/bindings/<n>/unbind` first (`bind.go:913-926`), so `--dry-run` is ignored locally *and* on the server. Same shape for `--delete` (documented "with --done", honoured only in `runGC`) and for `--archive` with `--done`.

## Audit: every `--dry-run` registration under `cmd/relevo` (non-test)

| verb | flag site | verdict |
|---|---|---|
| `unbind` | `unbind.go:39` | **bug** — plain and `--pick` ignore it. Refuse (this round). |
| `send` | `send.go:37` | honoured on both paths. `sendPreflight` returns before any `rt.Remote` call for a remote binding (`internal/relevo/send.go:349-372`; only `Store.Load`, `Git.RefSHA`), and `SendDryRun` (`send.go:780`) never calls `rt.Remote`. **Pin.** |
| `chain` | `chain.go:96` | start honoured (`chainDryRun`, `chain_dryrun.go:16`, read-only); `--resume --dry-run` already refused by name (`chain.go:325`). No change. |
| `config agents` | `agent.go:70` | honoured; `DryRun` threaded to `harness.Install` (`install.go:119`), `InstallCustomAgents`, `InstallAccountHome` (`install.go:366`), each skipping writes. No change. |
| `serve gc` | `serve_admin_write.go:304` | honoured; passed to `serve.GCAbandoned` (`admin.go:236`) → `gcArchive` (`admin.go:217`) lists only. No change. |
| `serve unbind` | — | registers no `--dry-run`. Nothing to audit. |
| `unbind`'s other scoped flags | `unbind.go:38,41,42` | `--delete` without `--done` ignored → refuse; `--archive` with `--done` ignored → refuse; `--mastermind`/`--all-masterminds` without `--done` already refused (`unbind.go:65`). |

## Behaviour and cases

A flag a verb does not honour on the same command. `cmdUnbind` refuses with `codeRefused` (`fail`, `clierror.go:118`), before `newRuntime` and any store or network access:

1. `unbind <name> --dry-run` → `--dry-run goes with --done or --sweep`.
2. `unbind --pick --dry-run` → same message.
3. `unbind <name> --delete` and `unbind --pick --delete` → `--delete goes with --done`.
4. `unbind --done --archive` → `--archive goes with a named binding or --pick, not --done` (audit extension; `--done` archives by default, `gc.go:70`).
5. `unbind --sweep --dry-run`, `unbind --done --dry-run`, `unbind --done --delete` keep working unchanged.

No dry run of a single unbind is implemented this round. Flag help text already scopes both flags ("with --done or --sweep", "with --done"), so `main.go` and `registry_rows.go` need no change; the registry flag list is unchanged.

## Seams

- `cmd/relevo/unbind.go:47` `cmdUnbind`: insert a refusal block between the `--mastermind`/`--all-masterminds` check (`unbind.go:67`) and `if *done` (`unbind.go:71`), so the `--sweep` branch and its message stay first (existing `TestUnbindSweepTakesNoBinding`).
- `cmd/relevo/unbind_test.go` (new): refusal table test reusing `captureOutput`, `requireCLIError`, `seedWriteBinding` (`contract_write_test.go:125`), `store.New`/`DefaultRoot`.
- `internal/relevo/remote_test.go` (near `TestSendRemoteRecordsOnlyOnSuccess`, `remote_test.go:1622`): `fakeRemote` (`remote_test.go:55`, records every call in `calls`) with a `beforeCall` that fails the test, and an assertion that `len(fr.calls) == 0` after `SendDryRun` on a remote binding.
- `docs/plans/2026-10-02-unbind-dry-run-refusal.md` (new): this plan.

## Ordered steps

1. **Edit `cmd/relevo/unbind.go`** — add the three refusals (cases 1–4) at the seam above, each `fail(codeRefused, …)`. Know it worked: `gofmt -l cmd/relevo/unbind.go` is empty and `go test ./cmd/relevo -run 'TestUnbind|TestContractWriteForcedFailures'` passes.
2. **Add `cmd/relevo/unbind_test.go`** — one table test (name states what it pins, e.g. `TestUnbindRefusesFlagsItsPathDoesNotHonour`) covering cases 1–4: assert `codeRefused`, exit 2, the message, and that a seeded binding still loads from the same root afterwards. Reaches no network and spawns no harness because the refusal precedes `newRuntime`. Know it worked: `go test ./cmd/relevo -run 'TestUnbindRefuses'` passes.
3. **Mutation check** — delete the `--dry-run` refusal line, confirm the named test fails, restore it. Know it worked: the test is red without the line and green with it.
4. **Add the send pin in `internal/relevo/remote_test.go`** — `TestSendDryRunRemoteContactsNoServer`: seed a remote binding (`store.New(t.TempDir())`, `ModeRemote`, branch `relevo/api`), `fakeGit.refSHA` for `refs/heads/relevo/api`, `rt.Transport = &fakeTransport{}`, `rt.Remote = &fakeRemote{beforeCall: fail}`; call `SendDryRun`; assert `len(fr.calls) == 0`. Know it worked: `go test ./internal/relevo -run 'TestSendDryRunRemoteContactsNoServer'` passes, and it fails if `SendDryRun` ever calls `rt.Remote`.
5. **Write `docs/plans/2026-10-02-unbind-dry-run-refusal.md`** — this plan, in the same commit series. Know it worked: the file exists under `docs/plans/`.
6. **Full check** — `make check` green; no `testdata/coverage-baseline.txt` change, no new `.golangci.yml` or `check-filesize.sh` exclusion. Know it worked: `make check` exits 0 and `git diff --stat` shows only the four paths above.

Focused commands: `go test ./cmd/relevo -run 'TestUnbind'` and `go test ./internal/relevo -run 'TestSendDryRunRemoteContactsNoServer'`. Full: `make check`.

## What this deletes

1. `unbind <name> --dry-run` silently unbinding for real (locally and the remote POST).
2. `unbind --pick --dry-run` silently unbinding for real.
3. `unbind <name|--pick> --delete` without `--done` silently ignoring `--delete`.
4. `unbind --done --archive` silently ignoring `--archive`.
No files or code are removed; no flag is removed.
