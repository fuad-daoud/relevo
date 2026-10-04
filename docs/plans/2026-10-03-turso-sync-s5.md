# S5 plan — cockpit `:sync` view (#473)

## 0. Scope

Build spec §6 **S5** only: the cockpit `:sync` view. Consume S3's `Stats`
surfacing and S4's enable/disable verbs as specified — do not re-decide them.
No network handle is ever needed by the view itself: it renders from fake/local
state only. Plan only; no file is changed by this round. Seed/issue-body
contradictions with the tree are stated in §7, not guessed around.

Spec: `docs/specs/2026-10-03-turso-sync-design.md` §6 S5 (lines 207–210) plus
the `:sync` section of the round-1 seed
(`/home/fuad/.local/state/relevo/turso-plan-s5/001-prompt.md` lines 8–18):
pre-on short form (database URL, masked token, test connection, what-syncs
list with secrets/machine-local locked off, first-upload estimate from #475
numbers, confirm naming the target); post-on (on/off + reachability, last
push/pull, unpushed CDC ops `Stats().CdcOperations`, bytes sent/received,
first-upload progress, per-installation rows with labels, unlinked remote
bindings — no backfill per migration 015, listed for re-bind). Keys: `p` push,
`l` pull, `t` test, `e` edit, `d` disable with `y/N` naming the database.
Header shows `sync · offline` / `sync · N behind` when it matters.

## 1. Behaviour and cases

1. **`:sync` command exists.** `:sync` opens the view; unknown-command and
   no-Actions-seam handling match the existing config views (notice, no crash).
2. **Pre-on form (sync off).** Shows database URL, masked token, test
   connection affordance, what-syncs list with secrets/machine-local locked
   off, first-upload estimate, and a confirm naming the target database.
   Token value is never rendered and never logged.
3. **Post-on status (sync on).** Shows on/off + reachability, last push/pull
   times, unpushed CDC ops, bytes sent/received, server revision,
   first-upload progress. All values come from S3's `Stats` snapshot read off
   the update loop; the frame draw performs no I/O.
4. **Per-installation rows.** One row per `installation` row with its
   owner-editable label; other origins' rows are read-only attribution.
5. **Unlinked remote bindings list.** `binding_record` rows with
   `link_origin IS NULL AND link_id IS NULL` (migration 015 semantics) are
   listed for re-bind; nothing backfills them.
6. **Keys.** `p` push, `l` pull, `t` test connection, `e` edit settings,
   `d` disable opening a `y/N` confirm naming the database. `d` follows the
   §5 turn-off flow via S4's verb (best-effort bounded final push, mark
   disabled, delete `turso.token`, close handle). Without the Actions seam
   (serve ui) all action keys are hidden and inert, as on `:servers`/`:settings`.
7. **Header attention.** The shell header shows `sync · offline` /
   `sync · N behind` when it matters, alongside — never replacing — the
   existing needs-you chip.
8. **Error/halted states.** Failed last tick → `sync:behind`-family display;
   handle error needing attention (e.g. authorisation refused) →
   `sync:err` display; disabled → `sync:off`. Exact four-token mapping is
   S2/S3's; S5 renders it.

## 2. Seams (all verified in the tree)

- Command table: `internal/ui/cmdline.go:22-39` (`commands`); dispatch:
  `internal/ui/view_rounds.go:209-230` (`execLine`), config-view cases
  `267-314` (pattern: refuse with notice when `env.Actions == nil`).
- View contract: `internal/ui/view.go:13-20` (`View`: `Crumbs`, `Context`,
  `Keys`, `Capturing`, `Update`, `Body`); `Env` with `Actions` seam
  `35-55`; stack messages `push`/`rootThen`/`notice` `79-96`.
- Pattern views: `:servers` `internal/ui/view_servers.go:32-64`
  (doc-load msg → probe command off the update loop, placeholder state
  `serverNoProbe` line 17); `:settings`
  `internal/ui/view_settings.go:27-52`; S3-shape analogue `:stats`
  `internal/ui/view_stats.go:54-72,183-206` (async fetch msg, never opens DB
  on render path).
- Actions seam: `internal/ui/actions.go:28-95` (`Actions` interface; S5 adds
  sync reads/writes here as thin calls into the same `internal/relevo`
  functions the CLI verbs call). Fake: `internal/ui/actions_test.go:43-120`
  (`fakeActions`); env helper
  `internal/ui/view_candidates_test.go:147-150` (`candActionEnv`); drain
  helper `internal/ui/split_test.go:59`.
- Header: `internal/ui/frame.go:143-179` (`headerView`; needs-you chip
  `168-171` — the sync token joins `rightParts`, must not disturb it).
- Goldens: `internal/ui/golden_test.go:29-44` (`goldenModel`,
  `-update` flag); precedent goldens `internal/ui/testdata/settings-132.golden`,
  `stats-overview-132.golden`; ANSI strip
  `internal/ui/rail_test.go:24`.
- Unlinked source: `internal/db/migrations/015_binding_link.sql:1-18`
  (nullable `link_origin`/`link_id`, no backfill); record comment
  `internal/db/record.go:29-56` (empty link + origin semantics).
- Installation rows: `installation` table + `InstallationTouch` —
  `internal/db/origin_test.go:102-156`.
- S3/S4 inputs (consumed, not re-decided): `SyncClient`
  (`Push`/`Pull`/`Stats`/`Checkpoint`), `Stats` fields (`CdcOperations`,
  `LastPullUnixTime`, `LastPushUnixTime`, `NetworkSentBytes/ReceivedBytes`,
  `Revision`), four statusline tokens — spec §0 lines 38-43 and §3 lines
  103-109. **Tree state: no `SyncClient`/`CdcOperations` exists yet outside
  the spec** (grep over `internal/relevo`, `internal/db`, `internal/ui`
  finds only spec text and unrelated `sync.Mutex`/`SyncRemote` hits) —
  i.e. S2–S4 have not landed; step 1 verifies their shape first.
- TUI-capture method: the `capturing-tui-screens` skill (real-screen check
  before merge, §5).

## 3. Ordered steps (deliverable + how to know it worked)

1. **Verify S3/S4 landing shape.** Deliverable: note in the report of the
   actual `SyncClient`/`Stats`/enable-disable signatures found. Done when
   every S5 read maps to a real symbol, or the round halts per §7.
2. **`:sync` command + pre-on form.** Deliverable: `sync` entry in the
   command table, `execLine` case, `syncView` with pre-on form (URL, masked
   token, test affordance, locked-off what-syncs list, estimate, confirm
   naming target). Done when `:sync` with sync off renders the golden.
3. **Post-on status block.** Deliverable: status rendering from the S3
   `Stats` snapshot (push/pull times, CDC ops, bytes, revision, progress).
   Done when the post-on golden renders from a fake snapshot with no
   network handle present.
4. **Installations + unlinked list.** Deliverable: per-installation rows
   and unlinked-bindings section sourced per §2. Done when the list matches
   the seeded fixture exactly (names, labels, link-NULL rows only).
5. **Keys + disable confirm.** Deliverable: `p`/`l`/`t`/`e`/`d` wiring
   through the Actions seam; `d` confirm names the database (`y/N`).
   Done when each key records exactly one fake call and inert without
   Actions.
6. **Header attention.** Deliverable: `sync · offline` / `sync · N behind`
   chip in `headerView`. Done when header golden shows it beside an
   untouched needs-you chip.
7. **Goldens + mutations + `make check`.** Deliverable: new
   `sync-*.golden` files, green focused suites then green `make check`.
   Done when both commands pass and every §4 mutation fails its test.
8. **TUI-capture real-screen check.** Deliverable: captured real screen of
   `:sync` pre-on and post-on attached/described in the report. Done when
   the real screen matches the goldens (no `loading…` hang, no width
   overrun). Required before merge.

## 4. Tests (all named; TUI goldens + fake stats only; no network in any test)

- `TestSyncPreOnFormGolden` — pre-on form golden; mutation: mask the token
  render → must show the secret.
- `TestSyncPostOnStatusGolden` — post-on status golden from fake `Stats`;
  mutation: zero `CdcOperations` → behind-count line must change.
- `TestSyncHaltedErrorGolden` — behind/err/off states golden; mutation:
  flip the failing marker → header/body token must change.
- `TestSyncUnlinkedMatchesFixture` — unlinked list equals seeded fixture
  (link-NULL rows only, no backfill write); mutation: link one fixture row
  → it must drop from the list.
- `TestSyncRendersWithNoNetworkHandle` — body renders with nil Actions and
  no client; mutation: touch the network in the render path → test must
  fail (it asserts no handle is consulted).
- `TestSyncKeysRecordOneCall` — `p`/`l`/`t`/`e`/`d` each record exactly one
  fake call; `d` opens the naming confirm; mutation: drop the `d` confirm
  gate → must fail.
- `TestSyncHeaderAttention` — header chip golden; mutation: force
  `N behind = 0` with a failed tick → chip must still show.
- One mutation per behaviour test: each mutation above is run and must turn
  its test red, then is reverted.

## 5. Untouched sites

`internal/db/migrations/*` (no schema change); S1 split routing; S2
section/token validation; S3 push/pull timing and markers (read-only);
S4 enable/disable and seed logic; statusline formatter; `:stats`,
`:servers`, `:settings` views; `Source` interface; `testdata/coverage-baseline.txt`
(regenerate with `sh scripts/check-coverage.sh --write` only if the round
moves code between packages, and say so).

## 6. What is deleted

1. Nothing — S5 adds a view; no behaviour is removed.

## 7. Halt conditions (halt rather than improvise)

- S3 `Stats` fields or S4 enable/disable verbs land with different names or
  semantics than spec §6 states: halt, report the divergence with file:line.
- Any `origin = ''` row handling or secret/config placement contradicts §1/§5
  of the spec: halt, do not work around it in the view.
- A step is impossible as written (e.g. no Actions-seam path for a key):
  halt at that step and report.

## 8. Verification and report

- Focused suites: `go test ./internal/ui/ -run 'Sync' -count=1` (iterate
  until green), then full `make check` once at the end (covers gofmt, vet,
  tidy, lint, coverage baseline).
- TUI-capture real-screen check per step 8, stated as a merge precondition.
- Report must include: base SHA + commit list (new commits only — never
  amend/rebase a commit on the remote binding's branch); focused-suite and
  `make check` outputs quoted; each §4 mutation and its red result;
  TUI-capture result; what was left out (nothing after S5 — sync is
  complete; named follow-ups: zstd dictionary for planner transcripts,
  per-section config sync, client-side encryption — all spec §8).
