# Plan: placement preference S3 — resolution: `bind` obeys the actor's placement

Seed: `docs/specs/2026-09-30-placement-preference-design.md` §4, §4.1, §6
(in this worktree; S1 shipped it). Read it first. Where this plan and the spec
disagree, halt and say so; the spec is authoritative.

**Scope:** the client-side resolution. This round makes `bind` pick a
placement: the actor's ordered list, probed read-only, first viable wins,
skips recorded. It does not add the TUI (S4) and it changes no server code
(S2 shipped `GET /v1/actors/{actor}` and the `placement` feature token).

**Behavior recap (the contract):**

- Explicit `--server X` → placement `[X]`, one attempt, no fallback.
- New `--local` → placement `[local]`, regardless of the actor. `--server`
  and `--local` together are refused on one line, exit 2.
- Neither → the actor's `placement` list; empty/absent → today's local path
  byte for byte (no probe, no new messages).
- `--resume`/`--rebind` never re-place; a resumed binding keeps its placement.
- `send`, `switchBuilder`, `staleBuilder` never re-place.
- A placement list that names a remote is probed read-only before any create;
  the create then runs exactly once through today's paths. Failures after the
  create call is sent never fall through.

## 1. What changes

1. **`internal/roles`** — carry placement to the registry:
   - `file.go`: `Row` gains `Placement []string \`json:"placement,omitempty"\``
     (doc: set by the actors conversion; the resolution reads it).
   - `actors_convert.go` `rowFor`: copy `actor.Placement`.
   - `registry.go`: `Role` gains `Placement []string`; `buildFile` copies
     `row.Placement` next to the candidate mapping.
   - Legacy `roles.json` may carry the key after this; it flows the same way.
     Do not add a separate legacy check.
2. **`internal/config`** — the cross-check moves to one source:
   - `placement.go`: `checkActorPlacement` iterates the built registry's roles
     (`L.Registry`) instead of `L.Actors`, so the check covers the actors path
     and any placement that a legacy roles row carried. Same error wording
     (`actors: <name>.placement[<i>]: server %q is not in the servers section`).
     Move the call in `document.go` after `roles.Build`. The S1 tests must keep
     passing; adjust only if the wording or ordering genuinely differs, and say
     so in the report.
3. **`internal/remote/client/actors.go`** (new) and the interface:
   - `func (c *Client) Actor(ctx, server, actor, candidate string) (remote.ActorView, error)`
     using the existing `getJSON` helper and a 30 s timeout, exactly the
     `Candidates` pattern; `?candidate=` is URL-escaped and omitted when empty.
   - `internal/relevo/runtime.go` `RemoteClient` gains the method.
   - `internal/relevo/remote_test.go` `fakeRemote` gains it: per-server
     canned `remote.ActorView`/errors, defaulting to accepted for a configured
     server so existing tests do not change.
4. **`internal/relevo/placement.go`** (new) — the resolver:
   ```go
   type PlacementSkip struct{ Name, Reason string }
   type PlacementResolution struct {
       Name    string // "local" or a server name
       How     string // "explicit" | "actor" | "" (no preference)
       Skipped []PlacementSkip
   }
   ```
   - `choosePlacement(ctx, rt, role, pinned string) (PlacementResolution, error)`:
     reads the role's `Placement` from the registry. Empty list → the zero
     resolution (local default), no probe. Otherwise walk entries in order:
     - `local`: viable when `resolveRole(reg, set, gates, pinned, role)`
       succeeds; a refusal is a skip whose reason is the resolve error,
       one line.
     - a server: `rt.Remote == nil` → skip "no client key"; then `WhoAmI`:
       `client.ErrUnreachable` → skip, 401 → skip "not enrolled", a
       `client.ErrCertChanged` or any other error → return it (loud). With a
       reached server:
       - reader role needs `remote.FeatureReaders`; `--actor`/`--feature`/
         `--ticket` need their features as `addRemote` requires them today —
         each missing feature is a skip naming the upgrade;
       - a pinned tier above `WhoAmI.MaxTier` (only when
         `remote.FeatureTier`) is a skip; use `harness.Tier.Above`;
       - when the server advertises `remote.FeaturePlacement`, call
         `rt.Remote.Actor(ctx, server, roleName, pinned)`; `Accepted == false`
         is a skip with `Reason`; a transport error classifies as above;
       - a server without the feature is reachable-only and counts as viable
         (the create then answers; a refusal there is loud).
     - first viable wins; no viable → an error listing every skip, one line
       each.
   - The resolver is pure over `rt` and the registry; it creates nothing.
5. **`internal/relevo/add.go`** — wire `Add`:
   - `AddOptions` gains `Local bool` (the `--local` flag) and
     `Placement PlacementResolution` (an already-made choice, for callers that
     resolved first).
   - `Add`: explicit `Server` → `Placement{Name: Server, How: "explicit"}` and
     today's `addRemote`, no fallback. `Local` → `Placement{Name: "local",
     How: "explicit"}`, local path. Otherwise, when the actor's list is not
     empty, call `choosePlacement`; a server result sets `opts.Server` and runs
     `addRemote`; a local result proceeds to today's local path carrying the
     resolution for the pick note; no viable placement → return the resolver
     error.
   - `AddResult.Resolution` carries the placement (see 6).
6. **`internal/relevo/candidate.go`** — surface it:
   - `Resolution` gains `Placement PlacementResolution` (zero = no preference).
   - `ExplainResolution`/`PickText` append one clause only when
     `Placement.How != ""`: `; placement <name> (<how>)` plus
     `; skipped <name> (<reason>)` per skip. **Do not touch the notes of
     resolutions with a zero Placement** — `internal/ingest/outcome.go` parses
     KindPick/KindSwitch notes and existing tests pin them.
   - `remote_add.go`: `remotePickEntry` gains the same optional clause; the
     existing sentence stays byte-identical without a placement.
7. **`internal/relevo/bind.go`** — `BindResolved`/`create`:
   - Before `create`, engage the same resolver (unless `--server`/`--local`;
     `BindOptions` gains `Local bool`). A server result calls the `Add` remote
     path with `Server` set, `Repo: opts.CWD`, `CWD: ""`, and the mapped
     fields (`Role`, `Tier`, `Feature`, `Ticket`, `Candidate`); the returned
     resolution carries the placement. A local result is today's `create`.
   - `resume`/`rebind` untouched.
8. **`cmd/relevo/bind.go`** — the flag:
   - `--local` added to the flag set and to `bindFlags`; passed into both
     `BindOptions.Local` and `AddOptions.Local`.
   - `--server` and `--local` together refused next to the other route
     refusals, one line, exit 2. `bindRouteFor` must treat `--local` as not a
     route flag (a plain `bind --local` stays `routeBind`).
9. **README** — the `relevo bind` flag line gains `[--local]`; the actor
   `placement` bullet gains one sentence: `bind` probes the list in order and
   `--server`/`--local` override it. Do not restructure.
10. **Plan file** — save this plan as
    `docs/plans/2026-09-30-placement-s3-resolution.md` and commit it.

## 2. Ordered steps (done-when)

1. `roles` registry carries placement; `internal/roles` tests pass.
2. `config` cross-check via the registry; `internal/config` tests pass
   (S1's placement tests stay green).
3. Client `Actor` method + interface + fake; `internal/remote/client` and
   `internal/relevo` build.
4. Resolver + unit tests (fakeRemote) pass.
5. `Add` wiring + tests pass; then `BindResolved` wiring + tests pass.
6. CLI flag + refusal + flag-surface test; README.
7. `make check` once; mutation pins; commit on this binding's branch
   (message like `feat(bind): an actor's placement decides where it runs`).
   On this server host the gate may take longer than on the laptop; it is
   still one run at the end.

## 3. Tests

Follow the fakeRemote/fake runner patterns in `internal/relevo/remote_test.go`
and the existing bind tests; do not spawn a harness.

- **resolver table** (new `placement_test.go`): order honored; unreachable
  skipped; 401 skipped; cert-changed loud; `ActorView{Accepted:false}` skipped
  with its reason; old server (no `placement` feature) viable; reader on a
  server without `FeatureReaders` skipped; pinned tier above max skipped;
  local gated (all candidates gated) skipped; all skipped → error naming every
  placement and reason; empty list → zero resolution.
- **Add integration**: placement `["zen","local"]` with zen unreachable →
  binding is local, resolution carries the skip; zen accepted → remote
  binding on zen; local gated, zen accepted → remote binding; `--server X`
  with the actor placing elsewhere → one attempt on X, no fallback;
  `--local` with the actor placing on zen → local.
- **BindResolved integration**: plain bind, actor placement remote → remote
  binding (fakeRemote `CreateBinding` + fake git); local → today's headless
  path, byte-identical resolution when no placement is configured.
- **Resume**: a local binding resumed while the actor places remote stays
  local.
- **CLI**: `--server` + `--local` refused as a pure flag test; the flag
  surface test (`TestBindFlagsHaveActorNotRole` or its sibling) still passes
  with the new flag.
- **Note format**: a resolution with no placement produces the byte-identical
  note S1's code produced; one with a placement appends the clause and
  `ingest`'s parsers still read the candidate (`parsePickNote` is
  prefix-based, but pin it with a test).

## 4. Mutation pins

Each must fail its named test, then be reverted.

- Remove the fallback loop (always take the first entry) → the unreachable-skip
  test fails.
- Ignore `ActorView.Accepted` → the refused-actor test fails.
- Ignore `--local` → the `--local` test fails.
- Let explicit `--server` engage the resolver → the explicit-no-fallback test
  fails.

## 5. Files this round touches (closed)

1. `internal/roles/file.go`, `internal/roles/actors_convert.go`,
   `internal/roles/registry.go`, and their tests
2. `internal/config/placement.go`, `internal/config/document.go` and the
   placement tests
3. `internal/remote/client/actors.go` (new), a client test file,
   `internal/relevo/runtime.go`
4. `internal/relevo/placement.go` (new), `internal/relevo/placement_test.go`
5. `internal/relevo/add.go`, `internal/relevo/bind.go`,
   `internal/relevo/candidate.go`, `internal/relevo/remote_add.go`
6. `internal/relevo/remote_test.go` (fakeRemote + tests)
7. `cmd/relevo/bind.go` and its flag-surface test
8. `README.md`
9. `docs/plans/2026-09-30-placement-s3-resolution.md` (new, this plan)

If a file outside this list (or S1/S2's already-merged files) needs a change,
that is the pipeline pins again: halt and name the minimal addition rather
than improvising. Anything outside the list is otherwise a halt.

## 6. Rules

- `make check` is the gate: gofmt, vet, tests, comment and file-size checks.
  Run it once at the end.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code.
- Functions ≤ 70 lines, non-test files ≤ 600; no new exclusions.
- Coverage: report whether the baseline moved; never lower it. Regenerate
  with `sh scripts/check-coverage.sh --write` only if the round's moved code
  justifies it, and say so.
- The spec's §4.1 classification is the contract: unreachable and not-enrolled
  are skips; cert-changed and unexpected errors are loud.

## 7. What the report must include

- `git diff --stat` compared against §5; anything outside is a halt.
- The resolver's exact error line when every placement is skipped, and one
  full pick note with a skip and one without (proving the without case is
  unchanged).
- For each mutation in §4: the mutation and the named failing test.
- `make check` result and coverage numbers.
- Confirmation that the plan is committed and the tree is clean.
