# Placement preference: an actor names where its rounds run, and `bind` obeys

**Status:** design agreed 2026-09-30 (fuad + mastermind session). No issue filed yet.
**Amends:**
- `roles.Actor` gains `placement` (the `actors` section);
- `local` becomes a reserved server name: `remote.ParseServers` refuses it, so
  a placement entry is never ambiguous;
- `config.decodeDoc` gains the actors × servers cross-check (after
  `loadServers`, which runs after `loadActors` today);
- `relevo bind` gains `--local`;
- protocol: one read endpoint `GET /v1/actors/{actor}`, feature token
  `placement`;
- `AddResult`/`BindResolved` carry the placement resolution, and the pick log
  entry names the placement;
- `relevo.EditActor`/`ConfigDoc` and the TUI gain the `servers` section and a
  `:servers` view.

## 1. Why

Today the MasterMind must pass `--server <name>` on every fresh bind that
should run remotely; forget it and the round runs locally. Placement is not a
per-invocation decision, it is a property of the work an actor does: "planning
runs locally, building prefers zen, then backup, then the laptop". That
property belongs in the actor config, not in any agent's memory.

## 2. Config shape

```json
"builder": {
  "agent": "plan-executor",
  "candidates": ["glm-5.3-flash", "opus"],
  "placement": ["zen", "local"]
}
```

- Ordered, most preferred first. First viable placement wins.
- `local` is the reserved sentinel for this machine. It is always valid.
- Missing or empty `placement` means `["local"]` exactly: no behavior change,
  no migration, no new default.
- Every other entry must name an entry of the `servers` section. The check
  runs in `config.decodeDoc` after `loadServers`, so it covers `config set`,
  `PutDoc`, TUI writes and `Delete` alike: removing a server an actor names is
  refused with the actor's name, and an unknown server in a placement list is
  refused when the actor is written.
- Duplicates are refused. Entry shape itself is only "non-empty string"; the
  cross-check gives it meaning.
- `remote.ParseServers` refuses a server named `local` (reserved word).
- Placement is per actor. There is no per-candidate-entry placement (see
  §9).

## 3. What a placement means

- `local`: exactly today's local creation path (worktree add, plain bind at
  cwd, headless). Nothing changes for it.
- `server X`: exactly today's remote add on server X: no local worktree, the
  branch is cut in the client repo, the server resolves the actor against its
  own actors section and runs the rounds. The client mirror records
  `Builder.Server = X`.
- Placement is chosen once, at fresh create. `send`, `switchBuilder`, `resume`
  and `rebind` never move it. A remote binding whose server dies keeps today's
  behavior (halt after the unreachable budget); re-homing a live binding is
  out of scope (§10).

## 4. Resolution

Flags first, exactly like candidates: explicit beats actor config beats
default.

- `bind --server X`: placement is `[X]`, one attempt, no fallback. Explicit
  means explicit, and today's `--server` output and errors stay.
- `bind --local`: placement is `[local]`, regardless of the actor. New flag.
- `--server` and `--local` together are refused on one line, exit 2, like the
  other route refusals.
- Neither flag: the binding's actor (`--actor`, default `builder`) supplies
  the list; no `placement` field means `[local]`.
- `--resume`/`--rebind` ignore placement entirely: a resumed binding keeps the
  placement it was created with (a remote binding keeps its server; a local
  rebind stays local).

Resolution is a read-only probe per placement, in order, before anything is
created:

- local placement is viable when `resolveRole` resolves the actor (with the
  pinned `--candidate`, gates included) as it does today;
- remote placement is viable when `WhoAmI` succeeds (reachable, enrolled, and
  the features the flags need: actors, readers, labels, tier) and the server's
  actor view accepts the actor and the pinned candidate. The actor view and
  the server's create share one helper server-side, so the probe can never
  disagree with the create about what is served.

First viable placement wins. Skips are recorded with a reason ("unreachable",
"not enrolled", "actor not served", "candidate x refused", "reader rounds
unsupported"), reported on the bind line and in the pick log entry. If no
placement is viable, the bind fails naming every placement and its reason.

After the pick, the create runs exactly once, through today's path. A failure
once the create call was sent -- a server refusal that raced the probe, or a
timeout -- never falls through to the next placement: the client cannot know
whether the server committed, and silently creating elsewhere is worse than a
loud failure. That is the whole reason the probe exists.

A server that does not advertise `placement` (an older server) cannot be
asked about its actors: such a placement is only checked for reachability,
and a create refusal there fails the bind loudly instead of falling through.
The degradation is documented, not silent.

### 4.1 Probe details

- Unreachable, a timeout, and `401 not enrolled` are skip reasons.
- `ErrCertChanged` and any unexpected 5xx fail the bind loudly; a changed
  certificate is a security event, not a placement choice.
- A pinned `--candidate` that the local client cannot resolve is refused
  before probing, as today.
- A pinned `--tier` above the server's max tier makes the placement
  non-viable (the server's create answers `tier_above_max` today; the probe
  reads `WhoAmI.MaxTier`).

## 5. Server side

- `GET /v1/actors/{actor}`, authenticated like every other route, owner-scoped
  like `/v1/candidates`, with the optional `?candidate=<token>` pin. Response:

  ```json
  { "actor": "builder", "shape": "writer",
    "accepted": true, "pick": "opencode/cline-pass/deepseek-v4.1-flash",
    "reason": "",
    "candidates": [ {"token": "...", "name": "...", "kind": "opencode", "gated": false} ] }
  ```

  `accepted` and `pick` are the exact answers `PickServedCandidateFor` gives a
  create; unknown actor answers 404. Candidate views reuse the `/v1/candidates`
  rendering.
- New feature token `remote.FeaturePlacement = "placement"`, advertised by a
  server that serves the endpoint.
- `buildServedBinding` (`internal/serve/bindings.go`) currently calls
  `Git.InitBare` before `pickServedTier`, so a refused actor/candidate/tier can
  leave a bare repo behind even though its comment promises otherwise. Reorder
  to resolve actor and tier first, so a refusal-before-create is truly
  side-effect free. This matters because the probe makes refusals rare, not
  impossible.
- No new refusal code, no create semantic change.

## 6. CLI and MasterMind surface

- New `--local` on `bind`, refused with `--server` on one line (exit 2).
- The bind line names the placement and every skip:
  `bound x: builder opus on zen (placement: actor order; skipped backup:
  unreachable)`.
- `relevo config` (human) shows each actor's placement; `--json`'s `ConfigView`
  gains the placement order per actor, so a MasterMind can read the preference
  without probing. The config view never probes (that is `doctor`'s and
  `config server list`'s job).
- The pick log entry (`KindPick`, `remotePickEntry`/`pickEntry`) carries the
  placement and skips, so `relevo show --log` explains a bind that landed
  locally.
- `relevo send` is untouched: placement is never re-resolved after create.

## 7. TUI

- New `:servers` view (`internal/ui/view_servers.go`, crumbs `servers`):
  one row per server from the `servers` section, state from `ProbeServers`
  (enrolled / not enrolled / unreachable / cert changed / no key), add and
  remove. The client key stays CLI-only: the view shows "key: set/none" at
  most, never edits key material.
- `configedit.ConfigDoc` learns the `servers` section, with edit APIs
  analogous to `EditActor`/`SetActorEntries`, so server changes go through the
  same validation, revision and audit path as every other config edit.
- Actor editing gains the placement list on `actorView`, next to the candidate
  list, with the same controls (reorder, add, remove); the add picker lists
  `local` first and the configured servers. `actor_form` keeps the scalar
  fields (name, agent, tier, check).
- Cross-validation means the picker can only offer real names, and a
  `config set` typo is refused with the actor's name.

## 8. Staging

1. **Config only (S1).** `roles.Actor.Placement`, parse/validate, cross-check
   in `decodeDoc`, `local` reserved, `relevo config` human/JSON. Behavior
   unchanged: nothing reads placement yet. Independent.
2. **Server (S2).** `/v1/actors/{actor}` + `FeaturePlacement` + the
   `InitBare`/`pickServedTier` reorder + tests. Independent of S1.
3. **Core resolution (S3).** `internal/relevo/placement.go` (probe, skips,
   pick entry), `--local`, wiring into `Add`, `create` (plain bind) and
   `addRemote`; placement reaches the cockpit and the opencode plugin for free
   because they call the same core. Depends on S1 + S2.
4. **TUI (S4).** `:servers` view, servers in `ConfigDoc`, placement list on
   `actorView`. Depends on S1 (and S2 for the health column).

Tests per stage in the existing style: table tests for parse/validate and
cross-check (`internal/roles`, `internal/config`), fake-remote table tests for
the probe order and skip reasons (`internal/relevo`, `fakeRemote`), handler
tests for the new endpoint (`internal/serve`), render tests for the views.
Docs (README command list, `relevo help`) ride each stage.

## 9. Rejected alternatives

- **Per-candidate-entry links** (`{"candidate": "opus", "server": "zen"}`):
  forces a cross product to express "these three candidates, remote then
  local", which is the repetition the feature exists to remove. The actor
  list is the link.
- **Fall through on a create refusal** (no probe): a refusal and a timeout are
  told apart cheaply, but a timeout after the create was sent leaves unknown
  server state; a second placement could then double-bind. Probing first keeps
  creation single-shot.
- **A global "prefer any remote" policy**: hides which machine runs the work;
  load-based selection is the burst feature's job, inside one server, not
  placement's. A policy default for actors that name none can be added later
  without changing this shape.
- **MasterMind memory/instructions**: invisible, per-session, and exactly what
  the user is tired of repeating.

## 10. Out of scope

- Mid-round re-home: a remote binding never falls back to local, and a dead
  server keeps today's halt + `RemoteUnreachableSince` behavior.
- Wildcards (`"remote"`, `"any"`) and load-aware choice between servers.
- Placement on `resume`/`rebind`, or changing an existing binding's placement
  (a future `relevo rehome`, if wanted).
- A global default placement for actors that name none.

## 11. Operational notes

Never amend or rebase a commit that is already on a remote binding's branch.
The client fetches each closed round as an incremental bundle based on the last
commit it absorbed, so a rewrite makes that base a non-ancestor of what the
server ships. The client survives that only by re-fetching the whole branch and
re-basing its own mirror onto it; when the binding adopted the branch rather
than mirroring `relevo/<name>`, the rewrite still ends in a `NEEDS YOU` halt.
A plan must not order an amend or rebase on such a branch.
