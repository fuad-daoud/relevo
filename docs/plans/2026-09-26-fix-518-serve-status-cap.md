# Fix #518 -- `relevo serve status` reports the builder cap from policy

## 0. Rules for this round

- Before anything else: `git fetch origin && git merge --ff-only origin/main`.
  If the fast-forward fails, stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it. Your turn
  ends only after the report is written and the done marker exists.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. System overview

`relevo serve status --json` is the server's admin census. Its
`builders.cap` must be the cap the server enforces: `serve.max_builders` from
the machine's policy, else `max(1, NumCPU-1)`. Today `cmdServeStatus` (`cmd/relevo/serve_admin.go` ~204) builds
its `serve.Server` from `serveAdminConfig`, which never sets `cfg.Policy`, so
`Server.cap()` always falls back to the NumCPU default. The wire-contract test
hides this by normalising `"cap"` to `"<CAP>"`.

Out of scope: the other admin verbs that use `serveAdminConfig` (`serve log`,
`show --owner`, `serve ui`, `serve unbind`, `serve gc`) -- they never read the
cap. A running daemon's `--max-builders` flag (`cfg.MaxBuilders`) is not
visible to an admin verb and stays so.

## 2. File structure

```
cmd/relevo/serve.go                          new helper serveAdminConfigWithPolicy
cmd/relevo/serve_admin.go                    cmdServeStatus uses it
cmd/relevo/serve_contract_test.go            drop "cap" normalisation; fixture sets a cap
cmd/relevo/testdata/contract/serve-status.golden   regenerated: "cap": <fixture value>
docs/plans/2026-09-26-fix-518-serve-status-cap.md  this plan (last step)
```

No other file changes.

## 3. Data and contracts

No new types. Contract change: `serve-status.golden` line 5 becomes a number,
e.g. `"cap": 7`, equal to the fixture's `serve.max_builders`.

New helper in `cmd/relevo/serve.go`, next to `serveAdminConfigWithCandidates`
(`cmd/relevo/serve.go` ~359; `serveAdminConfig` ~289, `loadConfig` ~300):

```
serveAdminConfigWithPolicy(root string, d *db.DB) (serve.Config, error)
  = serveAdminConfig(root, d) with Policy (and Registry) from loadConfig(d).
  Errors: whatever loadConfig returns. Unlike ...WithCandidates, an empty
  candidate set is NOT an error: the census is meaningful with no candidates.
```

`serveAdminConfigWithCandidates` should call the new helper and add its
candidate check and `cfg.Candidates`, so the policy loading lives in one place.

## 4. Pseudocode

```
cmdServeStatus (serve_admin.go ~204-235):
  root, d := adminRoot(fs)
  cfg, err := serveAdminConfigWithPolicy(root, d)   // was serveAdminConfig(root, d)
  if err: return err
  srv := serve.New(cfg)
  ... unchanged
```

## 5. Test

`cmd/relevo/serve_contract_test.go`:
- `wireNormalize` (lines 20-27): delete the `capRE` lines and their comment.
  First confirm with grep that `"cap"` appears in no other contract golden
  under `cmd/relevo/testdata/contract/`; if it does, stop and report.
- `TestServeContractStatusJSON` (lines 85-126): before `run(...)`, make the
  admin path see a policy with `serve.max_builders` = 7 (pick a value that no
  CI machine's NumCPU-1 is likely to equal by accident; 7 is fine). Do this
  through the path `loadConfig` reads: set `XDG_CONFIG_HOME` to a
  `t.TempDir()` with `t.Setenv`, and write `relevo/policy.json` there with the
  smallest valid policy document carrying `serve.max_builders: 7` (read
  `internal/policy` and `internal/config/import.go` for the file's shape). If
  the import path needs other files, or the import refuses the document, stop
  and report instead of seeding the database by hand.
- Regenerate: `go test ./cmd/relevo -run TestServeContractStatusJSON -update-wire`,
  then confirm the golden diff is exactly the `"cap"` line.
- Mutation check: revert `cmdServeStatus` to `serveAdminConfig` and confirm
  `TestServeContractStatusJSON` fails on the cap line; restore.

## 6. Error handling

`serve status` now fails if `loadConfig` fails (a broken config file, a
database newer than the binary is only a warning inside `loadConfig`). That
matches the gate verbs and is intended; no fallback to the default cap.

## 7. Working efficiently

- Read `cmd/relevo/serve.go` 280-375, `cmd/relevo/serve_admin.go` 195-240, `cmd/relevo/serve_contract_test.go`
  whole, and `internal/config/import.go` in one batch.
- One edit call per file.
- Focused loop: `go build ./... && go test ./cmd/relevo -run 'ServeContract|ServeGate' -count=1`.
- Full check once, at the end, in the foreground: `make check`.

## 8. Ordered steps

1. Fast-forward to origin/main (§0).
2. Add `serveAdminConfigWithPolicy`; make `serveAdminConfigWithCandidates` use
   it; switch `cmdServeStatus`. Build passes.
3. Test changes and golden regeneration (§5). Focused tests pass; golden diff
   is the one line.
4. Mutation check (§5). Report the failing assertion text.
5. `make check` in the foreground. `git diff --stat` shows only §2 files.
   Commit: `fix(serve): serve status reports the policy's builder cap (#518)`.
6. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-fix-518-serve-status-cap.md` and commit it.

Report: the diff stat, the golden diff, the mutation check's failure text, and
the `make check` result.

## Round 2

# Fix #518, round 2 -- the gate verbs load config once

## 0. Rules

- This tree is `relevo/fix-518` with two commits on top of main: the #518 fix
  (`serveAdminConfigWithPolicy`, `cmdServeStatus` using it, the pinned cap in
  `serve-status.golden`) and the saved plan. Keep both.
- First: `git fetch origin && git rebase origin/main`. If the rebase conflicts,
  stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it.
- Comments: *why* only, no issue numbers, no `§`, no history. Functions <= 70 lines.
- `cmd/relevo` tests must not spawn a harness or reach the network.

## 1. Problem

`serveAdminConfigWithCandidates` (`cmd/relevo/serve.go` ~376) now calls
`serveAdminConfigWithPolicy` -- which runs `loadConfig(d)` -- and then calls
`loadConfig(d)` a second time for the candidate set. `loadConfig` is not a pure
read: it imports config files into the database and runs one-time config
migrations. One admin verb must load once.

## 2. Change (only `cmd/relevo/serve.go`)

```
serveAdminConfigFrom(root string, d *db.DB, L config.Loaded) serve.Config
  = serveAdminConfig(root, d) with Policy = L.Policy, Registry = L.Registry.
  Pure; no error.

serveAdminConfigWithPolicy(root, d) (serve.Config, error):
  L, err := loadConfig(d); if err -> return
  return serveAdminConfigFrom(root, d, L), nil

serveAdminConfigWithCandidates(root, d) (serve.Config, error):
  L, err := loadConfig(d); if err -> return
  if L.Candidates.Len() == 0 -> errors.New("no candidates configured")   (unchanged text)
  cfg := serveAdminConfigFrom(root, d, L)
  cfg.Candidates = L.Candidates
  return cfg, nil
```

Keep the existing doc comments' substance (the empty-candidates rationale),
trimmed to fit.

## 3. Steps

1. Rebase (§0).
2. The change (§2). `go build ./... && go test ./cmd/relevo -run 'ServeContract|ServeGate' -count=1` passes.
3. `make check` in the foreground. `git diff --stat HEAD` shows only `cmd/relevo/serve.go`.
   Commit: `fix(serve): the gate verbs load config once`.
4. Append a "## Round 2" section to `docs/plans/2026-09-26-fix-518-serve-status-cap.md`
   containing this plan, and commit it.

Report: the diff, and the `make check` result.
