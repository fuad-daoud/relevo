# Plan: placement preference S1 — the actors `placement` field (config only)

Seed: the agreed design at
`/home/fuad/projects/relevo/docs/specs/2026-09-30-placement-preference-design.md`
(untracked in the main tree; copy it into this worktree and ship it with this
change — specs ship with the implementation in this repo). Read it first:
§2 and the staging section are this round's contract. Where this plan and the
spec disagree, halt and say so; the spec is authoritative.

**Scope: the config half only.** Parse, validate and display `placement`.
This round changes **no** bind, send, server or resolution behavior:

- no `internal/relevo` resolution work, no `--local`, no probe, no endpoint:
  those are S2 (server) and S3 (resolution), separate rounds;
- the registry (`Row`, `Ranked`) does **not** learn placement yet;
- `relevo.EditActor`/`SetActorEntries` need no production change (they mutate a
  loaded `Actor` copy), but add a test pinning that they preserve a placement.

## 1. What changes

1. **`internal/roles/actors.go`**
   - `Actor` gains `Placement []string \`json:"placement,omitempty"\``. Doc:
     ordered, most preferred first; `local` names this machine; empty means
     `["local"]`; a non-local entry names a `servers` section entry.
   - `validateActor` (`:186`): after the candidates loop, check each placement
     entry — non-empty, no duplicate (an empty entry error names the actor and
     index in the house wording, e.g.
     `actors: <name>.placement[<i>]: empty entry: <ErrBadActors>`).
   - `EncodeActors`/`ParseActors` round-trip the field with no work
     (`encoding/json`), but pin it in a test.
2. **`internal/remote/servers.go`**
   - `ParseServers` refuses the key `local` as reserved before validating the
     entry, with an error naming both the file and the reserved name (word it
     as the parser's other errors: `parse servers file: local: ...`). `local`
     is the placement sentinel, so a server may never shadow it.
3. **`internal/config/document.go`** (or a new `internal/config/placement.go`
   plus its test if that keeps files small)
   - after `loadServers` (`document.go:34`), run a new `checkActorPlacement(L
     *Loaded) error`: for every actor, every placement entry must be `local`
     or an entry of `L.Servers`. Error names the actor, the index and the
     unknown server, and wraps nothing new:
     `actors: <name>.placement[<i>]: server "<entry>" is not in the servers section`.
   - It must run inside `decodeDoc`, so `Load`, `Put`, `PutDoc`, `Delete` and
     the TUI write path all agree (the read and the prospective write share
     `decodeDoc`). `loadActors` runs before `loadServers`; the check runs
     after both.
4. **`internal/view/actors_list.go`**
   - `formatActor` (`:52`) gains one indented line, printed only when
     `len(a.Placement) > 0`, in the style of the `candidates` line:
     `  placement  zen, local`. Update the function doc comment; nothing else
     in the human view changes.
5. **`cmd/relevo/readjson.go`**
   - `ConfigView.Actors` is the stored map, so `--json` carries `placement`
     automatically once the field exists. Verify, and only if `ConfigView`
     transforms actors, add the field there. Pin it with a test.
6. **README.md**
   - document `placement` where the actors section is documented (one short
     paragraph: ordered, `local` sentinel, empty = local, cross-checked against
     `servers`). Do not restructure the README.

## 2. Ordered steps (done-when)

1. Field + validation in `internal/roles`; `go test ./internal/roles/...`
   passes and the new round-trip/validation tests exist.
2. Reserved `local` in `internal/remote`; its package tests pass.
3. Cross-check in `internal/config`; the add/set/delete tests in §3 pass.
4. Views; `go test ./internal/view/... ./cmd/relevo/...` passes.
5. Copy the spec into `docs/specs/2026-09-30-placement-preference-design.md`,
   save this plan as `docs/plans/2026-09-30-placement-s1-config.md`, update the
   README.
6. Once, at the end: `make check`. Then commit on this binding's branch with a
   message like `feat(config): an actor names its placement servers` (no issue
   number, no spec citations in the code).

## 3. Tests

Use the existing fixture helpers in `internal/config/helpers_test.go` and the
existing table styles; no new fixture shapes.

- **roles**: valid placement `["zen","local"]` round-trips through
  `EncodeActors`/`ParseActors`; an empty entry is refused; a duplicate is
  refused; an actor with no placement marshals without the key.
- **remote**: `ParseServers` refuses `local`, accepts any other name; entries
  unchanged otherwise.
- **config**: with a `servers` body containing `zen`:
  - `["zen","local"]` accepted through `Put(Actors, ...)`;
  - `["nope"]` refused, and the message names actor, index and server;
  - `Delete(Servers)` refused while an actor names `zen`, accepted once the
    actor drops it (prove both through the store, not the helper alone);
  - `["local"]` accepted with no servers section at all;
  - the legacy `roles` path is unaffected.
- **view**: `FormatActors` prints the placement line only when set;
  `ConfigView`'s JSON carries it.
- **configedit preservation** (in `internal/relevo`): `EditActor` and
  `SetActorEntries` keep an existing `Placement` untouched.

## 4. Mutation pins

Each mutation must make the named test fail; name each result in the report.

- Remove the `checkActorPlacement` call from `decodeDoc` → the unknown-server
  test fails.
- Remove the reserved-name refusal in `ParseServers` → the `local` test fails.
- Drop `Placement` from `Actor` (or its json tag) → the round-trip and view
  tests fail.

## 5. Files this round touches (closed)

1. `internal/roles/actors.go`
2. `internal/roles/actors_test.go`
3. `internal/remote/servers.go`, and its test file
4. `internal/config/document.go` (and/or a new `internal/config/placement.go`)
   plus test file(s)
5. `internal/view/actors_list.go`, `internal/view/actors_list_test.go`
6. `cmd/relevo/readjson.go` only if it transforms actors, plus its test
7. `internal/relevo/configedit_test.go`
8. `README.md`
9. `docs/specs/2026-09-30-placement-preference-design.md` (new, copied)
10. `docs/plans/2026-09-30-placement-s1-config.md` (new, this plan)

Anything outside this list is a halt.

## 6. Rules

- `make check` is the gate: gofmt, vet, tests, comment and file-size checks.
  Run it once at the end, not per step.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code (repo rule).
- Functions ≤ 70 lines, non-test files ≤ 600; if a new exclusion seems needed,
  halt instead.
- Coverage: if `make check` reports the baseline check, say so in the report;
  never lower a baseline. Regenerate with `sh scripts/check-coverage.sh
  --write` only if the round moved enough code to justify it, and say so.

## 7. What the report must include

- `git diff --stat` compared against §5; anything outside the list is a halt.
- The exact new error strings (cross-check and reserved-name).
- For each mutation in §4: the mutation made and the named failing test.
- `make check` result, and whether the coverage baseline moved.
- Confirmation that the spec file and this plan are committed in this branch.
