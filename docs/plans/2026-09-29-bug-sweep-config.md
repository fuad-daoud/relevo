# Revision note (round 2)

Round 1 halted at step 3: the plan's test data named `security-reviewer` as a
custom, non-shipped agent, but commit `64d2715` (#677) ships it, so that agent
name can never produce the pinned refusal. Steps 1-2 (the shared decode and the
three prospective writers) are complete and green; the halt changed no code.

Decision: substitute the custom agent name `sec-consult` for
`security-reviewer` throughout this plan (steps 3, 4 and Behaviour E). The
actor name `security` and everything else stays as written; the pinned refusal
line becomes `actor security: agent "sec-consult" is not shipped and not in
agents: bad roles`. Resume from step 3; steps 5-7 then run unchanged. This file
is committed verbatim as `docs/plans/2026-09-29-bug-sweep-config.md`.

# Plan: the config store refuses a write that would leave the document unloadable (#678)

Fixes #678. `relevo config unset agents.<name>` accepted the removal while an actor still named that agent, so the next command's `newRuntime` (`cmd/relevo/wire.go:205`) failed at `Load`, and every verb that could repair the document runs behind that same `Load`. This round does option 1 only: the write that creates a dangling actor → agent reference is refused, with the load-time error naming the actor.

## Behaviour

**A. One decode for reading and for writing.** New `internal/config/document.go` gains `decodeDoc(doc Doc) (Loaded, error)` — everything `Load` derives from section bodies: parse candidates/policy/roles/agents/actors/prices/servers/hooks, actors win over a roles section, `roles.FromActors`, `roles.Build` (today's `internal/config/config.go:107–154`). It touches no database. `Store.Load` becomes `currentDoc()` (`internal/config/revision.go:276–288`) → `decodeDoc` → `loadSecrets` → `ConfigVersion`, so the read path and the write check cannot drift. The eight section loaders (`config.go:156–314`) stay in `config.go` and take the document as a parameter instead of reading the store.

**B. The prospective check.** `validateProspective(doc Doc) error` is `decodeDoc`'s error, unwrapped.

**C. The three writers.** `Put` (`config.go:370–395`), `PutDoc` (`:412–461`) and `Delete` (`:397–410`) build the document the write would produce, inside their existing transaction, from `before.doc` (`readSnapshot`, `revision.go:57–67`): `Put` copies and replaces its section, `PutDoc` copies and overlays every named section (unnamed sections stay as stored), `Delete` copies and drops its section. The copy is mandatory: `record` (`revision.go:71–135`) diffs `before.doc` against what the transaction stored, so replacing a body in `before.doc` itself would diff as a no-op and record no revision. A refusal returns before the first `ConfigPut`/`ConfigDelete`: the transaction rolls back, so no body, no version bump and no revision row are written.

**D. What is refused, and what is not.** Exactly what `Load` refuses, no more: an actor whose `agent` is neither shipped nor in the agents section (`internal/roles/actors_convert.go:39–77`, message at `:42`/`:97`), a builtin actor name that would change shape, a reader actor with `check`, a tier above `max_tier` (`internal/roles/registry.go:103–114`). A candidate reference that resolves to nothing stays tolerated, exactly as `Build` tolerates it (`registry.go:246–252`), so `config unset candidates` and `config unset policy.order.builder` — pinned as succeeding by `cmd/relevo/config_test.go:98–157` — still succeed.

**E. The message.** Returned unwrapped, so a refusal prints the line the next command would have printed: `actor security: agent "sec-consult" is not shipped and not in agents: bad roles`. It names the first referencing actor in sorted order; a second referencing actor is not listed, because the message is the load-time one (see Seed-vs-code note 1).

**F. Cases the tests pin.** The unset of a referenced agent is refused and the error names the actor; unsetting the actor first, then the agent, succeeds; unsetting an agent no actor names still succeeds; a refused write leaves the stored body, the version and the revision rows untouched; a write that repairs a stored dangling reference is accepted; deleting candidates a surviving actor names is still accepted.

## Seams

| Seam | Where |
| --- | --- |
| Shared decode (new) | `internal/config/document.go`: `decodeDoc`, `validateProspective`, `copyDoc`, `docWithout` |
| Load | `internal/config/config.go:107–154` rewritten; loaders at `:156–314` take `doc Doc` |
| Writers | `internal/config/config.go:370–395` (`Put`), `:397–410` (`Delete`), `:412–461` (`PutDoc`); `Validate` at `:334–366` stays the per-section gate before the transaction |
| What the transaction diffs | `internal/config/revision.go:57–67` (`readSnapshot`), `:71–135` (`record`), `:276–288` (`currentDoc`) |
| The rule | `internal/roles/actors_convert.go:16–36` (`FromActors`), `:39–77` (`rowFor`), `internal/roles/registry.go:103–114` (`Build`) |
| CLI that reaches it | `cmd/relevo/config_edit.go:109–154` (`configUnset`; `:135` section form, `:152` key form), `:61–105` (`configSet`), `cmd/relevo/config_io.go:80` and `cmd/relevo/init.go:86` (`PutDoc`) |
| Cockpit (unchanged) | `internal/relevo/configedit.go:422–455` already refuses a used agent at the form; `:459–462` (`WriteConfigEdit`) inherits the store check |
| Store test (new) | `internal/config/prospective_test.go`, via `openStore` (`helpers_test.go:17–25`) and `seedSections` (`:95–106`) |
| CLI test | `cmd/relevo/config_test.go`, next to `TestConfigSetGetUnsetRoundTrip` (`:98–157`), which must stay unedited |
| Budgets | `internal/config` is not in `.golangci.yml`'s exclusions: funlen 70 lines, gocognit 30; files ≤ 600 lines (`scripts/check-filesize.sh`); no `#NNN`/`§` in comments; coverage baseline `testdata/coverage-baseline.txt:9` = 81.1, not touched |

## Seed-vs-code notes

1. The seed says the document is validated "through the same parse-and-`roles.Build` path `Load` uses, and refuses with an error naming the actors that reference the removed agent". The error that names the actor is `roles.FromActors`'s, not `roles.Build`'s: `Load` reaches `FromActors` first (`config.go:236`) and `Build` only ever sees a `*roles.File` that is already actor-valid. The plan reuses `Load`'s whole sequence, so the message is the load-time one (Behaviour E), naming the first referencing actor.
2. The seed's "current stored sections with the replaced or removed section applied" is read inside the write transaction (`readSnapshot` already runs there), not before it: validating a pre-transaction snapshot would race another writer.
3. `Store.Delete` "validates nothing" is right (`config.go:397–410`); `Put`/`PutDoc` validate only the section they write (`Validate`, `config.go:334–366`).
4. The CLI is the gap; the cockpit is not affected — `DeleteAgent` (`configedit.go:422–434`) already refuses a used agent before it builds an edit, so no edit is proposed. Nothing in `internal/relevo` changes.

## Steps

1. `internal/config/document.go` (new): `decodeDoc`, `validateProspective`, `copyDoc`, `docWithout`; rewrite `Load` (`config.go:107–154`) to `currentDoc` → `decodeDoc` → `loadSecrets` → version; the loaders at `:156–314` take `doc Doc`. Deliverable: one decode, one read path. Done when `go test -count=1 ./internal/config/` passes with no test edited — this step changes no behaviour.
2. Writers (`config.go:370–395`, `:397–410`, `:412–461`): build the prospective document from `before.doc` inside the transaction and validate before the first write; one clause added to each doc comment saying a write the next `Load` would refuse is refused here. Deliverable: the three writers refuse. Done when `go test -count=1 ./internal/config/ ./cmd/relevo/ ./internal/relevo/` is green with no test edited.
3. `internal/config/prospective_test.go` (new): `TestStoreRefusesRemovingAnAgentAnActorNames` seeds candidates + a custom agent + an actor naming it, then asserts `Put(Agents, …)` (narrowed body), `PutDoc(Doc{Agents: …})` and `Delete(Agents)` each fail with an error containing `actor security` and `sec-consult`, and that the stored agents body, `Version()` and the revision-row count are unchanged after every refusal. `TestStoreRemovesAnAgentNoActorNames` asserts an agent no actor names can be Put away; after `Delete(Actors)` the referenced agent can be Put away; and deleting candidates a surviving actor names is still accepted. `TestStoreAcceptsAWriteThatRepairsTheDocument` seeds a dangling reference through `s.db.Tx`'s `ConfigPut` (the `helpers_test.go:110–117` pattern), asserts `Load` fails, then asserts a `Put` of the fixed actors section is accepted and `Load` succeeds — store-level only, because the CLI cannot reach the store while `Load` fails. Deliverable: the cases pinned. Done when `go test -count=1 ./internal/config/ -run 'TestStoreRefusesRemovingAnAgentAnActorNames|TestStoreRemovesAnAgentNoActorNames|TestStoreAcceptsAWriteThatRepairsTheDocument'` is green.
4. `cmd/relevo/config_test.go`: `TestConfigUnsetRefusesAgentInUse`, the issue's repro as a CLI test — `config set candidates '[{"harness":"claude","provider":"p","model":"m"}]'`, `config set agents.sec-consult '{"shape":"reader","native":{"claude":{"agent":"sec-consult"}}}'`, `config set actors.security '{"agent":"sec-consult","candidates":["m"]}'`, then `config unset agents.sec-consult` fails and the error names the actor; `config get agents.sec-consult` still returns the entry (the document still loads); then `config unset actors.security` and `config unset agents.sec-consult` both succeed and the last `config get` reports `not set`. The test runs only `config` verbs: it spawns no harness and touches no network, per CLAUDE.md's cmd/relevo rule. Deliverable: the CLI wiring pinned. Done when `go test -count=1 ./cmd/relevo/ -run TestConfigUnsetRefusesAgentInUse` is green.
5. Mutations, each reverted before the next, output kept: (M1) `validateProspective` returns nil → `TestStoreRefusesRemovingAnAgentAnActorNames` fails; (M2) drop the check from `Put` only → `TestConfigUnsetRefusesAgentInUse` fails; (M3) drop it from `Delete` only → the `Delete(Agents)` case fails; (M4) drop it from `PutDoc` only → the `PutDoc` case fails; (M5) validate `before.doc` instead of the prospective document → `TestStoreAcceptsAWriteThatRepairsTheDocument` fails. Deliverable: each pin proven load-bearing. Done when each named test fails under its mutation and the focused set is green again after the revert.
6. Focused sweep `go test -count=1 ./internal/config/ ./cmd/relevo/ ./internal/relevo/`, then `make check` once at the end (gofmt, vet, lint, comments, filesize, tidy, plugin-version, name, scripts, coverage). Done when both are green; note in the report whether golangci-lint was installed. No exclusion and no coverage baseline is edited; a moved internal/config coverage number is reported, not adjusted.
7. `docs/plans/2026-09-29-bug-sweep-config.md`: this plan, transcribed verbatim (without the runner's status block). `git add` the four code/test paths plus the plan; one commit `fix(config): refuse a write that leaves the config unloadable`, body `Fixes #678`; do not push. Done when `git status` is clean, `git show --stat HEAD` names exactly those five paths, and `git log -1 --format=%B` carries the trailer.

## Deleted behaviour

1. `Store.Delete`'s unconditional removal: a section removal that would leave an actor naming an absent agent is refused instead of written (whole-section `config unset agents`, and every other section `Delete`).
2. `Store.Put`/`PutDoc`'s section-only validation: a write whose prospective document does not resolve an actor's agent is refused instead of stored — this is what closes `config unset agents.<name>`, the whole-section form, `config set agents…`, `config import` and `config edit`.
3. Nothing else: no verb, flag, exit code, revision message, file, golden or test is removed, and `TestConfigSetGetUnsetRoundTrip` plus every existing test of `Load`, `Put`, `PutDoc`, `Delete` and rollback runs unmodified and stays green.

## Deliberately not done

- Option 2 (degrade `Load`, record a warning, let repair verbs run) and option 3 (`config import --force` / `config repair`): out of scope this round per the seed; the lockout they would repair is what option 1 prevents.
- `Store.Rollback`'s whole-document check: `applySnapshot` (`revision.go:246–272`) still validates its target per section only (`validateSnapshot`, `:233–244`). A snapshot carrying a dangling reference can only exist on a machine already locked out (where `config rollback` cannot run) or from a pre-fix write, and the cockpit already refuses such a rollback (`RollbackPreview` → `CheckDoc`, `internal/relevo/configaudit.go:342`/`:409`). The follow-up is one `validateProspective(snapshot)` call beside `revision.go:195`; left out to keep the round to the writers the seed names.
- `ImportFiles`' file-provisioning write path (`import.go:46–101` → `commitImport:126–164`) keeps its per-section validation: the files are removed only after a committed import, so a refused import would leave them on disk to fix, and the `config import` verb is already covered through `PutDoc`.
- No README, spec or cockpit edit: the cockpit's own refusal already gives the form its message.

## The report must include

- The refusal line verbatim, that it is returned unwrapped, and that it names the first referencing actor in sorted order.
- Which files changed against this seam list with `git diff --stat`, and that `Load`'s existing tests passed unmodified after the split (the refactor's evidence).
- The test names added, the focused commands and their output.
- For each mutation: the exact line broken, the named test that failed, that it was reverted — with M2/M3/M4 proving `Put`, `Delete` and `PutDoc` each carry the check.
- `make check`'s output, whether golangci-lint ran, the internal/config coverage number against the 81.1 baseline, and that no baseline or exclusion was touched.
- That no existing test was edited, that the CLI test runs no harness and no network, and that `config unset candidates` still succeeds.
- The commit hash, one commit, not pushed, and the plan doc committed verbatim.
