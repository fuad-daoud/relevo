# Plan: offer muse in `relevo setup` (issue #1358 item 4)

## Behaviour

- `Probe` on a machine with `muse` on PATH now returns a `muse` status that is
  logged in exactly when `$XDG_CONFIG_HOME/muse/auth.json` exists (default
  `~/.config/muse/auth.json`) or `META_API_KEY` is set and non-empty.
  `META_API_KEY` takes priority over the account login, so the key alone is
  enough even with no auth file.
- A logged-in muse status offers the fixed catalog `muse-spark-1.3`
  (first, the default), `muse-spark-1.2`, `muse-wonder-1.0`, each runnable at
  `none minimal low medium high xhigh max ultra` with default effort `high`.
  A logged-out muse status offers no models and carries login hint
  `muse login`, so the setup line reads `muse: on PATH, not logged in ->
  muse login` instead of today's empty hint.
- The wizard lists muse models in the builder, planner and role questions
  exactly like codex's (choice value `muse/meta/<model>`, effort after `:`),
  and `Build` writes candidates of the `muse/meta/<model>:<effort>` form that
  `relevo config` already accepts. No candidate-parsing change is needed:
  `candidate.ParseRef` is open over harness/provider/model.

## Cases

- Auth file present, no env key: logged in.
- `META_API_KEY` set, no auth file: logged in.
- `META_API_KEY` set and auth file present: logged in (key priority is
  observable only in that no file read is required; both present is still
  logged in).
- Neither: logged out, no models, hint `muse login`.
- `XDG_CONFIG_HOME` set: the probe checks `$XDG_CONFIG_HOME/muse/auth.json`,
  not `~/.config/muse/auth.json`; unset/empty falls back to
  `HomePath(".config/muse/auth.json")`.
- Auth-file read failing with anything other than not-exist follows the
  `InstallEnv.ReadFile` contract (present, differs): treated as logged in.
- Muse-only machine: builder recommends the first muse model at medium,
  planner the first at high, reviewer/lite-planner derive from the cheapest
  muse pick, so `Build` succeeds with no other harness present.

## Seed-vs-code decision

- The seed says to reuse or mirror doctor's muse path
  (`internal/doctor/logins.go`: `home ".config/muse"` through `HomePath`).
  Doctor's row does not honour `XDG_CONFIG_HOME`; copying it literally would
  violate the seed's explicit XDG requirement. The seed wins: resolve the auth
  file through `XDG_CONFIG_HOME` first, falling back to `HomePath`, and do not
  touch `internal/doctor/logins.go`.

## Seams

- `internal/setup/probe.go`: `loginCommands` (lines 82-86), `claudeAliases`
  (line 90, neighbour for the new catalog vars), `probeHarness` (lines
  151-167), `probeClaude`/`probeCodex` (lines 169-208, shape to mirror),
  `HarnessStatus` doc comment (lines 53-59), `Inventory` doc comment (lines
  71-73, stale "three harnesses").
- `internal/harness/harness.go`: `knownHarnesses["muse"]` (lines 173-202,
  `Providers: ["meta"]`, already present), `All()` (lines 544-553, already
  yields muse), `providerFor` is derived from it.
- `internal/setup/questions.go`: `questionKinds` (line 51),
  `builderQuestion` (lines 147-168), `plannerQuestion` (lines 174-190),
  `refFor`/`providerFor` (lines 283-314, no change: muse resolves to
  `muse/meta/<model>` through `Providers[0]`), `harnessChoices` default
  branch in `internal/setup/effort.go` (lines 48-52, no change: it already
  carries per-model `Efforts`/`DefaultEffort` with `:` separator).
- `internal/setup/build.go`: `cheapestPick` (lines 135-150),
  `noHarnessMessage` (line 18).
- `cmd/relevo/setup.go`: `setupInventoryLine` (lines 410-424, no change: the
  generic logged-in / hint rendering covers muse once the status is right).
- `internal/candidate/candidate.go`: `ParseRef` (lines 28-41, no change).
- Fakes: `internal/setup/helpers_test.go` `probeEnv`/`pathEnv` (extend with an
  injected env-var map, mirroring the existing `ProbeTimeout` optional
  interface at `probe.go` lines 119-127).

## Steps

1. Deliverable: `muse` entry `"muse login"` in `loginCommands` plus a
   `case "muse"` in `probeHarness` mirroring codex's three-field shape
   (models plus per-model efforts plus per-model default). Check: the new
   probe-logout test shows the `muse login` hint.
2. Deliverable: fixed catalog vars beside `claudeAliases` (models in default
   order, full effort list, default `high`) plus `probeMuse` and a
   `museOffer`-style split, keeping every function under 70 lines and
   `probe.go` under 600. Check: logged-in status test shows the catalog,
   efforts and default exactly as measured.
3. Deliverable: `getenvFor`-style optional-interface seam (`Getenv(string)
   string` assertion with `os.Getenv` fallback, mirroring `probeTimeoutFor`)
   used for `XDG_CONFIG_HOME` and `META_API_KEY`, with the fake extended to
   inject both. Check: XDG-override and env-key tests pass without touching
   the real environment.
4. Deliverable: `"muse"` in `questionKinds` after `"claude"`, builder
   recommendation of the first muse model at medium, planner fallback after
   codex at high, `cheapestPick` fallback after codex on the first muse
   model. Check: builder/planner/role choice tests list `muse/meta/...`
   values with the expected recommendations.
5. Deliverable: `noHarnessMessage` naming muse
   (`agy, claude, codex, muse, opencode`) with the empty-probe test's
   expectation updated, and the two stale doc comments corrected. Check: the
   updated test passes.
6. Deliverable: new tests, one per pin: auth-file login with catalog/efforts/
   default/hint; env-key login with no file; logged-out with hint and no
   models; XDG override path; wizard listing plus recommendations; `Build`
   writing a `muse/meta/<model>:<effort>` candidate. Check: each named test
   fails when its condition is broken (mutation-check the login flag and the
   default effort).
7. Deliverable: plan saved as `docs/plans/2026-10-26-muse-setup-probe.md`,
   `gofmt -l` clean on changed files, `make check-affected` green, one new
   commit, then a report. Check: `git diff --stat` shows only the files named
   above plus the plan; CI (`-race`, coverage) is the judge.

## Must NOT change

- `internal/doctor/logins.go` and every other doctor file.
- The agy, claude, codex and opencode probes, questions and recommendations.
- `internal/setup/effort.go` (`effortWords` stays without `none`; adding it
  would retokenize agy model names) and `internal/candidate/candidate.go`.
- Contributor-mode flags (#1358 item 3), the muse plugin question, opencode
  handling, `cmd/relevo/agent.go` install text, `cmd/relevo/setup.go`
  rendering.
- No test may spawn a `muse` (or any harness) binary or reach the network;
  every new test runs through the injected `SetupEnv` fake only.
- No exclusion is added to `.golangci.yml`, `scripts/check-comments.sh`,
  `scripts/check-filesize.sh` or `testdata/coverage-baseline.txt`; no commit
  is amended or rebased.

## Deleted behaviour

1. None: no existing probe, question, recommendation or candidate behaviour
   is removed; the empty-probe message gains one name and two stale comments
   are corrected.

## Report must include

- Files changed and the commit hash.
- The login/model/effort mapping as measured (login rule, catalog order,
  effort list, default) confirming no re-probing happened.
- `gofmt -l` output and the full `make check-affected` result, plus which
  tests were mutation-checked and how each failed with the logic broken.
- Anything left unverified and why; the full `make check` run stays with the
  gate.
