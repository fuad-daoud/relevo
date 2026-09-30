# Plan: placement S5 — the actor view edits its placement list

Seed: `docs/specs/2026-09-30-placement-preference-design.md` §7, last bullet
(and §2 for the field's meaning, §8 stage 4). Read the spec first. Where this
plan and the spec disagree, halt and say so; the spec is authoritative.

**Scope:** `:actors › <name>` edits the actor's ordered `placement` list next to
its candidate list, and `internal/relevo` gains the one validated API those keys
call. `local` is the sentinel; every other entry names a `servers` section key.

**Non-goals**

- No CLI change: no `--local`, no bind/probe resolution, no `relevo config`
  output change (S1/S3 own those), no new verb, flag or registry row.
- No `:servers` change, and no new `:` command or view: `:actors` already
  exists, so `cmdline.go`, `view_rounds.go` and `command-real-132.golden` stay
  untouched.
- No new `Actions` method: `ApplyConfig` is the one write seam
  (`internal/ui/actions.go` is exactly 600 lines and must stay there).
- `actor_form.go` keeps the scalar fields (agent, tier, check). No placement on
  the actors list, the context line, `resume`/`rebind`, or re-homing.
- No `configaudit.go` change: a placement edit is an `actors.<name>` path, and
  `changeSubject` already answers `actor <name>` for that arm.
- No README/`relevo help` change: no verb, flag or view appears or disappears,
  and `placement` is already documented (S1).

## 0. Preconditions and one finding

1. S4 must be merged: `ConfigDoc.Servers` (`relevo.LoadConfigDoc` reading
   `config.Servers`), `remote.ServerEntry`, `relevo.ServerProbe`, and the UI's
   `serverNames(map[string]remote.ServerEntry)` helper. S1 (`roles.Actor.Placement`
   + `config.checkActorPlacement`) and S2 are already in this worktree.
   Read S4 from `relevo/placement-s4` through the shared object store if the
   branch is not yet in `main`.
2. **Finding (must be reported, not worked around).** On
   `relevo/placement-s4` as read, `internal/ui/view_servers.go` is 603 lines
   and is not in `scripts/check-filesize.allow`, so `scripts/check-filesize.sh`
   fails on that branch (ceiling 600, test files exempt). S5 cannot reach a
   green `make check` over that file, and fixing S4's file is not this round.
   Verify at the start; if the merged tree still carries it red, halt and report
   that S4 must land green first.

## 1. What changes

1. **`internal/relevo/configedit.go`** — `SetActorPlacement`, placed next to
   `SetActorEntries` (line 327, after it) and built on `actorEdit` (line 482)
   exactly as `SetActorEntries` is.
   ```go
   func SetActorPlacement(d ConfigDoc, actor string, entries []string) (ConfigEdit, error)
   ```
   - unknown actor → `&FieldError{"", "no actor named " + actor}`;
   - per entry, in order: `entry == ""` → `"placement entry is empty"`;
     a repeat → `"duplicate placement " + entry`; an entry that is neither
     `"local"` nor a key of `d.Servers` → `"no server named " + entry`;
   - otherwise copy `entries`, replace only `Placement` on a copied actors map,
     and return `actorEdit(d, acts, actor, "edit actor "+actor+" placement")`;
   - `nil` and empty `entries` both clear: `Placement` is stored as nothing, so
     the next load reads `["local"]` (§2). Nothing else on the actor changes.
   A doc comment says *why* two things: clearing stores nothing so the absent
   field keeps meaning `["local"]`, and the unknown-server refusal lives here so
   the cockpit's notice is human rather than the store's raw cross-check line
   (`internal/config/placement.go`) — which stays the backstop on write.
2. **`internal/relevo/configedit_test.go`** — one new test function after
   `TestConfigEditPreservesPlacement` (line 415). Reuse the S4 fixture
   `configeditServerDoc(t)` (servers `backup`/`zen`, builder placed on `zen`)
   and the existing `decodeActors`, `wantFieldError`. No fixture change.
3. **`internal/ui/view_actor_placement.go`** (new) — the placement pane's whole
   behaviour so `view_actor.go` stays well under the 600-line ceiling:
   - focus constants and `actorPlacementLines(...)`: the `PLACEMENT` header and
     the entry rows, or the one muted default line when the list is empty;
   - `copyPlacement`, `withActorPlacement`, `changePlacement` (the three steps
     `change` runs, with `relevo.SetActorPlacement`), and
     `updatePlacementKey(k, env)`: `↑↓`/home/end/pgup/pgdn move, `shift+up`/
     `shift+down` reorder, `d` remove, `a` picker, `e` the actor form, `space`
     inert;
   - `placementPickCmd`/`placementPickItems`: the `listBox` from `list_box.go`,
     `local` first then the configured servers (`serverNames(doc.Servers)`)
     minus what is already listed, appended on pick. An empty item list is a
     `notice`, never an empty box: `listBox`'s empty text speaks of candidates.
4. **`internal/ui/view_actor.go`** (+ ~60 lines, no new file needed for the
   integration):
   - struct (line 17): `focus int` (0 candidates, 1 placement) and
     `placeCur int`;
   - `bodyLines` (line 122): after the candidate rows, a blank, the placement
     header and rows, then the existing two blanks and check line;
   - `follow` (line 143): the selected line is the focused pane's — candidates'
     `2+cur` is unchanged; placement's is the first placement row plus
     `placeCur`;
   - `Keys` (line 166): focus-dependent (`tab` says `placement` on candidates,
     `candidates` on placement; placement has no `space on / off`);
     `HelpKeys` (line 179) keeps returning the union with `tab switch list`, so
     `?` documents both panes;
   - `Update` (line 222): clamp `placeCur` with `candClamp` on `candDocMsg`;
   - `updateKey` (line 249): the in-flight guard stays exactly as it is and
     covers both panes; then `tab` toggles `focus` and calls `follow`; then a
     `focus == placement` dispatch returns `v.updatePlacementKey(k, env)`.
     Candidate keys are untouched.
5. **`internal/ui/view_actors_test.go`** — the placement tests, plus (via the
   shared fixture in `view_candidates_test.go`) the servers the picker needs.
6. **`internal/ui/view_candidates_test.go`** — `candFixtureDoc` (line 36) gains
   `Servers` (`backup`, `zen`) and builder `Placement: []string{"zen"}`. This is
   the fixture the actor goldens already build from, so the pane is pinned in a
   golden and not only in unit tests; no other view renders servers or
   placement, so no other golden moves.
7. **`internal/ui/golden_test.go`** — one new case `actor-placement-132`
   (`goldenActorViewModel` then `tab`), in the table beside `actor-pick-132`
   (line 1328).
8. **Goldens** — regenerate `actor-builder-132`, `actor-edit-132`,
   `actor-pick-132`; add `actor-placement-132`.
9. **`docs/plans/2026-09-30-placement-s5-actor.md`** — this plan, saved and
   committed with the round.

## 2. Contracts

**`SetActorPlacement`.** Refusals are whole-form `FieldError`s, in this order:
unknown actor; then per entry empty, duplicate, unknown-server. `["",""]`
answers the empty entry, `["zen","zen"]` the duplicate. `"local"` skips only the
server lookup. A success writes the `actors` section alone, `Name` is the actor
and `Message` is `edit actor <actor> placement`. Clearing decodes to a nil
`Placement` and the encoded body carries no `placement` key.

**Body layout** (0-based, candidates `n`): `0` blank; `1` candidate header;
`2..2+n-1` candidate rows; `2+n` blank; `2+n+1` `PLACEMENT` header;
`2+n+2..` placement rows; then two blanks and the check line. Candidate rows
keep their existing index, so `follow`'s candidate arm does not move.

**Placement rows.** One row per entry: a faint number column and the name, `local`
carrying the muted note `this machine`. The focused pane's cursor row is bold;
the unfocused pane keeps its cursor, unbolded. An empty list is **not** an error
and not a zero-row table: one muted line reading `local` with a `default` marker,
the cursor staying 0, and every key but `a` a no-op there.

**Keys.** `tab` toggles `focus` and re-follows. Placement focused: `↑↓` (and
home/end/pgup/pgdn) move `placeCur`; `shift+up`/`shift+down` swap with the
neighbour (no-op at the ends) and follow the cursor; `d` removes (on the last
entry it clears the list, which is valid, unlike candidates); `a` opens the
picker; `e` opens the actor form; `space` does nothing. Every placement change
writes once through the same path as a candidate change: validate with
`relevo.SetActorPlacement`, update the local doc so the next key builds on it,
then `runAction(ctx, "edit actor", "actor:"+name, ApplyConfig(edit))` — same verb
and key, so the in-flight guard and the `:log` row are the same for both panes.

**The guard.** While `env.Running["actor:"+name]` is set, `shift+up`,
`shift+down`, `d` and `a` are ignored in *both* panes, exactly as the candidate
keys are today; `tab` still toggles focus (it changes nothing).

**Picker.** `kind` names the placement, `submit` is `add`; items are
`local` (note `this machine`) first, then the configured server names in name
order (reuse `serverNames`) minus the entries already listed, each server's note
its URL. Picking appends. With nothing left to add:
`notice("every server is already in " + name + "'s placement")`.

## 3. Ordered steps and done-whens

1. **Preconditions** (§0): S4's API present, S4's branch green.
   Done when `git grep` finds `Servers map[string]remote.ServerEntry` in
   `internal/relevo/configedit.go`, `func serverNames` in
   `internal/ui/view_servers.go`, and `sh scripts/check-filesize.sh` prints ok
   on the starting tree. Anything else: halt (§7).
2. **API** (§1.1) and **its test** (§1.2).
   Done when `go test ./internal/relevo -run
   'TestConfigEditSetActorPlacement|TestConfigEditPreservesPlacement|TestConfigEditServersRoundTrip' -count=1`
   is green.
3. **Pane file** (§1.3) and the **integration** in `view_actor.go` (§1.4).
   Done when `go build ./...` and `go vet ./internal/ui` are clean, and
   `go test ./internal/ui -run 'TestActor|TestActors' -count=1` still passes.
4. **Fixture and tests** (§1.5, §1.6).
   Done when `go test ./internal/ui -run 'TestActor|TestActors' -count=1`
   passes with the new tests, and `go test ./internal/ui -count=1` passes.
5. **Goldens** (§1.7, §1.8): `go test ./internal/ui -run TestGoldenViews
   -update`, then read `git status`: only the four actor goldens may move.
   Done when the same run without `-update` passes and `command-real-132.golden`
   is untouched; any other golden changing means revert it and report.
6. **Mutation pins** (§5): each mutation fails its named test, then is reverted.
7. **Plan doc and gate** (§1.9): save this plan, `git add -A` (the new files
   must be staged — `check-filesize.sh` reads `git ls-files` and cannot see an
   untracked file), then `make check` once at the end; commit.

Focused command: `go test ./internal/relevo -run Placement -count=1` and
`go test ./internal/ui -run 'Actor|GoldenViews' -count=1`.
Full gate, once: `make check`.

## 4. Tests the round must add

`internal/relevo/configedit_test.go`, `TestConfigEditSetActorPlacement`:
unknown actor; empty entry; duplicate; unknown server; `local` accepted with no
servers section; a replace that decodes to the new order with candidates, agent,
tier and check untouched; a clear that decodes to a nil `Placement` and encodes
no `placement` key; `Message`; the actors section is the only section changed.

`internal/ui/view_actors_test.go`, on `actorFixtureView` and the shared fixture:
`tab` toggles focus both ways; `shift+down` (placement focused) writes one
`ConfigEdit` whose decoded placement is swapped and whose candidates are
untouched, cursor following; `d` on the last entry writes an empty placement
(activated default), on a middle entry removes it; `a` opens the picker with
`local` first then the unlisted servers, a pick appends and writes, and a second
`a` with everything listed is the notice; `space` in the placement pane returns
no command and writes nothing; `e` still opens the actor form; an empty
placement renders one muted `local`/`default` line with no error; with
`env.Running["actor:builder"]` set, the placement change keys write nothing
while `tab` still toggles; a `candDocMsg` that shortens the list clamps
`placeCur`; `Keys()` names `tab` and drops `space on / off` when placement is
focused; `HelpKeys()` carries both panes.

## 5. Mutation pins (each must fail a named check, then be reverted)

1. Drop the `local` exemption in `SetActorPlacement` → the `local` case of
   `TestConfigEditSetActorPlacement` fails.
2. Drop the unknown-server refusal → the unknown-server case fails.
3. Let empty `entries` write `["local"]` instead of clearing → the clear case
   fails (decoded `Placement` is not nil).
4. Delete the `tab` arm in `updateKey` → the focus-toggle test fails.
5. Route a placement `shift+down` through `SetActorEntries` → the placement
   reorder test fails (the edit's candidates changed, placement did not).
6. Make `d` on the last placement entry notice instead of clear → the clear
   test fails.
7. Render zero rows for an empty placement → the default-line render test fails.
8. Remove `d`/`a` from the in-flight guard switch → the placement guard test
   fails.

## 6. Files this round touches (closed)

1. `internal/relevo/configedit.go`
2. `internal/relevo/configedit_test.go`
3. `internal/ui/view_actor.go`
4. `internal/ui/view_actor_placement.go` (new)
5. `internal/ui/view_actors_test.go`
6. `internal/ui/view_candidates_test.go` (fixture only)
7. `internal/ui/golden_test.go`
8. `internal/ui/testdata/actor-builder-132.golden` (regenerated)
9. `internal/ui/testdata/actor-edit-132.golden` (regenerated)
10. `internal/ui/testdata/actor-pick-132.golden` (regenerated)
11. `internal/ui/testdata/actor-placement-132.golden` (new)
12. `docs/plans/2026-09-30-placement-s5-actor.md` (new)

Explicitly not touched, and why: `internal/ui/actions.go` (600 lines, no new
seam), `internal/ui/cmdline.go` / `view_rounds.go` / `overlays_test.go` /
`testdata/command-real-132.golden` (no new command or view),
`internal/relevo/configaudit.go` + test (the `actors` arm already covers the
path), `internal/ui/actor_form.go`, `internal/ui/view_servers.go` (S4's file),
`internal/{serve,remote,roles,config}` (no behaviour change),
`scripts/check-filesize.allow` and `scripts/check-comments.allow` (no new
exclusion), `README.md`, `testdata/coverage-baseline.txt`. Halt on anything
outside this list.

## 7. Rules and stop conditions

- `make check` is the gate and runs once, at the end; the host is remote and
  slower. Coverage must not drop (`internal/relevo` 86.1, `internal/ui` 81.8);
  never lower `testdata/coverage-baseline.txt`.
- Comments say why, never what; no issue numbers, round numbers or spec
  citations in code. Functions ≤ 70 lines, non-test files ≤ 600.
- Halts: S4's `ConfigDoc.Servers` API or `serverNames` absent, or S4's file over
  the ceiling and unlisted (§0.2); a spec/decision conflict; a golden outside
  the four actor ones moving. If a name this plan uses (fixture helper, S4
  helper, wording) differs after S4's merge, use the real name and report the
  deviation rather than silently adapting.

## 8. What the report must include

- `git diff --stat` for the commit, its full hash and subject, and `git status`
  clean afterwards.
- The exact golden diffs accepted (three regenerated, one new), and
  confirmation that `command-real-132.golden` and every non-actor golden are
  byte-identical.
- For each mutation in §5: the mutation and the test that failed.
- `make check` result with the coverage numbers for `internal/relevo` and
  `internal/ui`, and whether any baseline moved (it must not).
- `sh scripts/check-filesize.sh` output, stating that the new file was staged
  before the check; the line counts of `view_actor.go`,
  `view_actor_placement.go` and `actions.go`.
- The precondition result of §0.1–§0.2, spoken plainly (S4's merged state and
  its `view_servers.go` size).
