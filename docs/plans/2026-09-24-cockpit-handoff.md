# Cockpit handoff: after waves 1 and 2 (2026-09-24)

**Read this first, then do the review in §3 before planning anything new.** The two
waves were built fast, by many parallel builders, and integrated twice. Each slice was
verified on its own, and each integrated tree passed the full check and CI. But nobody
has yet reviewed the result as a whole, against the design and against the code's
own quality. That review is the next job.

## 1. Where everything is

| what | where |
|---|---|
| Design spec | `docs/specs/2026-09-24-cockpit-design.md` (on `main`) |
| Mockups (design canvas, 13 terminal screens at 132×34) | **https://claude.ai/artifact/7W1oBWL1BC5Zym4G3F2JaA**. Open it with the Artifact tool's `read` action, not WebFetch. |
| Wave 1, merged | PR #429 → `26dd5d6` |
| Wave 2, merged | PR #441 → `b66c6fc` |
| Round plans for wave 1 | `docs/plans/2026-09-24-cockpit-{a1,a2a,a3a,a5a,b1,c2a}*.md` (on `main`) |
| Round plans for wave 2, both integration plans, and this file | `docs/plans/2026-09-24-cockpit-{a2-actors,a2-r2,a2-r3,b2-actions,c2b-stats-view,wave1-integration,wave2-integration,handoff}.md` |
| Installed on this laptop | `relevo v0.13.0-28-gb66c6fc` in `~/.local/bin/relevo`; the previous binary is `relevo.prev`. The daemon re-exec'd onto it. |
| This machine's config | migrated to actors as revision #3, `roles → actors` (`relevo config log`). Picks are unchanged. `relevo config rollback 2` undoes it. |

### What landed (spec slice → effect)

- **A1: candidate names.**
  - `candidate.Name`, derived by `DeriveNames` and stored once by
    `EnsureCandidateNames`. Every write of the candidates section also fills names
    (`fillCandidateNames` in `config.Put`/`PutDoc`).
  - `Set.Resolve`, `NameOf` and `NameFor`.
  - A name is accepted wherever a token was, and printed on every human surface.
  - JSON adds `builder_name`, `Gate.Name` and `CandidateName` beside tokens. Stored
    notes and the wire keep tokens.
- **A3a: config revisions.**
  - A `config_revision` table (migration 006). Every `config.Store` write records a
    revision in the same Tx: source, message, JSON-path diff, full snapshot (no
    secrets). There is a baseline revision.
  - `relevo config log [-n N] [--rev N] [--json]` and
    `relevo config rollback N [--yes]`.
- **A2 (rounds 1–3): agents and actors replace roles.**
  - `internal/actors` holds the sections, the shipped-agent table, and
    `ToRolesFile`. The actors are converted into today's `roles.Registry`, which is
    still the engine.
  - Per-candidate `off`.
  - `MigrateToActors`, with the equivalence test, runs from `newRuntime` and serve
    `loadConfig`.
  - `config init` seeds actors; `roles-init` is removed; `relevo config` shows an
    actors block.
  - `--actor` replaces `--role` on bind and ask (breaking).
  - **Round 4 was never done:** custom agents from the `agents` section rendered by
    `agentsrc` and installed with the manifest rules, plus doctor checks.
- **A2a: `internal/agentsrc`.** Single-source agent format; `Parse`, `Format`,
  `Validate`, `Render` for claude, opencode, agy and codex. Shipped agents are **not**
  rendered from it: they stay hand-maintained per kind. **Not wired:** nothing installs
  a custom agent yet (A2 round 4).
- **A5a: the scratch worktree primitive.** `git.MaterializeTree`,
  `relevo.CreateScratch`, `RemoveScratch` and `SweepScratch`, plus
  `Store.ScratchWorktreePath` (`.worktrees/.scratch/<b>-NNN`). **Not wired** (A5).
- **B1: the k9s-style shell** (`internal/ui/{view,shell,frame,cmdline,view_fleet,view_round,view_rounds}.go`).
  - A view stack, `:` commands with fuzzy completion, `?` help, a header with
    breadcrumb and attention.
  - `:fleet` is a table with a header row and a `/` filter; round detail;
    `:rounds` is the dashboard.
  - A bare `relevo` on a terminal opens the cockpit; `relevo ui :view` opens a view.
  - **Removed:** split and stack layouts, the card rail and compact mode, the `a`
    scope toggle, mouse capture, `d`/`--dashboard` (plan X1–X7).
- **B2: actions.**
  - `ui.Actions` over the `internal/relevo` verb functions.
  - Keys: `x` stop, `D` done, `u` unbind, `g` gate, `o` shell, `s` send (file prompt,
    or `E` for `$EDITOR`), `b` bind, `r` retry on another candidate; `:ungate` and
    `:log`.
  - Confirms name the owning planner.
  - Planner `you` (kind `human`), with report-ready delivery (`relevo.Pull`, route
    `tui`).
  - stderr and slog are captured into the footer.
  - Sort moved to `a`.
- **C2a/C2b: stats.**
  - `internal/stats` (a `Build`/`Render` pair plus `relevo.StatsInputs`).
  - `relevo history --stats` (default 30d, `--since all`) and the `:stats` panels.
  - `RoundRow.Switches`; relaunches no longer count as switches.

## 2. What the mockups show that is NOT built yet

Read the canvas and compare it with the list below.

| mockup board | state |
|---|---|
| `:fleet` | Built. The "recent" events strip under the table is **not** built. |
| round detail | Built with today's five tabs. The **artifacts tab** (reader output files) needs A4/A5. |
| `: command palette` | Built as plain lines in the body. The mockup draws a box. |
| confirm · stop a round | Built as plain lines above the footer rule. The mockup draws a box. |
| `:actors`, `:actors › builder` (reorder, on/off, draft marks) | **Not built** (C1). |
| `:agents`, `:candidates`, add candidate | **Not built** (C1). The add form needs a draft engine. |
| save draft, `:audit`, `:settings` | **Not built** (A3b + C1). `config log`/`rollback` exist on the CLI only. |
| `:stats` | Built (C2b). |

## 3. The review (do this first)

**Goal:** a written findings list. It covers design conformance, correctness, safety
and UX, ranked by severity, with each item marked "fix now", "fold into the next
wave", or "accept". Write it to `docs/plans/<date>-cockpit-review.md`.

**3.1 Code review of the two merged PRs.**
- The ranges are `ff04f5d..26dd5d6` (#429) and `26dd5d6..b66c6fc` (#441, which also
  includes #439/#440 from another planner; review only the cockpit paths).
- The diffs are large: about 5k lines each, including tests and goldens. Split the
  review by slice: A1, A3a, A2, B1, B2, C2. Use the `code-review` skill at high
  effort per slice, or parallel `reviewer` consults (`relevo ask --actor reviewer`).
- Questions worth answering explicitly:
  - **A1:**
    - `Resolve` precedence, and the name vs provider-name collision rule.
    - The `ImportFiles` path writes candidates without `fillCandidateNames`, so the
      migration labels that write. Is that the behaviour we want?
    - The remote `addRemote` name matching, when an old server sends no names.
  - **A3a:**
    - Does *every* config write path record a revision? Check `Put`, `Delete`,
      `PutDoc`, `PutSecret`, `SecretDelete`, `ImportFiles`, `Rollback` and
      `MigrateToActors`. `db.SecretStore` writes (TLS keys, agy creds) are
      intentionally not recorded: confirm that is acceptable.
    - A full config snapshot is stored per revision. Estimate the growth, and decide
      whether pruning is needed.
    - Rolling back to a pre-migration revision re-migrates on the next run (tested).
      Is that the right UX?
  - **A2:**
    - Migration edge cases: legacy unlisted candidates, native agents built from
      custom definitions, `off` carried through, candidate `tier` dropped with a
      note.
    - The `roles` section is still accepted by `Validate` (for rollback and
      imports). Should `config set roles` be refused now?
    - `--actor` is a breaking change. Check the plugin and any planner-facing text
      for `--role`.
  - **B1:** the key-routing rules (plan §5.2). Every non-key message is forwarded to
    every view in the stack. Check the perf and correctness of that.
  - **B2 (safety first):**
    - Every destructive action goes through a confirm.
    - Does stderr capture restore on panic?
    - `Stop` and `Done` hold the state lock through the kill grace, inside a
      `tea.Cmd`. Check the UI's responsiveness while they run.
    - Retry's order: stop, then read the plan, then send a **new** round with
      `Builder` set, and the builder change **persists**.
    - Report-ready `Pull` for planner `you` versus an AI planner's background
      `relevo wait`: could the TUI ever consume a report meant for an AI planner?
      It should only pull for `PlannerName == "you"`. Verify.
    - The `you` record (kind `human`, session `tui`) in `relevo planner list` and
      pruning.
  - **C2:** the definitions (closed, done %, halt %, $/round with plan exclusion and
    histq's cost rule) against spec §5. The `:stats` refresh throttle.
  - **A2a/A5a:** unused so far. Is their API fit for A2 round 4 and A5 before anything
    depends on it?
- **Output hygiene:** twice, builders wrote ANSI escapes into piped text output
  (both fixed). Grep `internal/relevo` and `cmd/relevo` for `\x1b`/`ansi` usage
  outside the statusline and confirm there are no others.

**3.2 UX review in a real terminal.** This needs a human, or a session with a
terminal. Run `relevo` and walk every built board of the canvas:
- `:fleet`, `/`, `a`, `enter`, `?`;
- `:rounds` (`/` query, `b` regroup, `enter`);
- `:stats` (`w`, `tab`, `enter`, `p`);
- `:log`;
- a confirm (`x` then `n`);
- the gate prompt (`g` then `esc`).

List each deviation from the mockups and anything confusing.

**Polish items already known:**
- The command box and the confirm are unframed.
- The round view shows the binding name and round twice: in the context row and in
  the pane head.
- The spend bars were just widened; check them at real widths.
- The fleet has no "recent" events strip.
- The footer drops keys by priority. Check that the important ones survive at 100
  columns.

**3.3 Doc review.** Check that README's new `### Actors and agents`, `relevo config`,
`--actor`, `relevo ui :view` and `history --stats` sections match the code.

## 4. Known issues and operational notes

- **Open bugs** filed during this work:
  - #436: `send` on a busy DB can leave an untracked builder running.
  - #437: `bind` on a busy DB leaves an empty `relevo/<name>` branch.
  - #438: the report tail rejects lists spread over several lines, so the round
    reads as unmarked (wait exit 2).

  All three bit several times today: the shared `relevo.db` is busy with several
  planners and the daemon. When `bind` fails as busy, delete the empty branch (it
  points at the base commit) and retry. When `send` fails as busy, check `pgrep -fa
  'builder "<name>"'` before retrying.
- **contabo:**
  - Its serve daemon (`~/srv/bin/relevo-serve`, state `/srv/data/relevo-serve`) is a
    separately deployed, older build.
  - Its builder order has drifted: deepseek is first, while the CLI config on the
    same box has agy first. That is why every server round ran on opencode/deepseek.
  - Its CLI and serve binaries are **not** updated to wave 2. Updating them goes
    through the `deploying-to-contabo` skill.
- **agy right now:** both agy candidates are quota-gated. gemini resets within the
  hour. `claude-sonnet-4-6` on antigravity resets about Sep 26 18:04. The configured
  order skips them. Do not hand-pick builders.
- **The feat-drift guard** (`scripts/check-plugin-version.sh`, max 10 first-parent
  `feat` commits since `v0.13.0`): `main` is at **8**. A release (`make release
  VERSION=…`) is due within about two more feature PRs. Squash a multi-commit wave
  into one commit before pushing, or its commits count separately.
- **Verifying on this laptop:**
  - `make check` is hook-blocked.
  - `dev run` mirrors without `.git` and `dist/` (so gofmt passes vacuously, and
    `dist`/`internal/migrate` fail to build there).
  - `/tmp` is a 3 GB tmpfs with a quota; test temp dirs overflowed it once.
  - Use `~/.cache/relevo-verify/verify.sh <branch> [pkgs…]`. It makes a real clone
    under `$HOME`; runs gofmt, vet, tidy, agents-shipped and local `-race` for
    `cmd/relevo`, `dist`, `internal/migrate` and the named packages (with `TMPDIR`
    under `~/.cache`); and runs `-race` for everything else via `dev run`.
    `server-exit=1` means no failing lines (it is grep's exit code).
  - For a merged main, also run `make e2e` with `TMPDIR` set.
- **This planner session's relevo MCP server** is older than the installed binary.
  Reconnect with `/mcp`.
- **Leftover branches:** the `relevo/ck-*` bindings are all marked done (worktrees
  released, branches kept). `cockpit/wave-1` and `cockpit/wave-2` are merged, so they
  and the `relevo/ck-*` branches can be deleted, and `relevo unbind --done` archives
  the bindings.

## 5. Remaining roadmap (after the review's fixes)

In dependency order (spec §9):

1. **A2 round 4:** install custom agents from the `agents` section (`agentsrc.Render`
   plus harness manifest rules), and doctor checks.
2. **A3b:** the typed draft engine: `config_draft`, typed changes (`actor.move`,
   `actor.off`, `candidate.add`, `candidate.rename` rewriting actor lists,
   `setting.set`, `agent.put`), `Describe`, and conflict replay on save (spec §6.4).
3. **C1:** the config views: `:actors` (reorder with K/J, space on/off), `:candidates`
   (add form, probe, rename), `:agents` (`$EDITOR` source), `:settings`, `:draft`,
   `:audit` with rollback, `:gates`, `:planners`, `:servers`; `relevo config` on a
   terminal opens `:actors`; the header shows `draft · N unsaved`. It needs A3b and B2.
4. **A4:** the state rename. The binding's `builder` slot becomes actor + per-round
   candidate; `--builder` becomes `--candidate`; `NNN-<actor>/` artifact dirs with
   `summary.md`/`report.md`; `wait` output, MCP tools, the plugin, and
   `architect.*.md` "Handing off" (needs `scripts/agents-shipped.sh --write`). The
   riskiest slice.
5. **A5:** reader rounds. Any actor can be bound; scratch worktree wiring (A5a) plus
   the daemon sweep calling `SweepScratch` and `PruneWorktreeDirs`; the artifacts tab.
6. **C3:** the site (`~/projects/relevo-site`). It still describes herdr, statusline,
   `relevo policy`/`unavailable`; it needs the new vocabulary and TUI specimens from the
   goldens.

## 6. How this was run (keep what worked)

- One spec, then small plans per slice. Several builders ran in parallel on local
  worktrees and on contabo.
- Every report was verified by the planner, with a planner-chosen mutation per slice.
- Each wave ended with an **integration round** on a combined branch. It caught a
  real A1×A3a semantic conflict, merge conflicts with other planners' work on `main`,
  and the feat-drift guard. Keep that step.
- Plans must say "plain text only (no ANSI)" for any CLI text. They must give exact
  signatures that compile (`(T, tea.Cmd, bool)`, not a named result after unnamed
  ones), and they must not assign one key to two actions.
