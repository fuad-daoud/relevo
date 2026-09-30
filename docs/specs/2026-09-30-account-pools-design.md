# Account pools: several logins per provider, one gate key per account

**Status:** design agreed 2026-09-30 (issue #485, plus the owner decisions
D1–D3 in §3). Supersedes nothing; adds behaviour.
**Issue:** #485. The incident this answers: 2026-09-29 20:24 +03, on the serve
hosts zen and contabo.
**Related:** #204 (served tenants are mutually untrusted — it gates the serve
half of this design), #488 (per-round file sandbox — it consumes the account
homes this design introduces), #312 (gated limits: blocked on #311).
**Plan:** the sliced first-cut plan is
`docs/plans/2026-09-30-account-pools-s1.md`.

## 1. Why

The active ClinePass account on both serve hosts hit its weekly limit mid-round.
Five remote bindings of one MasterMind (plus another MasterMind's) were
mid-round; the servers switched those builders to
`opencode/openrouter/z-ai/glm-5.3-flash`, which then failed on openrouter
credits, so every binding halted NEEDS YOU.

The account dimension was invisible throughout. The gate recorded the model
(`deepseek-v4.1-flash`), because a rate limit is recorded per provider, and no
status surface said which login a round drew from. Recovery was manual on each
host: `opencode auth switch --standalone cline-pass ClinePass`, then
`relevo gate --serve --clear cline-pass`, then resume the rounds.

Two things are missing, and this design adds both:

- **a login is a thing relevo knows about**, so a round records which one it
  used, and a limit on one login does not gate the whole provider;
- **rotation**, so the next round or the next switch lands on another login of
  the same provider instead of halting or moving to a different model.

No accounts configured means exactly today's behaviour. Every new code path is
skipped when the pool is empty, and existing tests pass unchanged.

## 2. What step 2 checked, and what it did not

Everything about harness mechanisms below was checked read-only on this laptop
on 2026-09-30: only `--help`, `--version` and list commands, no login, no
`auth switch`, no writes under `~/.claude`, `~/.codex` or opencode's data
directory.

| Harness | Version | Mechanism this design uses | Evidence line |
| :- | :- | :- | :- |
| claude | 2.1.285 (Claude Code) | a per-process config home: `CLAUDE_CONFIG_DIR` | `claude --help` does **not** name it; the docs do: "To keep the home-directory files somewhere else, set [`CLAUDE_CONFIG_DIR`](/docs/en/env-vars); Claude Code then stores your settings, session history, and plugins there instead." (docs.claude.com, settings page, fetched 2026-09-30) |
| codex | codex-cli 0.159.0 | a per-process home: `CODEX_HOME` | `codex --help`, under `-p, --profile <CONFIG_PROFILE_V2>`: "Layer `$CODEX_HOME/<name>.config.toml` on top of the base user config". Also `docs/specs/2026-09-19-codex-harness-design.md:142`. |
| opencode | v2.0.19 | the install-global credential row, flipped by `auth switch`; no per-process override | `opencode auth switch --help`: "switch the active account for an integration", usage `opencode auth switch [flags] [<target>] [<credential>]`, "credential string — Credential ID or label (opens an account picker when omitted) (optional)". Its only flags are `--standalone` and `--server`; `opencode auth list --help` adds only `--format`. No flag or environment variable selects a credential for one process. |
| agy | 1.2.13 | none — refused | `agy --help` offers no login, account, credential or home selector at all (its `install` subcommand only configures PATH, `--dir`/`--skip-aliases`/`--skip-path`). |

Two halt conditions from the seed's step 2 did not trigger:

- **No halt on opencode.** `auth switch`, `auth list`, `auth login`,
  `auth logout` and the top-level `opencode --help` expose no per-process
  credential override. The one global active row per integration is therefore
  the whole mechanism, and §10 is the design.
- **No halt on claude or codex.** `CODEX_HOME` is in codex's help. claude's
  help does not name `CLAUDE_CONFIG_DIR`, but Claude Code's own documentation
  does, so it is verified by docs rather than by help text. The claude row of
  the table is the one row whose evidence is a documentation line; nothing
  below claims it was seen in `--help`.

Not verified here, and stated as a hazard rather than as fact: whether a
**running** opencode process re-reads the credential on each request (§10a).

## 3. Decisions D1–D3

The seed (the writing-round plan) flagged three places where the owner's memory
notes and the code disagree. Issue #485 settles the second one in this design's
favour; it does not touch the first or the third, so the recommendation stands
as the owner decision.

### D1 — rotation does not count against `max_switches`

**Decision: rotation stays uncounted, like every other limit switch.**

The code deliberately does not count rate-limit switches: `switchBuilder`
(`internal/relevo/switch.go:98`) takes `counted`, and `gateOnLimit`
(`internal/relevo/switch.go:213`) passes `counted=false`. The reason is in the
code: "max_switches counts builders that fail, not providers that close". With
the default of 2, counting a rotation would halt a 4-account cline-pass pool
after the third account — the pool would be unable to use the logins it exists
for.

Rotation is bounded naturally instead: every account it leaves is gated, so the
number of rotations is at most the size of the pool, and the walk then falls
back to today's actor-order switch (which the limit still bounds).

**Rejected alternative** (the seed's reading): rotation counts against
`max_switches`. It was rejected because it makes pool size and `max_switches`
interact in a way nobody asked for, and because it would count a provider
closing — the exact thing the current code refuses to count.

### D2 — the opencode mechanism is the credential row, not a per-account data dir

**Decision: a per-account opencode data dir (`OPENCODE_DB` or a private
`XDG_DATA_HOME`) is rejected.** An opencode account is a row in the install's
`credential` table, and rotation flips the active row with
`opencode auth switch --standalone <integration> <label>`.

Three independent reasons:

- Issue #485 records, from the incident itself, that opencode v2.0.18 keeps
  several credentials per integration in the `credential` table
  (`id, integration_id, label, active`) and that "the next `opencode run` reads
  it. No XDG_DATA_HOME juggling is needed."
- The owner's memory note `opencode-abandoned-sessions-570` records that a
  private DB fails with "Model unavailable", because provider routing lives in
  the DB.
- relevo itself reads the one default `opencode.db`: delivery
  (`cmd/relevo/wire.go:87`, `opencodeDBPath`), `session delete` for abandoned
  sessions, doctor (`internal/doctor/opencode.go`), and transcripts.

The consequence is a **global** mechanism, and §10 is written around it: one
active account per (host, integration), shared by every round on that host.

### D3 — a new package `internal/account`

**Decision: a new pure package `internal/account`**, with no harness spawn and
no I/O, so it is CI-safe.

The seed listed `internal/candidate`, `internal/availability` and
`internal/relevo` as the possible homes. That would put account selection,
gate-key parsing and pool resolution in packages that own something else:
`internal/candidate`'s package comment says what it owns "and nothing else",
and CLAUDE.md says one package per concept. The seed's other intent — keep the
logic out of `cmd/relevo` — is met.

## 4. The account schema

Accounts live in a new config section `accounts`, stored as a JSON body with no
file, exactly like Agents and Actors
(`internal/config/config.go:20-37`, `Sections`). Each account has:

- `name`: `^[a-z0-9][a-z0-9.-]{0,23}$`, and no `@`.
- `harness`: the harness kind (`claude`, `codex`, `opencode`; `agy` is refused,
  §4.1).
- `groups`: the quota groups (providers) this account serves.
- **exactly one** kind-specific selector.

The selector is typed per harness kind, never a free `env` map:

| Kind | Field(s) | Passed to the harness as |
| :- | :- | :- |
| claude | `config_dir` | `CLAUDE_CONFIG_DIR` |
| codex | `home` | `CODEX_HOME` |
| opencode | `integration`, `label` | `opencode auth switch --standalone <integration> <label>` |

A typed selector can be validated, `doctor` can check it, and it cannot inject
arbitrary environment such as `LD_PRELOAD` or `PATH`.

```json
"accounts": [
  {"name": "clinepass-1", "harness": "opencode",
   "groups": ["cline-pass"], "integration": "cline-pass", "label": "ClinePass"},
  {"name": "clinepass-2", "harness": "opencode",
   "groups": ["cline-pass"], "integration": "cline-pass", "label": "ClinePass 2"},
  {"name": "work", "harness": "claude",
   "groups": ["anthropic"], "config_dir": "/home/fuad/.claude-work"}
]
```

### 4.1 agy is refused

`agy` gets no account kind. The harness offers only an implicit account, and the
one override the seed considered — a `HOME` override — is unverified and would
also move `.gemini/config/agents` (the role definitions relevo installs,
`README.md:1487`). An account entry for `agy` is a validation refusal, not a
warning.

## 5. Validation

Checkable rules, all in the pure core:

- **Names are unique.**
- **The harness is known** (`claude`, `codex`, `opencode`; `agy` is refused),
  and **the selector fields match the kind**: the required ones are present and
  the ones that belong to another kind are absent.
- **A group that no candidate of that harness uses produces a warning** (not an
  error): a typo in `groups` is usually a stale candidate, not a broken account.
- **Two opencode accounts with the same integration and label are refused** —
  two names for one row would make the active-row kv record ambiguous.
- **Providers may not contain `@`.** This is a new refusal in `checkRequired`
  (`internal/candidate/candidate.go:300`), because `@` is the account separator
  of the gate key (§7). It is a new refusal on existing data, so `doctor` and
  `relevo config` must check existing candidates before this ships.

## 6. Policy

A new `policy.accounts.rotation` (`internal/policy/policy.go:14`, validated in
`internal/policy/validate.go`) with two values:

- **`failover`** — the default. A round uses the first account of its group
  that is not gated; the pick only moves on when nothing is left.
- **`round-robin`** — rotate after each round.

`round-robin` is **refused by validation** for any group served by opencode
accounts, because the flip is global to the install (§10): a per-round rotation
would move every other round on that host too.

**Least-recently-limited selection, fed by the 30-day history**
(`internal/availability/history.go:21`, `HistoryRetainWindow`), is named as a
later slice, not part of the first cut.

## 7. The gate key is `group@account`

An account gate is encoded in `Entry.Subject` (`internal/availability/ledger.go:48`)
of a `RateLimited` entry as `group@account`. It is deliberately **not** a new
`Entry.Account` field.

**The rollback property.** An older binary decodes the new entry and then
re-marshals it. An unknown field would be dropped by that rewrite, turning an
account gate into a whole-group gate that over-gates every account of the
provider. A subject of `group@account` instead simply never matches in an older
binary, so the gate is invisible there: the older binary sees no gate at all.
That is the safe failure — an invisible gate can be re-recorded, an over-gate
silently burns every account.

The same encoding applies to the history mirror (`Event`, §12) and to the
`--clear` forms (§8).

## 8. Gating rules

- **`relevo gate <token>`** gates the account recorded on each open round whose
  builder is that token (`cmd/relevo/gate.go:63`, `cmdGate`). With no open
  round, it gates the account the pick would use now. It prints which accounts
  it gated.
- **`--clear <group>`** clears the bare group **and every `group@*`**;
  **`--clear <group>@<account>`** clears one.
- **A token counts as gated** — for the pick, for `Gated`
  (`internal/availability/ledger.go:238`), for `status` and for the "gated"
  wording — **only when a bare `group` entry is live, or every account in the
  group's pool is gated.** A partially gated pool is not a gated token: the
  pick has somewhere to go.

## 9. Rotation and the switch

- On a limit, `gateOnLimit` (`internal/relevo/switch.go:213`) records
  `group@<b.BuilderAccount>`.
- `switchBuilder` (`internal/relevo/switch.go:98`) **first retries the same
  candidate on the next ungated account in pool order.** Only when the pool is
  exhausted does it walk the actor's candidate order as today. The retry is
  decided by a pure `nextBuilder` function (same candidate, next account,
  before walking the order).
- The switch log entry (`switchEntry`, `internal/relevo/switch.go:58`) names the
  account.
- Counting follows D1: a rotation switch passes `counted=false`.

## 10. opencode: the install-global active row

The active credential row is global to the install, so parallel rounds on one
host can only share one active account per integration.

- relevo keeps the active account per (host, integration) in a kv row.
- relevo flips with `auth switch` **only under the state lock**, and **only
  when the active account is gated**. Every round on it is then limited anyway,
  so no healthy round is moved.
- relevo **never flips per round**. Round-robin is refused for these groups
  (§6).
- The flip goes behind an injectable Runtime seam, so tests use a fake and
  nothing is spawned in CI.

Hazards this design states rather than hides:

- **(a)** Whether a running opencode process re-reads the credential on each
  request is unverified. If it does, a flip moves every running round on that
  integration to the new account, and the account recorded on a round means "the
  account at start".
- **(b)** On the laptop, the owner's own interactive opencode sessions share the
  active row. A flip moves them too.
- **(c)** A human running `auth switch` concurrently desynchronises the kv row.
  relevo therefore re-reads the active row before flipping (the list command)
  and records drift.

## 11. claude and codex: a per-process home

A per-process env home, appended to the spawn environment.

- claude reads agents, settings and `projects/` under `$CLAUDE_CONFIG_DIR`.
  codex reads role profiles as `$CODEX_HOME/<name>.config.toml` (codex spec §5,
  `docs/specs/2026-09-19-codex-harness-design.md:140-142`). **So every account
  home must hold its own role definitions**, and `relevo config agents` installs
  them into each home (S6).
- The session locator (`internal/relevo/session.go:24`,
  `HomeSessionLocator`) must search the account's dir, not the single `home`.
- `resumeRound` (`internal/relevo/headless.go:346`) must resume on the round's
  **recorded account**, never on a rotated one — a session id only resolves
  under the home that wrote it.
- Env entries are appended **after** `proc.ChildEnv`'s deny filter, so a parent
  `CLAUDE_CONFIG_DIR` cannot shadow them, and the variable is **removed from the
  parent set** as well. This is the same rule `roundEnv`
  (`internal/relevo/headless.go:179`) already follows for the runner marker.

## 12. Record and surfaces

- **`Binding.BuilderAccount`** (new json field), with `BindingFormat` bumped
  10→11 (`internal/store/format.go:23`, `:27` `recordFormat`), so an older
  relevo refuses a record it would otherwise save back with the account erased.
- Pick and switch log entries carry the account. Ingest writes it into a new
  `round.account` column, migration `016_round_account.sql`, **add-only**
  (migrations live in `internal/db/migrations/`; 015 is the highest today).
- History `Event.Account` (`internal/availability/history.go:21`),
  `omitempty`, display-only.
- Surfaces:
  - `status`: a column for the account.
  - `history --by account` (the `--by` axis, `cmd/relevo/history.go:88`).
  - `policy`: the pool and its gates (`internal/relevo/policy_view.go:317`,
    `FormatPolicyFor`).
  - `doctor` (`cmd/relevo/doctor_checks.go`): the home exists and holds a login
    — a read-only file check for claude and codex; the opencode row exists.
  - cockpit stats by account.

## 13. serve: before and after #204

- `serve` runs the same `internal/relevo` reconcile, so rotation works on a
  server once its own `accounts` section is configured.
- The ledger is the server's own, so there is a **per-account server ledger for
  free**.
- **Additive wire:** `WhoAmI.Features` gains `accounts`
  (`internal/remote/proto.go`, the `Feature*` constants; advertised in
  `internal/serve/routes.go:120`). A client forwards `group@account` gates and
  clears **only to a server that advertises it**
  (`internal/relevo/remote_gates.go:145` `ForwardUnavailable`, `:223`
  `ForwardAvailable`; `internal/serve/unavailable.go` is the receiving side).
  Round and status views carry the account label, **to the owner only**.
- **Can land before #204:** owner-configured accounts on owner-run servers
  (zen, contabo), used for every round there. That is where the incident
  happened.
- **Cannot land before #204:** client-supplied accounts or credentials over the
  wire, per-tenant pools, and **any account home on a host with untrusted
  tenants** — tenant builders run as the same OS user and can read every home.
  #488 (the per-round file sandbox) is the layer that hides one account's home
  from another's builder.

## 14. Terms of service

One neutral note, and no encouragement: several logins per provider may breach
that provider's terms. Whether to configure them is the user's choice.

## 15. Out of scope

Pointing at a separate issue (filed with this design): a rate-limited, halted
remote round refuses `stop` (`internal/serve/bindings.go:445`,
`remote.CodeRoundHalted`), and `send --candidate` is refused while the prompt
has no report (`internal/relevo/send.go:223`, `candidateSendRefused`). The
recovery that worked on 2026-09-29 was a plain `send`, which restarts the round
so the automatic switch lands on the ungated account. It would be better if a
halted round accepted `stop` or `send --candidate`, so a human could point it
straight at another account or candidate. That is a recovery-path change, not an
account-pool change, and it is not part of this design.

## 16. Slices

The first cut is six independently mergeable slices, in
`docs/plans/2026-09-30-account-pools-s1.md`. **S3 delivers the 4-account
cline-pass rotation** on the laptop, and on serve once deployed and the
server's own `accounts` section is set. Least-recently-limited selection from
the 30-day history is named as later work.

## 17. Seams

Every seam this design names, as `file:function` (line numbers are the ones on
2026-09-30 and are not part of the contract):

- **Gate projection and write paths:** `internal/availability/ledger.go`
  `Entry`, `Gated`, `Clear`.
- **Gate operations:** `internal/availability/gates.go` `ProviderOf`,
  `Unavailable`, `Available`, `LedgerGates`, `Gates`, `BindingsOnProvider`,
  `rolesMissingGates`; `internal/availability/available.go`
  `ResolveClearSubject`.
- **History:** `internal/availability/history.go` `Event`, `FromEntry`.
- **Pick:** `internal/relevo/candidate.go` `resolveRole`, `skipsFor`,
  `Resolution`.
- **Switch:** `internal/relevo/switch.go` `gatedBuilder`, `switchBuilder`,
  `gateOnLimit`, `switchEntry`.
- **Spawn:** `internal/relevo/headless.go` `builderEnv`, `roundEnv`,
  `startRound`, `startProcess`, `resumeRound`; `internal/relevo/session.go`
  `HomeSessionLocator`.
- **Policy:** `internal/policy/policy.go` `Policy`;
  `internal/policy/validate.go`.
- **Config:** `internal/config/config.go` `Section`, `Sections`,
  `internal/config/document.go` `decodeDoc`.
- **Candidate validation:** `internal/candidate/candidate.go` `checkRequired`.
- **Round record:** `internal/store/binding.go` (`Binding`),
  `internal/store/format.go` (`BindingFormat`), `internal/db/types.go`
  (`Round`), `internal/ingest/ingest_tx.go` (`upsertRounds`, `upsertRound`).
- **CLI and wire:** `cmd/relevo/gate.go` (`gateFlagSet`, `cmdGate`),
  `internal/relevo/remote_gates.go` (`ForwardUnavailable`,
  `ForwardAvailable`), `internal/serve/unavailable.go`,
  `internal/serve/bindings.go` (the stop refusal).
- **Role definitions:** `internal/harness/definition.go` `DefinitionPath`.
- **Surfaces:** `internal/relevo/policy_view.go` `FormatPolicyFor`,
  `cmd/relevo/history.go` (`historyFlagSet`, `--by`),
  `cmd/relevo/doctor_checks.go`.

## 18. Rejected alternatives

- **A free `env` map per account** (the seed's first shape): unvalidatable,
  unchecked by `doctor`, and an injection surface (`LD_PRELOAD`, `PATH`).
  Replaced by one typed selector per kind (§4).
- **A per-account opencode data dir:** rejected, §3 D2.
- **A new `Entry.Account` field:** rejected, §7 — the rollback property is the
  whole reason.
- **An `agy` account via `HOME`:** rejected, §4.1.
- **Round-robin for opencode-served groups:** refused, §6 — the flip is
  install-global.
- **Counting rotation against `max_switches`:** rejected, §3 D1.
