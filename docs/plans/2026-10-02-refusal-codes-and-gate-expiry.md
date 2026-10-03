# Builder round plan: refusal codes and gate expiry — base `origin/main`

## Behaviour

- `bind` with an invalid binding name is a caller error (`usage`, exit 2, `next: relevo help`), never `internal`.
- `send` while the previous round's process is still alive is a refusal (`refused`, exit 2, `next` names `wait`/`done`), never `internal`.
- `show` with an out-of-range `--round` is `round_not_found` (exit 1, `next: relevo history`), never `internal`; same for every `round N: binding has M rounds` site.
- All three classifications happen where the error is created via typed sentinels wrapped with `%w`, mapped once in `writeError` / `classifyReadErr`; no string-matching in `cmd/relevo`.
- `gate <token>` / `gate --clear` forwarding never prints one `not found` line per stale binding; bindings the server no longer has are skipped, silently or as a single summary line.
- A manual `gate` whose `--reason` carries a parseable reset (`Resets in 51m30s`, `resets in 4h34m37s`, `try again at <time>`) expires then by reusing `availability` limit-text parsing; only a reason with no parseable reset stays `until cleared`. Same rule applies to the server-side `Unavailable` path, which today always records zero `Until`.

## Cases

1. `bind --name plB` → `usage: binding name "plB" has an invalid character "B"` + catalog `next`.
2. Empty / too-long / bad-first-letter names, `add`/`DefaultBindingName`/`builderAgentName` failures, and every other plain-`errors.New` input refusal in `internal/relevo/bind.go` + `add.go` → `usage` (sweep the same files for the same shape).
3. `send --name plb` with live PID → `refused: binding "plb" (pid N): …wait for the round to close, or relevo done` + `next` naming the way out; covers preflight (`send.go`) and in-lock (`send.go`) and chain (`chain_send.go`) sites, plus `ErrReportPending` / `ErrScopeActive` siblings if they are the same refusal shape.
4. `show plb --round 2 --output` (1 round) and `--prompt/--report/--diff/--drift/--log/--transcript/--gate/--findings/--artifacts/--artifact`, plus `findingsRound`, `showLive`, `showArchived`, `showDB`, `printDiff` upper-bound → `round_not_found`.
5. `gate <cand> --for 130h --reason …` and `gate --clear <cand>` against servers whose bindings are gone → no per-binding `<binding>: <server>: not found` lines; skip + at most one summary.
6. `gate <token> --reason "RESOURCE_EXHAUSTED 429: … Resets in 51m30s"` with no `--for` → `Until ≈ now+51m30s`, pruned by normal ledger expiry; unparseable reason → zero `Until` as today.

## Seams

- Codes/frame: `cmd/relevo/clierror.go:14-70` catalog, `:118-166` `fail/failNext/failWrap`; `cmd/relevo/args.go:239-289` `noticeWriter/writeError`; `cmd/relevo/show.go:257-274` `classifyReadErr`, `:403-481` `printDiff`; `cmd/relevo/registry_rows.go:9-21,284-292,410-421,513-526` verb `Errors` lists; `cmd/relevo/contract_read_test.go:163-168`, `contract_write_test.go:373-402`, `clierror_test.go:44-108` pins to update.
- Name validation: `internal/store/store.go:160-181` `ValidName`; `internal/relevo/bind.go:635-641` + `:762-769` `builderAgentName`; `internal/relevo/add.go:234-247,524-550` `ValidName/DefaultBindingName`; `cmd/relevo/bind.go:190-210,228-240,368-387` `refuseFlag/runBind/runAdd`.
- Busy refusal: `internal/relevo/headless.go:36,43,49` `ErrBuilderBusy/ErrReportPending/ErrScopeActive`; `internal/relevo/send.go:387-394,510-520,399-400,529-531,585-589` preflight/in-lock/scope sites; `internal/relevo/chain_send.go:54-70` `RoundOpenError/Busy/Pending`; `internal/relevo/refusals.go:8-27` `ErrRefused/refuse`.
- Round range: `internal/relevo/show.go:18-20,188-217,245-287,410-446,483-515` `ErrNoCompletedRound/findingsRound/showLive/showArchived/showDB`; `internal/relevo/chain_trace.go:18-20` `ErrNotAChain` (reference, do not change).
- Gate forward: `internal/relevo/remote_gates.go:191-234,242-333,343-405` `serverTokenFor/mismatchLine/ForwardUnavailable/ForwardAvailable`; `internal/serve/unavailable.go:38-101` `handleUnavailable` (`:54,:61` `not found`, `:95` zero `Until`); `internal/remote/client/bindings.go:101-117` `Unavailable/Available`; `internal/remote/proto.go:352` `CodeNotFound`.
- Gate expiry: `cmd/relevo/gate.go:18-33,106-162,219-261` `parseFor/gateUnavailable/gateClear`; `internal/availability/gates.go:148-182,193-251` `Unavailable/Available`; `internal/availability/limit.go:26-43,66-87,115-162,331-363` regexes/`MatchLimit/parseReset/LimitPatterns`; `internal/availability/ledger.go:60-65` `Expired`; `internal/policy/policy.go:212,265-270` `DefaultLimitGate/LimitGateDefault()` (1h); `internal/relevo/switch.go:272-337` `limitText/gateOnLimit` (reference reuse target).
- Seed contradicts code: `contract_read_test.go:168` pins `show unknown round → internal`, which decision 3 overrules to `round_not_found`; `serve/unavailable.go:95` hard-codes zero `Until`, which decision 5 overrules when the reason parses.

## Steps

1. Step 1 — rebase on `origin/main` and map the three error families to sentinels at creation (`store` name errors, `relevo` busy/pending, `relevo` round-range) with `writeError`/`classifyReadErr` as the only mappers; verified by focused `go test ./internal/relevo ./internal/store ./cmd/relevo -run 'ValidName|Send|Show|Contract|Catalog'` green with no harness spawn or network.
2. Step 2 — sweep the same files for same-shape `internal` refusals (`ValidFeature/ParseTicket/already-exists/CWD` neighbours, second `Busy/Pending/Scope` site, all `binding has M rounds` sites) into the same sentinels; verified by the same focused suite plus one mutation per changed mapping (break the wrap and see the named test fail).
3. Step 3 — fix `ForwardUnavailable/ForwardAvailable` to skip `CodeNotFound` per-binding failures and emit at most one summary line; verified by pure `internal/relevo` forward tests with a fake `Remote` (no listener, no network).
4. Step 4 — make manual `gate` derive `Until` from `--reason` via the existing `availability` limit parsing when `--for` is empty, and make the serve `Unavailable` handler do the same instead of zero `Until`; verified by pure `internal/availability` + `internal/relevo` expiry tests showing `51m30s`-style reasons expire and unparseable reasons stay until-cleared.
5. Step 5 — update `registry_rows.go` verb `Errors`, `contract_*_test.go` rows (notably `show unknown round → round_not_found`), and catalog docs without adding exclusions; verified by `go test ./cmd/relevo -run 'Contract|Registry|Catalog'` green.
6. Step 6 — run the full `make check` once (gofmt+vet+lint+tidy+coverage baseline; regenerate baseline with `sh scripts/check-coverage.sh --write` only if code moved packages) and save the plan to `docs/plans/2026-10-02-refusal-codes-and-gate-expiry.md`; verified by `make check` green and the plan file present at that path.

## Deleted behaviour

1. `internal` code + `next: relevo bugreport` for invalid binding names.
2. `internal` code + `next: relevo bugreport` for send-while-previous-process-alive (and same-shape pending/scope refusals swept in).
3. `internal` code for out-of-range `--round` (`round N: binding has M rounds`, including findings/side branches).
4. Per-binding `<binding>: <server>: not found` lines from gate push/clear forwarding (replaced by skip or one summary line).
5. `until cleared` default for a manual/server gate whose reason carries a parseable reset duration.

## Report must include

- `git diff --stat` vs this plan's scope, base commit on `origin/main`, and that no amend/rebase of a remote branch was done (new commits only).
- Focused command(s) run and full `make check` result; any coverage-baseline regeneration stated explicitly.
- Per-case before/after code + `next` hint, and the sentinel/wrap site for each.
- Mutation evidence: one broken-condition run per new/changed test showing it fails without the logic.
- Gate-forward evidence: which skip-vs-summary choice was taken and where stale bindings went; gate-expiry evidence: parsed `Until` for a `Resets in …` reason and prune behaviour, plus the unparseable-reason control.
- Halt note if any step is impossible as written or contradicts the code (design halt beats green suite bending a test).
