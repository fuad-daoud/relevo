# Handover remote, plan 2 of 3 (#1301)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: `relevo handover --remote <r>` is refused unless r's URL is origin's URL once normalised. `--remote origin` and no flag behave as before. Nothing is recorded on the binding.

Behaviour and cases (the rule is a pure function in `internal/relevo`):
- `remote == "origin"`: allowed, no URL lookup at all (keeps today's path and today's call count).
- Another name whose URL normalises equal to origin's URL (`git.NormalizeOriginURL`, same compare as `SameRepo` at `internal/relevo/handover_rules.go:180`): allowed. The push still goes to that named remote, as today.
- Another name with a different URL: refused. The sentence says why (the target's `bind --resume` always fetches `origin`, so a branch pushed anywhere else strands the binding) and ends with a `next:` the owner can run: push through origin, i.e. `relevo handover <name> --to <to>` with no `--remote`, or set the remote's URL equal to origin's. Keep it one sentence plus the `next:` clause in the same style as the other refusals in `handover_rules.go`.
- Origin has no URL (no origin remote, or empty): any non-origin `--remote` is refused with the same reason; `--remote origin` is left to the existing "checkout has no remote named origin" check.
- The checkout lacks the named remote: keep the existing `Unpushable` wording ("the checkout has no remote named X"); the new rule does not fire.

Steps:
1. `internal/relevo/handover_rules.go`: add to `HandoverFacts` (line 27) three fields: `Remote string`, `RemoteURL string`, `OriginURL string` (comment: the URLs are empty when the remote is origin or could not be read). Add a pure `func RemoteRefusal(f HandoverFacts) error` (or an unexported helper if the file stays short; the file is 217 lines, adding ~25 is fine) implementing the cases above, and call it from `bindingStateRefusal` (line 90) after the `Unpushable` case, so a missing remote or a dirty tree is reported first. Know it worked: the table test below.
2. `internal/relevo/handover.go`: in `branchFacts` (line 134) the remote list is already read. After the loop finds the remote and `remote != "origin"`, read both URLs with `rt.Git.RemoteURL(ctx, dir, remote)` and `rt.Git.RemoteURL(ctx, dir, "origin")`. A failed read of the origin URL leaves `OriginURL` empty. Return them through new named returns (change `branchFacts` to return a small struct if the signature gets past three results) and set `f.Remote`, `f.RemoteURL`, `f.OriginURL` in `gatherHandoverFacts` (line 112). Keep each function under 70 lines.
3. `internal/relevo/handover_rules_test.go`: add `TestHandoverRefusalRefusesARemoteThatIsNotOrigins`, a table over: origin by name, other name with the same URL in SSH vs HTTPS form, other name with a different URL (expect the message to contain "origin" and "next:"), other name with an empty origin URL, and an unreadable remote URL. Reuse `okFacts` (line 9).
4. `internal/relevo/handover_flow_test.go`: add `TestHandoverRefusesAForeignRemoteBeforePushing` (remotes `["origin","mirror"]`, `remoteURLs` differing: no push call, binding still loads) and `TestHandoverAcceptsAnotherNameForOriginsURL` (same URL: one push to `mirror`). Existing flow tests pass `Remote: "origin"` and must stay green untouched.
5. CLI note: this plan adds no test in `cmd/relevo`; the rule is tested as a pure function in `internal/relevo` because CI has no harness and no network.
6. Run `go test ./internal/relevo/ -run 'Handover'` until green, then `make check-affected`, then commit "handover: refuse a --remote that is not origin's URL".

Report: the exact refusal sentence, and confirmation that `--remote origin` makes zero `RemoteURL` calls (the flow test asserts it through `fakeGit.calls` or a dedicated counter).


