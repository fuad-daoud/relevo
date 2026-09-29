# Cockpit config views, round 7: auto-pick a sole provider (2026-09-26)

Branch `relevo/ck-config` (PR #510). It has one commit. **Amend it** with `git commit --amend --no-edit`, which keeps
the message.

## Change, closed list

1. **The candidate form auto-picks a harness's only provider.** The user saw the add form on claude show the
   `anthropic` chip unselected, with `a provider is required` under it.
   - In `internal/ui/candidate_form.go`, wherever `psel` is set for a harness **with** a provider list, set `psel = 0`
     whenever that list has **exactly one** entry. That is `newCandidateForm` (line ~61/89) and the harness switch
     (line ~254).
   - A list of two or more (agy) keeps today's rule: select the current provider if it is in the list, else -1 (none).
   - Put this in one small helper, `pickProvider(list []string, current string) int`, that both sites call.
2. **`:log`'s gate row shows `provider · ·` for an empty reason.** In `internal/ui/view_log.go` (line ~349), omit
   ` · <reason>` when `statsGateReason(h.Note)` is empty or `·`. Use the helper round 6 introduced for the same fix, if
   there is one.

## Tests

- **Unit, candidate_form_test.go:**
  - adding on a claude row gives `psel == 0`, provider `anthropic`, and no provider error even after enter is tried;
  - switching harness to claude from opencode auto-picks `anthropic`;
  - switching to agy leaves `psel == -1`.
- **Unit, view_log:** a gate history row with an empty note renders the provider without a trailing ` · `.
- **Mutation (required):** change the helper's `== 1` to `== 0`, and the claude tests must fail. Restore it.
- **Goldens:** regenerate with `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -run TestGoldenViews -update -count=1`.
  A golden may change only if it shows a claude add or switch; report any change. Any other golden changing means halt.

## Checks

Run:
- `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`
- `go vet ./internal/ui/`
- `test -z "$(gofmt -l $(git ls-files '*.go'))"`
- `sh scripts/check-name.sh`

`make check` is refused on this machine by a hook, so do not run it.

Then copy this plan into the worktree's `docs/plans/`, `git add -A`, and `git commit --amend --no-edit`.

## Stop rather than improvise

If `psel` is set in places other than the ones named, handle them the same way and report them. If this plan
contradicts the code, halt and report.
