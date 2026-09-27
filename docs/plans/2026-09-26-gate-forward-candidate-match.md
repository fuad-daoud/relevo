# gate forward: resolve the forwarded candidate the way the server knows it (2026-09-26)

## 1. Overview

`relevo gate <token>` records the gate in this machine's own ledger and forwards it to every server a
remote binding names. The forward sends **this machine's** canonical token; the server resolves that
token against **its own** candidate set. The two machines can label the same quota group differently --
contabo listed `agy/antigravity/claude-sonnet-4-6` where the laptop had `agy/agy-extra/claude-sonnet-4-6`
-- and the server's exact-token lookup then misses. The forward prints one `unknown candidate` line per
remote binding, and the gate the operator asked for never reaches the server, which keeps running the
gated provider.

The fix is client-side, so it reaches a server that was never upgraded: before posting, the client asks
each server for its candidate list (`GET /v1/candidates`) and resolves the forwarded candidate against
*that* list -- the same token, else the same provider (a rate-limit gate is per provider), else the
candidate's name, else, only on a server that sends no name, the same harness and model (a provider
renamed between the machines). The server's own token is what travels. When no rule matches, the forward
prints **one line per server** naming the candidate, its provider and what the server does have, and
posts nothing.

The server is not changed: `internal/serve` and the wire types in `internal/remote` keep their routes,
fields and error text, so an un-upgraded server is fixed by the client the operator runs.

## 2. Facts, verified against 0c8da537 (origin/main)

**F1.** `internal/relevo/remote_gates.go:18-51` -- `ForwardUnavailable` forwards the client's token
verbatim, once per open-round remote binding, with no candidate lookup at all:

```go
		if err := rt.Remote.Unavailable(ctx, b.Builder.Server, b.Name, token, reason); err != nil {
			lines = append(lines, fmt.Sprintf("%s: %s: %v", b.Name, b.Builder.Server, err))
		}
```

**F2.** `cmd/relevo/gate.go:96-103` -- where that token comes from: the *local* resolve.

```go
	// The argument may be a candidate name or a token. It is resolved here,
	// first, so the ledger and every server the bindings name see the
	// canonical token, never the raw argument (A1 §4.2).
	c, err := rt.Candidates.Resolve(token)
	if err != nil {
		return err
	}
	canonical := c.Ref().String()
```

**F3.** `internal/serve/bindings.go:539` is the server's only resolve of the forwarded token:

```go
	if _, err := availability.Unavailable(relevo.AvailabilityDeps(rt), req.Token, time.Time{}, req.Reason); err != nil {
```

`internal/availability/gates.go:154-158` runs `d.Candidates.Resolve(token)`, and
`internal/candidate/candidate.go:100-106` (`Set.Lookup`) is exact-token:

```go
	c, ok := s.byRef[ref.String()]
	if !ok {
		return Candidate{}, fmt.Errorf("candidate %q not found (configured: %s): %w", ref.String(), strings.Join(s.Names(), ", "), ErrUnknownCandidate)
	}
```

That `configured: <names>` tail is where the issue's "contabo lists a candidate named
claude-sonnet-4-6" comes from: `Set.Names()` is per-entry `Name`, not per token.

**F4.** `internal/serve/candidates.go:12-48` -- `GET /v1/candidates` lists exactly that set
(`s.cfg.Candidates`), one `remote.CandidateView` per candidate:

```go
			views = append(views, remote.CandidateView{
				Token: token,
				Name:  c.Name,
				Kind:  c.Harness,
				Gated: gatedMap[token],
				Pick:  token == pickedToken && pickedToken != "",
			})
```

`internal/serve/serve.go:221` hands the same pointer (`Candidates: s.cfg.Candidates`) to
`handleUnavailable`'s runtime, so the list a client reads is authoritative for what the handler will
resolve. `internal/remote/proto.go:247-253` is the view; `Name` is `omitempty` and a pre-`Name` server
sends only `Token`.

**F5.** `internal/remote/client/bindings.go:75-79` and `:101-117` are the two client calls this plan
uses (`Candidates`, `Available`) plus `Unavailable` (`:101-109`). Both gate routes and the candidates
route shipped in the same first serve commit (b087ff67, `internal/serve/routes.go` lines 39, 50-51 of
that commit), so a server that can take a forwarded gate can be asked for its candidates.

**F6.** `internal/relevo/remote_gates.go:61-106` -- `ForwardAvailable` maps a name to this machine's
token (`:69-73`) and never to the server's, so a clear of a renamed candidate fails the same way, one
line per server. The clear path's server side accepts more than the set path does:
`internal/availability/available.go:41-78` (`ResolveClearSubject`) still clears a token whose provider
the ledger gates, or a bare provider a candidate uses.

**F7.** `internal/candidate/names.go:23-49` -- `DeriveNames` derives a name from the model (and reserves
providers first), which is why both machines call that candidate `claude-sonnet-4-6` while their
providers differ.

**F8.** `internal/relevo/remote_test.go:51-96` (`fakeRemote`, one `candidatesResp` for every server),
`:103-106` (`Candidates` records `Candidates:<server>` and returns that one response), `:180-188`
(`Unavailable`/`Available` record `Unavailable:<server>:<name>:<token>` / `Available:<server>:<subject>`);
the three tests that call the forwards are at `:2316` (`TestForwardUnavailable`), `:2370`
(`TestForwardAvailablePostsToEveryServerOnce`) and `:5510` (`TestForwardAvailableResolvesNameToToken`).

## 3. Files (anything else: halt and report)

```
internal/relevo/remote_gates.go   the four helpers and the two rewrites
internal/relevo/remote_test.go    one fixture change (TestForwardUnavailable) + five new tests
docs/plans/2026-09-26-gate-forward-candidate-match.md   this plan, written by the last step
```

`go.mod`/`go.sum` are untouched (no new dependency). `internal/serve`, `internal/remote`,
`internal/availability`, `internal/candidate` and `cmd/relevo` are **not** touched.

## 4. Contracts

All four helpers are unexported and live in `internal/relevo/remote_gates.go`, placed immediately above
`ForwardUnavailable`'s doc comment (line 12), below the imports. That file gains one import,
`"github.com/fuad-daoud/relevo/internal/candidate"`; it keeps `context`, `fmt`, `sort`, `remote`,
`store`.

### 4.1 `clientCandidateToken`

```go
// clientCandidateToken resolves subject -- a candidate name or a canonical token
// -- to this machine's token and the candidate's short name. A subject this
// machine does not configure comes back unchanged, name "", ok false: a bare
// provider must still travel as itself.
func clientCandidateToken(rt Runtime, subject string) (token, name string, ok bool)
```

- `rt.Candidates` may be nil: `(*candidate.Set).Resolve` has a nil branch and returns an error
  (`internal/candidate/candidate.go:110-112`), so `ok` is false and `token == subject`.
- Postcondition: `ok` implies `token == c.Ref().String()` and `name == c.Name` for the resolved
  candidate; `!ok` implies `token == subject` and `name == ""`.
- No error return; an unresolvable subject is a fact, not a failure.

### 4.2 `serverCandidate`

```go
// serverCandidate maps a client candidate -- its token, and its name when this
// machine knows one -- to the token a server resolves it by, from that server's
// own /v1/candidates list: the same set its handlers resolve against. Rules, in
// order: the same token; a candidate on the same provider, the same model
// first (a rate-limit gate is per provider); a candidate the server names the
// same; a view the server sent no name for with the same harness and model, a
// provider renamed between the two machines. Pure.
func serverCandidate(views []remote.CandidateView, token, name string) (string, bool)
```

Ordered rules, each scanning `views` in the server's order (its own `Refs()` order, so deterministic):

1. `v.Token == token` -> `v.Token`. No candidate set needed; a token the server lists is always right.
2. Parse `token` as a candidate ref (`candidate.ParseRef`). On success, over views whose own token
   parses: the first with the same `Provider` **and** the same `Model`; failing that, the first with the
   same `Provider`. A gate is per provider, so the provider is the stronger identity than the name.
3. `name != ""` and `v.Name == name` -> `v.Token`: the same candidate, under whatever provider the
   server gives it.
4. Parse `token` again; the first view with `v.Name == ""` (a server too old to send names), the same
   `Harness` and the same `Model` -> `v.Token`. A view the server *did* name and that we did not match is
   a different candidate: never guessed over.

Postcondition: a returned token is one of `views[i].Token`; `false` when no rule fires. Pure and
deterministic: same inputs, same view.

### 4.3 `serverTokenFor`

```go
// serverTokenFor fetches server's candidate list and maps token to the token
// that server resolves it by (serverCandidate's rules). err is set when the list
// itself could not be read; srv is "" then. A readable list with no match
// leaves srv "" and fills miss with the one line to report instead of posting.
func serverTokenFor(ctx context.Context, rt Runtime, server, token, name string) (srv, miss string, err error)
```

- Calls `rt.Remote.Candidates(ctx, server)` exactly once. Precondition: `rt.Remote != nil` (the caller
  checks).
- Exactly one of the three outcomes: `err != nil` (with `srv == ""`, `miss == ""`); `srv == ""` and
  `miss` the mismatch line; `srv != ""` and `miss == ""`.
- `miss` is `mismatchLine(server, token, views)`.

### 4.4 `mismatchLine` and `candidateOptions`

```go
// mismatchLine is the one line a forward reports when a server lists no
// candidate the client's candidate maps to: the token, its provider, and what
// the server does have.
func mismatchLine(server, token string, views []remote.CandidateView) string

// candidateOptions renders servers' candidates for that line: "name (token)"
// when the server names one, "token" otherwise, comma separated; "none" for an
// empty list.
func candidateOptions(views []remote.CandidateView) string
```

- `mismatchLine` renders
  `<server>: candidate "<token>" (provider <provider>) not on the server (available: <candidateOptions>)`,
  dropping the ` (provider <provider>)` clause when `candidate.ParseRef(token)` fails.
- `candidateOptions` mirrors the wording `internal/relevo/remote_add.go:167-174` already prints for
  `add --server`, and is not shared with it: that message is a different sentence and is pinned by its
  own test.

### 4.5 `ForwardUnavailable` (signature unchanged)

```go
func ForwardUnavailable(ctx context.Context, rt Runtime, token, reason string) []string
```

- Calls: `rt.Store.List` once; `rt.Store.ReadLog` once per remote binding (unchanged); then, per distinct
  server that has an open-round remote binding, sorted: `rt.Remote.Candidates` **at most once**, and
  `rt.Remote.Unavailable` once per open-round binding on that server whose candidate resolved.
- A server whose list cannot be read contributes exactly one `"<server>: list candidates: <err>"` line
  and no post. A server with no match contributes exactly one `mismatchLine` and no post -- this replaces
  today's one error per binding.
- When the resolved token differs from the forwarded one, one line
  `"<server>: gated <server token> for <client token>"` precedes the posts: the mapping is announced, not
  silent.
- Never fails the caller: every failure is a returned line, and the local ledger gate stands either way.

### 4.6 `ForwardAvailable` (signature unchanged)

```go
func ForwardAvailable(ctx context.Context, rt Runtime, subject string) []string
```

- `clientCandidateToken` resolves the subject. When it is **not** this machine's candidate (a bare
  provider), no candidate list is fetched and the subject travels unchanged, exactly as today.
- When it is, `serverTokenFor` is called once per distinct server. A readable list with a match sends the
  server's token; a list that cannot be read, or one with no match, sends this machine's token -- the
  server then decides, because `ResolveClearSubject` (`internal/availability/available.go:41-78`) still
  clears a provider the ledger gates but no configured candidate uses. The clear path adds no line for a
  mapping: the server's own answer already names the provider it cleared.
- One `rt.Remote.Available` per distinct server, sorted, and one answer line per server (unchanged).

### 4.7 Compatibility

- **New client, older server.** Works, and needs no server upgrade: the client speaks only
  `GET /v1/candidates` and the two gate routes (F5). A server that predates `CandidateView.Name` sends no
  names, and rule 4 still maps the renamed provider from the token itself. A server older than
  `POST /v1/available` fails a clear exactly as it does today, with one line per server.
- **Old client, newer server.** Unchanged from today, and this plan changes nothing for it: there is no
  wire change and no handler change (`internal/remote/proto.go`, `internal/serve` are untouched). An old
  client sends its own token; the newer server resolves it by exact token and prints the same per-binding
  `unknown candidate` error. Fixing an old client would take a server-side guess at a provider from a
  model, which could gate a quota group the operator never named -- deliberately not done.
- **New client, older client's data.** No state, config or DB schema changes: the mapping is computed per
  call from the server's live list.

## 5. Pseudocode

```
ForwardUnavailable(ctx, rt, token, reason):
    if rt.Remote == nil: return nil
    bindings, err := rt.Store.List();  err -> [ "list bindings: err" ]

    token, name, _ := clientCandidateToken(rt, token)      # a name becomes the token

    open := []
    for b in bindings:
        if !b.Builder.Remote(): continue
        entries, err := rt.Store.ReadLog(b.Name)
        if err != nil: lines += "<b.Name>: read log: err"; continue        # unchanged
        if HasEntry(entries, b.Round, DirToBuilder, KindPlan)
           and !HasEntry(entries, b.Round, DirToPlanner, KindReport):
            open += b

    for server in sortedDistinctServers(open):
        srv, miss, err := serverTokenFor(ctx, rt, server, token, name)
        if err != nil: lines += "<server>: list candidates: err"; continue
        if srv == "": lines += miss; continue
        if srv != token: lines += "<server>: gated <srv> for <token>"
        for b in open where b.Builder.Server == server:
            err := rt.Remote.Unavailable(ctx, server, b.Name, srv, reason)
            if err != nil: lines += "<b.Name>: <server>: err"              # unchanged
    return lines


ForwardAvailable(ctx, rt, subject):
    if rt.Remote == nil: return nil
    token, name, isCandidate := clientCandidateToken(rt, subject)
    servers := sortedDistinctServers(all remote bindings)                    # unchanged
    for server in servers:
        send := token
        if isCandidate:
            srv, _, err := serverTokenFor(ctx, rt, server, token, name)
            if err == nil and srv != "": send = srv
        resp, err := rt.Remote.Available(ctx, server, send)
        ... unchanged: one line per server, naming resp.Provider and resp.Removed
    return lines


serverTokenFor(ctx, rt, server, token, name):
    resp, err := rt.Remote.Candidates(ctx, server)
    if err != nil: return "", "", err
    srv, ok := serverCandidate(resp.Candidates, token, name)
    if !ok: return "", mismatchLine(server, token, resp.Candidates), nil
    return srv, "", nil


serverCandidate(views, token, name):
    for v in views: if v.Token == token: return v.Token, true          # 1
    ref, rerr := candidate.ParseRef(token)
    if rerr == nil:
        for v in views: if sameProvider(v) and sameModel(v): return v.Token, true
        for v in views: if sameProvider(v): return v.Token, true        # 2
    if name != "":
        for v in views: if v.Name == name: return v.Token, true         # 3
    if rerr == nil:
        for v in views: if v.Name == "" and sameHarness(v) and sameModel(v): return v.Token, true  # 4
    return "", false
```

## 6. Error handling

- `ForwardUnavailable`'s contract is unchanged: it never returns an error. Every failure is a line.
- List failure: one line per server, `"<server>: list candidates: <err>"`, no post for that server. The
  old code would have posted and printed one server error per binding; the new line is emitted once.
- No match: one `mismatchLine`, no post. The line carries the client token, its provider and the server's
  candidates, so the operator can see the mismatch (the issue's alternative remedy) and rename on either
  side.
- Post failure: unchanged per-binding line `"<binding>: <server>: <err>"` -- those are genuine per-binding
  failures (`not_found`, auth, transport) and stay per binding.
- `ForwardAvailable` keeps its one answer line per server. It never turns a map miss into a refusal: an
  unmapped subject still reaches the server, which owns the decision to clear or refuse.
- `cmd/relevo/gate.go` and `internal/ui/actions.go` are unchanged: both already print the returned lines.

## 7. Tests

All new tests live in `internal/relevo/remote_test.go`, next to `TestForwardUnavailable`. Fixtures use
invented names and the existing helpers (`candidateSet`, `remoteBinding`, `fakeRemote`).

New:

1. `TestServerCandidateMatchesInOrder` -- table over `serverCandidate`, each case pinning one rule and
   the order: same token; same provider and model; same provider, another model; the same name under
   another provider (provider rule must have missed); an unnamed view with the same harness and model
   (the pre-`Name` server); a *named* view with the same harness and model but another name (must **not**
   match); nothing in common.
2. `TestForwardUnavailableSendsTheServerTokenForTheSameName` -- client set
   `{"harness":"opencode","provider":"laptop-group","model":"test-model-1","roles":["builder"]}` (name
   `test-model-1`); one open-round remote binding on `zen`; `fakeRemote` views
   `[{Token: "opencode/server-group/test-model-1", Name: "test-model-1"}]`. Asserts exactly one
   `Unavailable:zen:open-remote:opencode/server-group/test-model-1` call, and that the returned lines are
   `zen: gated opencode/server-group/test-model-1 for opencode/laptop-group/test-model-1`.
3. `TestForwardUnavailableSendsTheServerTokenForARenamedProvider` -- same, but the view carries no `Name`
   (a server predating the field), so rule 4 carries it.
4. `TestForwardUnavailableReportsOneLinePerServerWhenNothingMatches` -- two open-round bindings on one
   server; views `[{Token: "opencode/other-group/other-model", Name: "other-model"}]`; asserts exactly
   one returned line, that it names the client token, its provider and the server's candidate, and that
   no `Unavailable:` call was made.
5. `TestForwardAvailableSendsTheServerTokenForTheSameName` -- the clear mirror: asserts the one call is
   `Available:zen:opencode/server-group/test-model-1`.

Changed existing test:

- `TestForwardUnavailable` (`remote_test.go:2340`): the fake is `&fakeRemote{}` with no candidate list,
  so under the new code its token would not map and its one expected call would vanish. Give it
  `candidatesResp: remote.CandidatesResponse{Candidates: []remote.CandidateView{{Token: "some-token"}}}`.
  Its assertion -- exactly one call, and only for the open round -- is unchanged, and it now also pins
  rule 1.
- `TestForwardAvailablePostsToEveryServerOnce` (`:2370`) and `TestForwardAvailableResolvesNameToToken`
  (`:5510`) must pass **untouched**: the first's runtime has no `Candidates` (a pass-through), the
  second's fake lists no candidates (a map miss that falls back to the client's token). They pin the
  clear path's fallback.

Mutation test (step 3): comment out the rule-4 loop in `serverCandidate`, run
`go test ./internal/relevo -run TestForwardUnavailableSendsTheServerTokenForARenamedProvider -count=1`,
confirm it **fails**, restore the loop, re-run and confirm it passes. Name that test in the report.

## 8. Working efficiently

- Read once, at the locations this plan names: the whole of `internal/relevo/remote_gates.go` (133
  lines), and `internal/relevo/remote_test.go` lines 2316-2362, 2400-2435, 5505-5535 (the three forward
  tests). Do not re-find what section 2 and 3 locate.
- Four helpers and two rewrites are two files: write each file's changes in one edit call. Insert the
  helpers in one block above `ForwardUnavailable`; append the new tests in one block after
  `TestForwardUnavailable`.
- Focused build and test: `go test ./internal/relevo -run 'TestServerCandidate|TestForward' -count=1`
  after each step, fixing every reported error before the next run.
- Full check, once, in step 3: `make check`. It is the strict one -- it adds `gofmt -l .` over every
  tracked `.go` file, `go vet`, golangci-lint, `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh`,
  a `go mod tidy` comparison and `go test -race -cover ./...` plus the coverage baseline. `gofmt -l .` and
  `sh scripts/check-comments.sh` are cheap re-runs if a comment or a tab slips.
- New comments say why, and never cite an issue number, a section or history; functions stay under 70
  lines and the file under 600.
- `internal/relevo`'s coverage baseline is 86.2 (`testdata/coverage-baseline.txt`) and a drop of more than
  one point fails `make check`. If it fails, add the test for the uncovered branch; never lower the
  baseline, add a lint/size/coverage exclusion, or edit the baseline.
- No new `cmd/relevo` test: nothing in `cmd/relevo` changes, no harness is spawned and no network is
  reached from that package.

## 9. Steps

**Step 1 -- the helpers and their unit test.**
Edit `internal/relevo/remote_gates.go`: add the `candidate` import and the four helpers (4.1-4.4) in one
block above `ForwardUnavailable`. Add `TestServerCandidateMatchesInOrder` (test 1) to
`internal/relevo/remote_test.go`.
Verify: `gofmt -l internal/relevo` prints nothing; `go test ./internal/relevo -run TestServerCandidate -count=1`
passes.

**Step 2 -- `ForwardUnavailable` and its tests.**
Rewrite `ForwardUnavailable` per 4.5 and section 5. Change the `fakeRemote` fixture in
`TestForwardUnavailable`, and add tests 2, 3 and 4.
Verify: `go test ./internal/relevo -run TestForwardUnavailable -count=1` passes (four tests: the existing
one plus the three new); `go test ./internal/relevo -run TestForwardAvailable -count=1` still passes.

**Step 3 -- `ForwardAvailable`, then the mutation test and the full check.**
Rewrite `ForwardAvailable` per 4.6, add test 5. Then the mutation test of section 7. Then `make check`.
Verify: the mutation step fails before the restore and passes after; `make check` exits 0; `gofmt -l .`
and `sh scripts/check-comments.sh` print nothing failing.

**Step 4 -- the plan and the commit.**
Write this plan's body, verbatim, to `docs/plans/2026-09-26-gate-forward-candidate-match.md`: from the
`# ` title through section 10, without any runner status footer. Then one commit:

```
git add -A && git commit -m "fix(gate): the forward resolves the candidate the way the server knows it"
```

Verify: `git show --stat HEAD` names exactly the three files of section 3; `git status --short` is clean.

## 10. Stop rather than improvise

Halt and report if any of these happens:

- a location above is not where this plan says it is (a function, a line range, a fake field);
- `RemoteClient` has no `Candidates` (`internal/relevo/runtime.go:406-425`) or `fakeRemote` has no
  `candidatesResp` (`internal/relevo/remote_test.go:56,103-106`);
- either `TestForwardAvailablePostsToEveryServerOnce` or `TestForwardAvailableResolvesNameToToken` fails
  after step 3: this plan says they must pass untouched;
- `make check` reports a coverage failure in `internal/relevo` that one added test cannot fix;
- a step is impossible as written.

Tell the report what you could not do; never bend a test to fit. A halt that finds a planning error is
worth more than a green suite.
