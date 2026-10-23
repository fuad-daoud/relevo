# Unbind kept message, plan 3 of 3 (#1289)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: the two callers that print the refusal raw, the `--pick` list and the cockpit, give back the one force hint B1 took out of the message; docs match. No behaviour change beyond text.

Steps:
1. `internal/relevo/unbind_kept.go`: add `func KeptHint(name string) string` returning the single sentence for the local verb: commit or discard the work and unbind again, or `relevo unbind <name> --force` archives it anyway. No other new API.
2. `internal/pick/model.go:89-92` and `internal/ui/actions.go:209-212`: when `errors.Is(err, relevo.ErrWorktreeKept)`, wrap as `fmt.Errorf("%w; %s", err, relevo.KeptHint(name))`. Other errors pass through unchanged. Both use the same `KeptHint(name)`: `relevo unbind <name> --force` is a command the user runs afterwards, so it is valid after `--pick` too. No second helper.
3. Tests: `TestKeptHintNamesTheBindingAndTheForceFlag` (pure, `internal/relevo`); in `internal/pick` and `internal/ui`, one test each, `TestPickUnbindOfAKeptWorktreeShowsTheHint` and `TestCockpitUnbindOfAKeptWorktreeShowsTheHint`, using the existing fakes for those packages (grep their tests for `Unbind` to find the seam; if the seam needs a harness or network, test only `KeptHint` and say so in the report).
4. Docs: `README.md:513-517` (the `relevo unbind` verb entry): one sentence that the refusal prints the reason and a `next:` line with the `--force` form, and that for a served binding `relevo serve unbind --force` runs on the server. Do not edit `docs/plans/` or `docs/specs/`.
5. Run `go test ./internal/relevo/ ./internal/pick/ ./internal/ui/` until green, then `make check-affected`, then commit "unbind: pick and the cockpit keep their one force hint".

Report: the two wrapped messages as printed by the tests, and the final `git diff --stat` for the set B commits (expect about ten files; if `internal/relevo/bind.go` grew, say why).
