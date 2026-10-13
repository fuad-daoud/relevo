# Sync step 3c: gates, consents, files — builder plan

## MasterMind corrections (binding; where these disagree with the plan below, these win)

C1. **Spawn failures stay on their machine.** A `spawn_failed` entry says relevo could not start a candidate *on this machine* (binary missing, local auth, local crash). It must not gate that candidate elsewhere. The `gates` doc carries `rate_limited` entries only (a provider's quota is shared by every machine), including manual `relevo gate` writes. `spawn_failed` and `Other` entries are local:
- `Items()` never exports them.
- Apply never touches them.
- A tombstone clears only the subject's `rate_limited` entries.

Name the test `TestSpawnFailuresNeverLeaveTheMachine`, and mutation-pin it.

C2. **Repo rows already sync.** `repo` is a `SharedTable`: every machine writes its own row per remote URL (unique `(origin, origin_url)`, migration 014), and those rows, `mastermind_consent` and `consent_at` included, already reach every peer through the row log. Today `repoConsent` reads `WHERE origin_url = ?` with no origin filter, so on a machine holding several origins' rows it returns any of them. Round 2 must:
- make `RepoConsent` read this machine's own row (`origin` = this installation) for each key;
- make the `consent` doc's `Items()` and `ApplyIn` read and write only this machine's own row (a doc apply writes the local row, which is this machine's to write);
- state in the report what peers' copies of `mastermind_consent` in their own rows mean after this: nothing reads them for the decision. Do not drop the columns.

Add a test that a peer's row with a different answer for the same URL does not change this machine's answer, except through the doc.

C3. **A gate applied from a peer takes effect without a restart.** The running daemon's candidate switching must see an applied `rate_limited` gate on its next decision, the way 3b's config apply bumps the version the watcher reads. If the ledger is cached in memory, the apply invalidates it. Test: `TestAppliedGateGatesTheRunningDaemon`.

C4. **Hand edits on disk propagate.** A person editing a hook script or a native agent file on disk is a person's edit, and it should export. Run the file capture (hash compare, stamp and signal on change only) at the start of every steady attempt, as well as on the paths the plan lists (daemon start, config write, import, join, unlock). The capture must be cheap: hash only the files the config names, and stat first. The stamp is the capture time. Test: `TestHandEditedHookExportsOnTheNextAttempt`.

C5. **Paths come from this machine, never from the entry.**
- An agent name and a hook file name are validated as one safe path component (no `/`, no `\`, not `.` or `..`, no NUL, a length cap).
- The directory is computed locally from the kind and the config root.
- Refusals are tested per rule.
- Writes go through a temp file plus rename in the target directory. A target that exists as a symlink or a non-regular file is refused, not followed.

C6. **Write-stamp keys.** Prefixed keys in `config_write_stamp` are fine. Add a test that no config section name equals a doc prefix (`gates`, `consent`, `session_consent`), so a config key can never collide with another doc's stamp.

C7. **Every new doc is bound through `sharedDocs()` only.** No call site binds by hand, and each round's e2e goes through the real steady pipeline on the memfake. The round-5 wiring audit stays.

C8. **Gate bursts.** `SyncNow` must coalesce: several gate writes inside one attempt cause at most one follow-up attempt (the existing in-flight/dirty flag). Test it.

C9. **The gate is the coverage gate.** Every round's gate is `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`. If `internal/e2e` hangs in the builder environment (#1162), run it with `.shims` removed from PATH and say so.

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §4 (framework, built in 3b,
commit `f0e5e732`), §4.2 (gates only — availability/latency tables are step 4),
§4.3 (files), and the 3c rows of §2. Secrets are step 5, not here.

Seed decisions (not reopened): every instance is a `SharedDoc`/`TxDoc` on the 3b
framework with per-key write stamps, newest-wins with ties to the higher origin,
superseded recorded, park-while-locked, unknown-doc holds the origin, no remote
format change; gates doc `gates` one key per ledger subject, clear = tombstone,
`until` absolute UTC, gate write queues an immediate attempt; repo consent keyed
by remote URL (no URL = no share), session consent keyed by kind + session id;
files embedded in config-doc `agents`/`hooks` entries with hash-guarded apply,
hand-edits kept and reported by `relevo doctor`, agents re-render through the
`refreshRoles` path (`cmd/relevo/daemon.go:475`).

Plan-level decisions (made here, built as stated):

1. Consent is **two docs**, `consent` (repo) and `session_consent` (session), not
   one doc with two key kinds. Backing stores differ (shared-file `repo` table
   vs local-file `session_consent` table), validity rules differ (non-empty
   remote URL vs non-empty kind + id), and per-`(doc,key)` export/applied state
   then needs no key-kind dispatch and cannot collide.
2. A `gates` value is a **JSON array of that subject's live (pruned) entries**,
   not one entry: a subject can hold several live entries (two rate limits, a
   spawn failure plus a rate limit), and `Other` (unknown kind/source) entries
   ride in the same array as raw bytes. Apply replaces the subject's
   known-kind entries wholesale and **never deletes `Other`** (mirroring
   `Ledger.Clear`, which never clears `Other`); a tombstone clears known
   entries and keeps `Other`.
3. Write stamps reuse the generic `config_write_stamp(key,stamp)` table
   (`internal/db/config_write_stamp.go:10-44`) keyed **`<doc>/<key>`**
   (`gates/<subject>`, `consent/<url>`, `session_consent/<kind>/<id>`,
   config keeps its bare keys). A join already clears that table whole, so no
   new clear path is needed — each round pins it per doc with a test.
4. Files are **embedded in config-doc items**, not new docs: a native `agents`
   item carries `{kind: contents}` for its harness files; a `hooks` item
   carries its scripts' contents + executable bit; hook argv is stored
   root-relative (`hooks/<event>.d/<file>`). No new `shared_outbox` doc name,
   no registry change for files.
5. Disk writes on apply go through a **`config.FileApplier` interface** built
   in `internal/relevo` from the XDG config root (same rule as
   `userConfigRoot()`, `cmd/relevo/main.go:329`) and attached at `sharedDocs()`
   (`internal/relevo/syncdocs.go:16`). `ApplyIn` calls it inside the local
   transaction callback; pure tests leave it nil (files skipped, config still
   lands); memfake e2e binds a temp-root applier. The importer never executes
   anything; the executable bit is only ever set under `<config root>/hooks/`.
6. A gate write's immediate attempt is a new **`Daemon.SyncNow`** that calls
   the `queueSync` path (`internal/relevo/sync_tick.go:28`) bypassing only the
   `exportDebounce` age check (`internal/relevo/sync_liveness.go:140-151).
   `availability.Deps` gains a nil-safe `SyncNow func()` hook; the daemon
   wires it to `SyncNow`, `relevo gate` (a one-shot) reaches it over the owner
   socket through the existing sync-verb surface, extended only if no trigger
   exists. Breaker/latch/enabled rules still apply.
7. Caps (refused with a test each): one embedded file ≤ 256 KiB, one `hooks`
   item total ≤ 2 MiB, one `gates` value ≤ 64 KiB. Hook entry refuses `..`,
   absolute paths, escapes from `<config root>/hooks/`, symlinks, and
   non-regular files. Hook scripts carried = union of argv-referenced scripts
   and executable files in `*.d` dirs (what `readHooksDir`,
   `internal/config/import.go:316-360`, would import).
8. `session_consent` syncs `answer` + `answer_at` only; `told`/`told_at` stay
   machine-local. Repo rows without a remote URL never enter `Items()`.
   `ConsentUnset` is never exported as a value — clearing is a tombstone.
   (Local `ClearSessionConsent` NULLs and repo reset to unset both diff as
   deletes.) Expiry prunes on write and exports tombstones; a pure expiry with
   no later write waits for the next write — accepted, pinned by a test.

## Round 1 — gates shared doc + immediate attempt

Behaviour: new `TxDoc` `gates` in `internal/availability` (`gatedoc.go`):
`Items()` groups the pruned local ledger (`LoadLedger`, `ledger.go:103-152`)
by subject into arrays (known entries encoded, `Other` raw); `Apply`/`ApplyIn`
validate each entry (known kinds `spawn_failed`/`rate_limited` only, non-empty
subject equal to the key, non-zero `at`, `until` zero or ≥ `at`, absolute UTC
kept as-is) and replace the subject's known entries, preserving local `Other`;
`SupersedeIn` changes nothing (the caller's applied-state advance is the
record). `DocWriteStamper` + `TxWriteStamper` read `gates/<subject>` stamps.
Gate write paths (`Unavailable` `gates.go:183-221`, `Available`
`gates.go:232-290`, spawn-failure record `gates.go:99-138` via
`AppendEntryLocked` `gates.go:75-87`/`mutateLedgerLocked` `gates.go:59-69`)
record per-subject stamps in the write's own transaction, signal
`shared_outbox(doc=gates)`, and call the `Deps.SyncNow` hook; a write that is
not a person's edit (seed/migration/provisioning import, arriving apply, join)
records no stamp. `SyncNow` + owner trigger per decision 6. Wiring: extend
`sharedDocs()` (`internal/relevo/syncdocs.go:16`); exporter/importer pick it up
through the existing `BindDocs` sites (steady `internal/sync/steady.go:123,239`,
join `internal/sync/join.go:420`, unlock `internal/sync/unlock.go:151`, verbs
`internal/relevo/syncverb_key.go:18,30`, reconcile
`internal/relevo/syncverb_reconcile.go:28`) — no new call sites. `SCOPES.md`
gates row rewritten.

Cases: set vs clear vs expiry; multi-entry subject; `Other` survives apply and
tombstone; unknown-kind entry refused; oversize value refused; bad stamp/PK/op
refused by the framework; locked machine parks, unlock drains; join clears
gates stamps + export state and re-emits; gate write inside the debounce still
starts an attempt; breaker latched → no attempt (existing rule, pinned).

- Step 1: deliver `internal/availability/gatedoc.go` + unit tests; know it
  worked when the new tests pass and `go test ./internal/availability/` is green.
- Step 2: deliver stamped/signalled gate write paths + `Deps.SyncNow` hook;
  know it worked when write-path tests show stamps, signals, and hook calls,
  and unstamped paths show none.
- Step 3: deliver `Daemon.SyncNow` + `relevo gate` trigger + `sharedDocs`
  wiring; know it worked when the debounce test and the memfake steady-pipeline
  e2e (`internal/relevo/sync_gates_test.go`, pattern
  `internal/relevo/sync_config_test.go:16-77`) converge two machines with the
  loser superseded.
- Step 4: deliver `SCOPES.md` gates row; run the focused suite, fix every
  error, then `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

Named tests: `TestGatesItemsGroupBySubject`, `TestGatesApplyReplacesSubjectKeepsOther`,
`TestGatesApplyRefusesUnknownKind`, `TestGatesClearIsATombstone`,
`TestGatesOversizeValueIsRefused`, `TestGateWriteStampsSignalsAndSyncsNow`,
`TestSeedGateWriteIsUnstamped`, `TestGateWriteInsideDebounceStartsAnAttempt`,
`TestJoinClearsGatesStamps`, `TestTwoMachinesConvergeGatesWithLoserSuperseded`.

## Round 2 — repo + session consent docs

Behaviour: new package `internal/consent` (db cannot implement docs: it would
import `synclog`, which imports db) with docs `consent` and
`session_consent`, both `TxDoc`. `consent.Items()` reads every repo row with a
non-empty `origin_url` and a set answer (`RepoConsent`,
`internal/db/consent.go:31-54`) as `{"answer","at"}`; `ApplyIn` upserts by URL
(`Tx.SetRepoConsent`, `consent.go:85-102`) and tombstone resets to unset;
`session_consent.Items()` reads every set session answer (`SessionConsent`,
`consent.go:110-129`) keyed `kind/id`; `ApplyIn` writes answer + `answer_at`
only, never `told`. Both refuse malformed values (answer outside yes/no,
unparseable time, empty URL/kind/id, key that does not parse). Stamp keys per
decision 3, recorded in the `SetRepoConsent`/`SetSessionConsent`/
`ClearSessionConsent` transactions; `told` writes, `UpsertRepo`, and
repo-rename consent carry record no stamp (housekeeping, value unchanged) but
must not disturb stamps. Wiring: extend `sharedDocs()`; same `BindDocs` sites
as round 1, no new call sites; steady-pipeline memfake e2e per doc.
`SCOPES.md` consent rows rewritten.

Cases: URL-only upsert creates a row without touching `common_dir` lookup;
common-dir-only repos never sync; unset ↔ tombstone round trip; told survives
an answer apply; session rows for unknown sessions sit harmlessly; locked
parks, unlock drains, join re-emits; invalid values refused with failing tests.

- Step 1: deliver `internal/consent` docs + pure validation tests; know it
  worked when `go test ./internal/consent/` is green.
- Step 2: deliver stamped consent write paths in `internal/db/consent.go`;
  know it worked when stamp/signal tests pass and told/upsert/rename paths
  show no stamps.
- Step 3: deliver `sharedDocs` wiring + memfake e2e for both docs through the
  real steady pipeline (never hand-bound, per the 3b lesson); know it worked
  when a construction site that forgot a doc fails and convergence passes.
- Step 4: deliver `SCOPES.md` rows; run the focused suites, fix every error,
  then `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

Named tests: `TestRepoConsentItemsSkipRowsWithoutURL`,
`TestRepoConsentApplyUpsertsByURLOnly`, `TestRepoConsentClearIsATombstone`,
`TestSessionConsentSyncsAnswerNotTold`, `TestSessionConsentKeyRefusesBadShape`,
`TestConsentWritesRefuseBadAnswers`, `TestConsentWriteStampsAndSignals`,
`TestToldWriteIsUnstamped`, `TestJoinClearsConsentStamps`,
`TestTwoMachinesConvergeConsents`.

## Round 3 — hook files + hook argv

Behaviour: `hooks` config items embed script contents + executable bit and
store argv root-relative, all inside the existing `config` doc (decision 4).
`internal/config`: `Items()` merges disk contents for `hooks` keys (reads under
`<config root>/hooks/`); `ApplyIn` validates (decision 7) and writes through
`FileApplier`, missing-or-hash-matches only, setting the exec bit only under
`hooks/`; hand-edited files are kept. Argv relativization on read
(`readHooksDir` shape, `import.go:316-360`); an argv row whose `argv[0]` is
absolute and outside the config root is a machine field: extends the
`machineFields` split (`machinefields.go:17-20`, `splitMachineFields`,
`clearedFields`, `dropMachineFields`) with a path-aware hooks rule, moved by
an idempotent Go step behind a new local kv marker (3b pattern
`sync.config_local.v1`), with export refused until the marker is set;
`resolveHooksConfig` (`cmd/relevo/wire.go:34`) absolutizes relative argv via
`userConfigRoot()` before the dispatcher sees it. Hash cache
(`sync.filehash.v1`, path→sha256, local file) + `config_write_stamp` for the
item key, updated together; capture runs on config writes touching hooks,
config import, daemon start, join, and unlock — only on content change, then
signals `config`. `relevo doctor` reports kept hand-edits (new check in
`internal/doctor`, hook-manifest comparison). `outboxPending`
(`sync_liveness.go:153-182`) already sees the `config` signal — no change.
`SCOPES.md` hooks-files row rewritten.

Cases: script added/removed/executable-flipped; argv inside root relativizes
and re-applies under another root; argv outside root stays local and never
exports; escape/symlink/oversize refused, each with a failing-without test;
apply keeps hand-edits and doctor names them; join/unlock capture re-emits
changed files only.

- Step 1: deliver hooks embed/validate/relativize + machine-field rule +
  migration marker, with pure tests; know it worked when
  `go test ./internal/config/` is green.
- Step 2: deliver `FileApplier` seam + hash-guarded apply + capture paths
  (write/import/start/join/unlock); know it worked when apply tests show
  missing/matching written, edited kept, and unchanged files unstamped.
- Step 3: deliver `resolveHooksConfig` absolutization + doctor drift check;
  know it worked when resolve and doctor tests pass without touching the
  network or a harness.
- Step 4: deliver `SCOPES.md` row; run the focused suites, fix every error,
  then `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

Named tests: `TestHooksItemsEmbedContentsAndExecBit`,
`TestHooksArgvRelativizesInsideRoot`, `TestHooksArgvOutsideRootStaysLocal`,
`TestHooksApplyRefusesEscapeAndSymlink`, `TestHooksApplyRefusesOversize`,
`TestHooksApplyKeepsHandEdits`, `TestDoctorReportsEditedHookScripts`,
`TestHookFileChangeStampsAndSignals`, `TestUnchangedHookFileIsUnstamped`.

## Round 4 — native agent files + agents re-render on apply

Behaviour: native `agents` items embed `{kind: contents}` of each kind's
harness file for that agent (`roles.AgentEntry.Native`,
`internal/roles/actors.go:37-45`); source agents keep syncing as config text
only. `ApplyIn` writes native files through the round-3 `FileApplier`
(missing-or-hash-matches, never executable, never outside the harness homes),
then re-renders that agent on every kind on PATH via
`InstallCustomAgents(cfg, env, {Role: name})` without `Force`
(`internal/relevo/customagents.go:58-71`) — the no-`Force` keep-differs outcome
is what keeps hand-edits, matching `refreshRoles` (`cmd/relevo/daemon.go:475`)
minus the shipped definitions. Harness `InstallEnv`
(`internal/harness/install.go:17-31`) in the applier is db-backed (manifest in
the `agents-manifest` kv row, `cmd/relevo/agent.go:47` pattern) so no harness
binary runs. Doctor reports drifted native and rendered files against the
manifests. Capture (hash cache + stamp + signal) on the same paths as round 3
for files outside the config root: daemon start, config writes, join, unlock.
Wiring: attach the real applier in `sharedDocs()`; memfake e2e with a
temp-root applier proves an `agents` change lands files and re-renders.
`SCOPES.md` agents-files row rewritten.

Cases: native contents round-trip per kind; missing kind file on the peer is
skipped, not an error; hand-edited native or rendered file kept + reported;
unknown harness kind refused; oversize refused; re-render failure never fails
the import (warning path, like `refreshRoles`); source-agent text change
re-renders without touching native files.

- Step 1: deliver native embed/validate in `internal/config` + applier writes;
  know it worked when `go test ./internal/config/` is green.
- Step 2: deliver re-render-on-apply in `internal/relevo` + temp-root e2e
  through the steady pipeline; know it worked when files land, renders update,
  and edits are kept.
- Step 3: deliver doctor drift checks for agent files; know it worked when
  doctor names edited files with the fix line and pure tests pass.
- Step 4: deliver `SCOPES.md` row; run the focused suites, fix every error,
  then `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check`.

Named tests: `TestNativeAgentsItemsEmbedPerKindContents`,
`TestNativeAgentsApplyWritesMissingOrMatching`, `TestNativeAgentsApplyKeepsHandEdits`,
`TestAgentsApplyRerendersThatAgent`, `TestAgentsApplyRefusesUnknownKind`,
`TestAgentsApplyRefusesOversize`, `TestDoctorReportsEditedAgentFiles`,
`TestNativeFileChangeStampsAndSignals`.

## Round 5 — wiring audit, join/unlock coverage, spec amendment

Behaviour: audit every exporter/importer construction site for a missing
`BindDocs` (`steady.go`, `join.go`, `unlock.go`, `syncverb_key.go`,
`syncverb_reconcile.go`, plus any site added since — grep `UseDocs`,
`NewExporter`, `NewImporter`); close any gap with a test that binds by hand
failing. Prove join clears all four docs' stamps + export state and re-emits,
locked join parks new docs and unlock drains them, and keyed imports drain
parked gates/consents. Amend spec §4.2 (gates only) and §4.3 to what was
built, and finish `SCOPES.md`. Regenerate the coverage baseline with
`sh scripts/check-coverage.sh --write` only if code moved between packages,
and say so in the report; never lower a baseline to get green; remove a
lint/comment exclusion only if the package is now clean, never add one.

Cases: each doc converges after join from zero with a peer ahead (late joiner
does not outrank); parked entries verify-or-refused on unlock; unknown doc
still holds the origin; `config_local` migration marker idempotent on a
pre-3c database.

- Step 1: deliver the wiring audit + gap tests; know it worked when every
  construction site is named in the report and the suite is green.
- Step 2: deliver join/unlock/parked coverage for the new docs; know it
  worked when the late-joiner and drain tests pass through memfake.
- Step 3: deliver the §4.2/§4.3 amendment + `SCOPES.md` final + baseline
  handling; run the full `RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make
  check` once at the end.

Named tests: `TestAllPipelineStagesBindTheNewDocs`,
`TestLateJoinerDoesNotOutrankGatesOrConsents`, `TestLockedJoinParksNewDocs`,
`TestKeyedImportDrainsParkedNewDocs`, `TestLocalMigrationMarkerIsIdempotent`.

## What is deleted

1. Nothing. No behaviour is removed: the kv `ledger` row stays the local
   merged view, `told` columns stay local-only, hook argv keep working in both
   absolute-inside-root (relativized on read) and local-outside-root forms,
   and agent installation keeps its `--force` semantics. (Moves are not
   deletions: outside-root hook argv move to `config_local` behind a marker;
   file contents are additionally embedded, never relocated.)

## Report requirements (every round)

Files changed with `git diff --stat` against the declared scope; named tests
added and their result; the gate
`RELEVO_REQUIRE_COVERAGE=1 GOTOOLCHAIN=go1.27.2 make check` output and any
coverage-baseline movement; one mutation pin per new rule (break the
condition, show the named test failing); which write paths stamp and which
are unstamped and why; wiring sites touched and the memfake e2e that binds
nothing by hand; spec/`SCOPES.md` lines touched. Plan work as new commits;
never amend or rebase a commit on a remote binding's branch.
