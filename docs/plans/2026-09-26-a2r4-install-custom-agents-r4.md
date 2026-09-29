# A2 round 4, part 4: merge main, and word a custom agent's reset copy correctly

This branch (`relevo/a2r4`, three commits of A2 round 4) branched off `main` before #575
merged. #575 added `harness.FileEditedNewer` ("edit + newer"). Two jobs:

## 1. Merge origin/main

Run `git fetch origin && git merge --no-ff origin/main`. Expect **exactly one**
conflict, in `internal/ui/view_agent.go`'s `resetCmd` switch. If there is any other
conflict, stop and report.

Resolve it so that:
- the `harness.FileStale` case keeps this branch's line:
  `note = "with " + phrase + "; relevo has a newer copy"`;
- `#575`'s `harness.FileEditedNewer` case is kept, worded through the helper:
  `note = "with the newer " + <the copy phrase without its leading "the "> + "; your edit is lost"`.
  Do it with the helper changes in §2 rather than string surgery.

Commit the merge with its default message.

## 2. Fix the copy wording

`copyPhrase(custom)` (view_agent.go:~321) returns `"relevo renders from your config"`
for a custom agent. So the reset note reads "with relevo renders from your config;
your edit is lost", which is ungrammatical. Replace the helper with one that returns
the noun phrase **without** an article:
- `copyNoun(custom bool) string`:
  - custom: `"copy relevo renders from your config"`;
  - shipped: `"copy this relevo ships"`.
- The notes are:
  - edited: `"with the " + copyNoun + "; your edit is lost"`;
  - stale: `"with the " + copyNoun + "; relevo has a newer copy"`;
  - edit + newer: `"with the newer " + copyNoun + "; your edit is lost"`;
  - missing: `"the " + copyNoun`.
- A shipped agent's four texts must stay **byte-identical** to `main` today:
  - "with the copy this relevo ships; your edit is lost";
  - "with the copy this relevo ships; relevo has a newer copy";
  - "with the newer copy this relevo ships; your edit is lost";
  - "the copy this relevo ships".
- The edit + newer **detail line** in `bodyLines` (from #575) takes the same care:
  - shipped: "you edited this file, and this relevo ships a newer copy" (unchanged);
  - custom source agent: "you edited this file, and relevo renders a newer copy from your config".

## 3. Tests

- `TestAgentViewSourceCustomAgentResets` (view_agents_test.go) must assert the
  **whole** note, "with the copy relevo renders from your config; your edit is lost",
  not a substring. That is why the bad wording got through.
- Add `TestAgentViewShippedResetWordingUnchanged`, a table over the four shipped
  states, asserting the exact shipped texts above.
- Every existing golden must pass unchanged, including #575's `agent-edited-newer-132`
  and `agent-reset-132`. If one moves, stop and report.
- Required mutation: make `copyNoun` ignore `custom`, and confirm
  `TestAgentViewSourceCustomAgentResets` fails; then restore it.

## Checks

This laptop cannot take race suites. Do **not** run `make check`, `go test ./...` or
`-race`. Run:
- `gofmt -l $(git ls-files '*.go')`, which must print nothing;
- `go build ./...`;
- `go vet ./internal/ui/ ./internal/relevo/ ./internal/harness/ ./cmd/relevo/`;
- `sh scripts/check-comments.sh`;
- `sh scripts/check-filesize.sh`;
- `golangci-lint run ./internal/ui/... ./internal/relevo/... ./internal/harness/... ./cmd/relevo/...`;
- `go test -count=1 ./internal/ui/ ./internal/harness/ ./internal/doctor/`;
- `go test -count=1 ./internal/relevo/ -run 'Custom|RolesMissing|Gate|Served'`.

## Commit

The merge commit from §1, then **one** new commit for §2-§3:
`fix(cockpit): a custom agent's reset names the copy relevo renders`.
Never amend, never rebase, and never force-push.

If anything does not match, stop and report. The report covers the resolved switch as
it now reads, the mutation result and every check output.
