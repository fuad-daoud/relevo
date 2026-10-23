# Runner liveness, plan 5 of 7 (#1310, #1330, #1311)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

**Behaviour.** A policy key `silent_after_ms` (default 8 minutes, must be > 0
when present) with accessor `Policy.SilentAfter()`. Visible and editable in
`relevo config` and the settings UI; nothing reads it yet.

**Seams.**
- `internal/policy/policy.go`: field `SilentAfterMS *int` next to `StallAfterMS`
  (line 28-30), constant `DefaultSilentAfter = 8 * time.Minute` next to
  `DefaultStallAfter` (line 216), accessor next to `StallAfter()` (line 272).
- `internal/policy/validate.go:298`: a `> 0` check in `validateThresholds`, same
  message shape as its neighbours. `validateThresholds` is near the function-length
  limit; if the added lines push it over 70, split the duration checks into a
  helper.
- `internal/relevo/configpolicy.go`: new `Settings` row `silent_after` in the
  `timing` group (rows 9-12 are `stall_after`, `progress_interval`,
  `explore_after`, `stale_after`, lines 177-213; insert after `stall_after` and
  renumber), the `policyPaths` mapping at lines 388-393 (`"silent_after"` to
  `silent_after_ms`), and `[23]Setting` to `[24]Setting`, the doc comment "23
  policy rows" and the test counts (`configpolicy_test.go:30,66,289`). That file
  is 591 lines: first move the `timing` rows (and only them) or the key-to-path
  map into a new file `configpolicy_timing.go` so the edit does not cross 600.
- `internal/ui/settings_form.go:226-230`: add `{"silent_after", "silent_after",
  "silent_after_ms"}` to the timing form; `internal/ui/view_settings.go:66`: a
  one-line help text ("A running opencode builder whose model goes quiet this
  long is aborted and resumed."). Regenerate goldens with the test's `-update`
  flag (`internal/ui/golden_test.go:29`): `go test ./internal/ui -run Golden
  -update`; read the golden diff, only the timing rows may change.
- `README.md` policy section (~1750-1790): add `"silent_after_ms": 480000` to
  the example and one sentence beside `stall_after_ms`.
- `internal/policy/policy_test.go:317`: add `{"silent_after_ms", 8 * time.Minute,
  600000, Policy.SilentAfter}` to the accessor table; `configaudit_test.go` needs
  no change unless it enumerates keys.

**Steps.**
1. Policy field, default, accessor, validation, table row. Done when
   `go test ./internal/policy` passes, including a negative-value rejection test
   `TestValidateRejectsNonPositiveSilentAfter` and a zero-value one.
2. `relevo config` rows, path map, UI form, help, goldens, README. Done when
   `go test ./internal/relevo ./internal/ui ./internal/policy` passes.
3. Run `make check-affected`.
4. Commit.

**Report must include:** the sites you changed for the new key (a list), the
golden diff summary, and the line counts of `configpolicy.go` and
`configpolicy_timing.go`.


