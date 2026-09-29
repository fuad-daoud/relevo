# Cleanup P2-7a -- fold `internal/actors` into `internal/roles`; `config` stops importing the remote client

Phase 2 of the codebase cleanup (spec `docs/specs/2026-09-25-codebase-cleanup-design.md`,
§5: "roles/ -- roles + actors (actors is an input format for roles)" and
"`config` stops importing `remote/client`"). Base: `main` at `dbceaf71`.

## 0. Rules

- **Run every command in the foreground and wait for it.** Never a background
  task, `&`, a scheduled wakeup, or "I'll wait for the notification": you are a
  headless process; when you end your turn the process exits and the round is
  lost. Do not end your turn until the report and done marker exist.
- **No behaviour change.** Declarations move and are requalified; a few are
  renamed only where a name would collide or stutter (§1). Config document
  formats, JSON field names and on-disk/DB shapes do not change.
- **golangci-lint v2.14.0 must actually run** (install with
  `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0` if
  needed); use `--allow-parallel-runners`.
- **Keep every `t.Parallel()` call.** Goldens and `*contract_test.go` are not edited.
- Two other rounds are moving code out of `internal/relevo` at the same time
  (runner types to `internal/spawn`, capture/drift to `internal/capture`). If a
  rebase at the end conflicts with them, keep both changes.
- If a step is impossible as written, or the code contradicts this plan, stop
  and report. Never add a lint exclusion, `//nolint` or allow-list entry.

## 1. `internal/actors` -> `internal/roles`

Move `internal/actors/actors.go`, `convert.go`, `shipped.go` and their tests into
`internal/roles` (as `actors.go`, `actors_convert.go`, `actors_shipped.go` and
matching `_test.go` files), then delete `internal/actors`. Names move unchanged
(`roles.Actor`, `roles.Entry`, `roles.AgentEntry`, `roles.ShippedAgent`,
`roles.ParseActors`, `roles.ParseAgents`, `roles.EncodeActors`,
`roles.EncodeAgents`, `roles.Shipped`, `roles.ShippedAgents`) with two exceptions:
- `actors.ToRolesFile` becomes `roles.FromActors` (the old name reads
  `roles.ToRolesFile`).
- If any moved name collides with an existing `roles` declaration, prefix the
  moved one with `Actor` (for example `ActorEntry`) and list it in the report.

`internal/actors` imports `agentsrc`, `candidate`, `harness`, `policy`, `roles`;
after the move `roles` imports `agentsrc` -- check `agentsrc` does not import
`roles` (it must not; if it does, stop and report).

Requalify every importer (script it: `gofmt -w -r 'actors.X -> roles.X'` per
identifier, then `goimports -w`): `internal/config/config.go`,
`migrate_actors.go`; `internal/relevo/actors_list.go`, `actors_list_test.go`,
`configaudit.go`, `configedit.go`, `configedit_test.go`; `internal/setup/setup.go`,
`setup_test.go`; `internal/ui/actor_form.go`, `golden_test.go`, `view_actor.go`,
`view_actors.go`, `view_actors_test.go`, `view_agents.go`, `view_agents_test.go`,
`view_candidates.go`, `view_candidates_test.go`, `view_settings_test.go`. Where a
file already imports `roles`, just drop the `actors` import.

Remove `internal/actors`'s rule from `.golangci.yml` and its lines from both
allow-lists. `internal/roles` stays excluded (it is finished in a later round),
but the moved files must not add new `check-comments` violations beyond what
they carried: add each moved file to `scripts/check-comments.allow` **only if**
its source file was listed there (a rename of an existing exemption).

## 2. `config` stops importing `internal/remote/client`

`internal/config` uses only `client.Servers` and `client.ParseServers`
(`config.go:87,274,277,357`, `import.go:277,279`). Move the `Servers` type (with
its methods and whatever unexported helpers `ParseServers` needs) and
`ParseServers` from `internal/remote/client` to `internal/remote`, which `config`
already imports; update `internal/remote/client` and every other user to
`remote.Servers` / `remote.ParseServers`. `internal/remote` must not import
`internal/remote/client` (check). After this, `grep -rn 'internal/remote/client'
internal/config` prints nothing. Move the tests of the moved code with it.

## 3. Coverage baseline

Code moved between packages: `make check`, then
`sh scripts/check-coverage.sh --write`, then restore every line for a package this
round did not touch (only roles, actors (removed), config, relevo, setup, ui,
remote, remote/client may change). Report each changed line.

## 4. Steps

1. §1 move + requalify; `go build ./... && go vet ./...`.
2. `go test ./internal/roles/... ./internal/config/... ./internal/relevo/... ./internal/setup/... ./internal/ui/... -count=1`.
3. §2; build, vet; `go test ./internal/remote/... ./internal/config/... -count=1`.
4. `golangci-lint run --allow-parallel-runners ./...` 0 issues;
   `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` ok.
5. §3; `make check` (foreground) passes.
6. `git fetch origin && git rebase origin/main`; resolve; rebuild; re-run step 2.
7. Copy this plan to `docs/plans/2026-09-26-cleanup-p2-roles.md`; commit once.

Report: the final list of moved identifiers (old -> new), any renames from
collisions, importers changed, the §2 move, coverage lines changed.
