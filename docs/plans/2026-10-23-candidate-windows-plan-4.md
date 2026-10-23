HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 4 — the concurrency cap

Deliverable: a candidate at its cap is skipped, the provider is not gated, and
the count is the live rounds over the plan's pool.

1. Add `internal/relevo/inflight.go`: the live-builder predicate (headless,
   PID non-zero, not DONE/PAUSED) and `openRoundsByCandidate(bindings)`, plus
   `Runtime.InFlight func() map[string]int` (`runtime.go:148-...`) and its fill
   in `AvailabilityDeps` (`runtime.go:395-412`), defaulting to a walk over
   `rt.Store`.
2. Add `InFlight` and `InFlightExcept` to `availability.Deps`
   (`internal/availability/deps.go:18-48`).
3. Set `Runtime.InFlight` on the server's runtimes
   (`internal/serve/serve.go:366-390`, `internal/serve/admin.go:441-451`) to a
   machine-wide walk over every owner store, beside `census`
   (`internal/serve/admit.go:44-100`) — the same iteration, a different count.
4. Add `internal/availability/concurrency.go`: `concurrencyGates(d Deps)
   []Gate`, the pool rule (harness+provider) and the `2/2` note; append it in
   `GatesFrom`.
5. Pass `InFlightExcept: b.Name` in `switchBuilder`'s gate set
   (`internal/relevo/switch.go:233-234`).
6. Make `allGated` (`internal/relevo/candidate.go:434-442`) reason-aware for a
   cap, leaving the pure-gate text byte-identical.
7. Ignore `Demote` gates in the two wire views (`internal/serve/candidates.go:26`,
   `internal/serve/actors.go:66`).
8. Write `TestInFlightCountsOnlyLiveBuilders`, `TestInFlightSpansThePool`,
   `TestInFlightExceptSkipsTheResolvingBinding`,
   `TestConcurrencyGateAtTheCap`, `TestConcurrencySkipDoesNotGateTheProvider`,
   `TestAllGatedNamesTheCap`, `TestServerCountsInFlightAcrossOwners`.
9. Run `go test ./internal/relevo/... ./internal/availability/... ./internal/serve/...`
   — green, and `TestConcurrencySkipDoesNotGateTheProvider` fails if the gate is
   recorded as a rate limit or its token is the provider.

Commit: `availability: skip a candidate at its concurrency cap`.
