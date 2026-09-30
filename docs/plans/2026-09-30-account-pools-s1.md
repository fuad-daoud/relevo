# Plan: account pools (#485) — the sliced first cut

Seed: the design at `docs/specs/2026-09-30-account-pools-design.md` (this round
writes it). Where this plan and the spec disagree, the spec is authoritative;
where either disagrees with issue #485, the issue wins.

Read #485 first (§1 of the spec records what it settles). Decisions D1–D3 are
stated there as owner decisions: rotation stays uncounted; the opencode
mechanism is the credential row, not a per-account data dir; the core is a new
pure package `internal/account`.

**Six slices, each independently mergeable.** Each slice below names its files,
its pure functions and its tests. Nothing in S1–S5 spawns a harness or reaches
the network, so every test is CI-safe; the one place that does (the opencode
flip, S3) sits behind an injectable Runtime seam with a fake.

**Which slice delivers the 4-account cline-pass rotation: S3**, on the laptop,
and on serve once deployed and the server's own `accounts` section is set. S3
alone gives rotation on serve but no client control over `group@account` gates
— S4 adds that, and lands right after S3, because the incident was on serve.

**No accounts configured means exactly today's behaviour.** Every slice must
keep that true; a slice that changes behaviour with an empty pool is a halt.

## S1 — config and pure core

Package `internal/account` (new; nothing reads it yet, so this slice changes no
behaviour):

- the account type;
- parse and validate;
- `Pool(harness, group)`;
- `GateKey` and `ParseGateKey`;
- `Select(pool, gates, mode)`.

Plus:

- the `accounts` config section, as a JSON body with no file
  (`internal/config/config.go` `Section`, `Sections`, `sectionFile`;
  `internal/config/document.go` `decodeDoc`);
- `policy.accounts.rotation` with `failover` (default) / `round-robin`, and its
  validation (`internal/policy/policy.go` `Policy`,
  `internal/policy/validate.go`);
- the `@` refusal in the provider name (`internal/candidate/candidate.go`
  `checkRequired`), since `@` is the gate-key separator.

Tests: table tests for validation (unique names, unknown kind, `agy` refused,
selector fields per kind, opencode duplicate integration+label, the group
warning); selection (failover order, round-robin refused for opencode groups);
key round-trip (`GateKey` → `ParseGateKey` → `GateKey`, including a name
without `@`).

## S2 — gate key

- `Gated`, `Unavailable`, `Available` and `ResolveClearSubject` become
  account-aware, following the "every account gated" rule
  (`internal/availability/ledger.go` `Gated`, `internal/availability/gates.go`
  `Unavailable`, `Available`, `internal/availability/available.go`
  `ResolveClearSubject`).
- Add `Event.Account` (`internal/availability/history.go` `Event`,
  `FromEntry`), `omitempty`, display-only.
- `gate <token>` resolves the open rounds' accounts: a pure function beside
  `BindingsOnProvider` (`internal/availability/gates.go`), called from
  `cmd/relevo/gate.go` `cmdGate`.
- Add the `--clear group|group@account` forms (`cmd/relevo/gate.go`
  `gateFlagSet`).

Tests: a group is gated only when the pool is fully gated; a bare group entry
still gates everything; clear by group removes the bare entry and every
`group@*`; an old-format ledger (bare `group` subjects, no `@`) decodes and
behaves unchanged.

## S3 — rotation and the opencode flip

**This slice delivers the cline-pass rotation.**

- Add `Binding.BuilderAccount` (json field; `BindingFormat` 10→11,
  `internal/store/format.go`) and select the account at pick
  (`internal/relevo/candidate.go` `resolveRole`, `Resolution`).
- `gateOnLimit` records `group@account` (`internal/relevo/switch.go`).
- A pure `nextBuilder` (same candidate, next account, before walking the actor
  order) is used by `switchBuilder` (`internal/relevo/switch.go`
  `switchBuilder`, `gatedBuilder`, `switchEntry`). Counting follows D1.
- `resumeRound` keeps the account (`internal/relevo/headless.go` `resumeRound`).
- The opencode flip goes behind an injectable Runtime seam (a fake in tests;
  nothing is spawned in CI), with the per-(host, integration) active-account kv
  row and the drift re-read before flipping.
- Pick and switch entries carry the account. Migration
  `internal/db/migrations/016_round_account.sql` (add-only); ingest writes it
  (`internal/ingest/ingest_tx.go` `upsertRounds`, `upsertRound`).

Tests: a limit on account 2 of 4 switches to account 3 on the same candidate;
all 4 gated walks to the next candidate; resume after a restart keeps the
account; no accounts configured gives byte-identical pick and switch entries.

## S4 — serve wire

- Add `FeatureAccounts` (`internal/remote/proto.go`, beside the other
  `Feature*` constants) and advertise it (`internal/serve/routes.go:120`).
- Forward `group@account` gate and clear only when the feature is advertised
  (`internal/relevo/remote_gates.go` `ForwardUnavailable`, `ForwardAvailable`;
  `internal/serve/unavailable.go`).
- Add an owner-only account label in the round and status views.

S4 lands right after S3.

Tests: a client against an old server refuses `group@account` with a clear
message; serve contract goldens are updated.

## S5 — surfaces

- The `status` column.
- `history --by account` (histq axis; `cmd/relevo/history.go`).
- The `policy` pool view (`internal/relevo/policy_view.go`
  `FormatPolicyFor`).
- `doctor` home and login checks (`cmd/relevo/doctor_checks.go`): pure over an
  injected FS.
- cockpit stats.

Tests: goldens. **Any CLI test must not spawn a harness** (CI has none); the
doctor logic is tested as a pure function.

## S6 — claude and codex homes

- Env entries in `roundEnv` (`internal/relevo/headless.go` `builderEnv`,
  `roundEnv`), appended after `proc.ChildEnv`'s deny filter, with the variable
  removed from the parent set.
- `relevo config agents` installs into each account home
  (`internal/harness/definition.go` `DefinitionPath`).
- `rolesMissingGates` runs per account home
  (`internal/availability/gates.go` `rolesMissingGates`).
- The session locator searches per account (`internal/relevo/session.go`
  `HomeSessionLocator`).

Tests: env composition (the account home wins over a parent variable);
definitions missing in one home gate only that account.

## Later

Least-recently-limited selection from the 30-day history
(`internal/availability/history.go` `HistoryRetainWindow`).

## Iterating and the gate

While iterating inside a slice, the focused command is:

```
go test ./internal/account/ ./internal/availability/ ./internal/relevo/ -run 'Account|Gate|Switch|Rotat'
```

Run the full `make check` once at the end of the round, not per step. Do not
add a `.golangci.yml`/file-size/coverage exclusion to get green: if a slice
seems to need one, halt and report.

## Files

Each slice is a closed list; anything outside a slice's own list is a halt.

1. **S1:** `internal/account/*.go` (new); `internal/config/config.go`,
   `internal/config/document.go`; `internal/policy/policy.go`,
   `internal/policy/validate.go`; `internal/candidate/candidate.go`.
2. **S2:** `internal/availability/ledger.go`, `gates.go`, `available.go`,
   `history.go`; `cmd/relevo/gate.go`.
3. **S3:** `internal/relevo/switch.go`, `candidate.go`, `headless.go`, plus the
   opencode flip seam; `internal/store/binding.go`, `internal/store/format.go`;
   `internal/db/migrations/016_round_account.sql`;
   `internal/ingest/ingest_tx.go`; `internal/db/types.go`.
4. **S4:** `internal/remote/proto.go`, `internal/serve/routes.go`,
   `internal/serve/unavailable.go`, `internal/relevo/remote_gates.go`, plus
   serve contract goldens.
5. **S5:** `cmd/relevo/history.go`, `cmd/relevo/doctor_checks.go`,
   `internal/relevo/policy_view.go`, the cockpit stats surface, plus goldens.
6. **S6:** `internal/relevo/headless.go`, `internal/relevo/session.go`,
   `internal/harness/definition.go`, `internal/availability/gates.go`, and the
   `relevo config agents` install path.

Docs this round adds: `docs/specs/2026-09-30-account-pools-design.md` and this
plan, both on the branch for S1 to build on.

## Rules

- **Specs and plans ride with the first implementation PR.** Do not open a
  docs-only PR; leave both files on this branch for S1.
- No accounts configured means exactly today's behaviour, in every slice.
- Comments say why, never what; no issue numbers, no spec citations in code.
- Functions ≤ 70 lines, non-test files ≤ 600; one package per concept.
- CI has no harness binary and no network: a `cmd/relevo` test never spawns a
  harness; anything that would is a pure function in `internal/account`,
  `internal/availability` or `internal/relevo`, tested there.
