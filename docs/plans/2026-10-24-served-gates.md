# Plan: served picks and switches must honour gates (issue #1351 comment "4.")

## Behaviour and cases

A SERVED binding or chain member pick, and every SERVED mid-round switch, skips gated providers exactly as a local `bind`/`send` does.

Cases the round must cover:

1. Served create pick (`bind --server`, chain member create) with all candidates gated refuses with the `ErrAllGated` refusal instead of creating a member with an empty candidate.
2. Served create pick with some candidates gated picks the first ungated one in role order, same ranking rule as the local pick.
3. Served mid-round switch (`switchBuilder` on a served runtime) resolves over the same gate set as the pick, so a 429 on the current provider never switches onto another gated candidate and an all-gated switch halts with the refusal rather than burning a switch.
4. A gate recorded by `relevo gate <token>` on the server host is visible to served picks and switches on that host.
5. A gate recorded by a served round's own 429 detection lands in a ledger served picks and switches read.
6. A gate forwarded from a client (`ForwardUnavailable`) or arrived via the synced gates document is visible to served picks and switches when it was not before; if it already is, the plan's ledger map says so and no duplicate write is added.
7. Explicit `--candidate` on a served create keeps today's rule (served as named, refusal only for shape/role errors), mirroring the local explicit-pick rule.

Out of scope for this round: item 3 of the seed (`config set --local` not bumping `config_meta.version`, stale served actor list) is a follow-up, listed at the end.

## Seams (verified by the planner against the worktree)

- `internal/serve/serve.go:216-226` (`New` builds `gates: db.PrefixKV{KV: local, Prefix: "serve."}`) and `internal/serve/serve.go:372-424` (`runtimeAt` sets `Gates: s.gates` with the comment "server-wide, not the owner's own" and sets no `GatesDB`): the served pick/switch gate source.
- `cmd/relevo/wire.go:334-349` (`buildRuntime` binds local `gates = local`, `gatesDB = local`, unprefixed machine-local file): the ledger `relevo gate <token>` writes on the server host (`cmd/relevo/gate.go:128-194` `gateUnavailable` writes through `availability.Unavailable` on that runtime).
- `cmd/relevo/serve_admin.go:261-296` (`serveGateUnavailable`) / `serve.AdminUnavailable`: the gate path that already writes the `serve.`-prefixed ledger.
- `internal/relevo/served.go:326-338` (`PickServedCandidateFor`: with `token == ""` a `resolveRole` error such as `ErrAllGated` is swallowed into `"", "", nil`): the all-gated refusal swallow.
- `internal/relevo/chain_served.go:326-341` (`servedActorPick` calls the above with `""` and returns the empty token with a nil error): the chain-member empty-candidate create.
- `internal/serve/bindings.go:180-206` (`pickServedTier`) and `internal/serve/actors.go:40` (actor view): served-create call sites of `PickServedCandidateFor`.
- `internal/relevo/switch.go:222-261` (`switchBuilder` resolves via `switchGates(rt, b)` at line 234) and `internal/relevo/inflight.go:59-66` (`switchGates` reads `availability.Gates(AvailabilityDeps(rt))`): the served-switch gate source, same `rt.Gates` as the pick.
- `internal/relevo/runtime.go:416-434` (`AvailabilityDeps`: `Gates: rt.Gates`, `Local: rt.GatesDB`) and `internal/availability/gates_projection.go:54-69` (`Gates` reads only `d.Gates`): why a served runtime sees exactly one ledger.
- `internal/relevo/remote_gates.go:341-~470` (`ForwardUnavailable`, client-to-server gate forwarding) and `internal/availability/gatedoc.go:54-141` (`GatesDoc`, rate-limit entries shared per subject, spawn failures kept local): candidate mechanisms by which a client gate could already reach the server; the builder establishes which ledger each lands in.
- `internal/serve/daemon.go:43` (served daemon ticks run on `s.runtimeAt` runtimes): confirmation that the served 429-detection write path and the switch path share the served `rt.Gates`.

## Steps

1. Deliverable: ledger map written as a short comment-adjacent doc or plan note naming, for each writer (`relevo gate` on server host, `gate --serve`, served-round 429 detection, client `ForwardUnavailable`, gates-doc sync apply) the exact KV/prefix it writes, and for each reader (served create pick, chain member pick, `switchBuilder`, local bind pick) the exact KV/prefix it reads; done when every seed question "which ledger(s)" has a file-and-line answer.
2. Deliverable: failing pure-function test in `internal/relevo` (existing fakes, no harness) showing a served runtime whose machine-local ledger holds a live rate-limit gate still picks the gated candidate; done when the test fails on the untouched code.
3. Deliverable: gate-source fix so served `AvailabilityDeps` sees the union of the `serve.`-prefixed ledger and the host's machine-local ledger with no double-counting of a subject present in both; done when the step-2 test passes and `relevo gate` on the host changes a served pick.
4. Deliverable: `PickServedCandidateFor` propagates the `resolveRole` refusal (including `ErrAllGated`) when `token == ""` instead of returning `"", "", nil`, with `servedActorPick` and `pickServedTier` surfacing it as the create refusal; done when an all-gated served create is refused and a partially-gated one picks the first ungated candidate.
5. Deliverable: decision record refuse-vs-queue for the all-gated chain-member create (refuse the whole preflight like a local bind does, or create queued); done when the chosen behaviour is named, justified against the local-bind parity rule, and pinned by a test.
6. Deliverable: switch-parity check proving `switchBuilder` on a served runtime skips the newly gated provider and halts with the refusal when all are gated without consuming a switch; done when a named test fails with the fix reverted (mutation check) and passes with it.
7. Deliverable: `make check-affected` green on the touched packages with no new lint exclusions and no coverage-baseline lowering; done when the command passes and `git diff --stat` touches only `internal/relevo`, `internal/serve`, and their tests.

## Deleted behaviour

None. No behaviour is removed by this plan:

1. No gate kind, ledger, prefix, or CLI flag is deleted.
2. No served endpoint, refusal code, or wire shape is removed.
3. The `token == ""` → `"", "", nil` path in `PickServedCandidateFor` is replaced by a refusal, which is a behaviour change, not a deletion; it is covered by step 5's test.

## Report must include

- The ledger map from step 1 (writer → KV/prefix → reader for all five writers and four readers), noting any seed fact the code contradicted.
- The refuse-vs-queue decision from step 5 and its justification.
- The mutation check from step 6 (which condition was broken, which named test failed).
- `git diff --stat` compared against the declared scope (`internal/relevo`, `internal/serve` plus tests) and the `make check-affected` result.
- Whether the synced-gates/client-forwarded path needed any change or already landed in a read ledger.

## Follow-up (not this round): stale served actor list after `relevo config set --local`

Confirmed in code: `internal/db/config_local.go:76-85` (`ConfigLocalPut` explicitly does not bump `config_meta.version`), `internal/serve/configreload.go:62-78` (`Refresh` reloads only on version change), source is the machine DB's config store (`cmd/relevo/serve_reload.go:26-40`, `Version()` at `internal/config/sectionread.go:45`). A `--local` actor edit therefore never triggers a reload and the server keeps its startup actor list until restart. Fix in a later round: make a local-layer write visible to the refresher (version bump or local-stamp check) without turning every tick into a full load.
