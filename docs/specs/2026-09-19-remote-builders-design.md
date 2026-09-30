# Remote builders: a relay server that hosts headless builders for enrolled clients

**Issue:** #100 (remote half of #6). Supersedes the issue's "ssh runner"
framing: clients have no shell on the server, so relay is the network
service, the identity and the file channel.
**Depends on:** nothing open. Builds on headless builders (#99), headless
recovery (#113/`2026-09-13-headless-recovery`), headless transcript (#168),
round commit facts (#130), done-releases-worktree (#179), structured report
tail (#198).
**Consumed by (later):** #203 (builders party: consent-based sharing keeps
the authz check in one function); #204 (OS-level tenant isolation on the
server); #142's cost surfaces gain an owner dimension.
**Surface freeze:** `servers`, `client *` and `serve *` are new verbs. #114
step 1 froze the verb list until ~2026-10-12. This lands after the freeze
lifts; implementation may start on a branch before then.
**Status:** implemented 2026-09-19 in five PRs (#206 `internal/remote`, #207
server core, #208 `relay serve`, #210 client, #212 surfaces) plus the
in-process end-to-end in `internal/e2e`. Plans: `docs/plans/2026-09-19-remote-builders-*.md`.
Amendments made during implementation are marked **[amended]** below.

## 1. System overview

Every builder relay runs today lives on the planner's machine: a pane in
the same herdr, or a headless process in a worktree under the same state
dir. #99 left one seam for "somewhere else" -- `Runner` -- and #100's
original reading was to implement it over ssh. That reading assumes the
client has a shell on the server and that the server has no relay state.
Neither holds for the case this design serves: **several people, none of
whom have ssh to the box, sharing one machine's harness logins and
compute for their builders.**

So relay becomes a peer on both sides. `relay serve` on the server owns
the builder half of a binding: the repo, the worktree, the headless
process, the round files, the result commits. The client keeps the
planner half: the planner pane, the local binding, the pending-delivery
log, a mirror of the round files, and a local branch the results land on.
The two talk over HTTPS with ed25519-signed requests; git bundles carry
the tree. A client picks a server per binding (`relay add --server zen`),
and one client may use several servers.

Principles kept:

- **The report file is the contract.** The server closes a round exactly
  as a headless binding does today; the client's `queueReport`, delivery,
  `pull`, `wait` and statusline are unchanged.
- **relay never commits on the builder's branch.** Uncommitted work at
  round close travels on a side ref, not as a commit on `relay/<name>`.
- **relay never touches the human's working tree.** The client's only git
  writes are `fetch` into `refs/heads/relay/<name>` and
  `refs/relay/<name>/*`.
- **The server never loses a round; the client never fakes one.** A closed
  round waits on the server until the owner acknowledges it. A send that
  did not reach the server records nothing.
- **Identity is a key.** No IP, hostname or uid anywhere. The client id is
  the fingerprint of a key the client generated; the label is what the
  admin called it at enrollment.
- **One authz function.** `allowed(caller, verb, binding)`; every network
  handler calls it; the server-local CLI never does.

### Scope boundary

- Headless builders only on the server. No remote pane builders, no herdr
  on the server, ever.
- `add`-shaped bindings only. `relay bind --server` (work in the planner's
  tree) is refused; `fork` across servers is refused.
- No consults over the wire (`relay ask` stays local); no `answer` (headless
  takes no dialogs).
- Server credentials are shared: the admin logs the harnesses in once;
  `candidates.json`/`policy.json` live on the server. Usage is recorded per
  owner; a client-facing usage view is a later slice.
- Owner-only over the network. No client sees another's bindings (#203
  relaxes this by consent). Admin = whoever can run `relay` on the server
  host; no admin role over the network.
- git-bundle transfer only. A git-agnostic tree sync is a second content
  type on the same seam (§4.1), not a v1 feature.
- Full history on the first send. `--seed-from <url>` (server clones origin
  once) is a follow-up for repos where history size is the problem.
- OS-level isolation between tenants' builders is out of scope (§2.5).

## 2. Design

### 2.1 Topology

```
laptop (client)                          zen (server)
-----------------------------            ---------------------------------------
planner pane (herdr)                     relay serve --listen :7777
relay daemon  --- HTTPS, signed ------>  server daemon: headless rounds
client store                             server store: all owners' bindings
  <name>/binding.json  Mode=remote         serve/bindings/<owner-hex>/<name>/binding.json
  <name>/NNN-{plan,report,diff,log}        serve/bindings/<owner-hex>/<name>/NNN-{plan,report,diff,log,stream}
  repo: branch relay/<name>                serve/repos/<owner-hex>/<repo_id>.git
                                           serve/bindings/<owner-hex>/.worktrees/<name>
```

- **Client config:** `~/.config/relay/servers.json` (`{"zen": {"url":
  "https://zen:7777", "fingerprint": "sha256:..."}}`) and
  `~/.config/relay/client.key` / `client.pub`. Paths are composed through
  `userConfigRoot()`. No server configured = today's behaviour.
- **Server:** `relay serve` on the same binary. State under the normal
  `$XDG_STATE_HOME/relay` plus `serve/`. Candidates and policy are the
  ordinary config files on that host. **[amended]** Each owner is a
  separate `store.Store` rooted at `serve/bindings/<owner-hex>/`, where
  `<owner-hex>` is the client id's digest as 64 hex characters
  (`ClientID.Dir()`): the `SHA256:<base64>` form contains `/`. The
  worktree is that store's own `WorktreePath(name)`, so `done`, `unbind`
  and `gc` work unchanged on a server binding.
- **Client binding:** `relay add --name api --server zen [--base <ref>]
  [--builder <token>]`. `Builder.Mode = remote`, `Builder.Server = "zen"`.
  No local worktree; the local branch `relay/api` is created at `--base`
  (default HEAD) and is where results land.
- **Server binding:** `Owner = <client id>`, same name as the client's.
  Names are unique per owner, so two clients may both have `api`. No
  planner endpoint; every planner-facing step is skipped on the server.
- **Multiple servers:** every server-touching verb takes the server from
  the binding after `add`, never from a flag. `relay servers` lists the
  configured servers and whether each answers.

### 2.2 A round, end to end

1. `relay send --name api --file plan.md` on the client: stage the plan
   locally as today; `Snapshot(LastShipped..relay/api)` produces a bundle;
   `POST /v1/bindings/api/rounds` carries plan, bundle and round number.
   The server stages the plan, absorbs the bundle (fast-forward of its
   `relay/api`), ensures the worktree, and runs the existing `startRound`.
   The client records `RoundStartedAt`, `LastShipped`, state ACTIVE.
2. The server daemon ticks the round exactly as headless today: drain
   stream, watch marker/report/exit, halt on timeout/cap/exit-without-
   report/limit. On close it additionally records commit facts, writes a
   side ref if the tree is dirty (§4.3), and marks the round `closed` with
   `result_commit` and `dirty_commit`.
3. The client daemon ticks: `GET /v1/bindings/api` -> round `closed`,
   newer than `LastKnown` -> `catchUp`: download report, diff, log into
   `Dir(name)`; `Absorb` the result bundle into local `relay/api` (and the
   side ref); set `LastKnown`; `POST .../ack`; then the existing
   `queueReport` -> pending entry -> delivery to the planner pane ->
   `pull`/`wait`, all unchanged.
4. Laptop asleep for days: step 3 happens later. At most one closed round
   ever waits (the planner must read round N to send N+1), so catch-up is
   a single fetch, never a replay. Every client verb that reads a remote
   binding syncs first, so `status`/`pull` work without the daemon.

### 2.3 Identity and enrollment

- `relay client init` writes `client.key` (ed25519, 0600) and `client.pub`;
  refuses to overwrite; prints the **client id** (`SHA256:<base64>` of the
  public key, ssh's form) and the public-key line. Losing the key means
  re-enrolling; the server keeps the old id's bindings until the admin
  gc's them.
- `relay serve enroll --label alice@laptop --key "ed25519 AAAA..."`
  appends `{id, label, pubkey, enrolled_at}` to `serve/clients.json`.
  `relay serve clients` lists; `relay serve revoke <id>` sets `revoked_at`
  (bindings stay, the key stops working); re-enrolling clears it.
  **[amended]** A running server re-reads `clients.json` whenever the
  file's mtime or size changes, so enroll and revoke take effect without
  a restart (found by the end-to-end test, plan 4b).
- Nothing over the network can enroll. The enrollment channel is "the
  admin pastes a public key".

### 2.4 Request signing and transport

- Headers: `Relay-Client: <id>`, `Relay-Audience: <audience>`,
  `Relay-Timestamp: <unix seconds>`,
  `Relay-Nonce: <16 random bytes, base64>`, `Relay-Signature: <base64
  ed25519 signature>` over the canonical string
  `audience "\n" method "\n" path "\n" timestamp "\n" nonce "\n" hex(sha256(body))`.
  **[amended]** `audience` binds the signature to the server identity the
  client verified itself: the pinned fingerprint, or `host:<lower-case host>`
  in CA and insecure mode. The server accepts a `host:` audience only when it
  was started with `--public-host <that host>`, and always accepts its own
  certificate's fingerprint. A server with an empty audience set refuses every
  request.
- Server checks, in order: id enrolled and not revoked; signature verifies
  against the stored key; `|now - timestamp| <= 5m`; nonce unseen within
  the window (in-memory set with expiry; a restart forgets nonces and the
  timestamp window bounds replay to five minutes of an idempotent API).
  Failure is `401` with exactly one of `unknown client`, `revoked`, `bad
  signature`, `stale or replayed`; the client prints it verbatim.
- TLS is for confidentiality, not authentication. `relay serve init`
  generates `server.key` and a self-signed cert; `relay serve fingerprint`
  prints it; the client pins it at `relay client add-server zen
  https://zen:7777 --fingerprint sha256:...`. A changed cert is a hard
  refusal (`server certificate changed; re-add with the new fingerprint if
  you expected this`). `--ca system` accepts a real cert instead of a pin.
  `http://` is refused unless `--insecure`, and then every tick logs it.

### 2.5 Authorization and threat model

```
allowed(caller ClientID, verb Verb, b ServerBinding) bool
    verb == VerbCreate  -> true
    otherwise           -> b.Owner == caller
```

Every handler calls it before touching the store; `false` is `404`, never
`403` -- a tenant must not learn that a name exists under another owner.
The server-local CLI reads the store directly and never calls it. #203
extends this one function with a grants lookup and nothing else, which is
why it takes the whole binding.

Stated plainly: this protects tenants from each other over the wire and
from a passive network. It does **not** protect tenants from the server
admin (who runs their code and reads their plans), nor from each other at
the OS level: every builder runs as the same unix user on the server, and
a plan that says `cat ../other-owner/...` succeeds. v1's answer is
per-owner directories and an admin who trusts the tenants they enrol.
Isolation by unix user or container is #204.

## 3. Wire protocol

JSON control plane, raw bytes for files and bundles, no streaming
subscriptions in v1 (the client polls on its tick). All routes under `/v1`;
a version mismatch is `426` with the versions the server speaks.

| Method / path | Purpose |
|---|---|
| `GET /v1/whoami` | `{id, label, server_version, transports: ["git-bundle"]}` |
| `GET /v1/candidates` | The server's candidate list and policy pick, read-only |
| `POST /v1/bindings` | `{name, repo_id, base_commit, candidate?, round_cap?, round_timeout_ms?}` -> `201` binding view; `409` if `(owner, name)` exists |
| `GET /v1/bindings` | Owner-scoped list |
| `GET /v1/bindings/{name}` | Binding view (§3.1) |
| `POST /v1/bindings/{name}/rounds` | Multipart `plan` (text), `bundle` (octet-stream), `round` (int). `409 round_open` if running; `409 round_started` if that round already started (client compares plan hash: same -> success); `422 not_fast_forward` |
| `GET /v1/bindings/{name}/rounds/{n}/files/{report\|diff\|log\|plan}` | Raw bytes; `404` until closed except `log`, readable while open |
| `GET /v1/bindings/{name}/rounds/{n}/bundle?since=<sha>` | `git bundle create <since>..<result> [<dirty>]`, streamed; empty `since` = full |
| `POST /v1/bindings/{name}/rounds/{n}/ack` | `acked_round = n`; idempotent |
| `POST /v1/bindings/{name}/unavailable` | `{token, reason}`; gates the server's candidate server-wide and switches mid-round |
| `POST /v1/bindings/{name}/done` / `unbind` | #179 semantics / drop worktree; `409 round_open` unless `?force` |
| `POST /v1/bindings/{name}/resume` | Restore a released worktree from the branch |

Not endpoints, on purpose: `answer`, `ask`, `fork`, `tab`, anything that
lists across owners.

### 3.1 Binding view

```
{
  name, state, round,
  round_state:    idle | running | closed | needs_you,
  halt:           string (needs_you only),
  result_commit:  sha (closed only),
  dirty_commit:   sha or "" (closed only),
  report_outcome: structured tail per #198, parsed server-side,
  acked_round:    int,
  closed_round:   int,                                   [amended] the round result_commit belongs to
  diff_note, diff_commits, diff_tree:                    [amended] the server's diff entry at close, so
                                                         the client writes its diff entry without a baseline
  candidate:      token,
  round_started_at, round_cap, round_timeout_ms
}
```

### 3.2 Errors

`{"error": "<code>", "message": "<sentence>"}`. Codes are a closed set:
`not_enrolled`, `revoked`, `bad_signature`, `stale`, `not_found`,
`round_open`, `round_started`, `not_fast_forward`, `no_runner`,
`spawn_failed`, `too_large`, `version`, **[amended]** `invalid` (400: a
malformed create -- bad name, missing repo id, base commit not 40 hex; also
409 for a duplicate name). The client prints `<server>:
<message>`, never a raw status.

### 3.3 Sizes

Bundles and logs stream; nothing is buffered whole. The server caps a
request body at `max_bundle_bytes` (default 512 MB) and answers `413`
naming the cap.

## 4. Git transfer

### 4.1 The seam

```
TreeTransport
  Snapshot(ctx, repo string, refs []string, since string) -> (Snapshot{ContentType, Body, Heads map[ref]sha, Empty}, err)
  Absorb(ctx, repo, contentType string, body io.Reader, refs []string) -> (moved map[ref]sha, err)
```

`refs` are full ref names; the inbound snapshot carries the branch and,
when present, the round's side ref in one body. A body carrying a ref the
caller did not name is refused (`ErrUnexpectedRef`); every named ref is
fast-forward only.

`git-bundle` (`application/x-git-bundle`) is the only content type in v1.
A tree sync later is a second content type on the same two calls and the
same two endpoints; `/v1/whoami` advertises what the server accepts.

### 4.2 Repo identity and layout

`repo_id = hex(sha256(root commit sha))` -- the first commit, stable across
clones and remote URLs, no secrets. The server keeps one bare repo per
`(owner, repo_id)`; no object of one tenant's is reachable from another's
repo. Worktrees at `serve/worktrees/<owner>/<name>`, added from the bare
repo at the fast-forwarded branch on the first send, reused across rounds,
released on `done`, removed on `unbind`.

### 4.3 Outbound, close, inbound

- **[amended] The outbound ref.** git refuses to fetch into a branch a
  worktree has checked out, and the server's worktree holds
  `relay/<name>`. So the client points `refs/relay/<name>/out` at its
  branch head and bundles that; the server absorbs it into the bare repo
  and fast-forwards the worktree with `merge --ff-only` (`422
  not_fast_forward` when it cannot; a dirty-tree conflict is the same code
  with its own message). Inbound is unchanged: the client never has
  `relay/<name>` checked out.
- **First send:** `Snapshot(since="")` bundles the full history of
  `base_commit`. **Later sends:** `since = LastShipped`; usually an empty
  bundle since the planner does not edit `relay/<name>`. Server `Absorb`:
  `git bundle verify` -> `git fetch <bundle> refs/heads/relay/<name>` ->
  must fast-forward, else `422`.
- **Round close on the server:** commit facts as today. Branch commits are
  already on `refs/heads/relay/<name>`. If the tree is dirty, write the
  worktree's tree object as a side commit on `refs/relay/<name>/round-<N>`
  (parent = branch head, author `relay`, message `[relay] <name>: round N,
  uncommitted work`). The branch is untouched; the worktree stays dirty
  for the next round, as locally.
- **Inbound:** `Snapshot(since=LastKnown, ref=result ∪ dirty)` -> one
  bundle -> client `Absorb`: `git fetch` into `refs/heads/relay/<name>`
  (fast-forward guaranteed: only the server writes that history) and,
  when present, `refs/relay/<name>/round-<N>`. The `Diff:` line the
  planner receives gains `uncommitted work at relay/<name>/round-N`.
  `ack` is sent only after both fetches succeeded.
- If `relay/<name>` is checked out locally, git refuses the fetch; the
  client reports `checkout another branch, then relay pull` and retries
  each tick.

## 5. Client side

### 5.1 Store

- `ModeRemote Mode = "remote"`; `Endpoint.Remote() bool`.
- Builder `Endpoint` gains `Server string`, `LastShipped string`,
  `LastKnown string`. Process fields stay zero.
- `Binding.Branch`/`Base` (from #130) are reused. `CWD` is the repo root;
  no worktree path. `Dir(name)` holds the mirrored round files, so `ui`,
  `status`, `show`, `tab` read them unchanged.
- `Binding.RemoteUnreachableSince time.Time` for the outage log dedup.

### 5.2 Runtime

`Runtime.Remote RemoteClient` -- one method per §3 endpoint, constructed in
`cmd/relay` from `servers.json` + `client.key`. Nil in tests unless a
`fakeRemote` is wired, mirroring `Runner`.

### 5.3 Reconcile

A third branch where `reconcile.go` splits on `Headless()`:

```
if b.Builder.Remote():
    view, err := rt.Remote.Get(server, name)
    unreachable          -> log once per outage; no state change
                            (halt only past round timeout + grace:
                             "zen unreachable for 3h; round may still be running there")
    401 revoked/unknown  -> haltBinding("zen: <reason>"), NEEDS YOU
    404                  -> haltBinding("zen: binding removed by the server admin")
    running              -> mirror log tail into NNN-builder.log (terminal tab); nothing else;
                            local checkRoundTimeout is skipped for remote
    needs_you            -> haltBinding(view.halt) unless already
    closed, acked_round < round
                         -> catchUp (files -> bundle since LastKnown -> LastKnown -> ack -> queueReport)
    closed, acked_round == round
                         -> fall through to deliverAndSettle
```

`catchUp` is idempotent and ordered; a failure at any step leaves the
binding as it was and retries next tick. After 10 consecutive absorb
failures: `NEEDS YOU: cannot absorb round N from zen: <err>`.

### 5.4 Send

After staging the plan locally: `Snapshot(LastShipped..Branch)` ->
`Remote.StartRound(plan, bundle, round)`. `201`: record `LastShipped`,
`RoundStartedAt`, ACTIVE, `send` log entry. `409 round_started` with the
same plan hash: success. `422`: refuse with the pull hint. Unreachable:
refuse with nothing recorded.

### 5.5 Verb matrix

| verb | remote binding |
|---|---|
| `add --server zen [--builder tok] [--base ref]` | validates `tok` against `/v1/candidates`; `POST /v1/bindings` first, then creates local `relay/<name>` at base; a failure after the server agreed removes the server binding and the branch. **[amended]** Remote bindings are exempt from the one-binding-per-CWD rule and are never resolved by cwd: several per repo, always addressed by `--name` |
| `bind --server`; `fork` from/to remote | refused: `remote builders are add-only`; `fork across servers is not supported` |
| `send`, `status`, `ui`, `show`, `log`, `wait`, `pull` | work; reads sync first |
| `unavailable <token>` | forwarded; local ledger untouched |
| `answer`, `ask` | refused, headless wording |
| `done`, `unbind` | forward first; on failure the local binding is left as it was and the verb says why |
| `bind --resume --name x` | `POST .../resume`, then ACTIVE locally; `--rebind` refused in v1 |
| `gc` | archives the local dir only after the server confirms DONE/unbound |
| `doctor` | per server: reachable / enrolled as `<label>` / cert pinned / `not enrolled: paste this key to the admin: ...` |
| `servers` (new) | the same table alone |
| `client init` / `client add-server` / `client rm-server` (new) | key and `servers.json` |

UI/status: builder column `zen:agy/...`; terminal tab tails the mirrored
log; unreachable server shows `zen ?` in the row; changed cert shows
`zen !cert`.

## 6. Server side

### 6.1 Process

`relay serve --listen [addr]:7777 [--state dir]`: the HTTP listener and
the server daemon in one process, sharing one `Runtime` with `Herdr` nil.
Runs under systemd; `docs/relay-serve.service` ships the unit.

### 6.2 Store and daemon

The ordinary `store.Store` rooted at `serve/bindings`, names keyed
`<owner>/<name>`; `Binding.Owner` stamped at create; `List()` walks two
levels. Round files, log, ledger, history, archive: existing code.

The tick is today's `Daemon.Tick` with `agents = nil` and a server flag
that skips planner-side steps (`deliverAndSettle`, held delivery,
`PendingForPlanner`) and, after `queueReport`, runs `closeRemoteRound`
(§4.3). Halts set `RoundState = needs_you` with `Halt` text via the same
`haltBinding`, the herdr notification replaced by a server log line;
`HaltNotifiedRound` dedup kept.

### 6.3 Concurrency

One store lock serialises ticks and CLI verbs; HTTP handlers take it per
request. Bundle absorb and emit run outside the lock with the lock
re-taken to record the result (the consults spec's "ask outside lock"
discipline).

### 6.4 Server-local CLI

| verb | behaviour |
|---|---|
| `relay serve init` | `server.key`, cert; prints fingerprint; refuses to overwrite |
| `relay serve enroll --label L --key K` / `clients` / `revoke <id>` | `clients.json` |
| `relay serve fingerprint` | for clients to pin |
| `relay serve status` **[amended: under `serve`]** | every owner's bindings grouped under the owner's label; the single-store verbs are not multi-owner |
| `relay serve log/show/tab --owner L <name>` | follow-up, not shipped |
| `relay unavailable <token>` | gates the server's candidate; same code, same ledger |
| `relay serve unbind --owner <label or id> <name> [--force]` **[amended]** | admin ends anyone's binding (archived); refused while a round runs unless `--force`; the owner's client halts with `binding removed by the server admin` on its next tick |
| `relay serve gc --abandoned 30d` | archives bindings whose owner made no signed request in N days; branch and files kept |
| `relay doctor` | plus listener, cert expiry, enrolled clients, `serve/` writable, harness logins |

Usage (#142) records gain `Owner`; `relay tab --by owner` on the server.

What the server refuses to be: a planner host (no `send` from the box for
an owned binding), a git host for anything but relay's branches, a pane
host.

## 7. Failure modes

Rule: the server never loses a round, the client never fakes one, each
side names the other by its label.

| Seam | Failure | Server | Client |
|---|---|---|---|
| Send | server unreachable | -- | `zen unreachable: <err>`; nothing recorded |
| Send | response lost after `201` | round running | resend -> `409 round_started`, same hash -> success; different -> `round N is already running on zen with a different plan` |
| Send | not fast-forward | `422` | `relay/api on zen has moved past your copy; relay pull first` |
| Send | spawn failed | ledger `spawn_failed`, `needs_you` | `NEEDS YOU: zen: <candidate> could not be launched` |
| Send | body over cap | `413` | `bundle is N MB; zen accepts M MB` |
| Round | exit without report | exit entry, `needs_you` | mirrored halt |
| Round | timeout | server `checkRoundTimeout` | mirrored halt; client's own timeout skipped |
| Round | rate-limited | `unavailable` forwarded -> switch mid-round | `switched to <next>` on next tick |
| Round | `relay serve` restarted | processes are detached; reattached by pid+start time; dead -> exit-without-report | nothing |
| Catch-up | client dies before ack | nothing acked | re-download; every step idempotent |
| Catch-up | `relay/api` checked out | -- | `checkout another branch, then relay pull`; retried, not a halt |
| Catch-up | absorb fails | round stays closed, unacked | retried; after 10: NEEDS YOU |
| Auth | revoked / unknown | `401` | NEEDS YOU once |
| Auth | clock skew | `401 stale` | `check this machine's clock`; log line, not a halt |
| Auth | cert changed | -- | hard refuse; `zen !cert` |
| Server | binding removed by admin | `404` | NEEDS YOU; local files kept |
| Server | down for a day | rounds' processes keep running; reattached on restart | `zen ?`; logged once; no halt |
| Server | disk full at close | `needs_you: cannot record round` | mirrored halt |
| Both | version mismatch | `426` | `zen speaks v1; this relay needs v2`; logged once |

Unreachable is never NEEDS YOU on the client until it has persisted past
the round timeout plus a grace.

## 8. File structure

```
cmd/relay/
  serve.go             relay serve {init,enroll,clients,revoke,fingerprint,gc} and the listener entry
  client.go            relay client {init,add-server,rm-server}, relay servers

internal/remote/       shared by both sides
  auth.go              canonical string, sign, verify, nonce window, ClientID
  proto.go             request/response types, error codes, version
  transport.go         TreeTransport interface
  bundle.go            git-bundle TreeTransport (Snapshot/Absorb over internal/git)

internal/remote/client/
  client.go            RemoteClient: one method per endpoint, signed, pinned TLS
  config.go            servers.json, client.key/pub via userConfigRoot()

internal/serve/
  server.go            HTTP handlers, allowed(), owner scoping
  clients.go           clients.json (enroll/revoke/list)
  tls.go               server.key/cert generation, fingerprint
  round.go             closeRemoteRound: commit facts, side ref, RoundState
  serve_test.go        httptest over a temp store with fakeRunner

internal/store/
  types.go             ModeRemote; Endpoint.Server/LastShipped/LastKnown; Binding.Owner/RemoteUnreachableSince

internal/relay/
  remote.go            reconcile remote branch, catchUp, send remote branch
  remote_test.go       every row of §7's client column, against fakeRemote

internal/git/
  client.go            + Bundle, FetchBundle, CommitTree (side ref), RootCommit

docs/relay-serve.service
```

## 9. Testing

- **Pure:** `allowed`; sign/verify round-trip with mutation targets (flip a
  body byte -> `bad_signature`; stale timestamp -> `stale`; reused nonce ->
  `stale`); `repo_id`; the reconcile remote branch against `fakeRemote`,
  one named test per §7 client row.
- **Handlers:** `httptest` over a temp store with `fakeRunner`; every
  endpoint's success and every error code. Owner scoping: B's `GET
  /v1/bindings/api` where `api` is A's -> `404`; removing the `allowed`
  call must fail a named test.
- **Git:** real git in a temp dir with `-c commit.gpgsign=false`: full
  bundle first send, incremental second, non-ff refusal, dirty tree ->
  side ref round-trips and the branch head is unchanged.
- **e2e, local-only:** client and `relay serve` on 127.0.0.1 with a
  scripted harness: `client init` -> `serve enroll` -> `add --server` ->
  `send` -> close -> catch-up -> delivery. Then: kill the client daemon
  before close, restart, assert one delivery and `acked_round == 1`.
- **CI:** no test in `cmd/relay` reaches herdr; the serve tests never
  need it.
