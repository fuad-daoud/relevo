# Plan: read OpenCode's real session key, and refuse an empty identity (#639)

Facts below were read on the round's base, `3e938a24`. Every location is named; if a quoted range does not match the tree, halt rather than improvise. Decisions are the seed's; the root cause (OpenCode 2.0.18 sends `_meta:{"ai.opencode/sessionID":"ses_…"}`, relevo reads `_meta.sessionID`) is established and not re-investigated.

## 1. Today (the reference)

- `internal/mcp/server.go:209-212`: `callMeta` has one field, `SessionID string \`json:"sessionID"\``; the comment on `toolCallParams.Meta` (200-207, at 203-205) claims opencode sends `_meta.sessionID`; `handleToolsCall` passes `params.Meta.SessionID` at line 221. The test at `internal/mcp/server_test.go:408-432` feeds the bare key, which is why the suite passes.
- `internal/mcp/verbs.go:34-39`: `masterMindFor` resolves the session only when `ResolveSession != nil && session != ""`, and otherwise returns `v.MasterMind, nil`. `Status` (41-60) calls it only when `!a.All` and filters `rep.Bindings` on `b.MasterMindID == id`; only `Status` calls it (line 49 is the sole caller). `Send` (106) and `Done` (143) ignore their `session` parameter — they address a binding by name, and `relevo.Send`/`relevo.Done` (internal/relevo/send.go:404, done.go:27) never take a mastermind id.
- `cmd/relevo/mcp.go:107` builds the verbs as `mcpVerbs(rt, kind, rec.ID)`; for `kind == "opencode"` the record block (71-92) is skipped, so `rec.ID` is `""` and `MasterMind` is `""`. `mcpVerbs` (209-217) sets `ResolveSession = opencodeSessionMasterMind(rt)`; that function returns a nil resolver when `rt.MasterMinds == nil` (222-225). Then `masterMindFor` returns `""`, and `Status` silently keeps only bindings whose `MasterMindID` is `""` — an empty list.
- `newRuntime` (cmd/relevo/wire.go:161-216) calls `buildRuntime(root, L, true)`, and `buildRuntime` wires a non-nil `MasterMinds` whenever `openGates` (wire.go:385-393), so today's CLI always has a registry: decision 3's refusal is defence-in-depth against a caller that wires a nil registry, and it converts the silent nil resolver into a startup failure.
- Test scaffolding: `runServer`/`runServerWith`/`splitLines`/`decodeResponse` (server_test.go:41,47,70,81); `mastermindRegistryAt` (cmd/relevo/mastermind_test.go:26-39); `internal/e2e/headless_test.go:571` and `internal/mcp/send_builder_test.go:55` build `RelevoVerbs` with a non-empty `MasterMind`, so they stay valid.

## 2. Behaviour, as a closed list

1. A `tools/call` whose `_meta` carries `ai.opencode/sessionID` reaches the verbs with that session; a bare `_meta.sessionID` still works; when both are non-empty, the namespaced key wins.
2. A call with no `_meta` still passes `""` to the verbs — the server never invents an identity.
3. `status` (the only verb that resolves identity) on an opencode server with no session is a tool error (`isError:true`, not a JSON-RPC error): "this call carries no OpenCode session (`_meta ai.opencode/sessionID`); cannot tell which MasterMind it belongs to". Never an empty list.
4. `status` on a server whose identity resolves to `""` (no resolver and an empty process fallback — a Claude server with no record) is a tool error naming the fix: `relevo mastermind init`; it mirrors the tools-only line already printed at `cmd/relevo/mcp.go:103`.
5. A session-named call whose resolver returns `""` with a nil error is also an error ("opencode session <id> resolved to no MasterMind") — an empty identity never becomes a filter.
6. `status` with `all: true` works with no identity, exactly as today: all bindings listed, no `HideDone`, and a `Name` filter still applies.
7. `relevo mcp --kind opencode` with a nil mastermind registry refuses to start: `opencodeSessionMasterMind` returns an error ("opencode tools server has no mastermind registry; cannot resolve tool-call sessions"), `cmdMCP` prints `relevo mcp: <err>` on stderr and exits 2, before the server serves. `opencodeSessionMasterMind` never returns a nil resolver silently.
8. Unchanged: the resolver's not-found text (`answer relevo's consent question…`), a resolver error propagating as the tool error, `status` filtering by a resolved identity, the upgrade notice, tools/list, channel mode, and `send`/`done` — they address a binding by name, so a missing identity cannot flip their result; adding identity-based authorization to them is out of scope (seed decision 2).

Not in scope: the OpenCode plugin (`~/.config/opencode/plugins/relevo/server.ts`) needs no change; the historical `_meta.sessionID` mentions in `docs/specs/2026-09-27-mastermind-consent-design.md:288,384` and the dated plans are records, left untouched.

## 3. Seams

```
internal/mcp/server.go        ~  callMeta (209-212): add a second string field tagged
                                 `ai.opencode/sessionID` and a small picker returning the
                                 namespaced value when non-empty, else the bare one; rewrite
                                 the Meta comment (200-207); pass the picker's value at 221.
internal/mcp/verbs.go         ~  masterMindFor (32-39): resolve, then error whenever the
                                 identity would be "" (three texts of §2.3-2.5); refresh the
                                 Verbs doc (12-15) and RelevoVerbs doc (22-30) to name the
                                 real key and state that send/done address a binding by name.
cmd/relevo/mcp.go             ~  opencodeSessionMasterMind (219-237) -> (func(string)(string,error), error);
                                 mcpVerbs (209-217) -> (*mcp.RelevoVerbs, error); cmdMCP (36-146)
                                 returns exitCodeErr{code: 2} on that error before the srv literal
                                 (106-125, Verbs at 107).
internal/mcp/server_test.go   ~  rewrite TestServerPassesCallSessionFromMeta (408-432); append
                                 TestServerStatusWithoutSessionIsAToolError.
internal/mcp/verbs_test.go    ~  extend TestRelevoVerbsStatusResolvesSession (260-296); append
                                 TestRelevoVerbsStatusWithoutIdentityErrors.
cmd/relevo/mcp_test.go        ~  extend TestOpencodeSessionMasterMind (37-62); append TestMCPVerbs.
docs/plans/2026-09-28-mcp-opencode-session-key.md  NEW  this plan, verbatim.
```

## 4. Cases and where each is pinned

| Case | Test | Pins |
|---|---|---|
| namespaced key reaches the verb | `TestServerPassesCallSessionFromMeta` | `_meta:{"ai.opencode/sessionID":"ses_ns"}` → verb session `ses_ns`; this is the mutation point |
| bare key fallback | same | `_meta:{"sessionID":"ses_bare"}` → `ses_bare` |
| namespaced wins with both keys | same | both present → `ses_ns` |
| no `_meta` | same | verb session `""` |
| opencode call without a session | `TestServerStatusWithoutSessionIsAToolError` | `isError:true`, text names `ai.opencode/sessionID`, body carries no `bindings`; the resolver closure is never called |
| `all:true` without a session | same | `isError` false, JSON lists a seeded binding |
| no identity, no resolver (Claude no record) | `TestRelevoVerbsStatusWithoutIdentityErrors` | error names `relevo mastermind init`; no report returned |
| resolver yields `""` with nil error | same (or the extended session test) | error names the session; never an empty list |
| nil registry refused | `TestOpencodeSessionMasterMind`, `TestMCPVerbs` | resolver is an error, not nil; `mcpVerbs(rt, "opencode", "")` errors naming the registry |
| Claude kind, nil registry | `TestMCPVerbs` | no error, `ResolveSession` nil — tools-only startup unchanged |

Test rules: pure/fixture only — in-memory pipe, `t.TempDir()` store, a temp DB registry through `mastermindRegistryAt`; no harness, no network, no server; cmd/relevo tests stay inside TestMain's isolated HOME/XDG roots.

## 5. Ordered steps

1. `internal/mcp/server.go`: add the namespaced field + picker to `callMeta`, rewrite the `Meta` comment, read the picker at line 221. Verify: `go build ./...` clean.
2. `internal/mcp/server_test.go`: rewrite `TestServerPassesCallSessionFromMeta` as the four-arm table and append `TestServerStatusWithoutSessionIsAToolError` (real `RelevoVerbs`, seeded store). Verify: focused command below, green.
3. `internal/mcp/verbs.go`: make `masterMindFor` error on an empty identity with the §2.3-2.5 texts; update the `Verbs`/`RelevoVerbs` comments. Verify: `go build ./...` clean.
4. `internal/mcp/verbs_test.go`: append `TestRelevoVerbsStatusWithoutIdentityErrors` and add the empty-resolver arm to `TestRelevoVerbsStatusResolvesSession`. Verify: focused command, green.
5. `cmd/relevo/mcp.go`: new signatures for `opencodeSessionMasterMind` and `mcpVerbs`; `cmdMCP` prints the error and returns exit code 2 before building the server. Verify: `go build ./...` clean; `git diff` shows no other change in the file.
6. `cmd/relevo/mcp_test.go`: nil-registry arm in `TestOpencodeSessionMasterMind`; append `TestMCPVerbs`. Verify: focused command, green.
7. Mutations of §6, each reverted before the next. Verify: focused command green after the last revert and `git diff` matches step 6.
8. Full check, once: `make check`, then `make e2e`. If a package's coverage dips more than one point, add the missing assertion — never edit `testdata/coverage-baseline.txt` (cmd/relevo 51.1 at line 2, internal/mcp 78.5 at line 23) or a lint exclusion.
9. Copy this plan (minus the trailing `relevo` status block, which is round metadata) to `docs/plans/2026-09-28-mcp-opencode-session-key.md`, then one commit — `fix(mcp): read the OpenCode session key so tools resolve a MasterMind (#639)` — carrying code, tests and the plan. Do not push.

Focused command throughout: `go test -count=1 -run 'TestServerPassesCallSessionFromMeta|TestServerStatusWithoutSessionIsAToolError|TestRelevoVerbsStatus|TestOpencodeSessionMasterMind|TestMCPVerbs' ./internal/mcp/ ./cmd/relevo/`.

## 6. Mutations

(a) revert the `callMeta` namespaced tag to a bare `sessionID` → `TestServerPassesCallSessionFromMeta` must fail on the namespaced arms; (b) restore `return v.MasterMind, nil` in `masterMindFor` → `TestServerStatusWithoutSessionIsAToolError` and `TestRelevoVerbsStatusWithoutIdentityErrors` must fail; (c) restore the nil return in `opencodeSessionMasterMind` → `TestOpencodeSessionMasterMind`/`TestMCPVerbs` must fail. Each reverted before the next.

## 7. Deleted (closed list)

1. `RelevoVerbs.masterMindFor`'s silent `return v.MasterMind, nil` fallback (verbs.go:34-39) — removed; an empty identity is an error. The `MasterMind` field stays: it is still the Claude fallback when non-empty.
2. `Status`'s silent empty-filter outcome for a missing identity (verbs.go:48-60) — removed; the filter itself still applies to a resolved identity.
3. `opencodeSessionMasterMind`'s nil-return arm (cmd/relevo/mcp.go:223-225) — removed; a nil registry is an error.
4. Nothing else: no file, flag, tool, verb or test is deleted; `callMeta`'s bare `sessionID` field is kept as the fallback; `TestServerPassesCallSessionFromMeta` is rewritten in place; no package moves, so the coverage baseline and the lint exclusions do not move.

## 8. The report must include

- Each case with its test name and the observed text: the tool error naming `ai.opencode/sessionID`, the no-record error naming `relevo mastermind init`, the resolver-empty error, and the startup refusal line with its exit code.
- The mutation results: which named test failed for (a), (b), (c), and that all three were reverted.
- `make check` and `make e2e` results, and that no baseline entry or exclusion was edited.
- `git diff --stat` against `3e938a24` — exactly the paths of §3 — plus the commit sha, subject, and the plan committed.
- The statement of §2.8: send/done keep ignoring the session and why; `TestServerPassesCallSessionFromMeta` fails if the JSON key reverts.
- Confirmations: the opencode plugin, the consent spec and the dated plans are untouched; no cmd/relevo test spawned a harness, reached a network, or read real user state.

## 9. Halt rather than improvise

- If `masterMindFor` has a caller other than `Status`, or `mcpVerbs`/`opencodeSessionMasterMind` have callers other than `cmdMCP` — halt and report.
- If any quoted range does not match the tree, or `callMeta`/`toolCallParams` moved.
- If the namespaced JSON key is rejected by vet or tooling (it is not: a tag name may contain `/`).
- If a cmd/relevo test would need a harness, a network, or the real config/state — halt.
- If covering the new branches would need a baseline edit or a new exclusion — halt.
- If a step can only pass by changing `TestRelevoVerbsStatusResolvesSession`'s existing assertions, the `all: true` behaviour, or send/done — halt: that is a scope change, not a fix.
