# Handover remote, plan 3 of 3 (#1301)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: every user-facing text that mentions `--remote` for handover states the rule. Text only; no behaviour change.

Steps:
1. `cmd/relevo/handover.go:33`: change the flag help from "git remote the branch is pushed to" to say the remote must have origin's URL (the target resumes from origin), default origin.
2. `cmd/relevo/registry_rows.go:462-469` (the `handover` row): leave `Args` and `Flags` as they are; extend `Summary` only if it stays one line. If `cmd/relevo` has a golden for `relevo help --json` or a registry test pinning summaries, update it in the same step (`grep -rn "give a binding to another machine" cmd/ docs/ testdata/`).
3. `docs/runbook.md:84-88` ("Ownership rollout"): add one sentence: a hand-over pushes to origin; `--remote` is accepted only for a remote with origin's URL, because the target's `bind --resume` fetches origin.
4. `README.md`: find the handover mention (`grep -n "handover" README.md`). If the verb list has a handover line, add the same clause; if README has none, add nothing and say so in the report. Do not edit `docs/plans/` or `docs/specs/`.
5. Run `go test ./cmd/relevo/ -run 'Registry|Help|Handover'` until green, then `make check-affected`, then commit "handover: say that --remote must be origin's URL".

Report: each file changed and whether README had a handover line.


