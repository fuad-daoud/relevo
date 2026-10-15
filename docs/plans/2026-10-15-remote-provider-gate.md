# Lane E: gates that do not reach a server (`remote-provider-gate`)

- Base: `origin/main` (`41bf202d`); builder is a plan-executor on server zen, starting from that base.
- Issue: fuad-daoud/relevo #1161 (`gate: a provider token gated on the laptop doesn't reach the server running the round`), read via `gh issue view 1161 --json title,body,state` (body carries the full report; no comments).
- Decisions already made (from the seed, all verified below): provider token accepted locally is forwarded so the server gates that provider's candidates too (server resolves a provider token, not only a candidate name; account gate keys keep working as they do); a remote round's next pick and mid-round switch honour the gate like a local round; the push list holds only servers configured on this machine (an old binding's unconfigured server is skipped with no error line); a failed push prints a runnable server-side gate command as `next:`.

## Premise verification (issue vs code; every premise true)

- P1 — laptop accepts `relevo gate opencode` as a provider (2 candidates): TRUE. `gateUnavailable` (`cmd/relevo/gate.go:128-194`) resolves via `availability.GateProvider` (`internal/availability/gates.go:114-138`) and prints `gated opencode (2 candidates)`.
- P2 — forward to zen fails because zen lookup treats the token as a candidate name: TRUE, client-side. `serverTokenFor`/`serverCandidate` (`internal/relevo/remote_gates.go:158-232`) map only token/name; bare `opencode` fails `candidate.ParseRef` (needs 3 segments, `internal/candidate/candidate.go:28-38`), so `mismatchLine` (`remote_gates.go:237-243`) emits exactly the issue's `zen: candidate "opencode" not on the server (available: ...)`.
- P3 — server already resolves a bare provider, so no server-handler change is needed: TRUE. `handleUnavailable` (`internal/serve/unavailable.go:38-109`) passes the subject to `availability.Unavailable`, whose bare-provider branch (`internal/availability/available.go:90-94`, via `GateProvider`) accepts a provider in the server's `set.Providers()`; candidate names can never equal a provider name (`internal/candidate/candidate.go:390-392`), so the name lookup cannot shadow it.
- P4 — server already honours its own ledger for next pick and mid-round switch: TRUE (no pick change needed; the issue's own manual server gate switching mid-round is the evidence). Served pick reads gates (`PickServedCandidateFor`, `internal/relevo/served.go:326-338`); headless reconcile switches a gated candidate (`internal/relevo/headless.go:794ff`). Builder pins this with tests (steps 7-8); if pinning finds a gap, that step halts instead of improvising.
- P5 — `contabo: list candidates: unknown server` from an unconfigured server in the push list: TRUE. `ForwardUnavailable` (`remote_gates.go:303-310`) enumerates servers from bindings, and `client.doRequest` (`internal/remote/client/client.go:141-145`) returns `ErrUnknownServer` (`client.go:28`), formatted by the list-read error line (`remote_gates.go:328-331`).
- P6 — no `next:` for a failed push today: TRUE. `next:` appears in `internal/relevo` only in workflow YAML; `gate.go:187-189` prints forward lines raw.
- P7 — clear path already forwards bare providers as-is (server owns the refuse decision): TRUE (`ForwardAvailable`, `remote_gates.go:391,426-431`). Only the unavailable path is broken.

## Behaviour

- B1: `ForwardUnavailable` with a bare-provider subject (not a resolvable candidate, not a `group@account` key) posts the provider itself to every open remote binding's server, letting the server's `handleUnavailable` gate it provider-wide; no mismatch line on success.
- B2: Provider posts carry the provider's account keys exactly as candidate-token posts do (ledger-derived `group@account` keys via the existing `forwardAccountKeys` path); direct `group@account` subjects and candidate token/name mapping stay byte-identical.
- B3: Bindings naming a server not configured on this machine are skipped with no line and no post, in both forwards (observably: the push list is the configured servers).
- B4: Every forward outcome that leaves a server ungated by error or mismatch carries one runnable `next:` server-side gate command (same subject, same expiry, same reason); stale skips keep the single summary with no `next:`, unknown-server skips stay silent, account-refusal lines stay as they are.
- B5: The clear path (`ForwardAvailable`) keeps its subject mapping; only B3 changes it.

## Cases

- C1: Bare provider, open zen binding, server lists that provider's candidates → `Unavailable` posted with the provider on each open zen binding, no output line.
- C2: Bare provider plus live ledger `group@account` keys for it → account keys forwarded via `forwardAccountKeys`; direct `group@account` subject → existing direct path unchanged.
- C3: Candidate token/name (same-name, renamed-provider, exact-token) → existing `serverTokenFor` mapping and `gated X for Y` announcement unchanged.
- C4: Binding names `contabo`, unconfigured locally → no `Candidates`/`WhoAmI`/`Unavailable`/`Available` post effect, no line, in both forwards.
- C5: Reachable server, list-read error / mismatch / per-binding post error → error line plus one `next:` command gating the same subject/expiry/reason on that server.
- C6: Stale bindings only → single `skipped N binding(s)...` summary, no `next:`.
- C7: Server receiving a bare provider → 200, provider-wide gate; served next pick excludes that provider's candidates; a running round on one switches as a local round does.
- C8: `gate --clear <provider>` → still travels bare; unconfigured servers skipped silently.

## Seams

- `internal/relevo/remote_gates.go:20-26` `accountGateKey`; `:32-38` `serverHasAccounts`; `:72-100` `forwardAccountKeys`; `:108-136` `forwardAccountNames`; `:142-149` `clientCandidateToken`; `:158-232` `serverCandidate`/`serverTokenFor`; `:237-261` `mismatchLine`/`candidateOptions`; `:269-372` `ForwardUnavailable`; `:382-455` `ForwardAvailable` (482 lines total; stays ≤600, new helpers ≤70 lines each).
- `cmd/relevo/gate.go:128-194` `gateUnavailable` (forward call `:187`); `:267-314` `gateClear` (forward call `:294`).
- `internal/mcp/gate.go:82-107` `Gate` (forwards `:88,105`; lines dropped, JSON shape unchanged).
- `internal/ui/actions.go:220-254` `Gate` (forward `:251`); `:259-279` `Ungate` (forward `:276`).
- `internal/serve/unavailable.go:22-36` `tokenForProvider`; `:38-109` `handleUnavailable` (receiving side, unchanged).
- `internal/serve/admin.go:440-450` `ledgerRuntime`; `:460-462` `AdminUnavailable`.
- `internal/availability/gates.go:114-138` `GateProvider`; `:151-189` `Unavailable`; `internal/availability/available.go:48-95` `ResolveClearSubject` (bare-provider branch `:90-94`).
- `internal/remote/client/client.go:28` `ErrUnknownServer`; `:141-145` unconfigured-server return.
- `internal/relevo/chain_remote.go:104-110` preflight `serverTokenFor` use (candidate tokens only; unchanged).
- `internal/relevo/served.go:326-338` `PickServedCandidateFor` (honours gates; pinned, unchanged).
- `cmd/relevo/serve_admin.go:264-296` `serveGateUnavailable` plus `adminRoot` (builder derives the exact `next:` argv here).
- Tests: `internal/relevo/remote_test.go:100-147` `fakeRemote` (`candidatesErr`, `whoAmIErr`, `unavailableErrFor`, `availableErrFor` seams), `:3028-3230` forward tests; `internal/relevo/remote_gates_stale_test.go`, `internal/relevo/remote_account_gates_test.go`; `internal/serve/serve_test.go:1401-1448` `TestUnavailableGatesServerWide`.

## Steps

1. Deliverable: failing provider-forward tests in `internal/relevo` (C1, C2) posting the bare provider per open binding with no mismatch line; worked when `make check-quick PKGS=./internal/relevo/...` shows exactly the new tests failing with today's mismatch line.
2. Deliverable: provider branch in `ForwardUnavailable` (`internal/relevo/remote_gates.go`, small helpers ≤70 lines, file ≤600) with candidate/account paths untouched; worked when step-1 tests plus all existing forward/account/stale tests pass on `make check-quick PKGS=./internal/relevo/...`.
3. Deliverable: failing unknown-server tests in `internal/relevo` (C4, both forwards, every remote call returning `ErrUnknownServer`); worked when they fail today with `unknown server` lines.
4. Deliverable: unknown-server skip in both forwards (`errors.Is(err, client.ErrUnknownServer)` → skip silently, no post); worked when step-3 tests and the full `internal/relevo` package pass.
5. Deliverable: failing `next:` tests in `internal/relevo` (C5 emits `next:` with same subject/expiry/reason; C6/C4 emit none); worked when they fail today for lack of any `next:`.
6. Deliverable: gate-expiry threaded into `ForwardUnavailable` with a pure `next:` renderer in `internal/relevo`, callers updated (`cmd/relevo/gate.go`, `internal/mcp/gate.go`, `internal/ui/actions.go`), MCP JSON shape and `gate --json` stderr routing unchanged; worked when `make check-quick PKGS='./internal/relevo/... ./internal/mcp/... ./internal/ui/... ./internal/serve/... ./cmd/relevo/'` is green.
7. Deliverable: serve-side pin (C7a, behaviour unchanged): bare-provider POST → 200 + provider-wide gate beside `TestUnavailableGatesServerWide`; worked when the test passes and mutation `unknown provider` → 400 fails it.
8. Deliverable: served-pick pin (C7b, behaviour unchanged): provider-gated ledger excludes that provider's candidates from the served pick; worked when the test passes and mutation `clear the gate` → gated candidate is picked again.
9. Deliverable, in this order: plan saved to `docs/plans/2026-10-15-remote-provider-gate.md`; `gofmt -l` over changed files prints nothing; full `PATH=$(echo "$PATH" | tr ':' '\n' | grep -v '\.shims' | paste -sd:) make check` passes; everything committed as new commits on the branch (never amend/rebase a pushed branch); report filed with `git status` clean.
- Mutations the builder must run to prove each fix (a test passing with and without the logic pins nothing): M1 revert the provider branch to always use `serverTokenFor` (step-1 tests must fail); M2 re-emit `unknown server` lines (step-3 tests must fail); M3 drop the `next:` append (step-5 tests must fail); M4 unknown-provider POST and cleared-ledger pick (steps 7-8 tests must fail).
- Iteration loop while working: `make check-quick PKGS=...` (package under edit plus the importers listed in step 6); full gate only once at the end (step 9). If `RELEVO_REQUIRE_COVERAGE=1` flags the package baseline, regenerate with `sh scripts/check-coverage.sh --write` and say so in the report; never lower a baseline to get green.

## Deleted behaviour (closed)

1. `<server>: list candidates: unknown server` (and any `<server>/<binding>: ...: unknown server`) lines for bindings naming servers not configured locally — replaced by silent skip (B3).
2. `<server>: candidate "<provider>" ... not on the server (available: ...)` mismatch lines when the gated subject is a bare provider — replaced by the provider post (B1; failures there now carry error + `next:`, B4).
3. Nothing else is deleted: stale summaries, account-key forward/refusal lines, candidate-mapping announcements, and the clear-path mapping all stay.

## Report must include

- Branch, base (`41bf202d`), and `git status` clean.
- `git diff --stat` against the plan's scope (seams above) plus premise-verification outcomes (P1-P7, all true; P3/P4 needed no code change and were pinned by steps 7-8).
- Every added test, its mutation (M1-M4) and the observed fail-before/pass-after.
- Iteration `make check-quick` commands with outputs, `gofmt -l` output (empty), full filtered-`PATH make check` output.
- Coverage-baseline status (regenerated via `--write` or untouched).
- `not_done`: account-refusal path unchanged (B4), `ForwardAvailable` mapping unchanged (B5), chain preflight unchanged, exact `next:` argv chosen with file/line reference.
