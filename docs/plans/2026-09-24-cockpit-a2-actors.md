# Cockpit A2: agents and actors replace roles

Spec: `docs/specs/2026-09-24-cockpit-design.md` §3.2 (Agent), §3.3 (Actor), §3.5
(Tier) and §3.8 (the A2 migration). This builds on `cockpit/wave-1`, which has A1
(names), A2a (`internal/agentsrc`) and A3a (revisions). Line numbers are as on that
branch.

**This plan runs in rounds. Each send states which round it is. Do only that round's
steps, then stop and report.**

| round | scope |
|---|---|
| 1 (this plan's §4–§9) | **Additive.** `agents` and `actors` sections, parsed by a new `internal/actors` package and converted into today's `roles.File`, so every consumer of `*roles.Registry` keeps working unchanged. Per-candidate `off`. Nothing is removed, and nothing is migrated. |
| 2 (later send) | The one-time migration from `roles` or legacy to `actors` + `agents`, recorded as a `migration` revision. The `roles` section, `policy.order`, `policy.tier` and `candidate.roles` stop being read. `config init` seeds actors. `roles-init` goes. `relevo config` prints an `actors` block. |
| 3 (later send) | `--actor` replaces `--role` on bind and ask. Help, README and docs. |
| 4 (later send) | Custom agents from the `agents` section are rendered by `agentsrc` and installed with the shipped-agent manifest rules. Doctor checks them. |

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise.** CI has no harness and no network. Everything in round 1 is pure, config
and registry code, tested without spawning anything.

## 1. System overview

Today a **role** is a `roles` config row, turned into `*roles.Registry` by
`roles.Build` (`internal/roles/registry.go:94`). A row holds:

- the shape;
- per-kind agent definitions;
- ordered candidates;
- the tier;
- the gate.

About 40 call sites read that registry: resolution, tier, bind, send, ask, doctor,
status and the config views.

The spec splits the row in two:

- An **agent** is what does the work. It is shipped (`plan-executor`, `reviewer`,
  `researcher`, `architect`), a custom single source (`agentsrc`), or a **native**
  entry that points at existing harness files.
- An **actor** is a named agent plus candidates in order, each of which may be `off`,
  plus a tier and a `check` (today's gate).

**Round 1 keeps the registry as the engine.** `internal/actors` parses the two new
sections and converts them into a `roles.File`, one row per actor. `config.Load`
prefers that conversion when an `actors` section exists. The only registry change is
`off`: a Ranked entry that is off is skipped by the pick and shown as off.

## 2. File structure (round 1)

```
internal/actors/actors.go        NEW  Agent, AgentEntry, Actor, Entry, Sections; ParseAgents, ParseActors, Encode*
internal/actors/shipped.go       NEW  the shipped agent table (name, shape, output, requires)
internal/actors/convert.go       NEW  ToRolesFile(agents, actors, set) (*roles.File, []string, error)
internal/actors/*_test.go        NEW
internal/roles/file.go           Row gains Off []string
internal/roles/registry.go       Ranked gains Off bool; buildFile marks it; Role gains OffCount (for views)
internal/relevo/candidate.go     rankedRole / resolveRole skip Off entries unless named explicitly
internal/relevo/policy_view.go   formatPolicyRole prints "off" in the status column for an Off entry
internal/config/config.go        Sections gains Agents, Actors (after Candidates); Validate cases; Load precedence
internal/config/*_test.go        + cases
```

## 3. Data structures

### 3.1 The `agents` section (JSON object keyed by agent name)

```
{
  "ui-designer": { "source": "---\nname: ui-designer\n...---\n<body>" },
  "my-exec":     { "native": { "claude": { "agent": "my-exec", "requires": ["my-scout"] } } }
}
```

```
// internal/actors/actors.go
type AgentEntry struct {
    Source string                     `json:"source,omitempty"` // agentsrc text; Parse must give Name == key
    Native map[string]roles.DefRow    `json:"native,omitempty"` // kind -> existing harness agent
}
```

- Exactly one of `source` and `native` is set.
- A key must match `^[a-z0-9][a-z0-9._-]{0,63}$` and must not be a shipped agent name.
- `native` kinds must be known (`harness.Lookup`), and their `agent` and `requires`
  names follow the same pattern.
- A `native` entry's shape is not in its data, so it carries `"shape": "writer" |
  "reader"` beside `native`. Add `Shape string json:"shape,omitempty"` to
  `AgentEntry`. It is required with `native` and forbidden with `source`, which has
  its own.

### 3.2 The `actors` section (JSON object keyed by actor name)

```
{
  "builder":  { "agent": "plan-executor",
                "candidates": ["deepseek-v4.1-flash", {"candidate": "gemini-3.8-flash-high", "off": true}, "sonnet"],
                "tier": "yolo", "check": true },
  "reviewer": { "agent": "reviewer", "candidates": ["sonnet"], "tier": "yolo" }
}
```

```
type Actor struct {
    Agent      string  `json:"agent"`
    Candidates []Entry `json:"candidates,omitempty"`
    Tier       string  `json:"tier,omitempty"`
    Check      *bool   `json:"check,omitempty"`   // writers only; nil = true
}
type Entry struct {        // JSON: a string (on) or {"candidate": s, "off": true}
    Candidate string
    Off       bool
}
func (e Entry) MarshalJSON() ([]byte, error)    // off=false -> "name"; off=true -> {"candidate":..,"off":true}
func (e *Entry) UnmarshalJSON([]byte) error
```

**Rules**
- An actor key matches `^[a-z][a-z0-9-]{0,31}$`, today's role-name pattern
  (`internal/roles/file.go:28`).
- `agent` is required.
- Each candidate is `candidate.IsName` or a valid token, with no duplicates by raw
  string.
- `tier` parses (`harness.ParseTier`).
- `check` on an actor whose agent is a reader is an error. The agent's shape is only
  known once agents are resolved, so this check lives in `ToRolesFile`.

### 3.3 The shipped agent table (`internal/actors/shipped.go`)

| name | shape | output | requires |
|---|---|---|---|
| `plan-executor` | writer | `report` | `researcher` |
| `reviewer` | reader | `findings` | — |
| `researcher` | reader | `notes` | — |
| `architect` | reader | `plan` | — |

```
type ShippedAgent struct{ Name string; Shape agentsrc.Shape; Output string; Requires []string }
func Shipped(name string) (ShippedAgent, bool)
```

The table must agree with `harness.RoleByName`'s Definitions for the three roles
(`internal/harness/harness.go:39`). Add a test that pins it.

### 3.4 Registry changes (`internal/roles`)

- `Row` gains `Off []string json:"off,omitempty"`: raw entries from `Candidates` that
  are off.
- `Ranked` gains `Off bool`. `buildFile` sets it when the entry's canonical token is
  among the resolved `Off` entries.
- **An off entry stays in `Ranked`**, at its position. `Resolved`, the list `Serves`
  uses, still includes it, so an explicit `--builder <off one>` is still served.
- `validate` (`file.go:131`) checks that every `Off` entry also appears in
  `Candidates`, as the same raw string.

## 4. Contracts (round 1)

### 4.1 `internal/actors`

```
func ParseAgents(body []byte) (map[string]AgentEntry, []string /*warnings*/, error)
func ParseActors(body []byte) (map[string]Actor, []string, error)
func EncodeAgents(map[string]AgentEntry) ([]byte, error)   // keys sorted, two-space indent, trailing \n
func EncodeActors(map[string]Actor) ([]byte, error)        // same
func ToRolesFile(agents map[string]AgentEntry, actors map[string]Actor) (*roles.File, []string, error)
```

**`ToRolesFile`** gives one `roles.Row` per actor:

- **Shape:**
  - a shipped agent uses its table shape;
  - a `source` agent uses its `agentsrc` shape;
  - a `native` agent uses its `Shape`.

  `writer` becomes the row's `"writer"` and `reader` becomes `"reader"`. The row
  shape words are the ones `roles.validate` accepts; see `shapeWord`, `file.go:216`.
- **Definitions:**
  - A **shipped** agent gets a `DefRow{Agent: name, Requires: table requires}` for
    every kind in `harness.All()`. That is exactly what `roles.builtins` produces
    (`builtin.go:13`), so a `builder` actor on `plan-executor` is indistinguishable
    from today's builtin builder.
  - A **source** agent gets `DefRow{Agent: src.Name, Requires: src.Requires}` for
    each of its rendered kinds (`agentsrc` `renderedKinds`, `source.go:248`; export
    it if it is not already).
  - A **native** agent gets its map as given.
- **Candidates:** every entry's raw `Candidate`, in order. **Off:** the raw strings of
  the off entries.
- **Tier:** the actor's tier, or nil when empty.
- **Gate:** `Check` when non-nil, otherwise nil (the registry then defaults a writer
  to true).
- **Errors** (wrapping `roles.ErrBadRoles`, so `config.Load`'s handling stays
  uniform):
  - an unknown agent (`actor <a>: agent "<g>" is not shipped and not in agents`);
  - `check` on a reader;
  - an actor named `builder`, `reviewer` or `researcher` whose agent's shape differs
    from that builtin's. The builtin names keep their builtin shape. Message:
    `actor builder must run a writer agent`.
- **Warnings:** an `agents` entry no actor uses (`agent <g> is not used by any
  actor`).

### 4.2 `config` (`internal/config/config.go`)

- **Sections:** add `Agents Section = "agents"` and `Actors Section = "actors"`.
  `Sections` becomes `[Candidates, Agents, Actors, Policy, Roles, Prices, Servers,
  Hooks]`. `sectionFile` has no file for them: they are DB-only, like hooks, and
  `FileName` returns `""`. Check that `ImportFiles` and `Files()` handle a section
  with no file. They already do for Hooks.
- **`Validate`:** `Agents` runs `actors.ParseAgents`, and `Actors` runs
  `actors.ParseActors`. Both are single-section checks, like the others.
- **`Load`** (`:122`). After reading every section:
  1. If `actors` is present, run `rf, w, err := actors.ToRolesFile(agents, actors)`
     and use `rf` as `L.RolesFile`. If `roles` is also present, add the warning
     `config: actors is set, so the roles section is ignored`.
  2. Otherwise keep today's path (the `roles` section, or legacy).
  3. Then `roles.Build(L.RolesFile, …)` as today.
- **`Loaded`** gains `Agents map[string]actors.AgentEntry` and
  `Actors map[string]actors.Actor`, for round 2's views.
- **`config export`** now shows `agents` and `actors` when present, in `Sections`
  order. A3a's `EncodeDoc` walks `Sections`, so this needs no code; confirm it.

### 4.3 The pick honours `off` (`internal/relevo/candidate.go`)

- `rankedRole` (`:109`) carries `Off` into its `rankedEntry`.
- `resolveRole` (`:218`), with **no explicit token**, skips `Off` entries exactly as
  it skips gated ones. The skip reason is the text `off`, so `ExplainResolution`
  prints `skipped <name> (off)`. The stored note then contains `(off)`. That is a new
  skip-reason string, not a new note shape; confirm that `ingest`'s parser
  (`internal/ingest/outcome.go`) only reads the `picked <token>` head.
- With an **explicit token that is off**, resolve it and add a note, as for an
  explicitly named gated candidate: `note: <name> is off for <role>; running it
  because you named it`. Print it where the gated note is printed.
- When every remaining entry is off or gated, the error is today's `ErrAllGated`
  (candidate.go), with the off entries listed as `<name> (off)`.

### 4.4 `relevo config`'s pick block (`internal/relevo/policy_view.go` `formatPolicyRole`, from `:201`)

An off entry prints `off` in the status column, dim, and never `<- would pick`.
Nothing else in the block changes.

## 5. Pseudocode

```
Load():
  read all sections
  if actors present:
     agents = ParseAgents(body(agents)) (absent = {})
     actors = ParseActors(body(actors))
     rf, warn, err = actors.ToRolesFile(agents, actors); err -> Load error (as a bad roles file is today)
     if roles present: warn "actors is set, so the roles section is ignored"
  else: rf = today's roles path
  L.Registry = roles.Build(rf, candidates, policy)
```

## 6. Error handling

| case | result |
|---|---|
| A bad `agents` or `actors` body | Refused by `Validate` on `config set`, `import` and `edit`. The stored config is unchanged, as for every other section. |
| An unknown agent, a reader with `check`, or a builtin actor name with the wrong shape | A `Load` error wrapping `ErrBadRoles`. It surfaces exactly where a bad roles file does today. |
| An off entry not in candidates | A `roles.validate` error. Only reachable through a hand-built `roles.File`, because `ToRolesFile` builds `Off` from the candidates. |

## 7. Tests (round 1)

### `internal/actors`

- `TestParseActorsEntries`: both entry forms, unmarshalling and round-tripping through
  `EncodeActors`. An on entry encodes as a string, an off entry as an object.
- `TestParseActorsErrors`: a bad key, a missing agent, a bad candidate, a duplicate,
  a bad tier.
- `TestParseAgents`: a source whose name must equal its key; a native entry needs a
  shape; both set or neither set; a shipped name as key; an unknown kind.
- `TestToRolesFileShipped`: a `builder` actor on `plan-executor` builds a registry
  (`roles.Build`) whose `builder` role equals today's builtin builder with the same
  candidates and tier. Compare `Spec("builder", kind)` for every kind, and
  `Ranked`.
- `TestToRolesFileCustomAndNative`: the definitions come from the source's kinds, and
  the native map is copied.
- `TestToRolesFileErrors`: an unknown agent; a reader with `check`; a `reviewer`
  actor on `plan-executor`.
- `TestShippedTableMatchesHarness`.

### `internal/roles`

`TestOffEntriesRanked`: an off entry keeps its position, `Off` is true, and `Serves`
is still true.

### `internal/relevo`

- `TestResolveSkipsOff`.
- `TestResolveExplicitOffNotes`.
- `TestAllOffOrGated`.
- `TestFormatPolicyShowsOff`.

Use the existing `rolesFileRegistry` helper (`roles_runtime_test.go:40`) with rows
that carry `Off`.

### `internal/config`

- `TestLoadPrefersActors`: with actors, agents and roles all present, the registry
  comes from actors and the warning is emitted.
- `TestValidateActorsAgents`.
- `TestExportIncludesActors`: the `EncodeDoc` order.

### Existing tests

Every existing test passes unchanged. Round 1 is additive. If one needs an edit,
stop and report why.

### Mutation check

Make `resolveRole` ignore `Off`. `TestResolveSkipsOff` must fail. Report it, then
revert.

## 8. Working efficiently

A builder pays one round trip per step whatever the step does, so:

- Batch the reads:
  - `internal/roles/{file,registry,builtin}.go`
  - `internal/relevo/candidate.go` (`:100-300`)
  - `internal/relevo/policy_view.go` (`:160-250`)
  - `internal/config/config.go` (`:28-60`, `:122-230`, `:251-280`)
  - `internal/agentsrc/source.go`
  - `internal/harness/harness.go` (`:13-80`)
- Write each new file in one call.
- Iterate on
  `go test ./internal/actors/... ./internal/roles/... ./internal/config/... ./internal/relevo/ -run 'Off|Resolve|FormatPolicy'`.
- Run `make check` once at the end.

## 9. Ordered steps (round 1)

**1. `internal/actors`.**
- Deliverable: §3.1–§3.3, `ParseAgents`, `ParseActors`, the encoders, the shipped
  table, and their tests.
- Verify: `go test ./internal/actors/...`.

**2. The registry's `off`.**
- Deliverable: §3.4 and `TestOffEntriesRanked`.
- Verify: `go test ./internal/roles/...`.

**3. `ToRolesFile`.**
- Deliverable: §4.1 and its tests.
- Depends on 1 and 2.

**4. config.**
- Deliverable: §4.2 and its tests.
- Depends on 3.

**5. The pick and the pick block.**
- Deliverable: §4.3, §4.4 and their tests.
- Depends on 2.

**6. Check and report.**
- Run the mutation check, then `make check`.
- Report:
  - the new functions with their line ranges;
  - the `ExplainResolution` text for an off skip;
  - `git diff --stat`, which must touch only the §2 files.
