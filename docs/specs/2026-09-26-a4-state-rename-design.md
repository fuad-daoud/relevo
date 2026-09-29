# A4: the state rename (builder/role → actor/candidate)

Date: 2026-09-26. It amends `docs/specs/2026-09-24-cockpit-design.md` §3.7-§3.8 and
§9's A4 row.

## 1. Decisions (the owner, 2026-09-26)

| # | decision |
|---|---|
| D1 | **Clean break.** No aliases for old flags, no dual JSON keys and no dual wire fields. The release that ships A4 goes to the laptop and the server together. |
| D2 | **Per-round artifact directories** (`NNN-<actor>/`, `summary.md`, §3.7's spool row) move to **A5**, their only consumer. A4 keeps the flat `NNN-*` round files. |
| D3 | **Internal identifiers stay** for now: the `internal/roles` package, `RoleRegistry`, `harness.Role*`, option fields named `Role`. The owner renames them much later. A4 renames what a user, a planner or another binary sees. |

## 2. Vocabulary (confirmed by the owner, 2026-09-26)

- **Actor**: config only. It links an agent to an ordered candidate list, with a tier
  and a check. An actor never runs by itself.
- **Runner**: the live thing. A binding has one runner. The runner **plays** one actor,
  fixed at bind (§3.4), and each of its rounds runs on one **candidate**. Today's
  `builder` endpoint is the runner.
- Words for something running say **runner**:
  - `runner_status`;
  - `to_runner`;
  - "sent to the builder runner" in `relevo wait`;
  - `runners 3/3` in serve status;
  - the statusline and the TUI.
- "Actor" appears only where the config is meant:
  - `bind --actor <name>`, which picks the actor the runner plays;
  - the `:actors` view;
  - the field naming it: `runner.actor` in the binding record, `actor` in
    `status --json`.

`builder` stays valid **only** as the name of the seeded writer actor.

## 3. Renames

### 3.1 CLI and MCP

| today | A4 |
|---|---|
| `bind --builder <c>`, `send --builder <c>` | `--candidate <c>` (`ask` already has it) |
| MCP `send` argument `builder` | `candidate` |
| `config agents --role <name>` | `config agents --agent <name>` |
| `config init --no-roles` | `--no-agents` |
| every help, error and hint text naming `--builder`, `--role`, "role", "roles.json" or "config roles" | the new words (the survey lists about 40 sites) |

### 3.2 Binding record (`store.Binding`, `BindingFormat` 6 → 7)

| JSON today | A4 |
|---|---|
| `builder` (the Endpoint) | `runner` |
| `builder_candidate` | `candidate` (the current round's) |
| `role` ("" = builder) | `actor`, always set; the empty value is stored as `builder` (amends §2: the record carries a top-level `actor`, like the status document) |
| `builder_missing_since` | `runner_missing_since` |
| `consults[].role` | `consults[].actor` |
| log entry `builder_session` | `runner_session` |
| log `direction` `to_builder` | `to_runner` |

The format bump makes an older binary refuse the new records, which it already does for
any newer format. That is D1's break, and it is intended.

### 3.3 `relevo status --json` (`BindingStatus`), and the MCP `status` tool

| today | A4 |
|---|---|
| `builder_candidate` | `candidate` |
| `builder_name` | `candidate_name` |
| `role` (omitted for builder) | `actor` (the actor the runner plays), always present |
| `builder_kind` | `harness` |
| `builder_definition`, `builder_definition_custom` | `agent_definition`, `agent_definition_custom` |
| `builder_status` | `runner_status` |
| serve status `builders` | `runners` |

The statusline JSON drops `role`; `actor` and `candidate` are already there. The
opencode plugin reads `actor` only.

### 3.4 Database (migration `007_round_actor.sql`)

- `round` gets `actor TEXT NOT NULL DEFAULT 'builder'`. Ingest writes it from the
  binding's actor, and stops inferring it from pick notes. `isRolePick`'s "builder"
  literal goes, and a custom writer actor's rounds are counted correctly.
- `round.builder_*` columns become `candidate`, `harness`, `provider`, `model` and
  `mode`. The indexes follow.
- The `history` JSON (`Builder*` keys) and the `histq` axis `by:builder` become
  `Candidate*` and `by:candidate`.
- Existing rows are rewritten in the same migration, one transaction. The schema golden
  moves.

### 3.5 Remote wire (`internal/remote/proto.go`), server and client in one release

| today | A4 |
|---|---|
| `CreateBindingRequest.role` | `actor` |
| `WhoAmI.builder_tier` | `default_tier` |
| `WhoAmI.builders` | `runners` |
| feature `builder` (per-round candidate) | `candidate` |
| feature `roles` | `actors` |

`BindingView.candidate` and the multipart `candidate` field are already new.

### 3.6 Text that planners and builders read

- The `relevo wait` line "never sent to %s's builder" becomes "never sent to %s's
  runner". The delivery origin headers "to builder" and "about builder" become
  "to runner" and "about runner".
- The stop and remote payload texts, the MCP instructions and tool descriptions, and
  `plugin.json`'s description all move to the new words.
- The architect "Handing off" section in all four copies, plus `internal/planner/handoff.md`
  (byte-identical, `handoff_test.go`): new words, and `--candidate`. Every shipped agent
  edit adds its hash to `shipped.sha256`.
- `README.md`, `docs/design.md` and `CLAUDE.md`: the new words and flags.

### 3.7 Actor `check`

`roles.Row.Gate` and `Role.Gate` become `Check`, and the roles.json key `gate` becomes
`check`. This is the one internal rename kept, because it is a stored key. `Binding.Gate`
(the check command) and `relevo gate` (providers) are different things and stay.

## 4. Migration (§3.8)

- **Config:** already done by A1 and A2.
- **State:** the DB migration 007 (§3.4) runs on open, as every schema migration does.
- **Binding records:** they are rewritten from format 6 to 7 on first load. `actor`
  comes from `role`, or `builder`, into `runner.actor`; the renamed keys are moved.
- **Open rounds:** none: a record is migrated on its next save because decoding
  accepts the old and the new keys and directions, so the "rounds open" gate the
  spool-path rename needed (now A5) is not used.
- **Server:** the same migration runs on the server's own DB and records.

## 5. Rounds

Build starts once the other planners' cleanup phase settles (P2/P3), because every round
touches widely shared files.

| round | delivers | size |
|---|---|---|
| A4-1 | §3.1, §3.6, §3.7: flags, text, MCP, architect, docs, `check` | M |
| A4-2 | §3.2, §3.3, §3.4 and §4: binding record, status JSON, DB 007, ingest, the pending gate | L |
| A4-3 | §3.5: the wire, server and client together | M |

The A4 PRs merge together and ship in one release. The deploy runs the laptop install
and the contabo deploy back to back, with no rounds open.

## 6. Tests

- **Contract goldens move deliberately:** each round lists every golden it rewrites
  and why:
  - CLI: `status*`, `serve-status`, `statusline*`, `history-*`, `show-log*`;
  - MCP: `tools-list`, `instructions`;
  - wire: `proto-*`;
  - serve: `status-document`;
  - store: `binding-shape`;
  - DB: `schema`.
- **Migration:** a format-6 record and a 006-schema DB with real-shaped rows migrate to
  the new shape. A second run is a no-op. An open round blocks the rewrite and shows
  the pending line.
- A test pins that `--builder` and `--role` are **unknown flags** (D1).
