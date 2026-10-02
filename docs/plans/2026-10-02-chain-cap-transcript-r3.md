# Plan: chain builder round cap + member transcript default (issues #859, #849)

Base is `origin/main` at the binding's HEAD (v0.15.0-52-g3d22ab1b).

Two independent, small chain defects. Do them as two commits; each has its own
tests.

## Defect A (#859): a chain builder hits the round cap of 20, and the refusal reads `internal`

A chain's builder member carries every plan of a long chain plus its correction
and repair rounds. At round 21 it is refused:

```
relevo: internal: cc-w2: round 21 could not start on zen: hit the round cap of 20
  next: relevo bugreport
```

Two problems: the cap is the same 20 as an ordinary binding, and the refusal is
unclassified, so it renders as `internal` with `next: relevo bugreport`.

Verified seams:

- `internal/store/store.go:38` `defaultRoundCap = 20`; `store.Binding.RoundCap`
  (`internal/store/binding.go:153`); stamped in `prepareSave`
  (`internal/store/lifecycle.go:226-228`) when zero.
- Cap refusal strings: `internal/relevo/send.go:378` and `:505`
  (`binding %q hit its round cap of %d`) and `internal/relevo/headless.go:687`.
  None is a typed sentinel, so `cmd/relevo/args.go:writeError` (`:251`) falls to
  `codeInternal` (`cmd/relevo/clierror.go:69`, `next: relevo bugreport`).
- A chain member is built at `internal/relevo/chain_start.go:362`
  `chainMemberBinding`, which never sets `RoundCap`, so `prepareSave` stamps 20.
  The writer member identity is `chainMembersFor` (`chain_start.go:252-262`).
- No `--round-cap` flag or config knob exists anywhere.

Required behaviour:

1. A chain's writer (builder) member gets a cap scaled to the chain it runs, with
   a floor of 20 — for example
   `max(20, plans × (1 + max_corrections + regate) + slack)`, computed where the
   member is built (`chainBuildMembers`/`chainMemberBinding`), from the chain's
   plan count and its correction/repair budget. Non-chain bindings keep 20.
2. The cap refusal is a typed sentinel (e.g. `ErrRoundCap`) returned from all
   three sites, mapped in `cmd/relevo/args.go` to a non-internal code
   (`codeRefused` or a new catalogued code) whose `next:` names the way out — for
   example binding a fresh builder, or the flag that raises the cap.
3. If it is cheap, add `bind --resume --round-cap N` (or a `config` knob) to raise
   a halted binding's cap, and name it in the `next:` hint. If it is not cheap,
   leave it out and say so; the scaling in (1) plus the honest code in (2) is the
   required core.

Tests:

- `internal/relevo`: a chain's writer member is built with the scaled cap (assert
  the value for a known plan/correction count), and an ordinary binding still gets
  20.
- `internal/relevo`: `Send`/`SendDryRun` on a capped binding return the typed
  sentinel, and it matches `errors.Is(..., relevo.ErrRoundCap)`.
- `cmd/relevo`: the cap refusal renders the chosen code and a non-`bugreport`
  `next:` (a pure classification test; no harness, no network).

Mutation: remove the scaling so the chain writer gets 20 again. The chain-cap
test must fail.

## Defect B (#849): `show <member> --transcript` defaults to the newest closed round, not the open one

For a server-chain member whose round is open, `relevo show <member> --transcript`
with no `--round` prints the newest *closed* round, because the default round is
computed from report/prompt entries and a server-chain member's rounds are only
installed at close.

Verified seams:

- `internal/relevo/show.go:245` `showLive`; the default block at `show.go:277-284`
  sets `round = completed` (highest round with a `KindReport` entry, falling back
  to `b.Round-1`). Transcript dispatch is `show.go:372-384` → `RoundTranscript`.
- For an open server-chain member, `b.Round` is the open round and its mirrored
  transcript lives at `BuilderLogPath(name, b.Round)` (see `remote_sync.go:56`,
  `:93`; `applyRemoteView` `:331-343`; `TestServerChainMemberObservesTheLiveRound`,
  `internal/relevo/chain_server_start_test.go:418`).

Required behaviour: for `ShowTranscript` only, when the open round (`b.Round`) has
a readable mirrored transcript or stream, default to it instead of the newest
completed round. Keep every other section (`--prompt`, `--report`, `--diff`) on
newest-completed, so `TestShowLiveDefaultsToNewestCompletedPlan`
(`internal/relevo/show_test.go:249`) stays green. `--round N` still wins.

Test: extend the server-chain seam so a member has closed round(s) and an open,
mirrored round; call `Show(..., ShowOptions{Name: <member>, Section: ShowTranscript})`
with no `Round` and assert the result's round is the open round and the body is the
mirrored log. Build on `seedServerChain`
(`chain_server_start_test.go:89`) and the pull helpers
(`chain_pull_test.go:130` `TestChainPullInstallsEveryRoundInOrder`).

Mutation: drop the transcript exception. The new test must fail.

## Constraints

- `make check` and `make e2e` green. Focused first:
  `go test ./internal/relevo/ ./internal/store/ ./cmd/relevo/ -count=1`.
- `internal/relevo` and `internal/store` have coverage baselines; never run
  `scripts/check-coverage.sh --write`, never lower a line. Add tests instead.
- Files ≤ 600 lines, functions ≤ 70 lines. Comments say why; no issue numbers,
  `§`, "round N" or "used to" in code or tests (`scripts/check-comments.sh`).
- `go mod tidy -diff` clean.
- Commit as new commits; save this plan's round section to
  `docs/plans/2026-10-02-chain-cap-transcript-r3.md` and commit it with the code.

## Halt if

- scaling the cap needs a wire/config field that would change an old server's
  behaviour (say which and stop);
- the transcript default cannot be changed for `ShowTranscript` alone without
  changing another section's default.

## Report includes

- `git diff --stat`, focused and full command outputs;
- the chosen cap formula and code/`next:` for the refusal;
- whether you added the `--round-cap` knob;
- new tests by name, each mutation, and any deviation.
