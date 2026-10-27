# Plan: muse contributor-mode guard

## Behaviour

- Every `muse exec` argv relevo builds carries an explicit
  `--contributor-mode on|off`, so a run never inherits the machine's saved
  muse setting. The flag is explicit both ways, on every muse launch.
- The value comes from a new typed per-candidate field `contributor_mode`;
  unset means `off`. Candidates files without the key load unchanged.
- A hand-written `--contributor-mode` inside a muse candidate's `extra_args`
  migrates into the typed field at load: the value becomes the field's value,
  the flag is dropped from the effective `extra_args`, and a warning names the
  migration. Load never fails for a config that works today. When both the
  field and `extra_args` set it, the typed field wins and the warning says so.
- Setting the field on a non-muse candidate fails candidate load. Non-muse
  harness argv is byte-identical with or without a mode handed to it.

## Cases

- Muse candidate `on` renders `--contributor-mode on`; `off` renders `off`;
  unset renders `off`.
- A bad field value fails load naming `contributor_mode`; the field set on an
  agy, claude, codex or opencode candidate fails load as muse-only.
- `extra_args` carrying `--contributor-mode` in bare (`--contributor-mode
  on`) or `=` (`--contributor-mode=on`) form on a muse candidate migrates as
  above: extra-args-only takes the value and warns of the migration, both-set
  keeps the field and warns that it wins, and a bare flag with no value fails
  load. An invalid value there fails load naming the field.
- A config without the key loads exactly as today.
- Resume and relaunch of a muse round keep the flag: `ResumeBuild` reuses the
  `Launch` value, and the session-resume argv renders it too.
- Builder rounds, planner, reviewer and consult launches, the availability
  probe, and the bind and served validation launches all render it, because
  they all flow through the two choke points below.

## Seed-vs-code decisions

- **Location: per-candidate typed field, not account or policy.** The flag is
  muse-exec-specific; accounts (`internal/account/`) group credentials by
  provider and policy sets tiers, neither knows harness kinds. The candidate
  is what every launch site already receives (`spawn.HeadlessLaunch(c, ...)`
  in `internal/spawn/launch.go:23`; direct `h.Launch(c.Provider, c.Model,
  c.ExtraArgs, ...)` in `internal/relevo/headless.go:430` and
  `internal/consult/verify.go:232`). In-schema precedents: `Tier string`
  validated via `harness.ParseTier` (`internal/candidate/candidate.go`,
  `Tier` field beside the new one) and `Plan bool`. Per-candidate also
  satisfies the per-repo ask: a binding names its candidate, so each repo's
  binding picks its own value.
- **Default unset is `off`.** The guard direction is `off` (training-data
  ingestion is silent and irreversible); the saving is preserved by
  explicitness and visibility in the candidates file, not by defaulting to
  `on` (default-`on` would silently opt private-repo content into training).
  Cost is visible on the bill and recoverable; leakage is not.
- **`extra_args` collision migrates, never last-wins at the CLI.** The live
  config carries this exact workaround, so refusing load would break a working
  config on upgrade. At candidate load, an `extra_args` `--contributor-mode`
  (bare or `=` form) is read as the typed field's value when the field is
  unset, dropped from the effective args either way, and reported through the
  existing warning channel; the typed field wins when both are set. Only an
  invalid value fails load.
- **Render inside `Harness.Launch`'s muse branch, not at call sites.** One
  choke point covers builder rounds (`internal/relevo/headless.go:211` via
  `spawn.HeadlessLaunch`), send (`internal/relevo/send.go:445`), consult
  reviewer (`internal/consult/verify.go:287`), the probe
  (`internal/availability/probe.go:134`), and validations
  (`internal/relevo/bind.go:828`, `internal/relevo/served.go:343`).
  `ResumeBuild` reuses the `Launch` value
  (`internal/harness/resume.go:82`), so the resume path inherits it; only
  `Harness.Resume`'s muse branch (`internal/harness/resume.go:51`), which
  builds its own base, renders the same flag.

## Seams

- `internal/candidate/candidate.go`: `Candidate` struct, `ContributorMode`
  field beside `Tier`/`Plan` (lines 69-72); `Parse` loop migration hook
  (lines 302-306); `validateCandidate` into `checkRest` for value vocabulary
  and the muse-only refusal (lines 341-353 and the `contributor_mode` checks
  beside the `Tier` check); `migrateContributorMode` (lines 383-431) for the
  bare/`=` collision, the field-wins warning, and the invalid-value refusal;
  unknown keys stay shape-derived via `internal/candidate/unknown_keys.go`,
  so the new key needs no form change.
- `internal/harness/harness.go`: `Harness.Launch` signature and doc comment
  (line 340) with the muse branch (lines 380-385); `museLaunch` as the flag
  render site (lines 428-449).
- `internal/harness/resume.go`: `Harness.Resume` signature and doc comment
  (line 37) with the muse base render (lines 51-63); `ResumeBuild` (line 82)
  inherits the flag from the `Launch` value, unchanged.
- `internal/spawn/launch.go:28` `HeadlessLaunch` threads the candidate's
  field into `h.Launch`; the two direct `h.Launch` call sites thread it the
  same way (`internal/relevo/headless.go:430`,
  `internal/consult/verify.go:232`).
- Tests in existing style: `internal/harness/muse_test.go:22`
  (`TestLaunchMuseArgv` table with on/off/unset rows),
  `internal/harness/resume_test.go:340`
  (`TestResumeMuseContributorMode`), `internal/spawn/launch_test.go`
  (headless choke-point pin), `internal/candidate/candidate_test.go`
  (validation table rows plus the value and migration pins).
- Config surface needs no form change: `CandidateInput` carries only
  Harness/Provider/Model (`internal/relevo/configedit.go`); the field is
  JSON-level like `tier`/`plan`.

## Deleted behaviour

One behaviour changes: `--contributor-mode` in `extra_args` previously
reached muse argv verbatim via the extra-args tail of `Harness.Launch`; after
this change it is consumed at load as the typed field's value (with a
warning) and no longer passes through as a raw extra arg. No key, default, or
path is deleted.
