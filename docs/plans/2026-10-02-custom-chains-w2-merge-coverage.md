# W2 merge: restore coverage in internal/chain and internal/view with tests

**Base:** this branch, which is W2 complete with main merged. On zen, `make check-test` fails only on coverage:

- `internal/chain`: 88.9%, baseline 92.5%.
- `internal/view`: 86.9%, baseline 88.1%.

Both baselines stay as they are. Never run `check-coverage.sh --write`, and never touch `testdata/coverage-baseline.txt`. Add tests until each package is within one point of its baseline. Aim for at or above it.

## Under-covered code, from `go tool cover -func`

- `internal/view/chain.go` `flowChainSegment` is at 0%. It is W2's status segment for a workflow chain (`<step> r<N> · plans i/N`, plus `check run <N>` while a check is awaited). Test every branch in `internal/view/chain_test.go`:
  - running at a run step;
  - running while a check is awaited;
  - a halted chain, and a done chain (no step);
  - a chain with no plans (a task-only workflow).
- `internal/chain/legacy.go`, the read side kept for trace rows written before workflows:
  - `stateWord` is at 40%;
  - `Encode` (event and action) is at 75%;
  - `DecodeEvent` is at 83%;
  - `findingWord` is at 75%;
  - `detail` is at 86%.

  Test each kind and word in `internal/chain`: every legacy event kind, an unknown kind refused, zero, one and many findings, and each step word.
- **Optionally**, if `internal/view` is still short: `QueueText`, `IsPayloadKind`, `DisplayState` and `HumanBytes` are at 0%. They are small pure functions.

## Rules

- Tests only. No production code changes.
- Each test pins one thing, and its name says what. CLAUDE.md test style applies: no history in names or comments.
- `make check` and `make e2e` green.
- Save this file as `docs/plans/2026-10-02-custom-chains-w2-merge-coverage.md` and commit it with the tests.
