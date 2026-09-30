# Burst workers: relevo serve reports its load, and runs the queue on the servers it is handed

**Issue:** #391. It replaces that issue's ask/list/release design, grants table and
reservation arithmetic with one exchange, `burst report`, agreed with the
provider side (fuad-daoud/servers#13, `contabo/burst/`) on 2026-09-26.
**Defers:** #359 (the laptop as hub). **Separate:** #360 (detached client).
**Depends on:** nothing open. The forwarder is built fetch-outside-the-lock
from the start, so it does not wait on #535.
**Amends:**
- `policy.serve` gains `burst`.
- `store.Binding` gains `Burst`, and `store.Endpoint` gains `RemoteNode`
  (BindingFormat 6 → 7).
- `remote.BindingView` gains `Node`.
- `relevo config server key` gains `--enroll-line`.
- `relevo serve status --json` gains `burst`.

## 1. What relevo does, and what it does not

relevo does not manage servers. The `relevo serve` head (contabo):

1. **knows its load:** rounds running, rounds queued, the cap, how long each
   queued round has waited, and the box's cores and memory;
2. **tells an external command that load** (`burst report`) and reads back the
   ready temporary servers ("nodes") the command has for it;
3. **runs queued rounds that do not fit locally on those nodes**, using the
   remote client code it already ships. It collects each result and closes
   the round on the head exactly as a local round closes;
4. **keeps reporting** per-node load, so the provider can decide when a node goes.

The provider owns everything else, and relevo has no concept for any of it:
- whether to add capacity and how much ("waited too long" is its `up_after`);
- sizes, prices, the budget and the vCPU cap;
- creating, seeding and verifying nodes at seed time;
- idle and age teardown, and the dead-man switch.

A node is a plain `relevo serve`. The provider seeds it with the head's
config (via `relevo config export`, then `import`, with `policy.serve.burst`
stripped), the harness logins and TLS, and enrolls the head's client key
(`relevo config server key --enroll-line`). The head is its only client.

**The laptop sees one new thing:** a forwarded round's statusline reads
`opencode@contabo→b12` instead of `opencode@contabo`.

## 2. The provider contract: `burst report` v1 (frozen 2026-09-26)

relevo runs the configured argv, writes one JSON object on stdin, and reads
one JSON object from stdout.
- Exit 0 means stdout is valid. Any other exit, bad JSON, `version` != 1, or
  a 10 s timeout means the call failed.
- Unknown fields are ignored on both sides.
- Times are RFC 3339 UTC. Durations are Go strings.

**stdin** (relevo → provider):
```
{ "version": 1,                                        required
  "at": "<time>",                                      required
  "reason": "interval|forwarded|closed|startup",       optional
  "head": {"name": "contabo", "cpus": 4, "mem_gb": 8}, name required
  "local": {"running": 3, "queued": 5, "cap": 3},      required
  "waiting": [ {"id": "<owner8>/<binding>#<round>", "since": "<time>",
                "candidate": "<token>", "harness": "<kind>"} ],
                          required, [] when none: every round queued and placed nowhere
  "nodes": [ {"id": "b7", "running": 2, "queued": 0, "unacked": 1,
              "forwarded_at": "<time>"|null,
              "problem": "unreachable|fingerprint|key",     optional, omitted when fine
              "rounds": [ {"id": "<owner8>/<binding>#<round>", "candidate": "<token>",
                           "started": "<time>"} ]} ]
                          required, [] when none: EVERY node from the last good response,
                          unused ones as all zeros; an omitted node is "no data", not idle
}
```

**stdout** (provider → relevo):
```
{ "version": 1,
  "nodes": [ {"id": "b7", "name": "b7 cx43@hel1", "url": "https://1.2.3.4:7777",
              "fingerprint": "sha256:<hex>", "cap": 7, "state": "ready|draining",
              "drain_at": "<time>", "kill_at": "<time>"} ],
                          only seeded, provider-verified nodes; a node missing from the list is gone
  "preparing": {"slots": 7, "since": "<time>"} | null,     display and log only
  "denied": {"reason": "budget|cap|no_capacity|failing", "retry_after": "10m"} | null,  display and log only
  "report_every": "30s"
}
```

**Obligations on relevo:**
- **R1.** Never forward to a node whose `state` is `draining`, or once
  `now >= drain_at`, or when `kill_at - now < min_runway` (default 30 m),
  even when working from an older list.
- **R2.** On a failed call, keep the last good list minus nodes past
  `drain_at`, and use only nodes relevo has already verified.
- **R3.** Call every `report_every` while anything is waiting or any node is
  held, otherwise every 5 min. Also call at once (out of cycle) after a round
  is forwarded to, or closes on, a node.
- **R4.** Verify a node yourself before its first forward (§6.2). Report a
  failure as `problem`. `fingerprint` is a security event on the provider's
  side.
- **R5.** A node that disappears, or passes `kill_at`, or stays unreachable
  for `node_lost_after`, has lost its rounds. Re-queue them on the head (§6.5).

**What the provider guarantees:**
- It deletes a node on idle only after marking it `draining`, and only when
  a later report still shows it 0/0/0.
- A node with `unacked > 0` is never deleted on idle.
- Voluntary teardown (idle, silence, release) happens only at the node's
  paid-hour mark, 5 min before its next billed hour starts. Hetzner bills per
  started hour. Broken nodes and `release-all` go at once.
- Quoted from the provider: "`drain_at` and `kill_at` are fixed for a node id
  from its first appearance; `kill_at` is a hard deadline regardless of
  load; `state` may flip ready ↔ draining (idle-drain and its un-drain
  guard) before `drain_at`, and is draining from `drain_at` on."
- Defaults: `drain_at` = created + 1h25m and `kill_at` = created + 1h55m
  (the paid-hour edge). A node deleted at `kill_at` while busy raises the
  provider's `kill_age` error alert.
- **Documented policy:** if contabo's `relevo serve` sends no report for
  15 min, idle nodes are deleted. After 45 min, every node is deleted and
  unacked work is redone.

### 2.1 The second cross-repo contract: `relevo serve status --json`

burst also reads the **worker's** `relevo serve status --json` over ssh, to
confirm a node is idle before it tears the node down. That makes the
document a contract with fuad-daoud/servers.

On 2026-09-26, #603 renamed three keys that burst parses: `builders` →
`runners`, `builder_candidate` → `candidate`, `builder_status` →
`runner_status`. After that, idle teardown never confirmed. burst now
accepts both spellings.

**The keys burst reads:**
- top level: `runners` and `last_contact`;
- per owner: `report.bindings[]`, and on each binding `name`, `round`,
  `state`, `candidate` and `runner_status`.

These are pinned by `TestServeStatusJSONKeysBurstReads` in
`internal/serve/admin_test.go`. The test asserts the literal key names, not
a golden file, because `-update` would regenerate a golden in silence.

Renaming any of these keys needs, in the same PR:
- a burst release that accepts the new spelling first, and
- a change to the test that names servers#13.

### 2.2 The third cross-repo contract: the relevo verbs burst runs on a worker

burst sets up each worker by running relevo verbs on the binary it ships.
On 2026-09-26 a rename of `relevo agent install` to `relevo config agents`
broke setup on real Hetzner nodes, and it cost 7 billed hours. burst now
checks these verbs with `--help` before it creates any server.

**The verbs and flags burst runs:**
- `config agents --force`
- `config import`
- `config export`
- `config server key --enroll-line`
- `serve init --host`
- `serve enroll --key`
- `serve fingerprint`
- `serve status --json`

A relevo test in `cmd/relevo` pins them: it runs each verb with `--help`
in process and asserts it is not removed or unknown and that it offers the
flag. No harness or network is involved. Renaming a verb or flag needs a
burst release that accepts the new spelling first.

## 3. File structure

```
internal/burst/                      contract, placement and report building; no I/O but Exec
  contract.go                        Report, Response, NodeOffer, NodeLoad, ... (§4.1) + Validate
  exec.go                            Provider interface + CommandProvider (argv, stdin/stdout, 10 s)
  placement.go                       Place: the pure forward-or-wait decision (§5.2)
  report.go                          BuildReport: census + node table -> Report (pure)
  cadence.go                         Due: whether a report is due now (pure, R3)
  nodes.go                           NodeTable: merge a Response into held nodes (pure)
  *_test.go                          table tests; testdata/fake-provider.sh
internal/serve/
  burst.go                           Server wiring: reporter loop, node table persistence
  forward.go                         claim -> send -> confirm (§6.3), adopt on restart
  forward_observe.go                 fetch outside the lock, apply under it (§6.4), requeue (§6.5)
  forward_fanout.go                  stop/done/unbind/gate fan-out to nodes (§6.6)
internal/policy/policy.go            ServePolicy.Burst *BurstPolicy
internal/store/binding.go            Binding.Burst *BurstFacts
internal/store/endpoint.go           Endpoint.RemoteNode string
internal/store/format.go             BindingFormat = 7 (+ testdata/binding-shape.golden)
internal/remote/proto.go             BindingView.Node string
internal/relevo/headless.go          reconcileHeadless: a forwarded round is the forwarder's
internal/relevo/served.go            ServedView fills Node
internal/relevo/remote_sync.go       the laptop copies view.Node into Builder.RemoteNode
internal/relevo/status.go            row.Node from Builder.RemoteNode
internal/view/statusline.go          "@server→node" suffix
cmd/relevo/serve.go                  pass L.ClientKey and the burst policy into serve.Config
cmd/relevo/config_server.go          `config server key --enroll-line`
internal/serve/admin.go              StatusJSON.Burst
```

The OpenCode plugin needs no change: it renders the Go-built `harness`
string (`internal/harness/opencodeplugin/tui.tsx`, sidebar line B and the
needs-you dialog).

## 4. Data structures

### 4.1 `internal/burst` contract types (wire, §2)

- **`Report`:**
  - `Version int` (=1)
  - `At time.Time`
  - `Reason string` (one of the four, or empty)
  - `Head HeadFacts{Name string (required, the serve host name), CPUs int, MemGB int}`
  - `Local LocalLoad{Running, Queued, Cap int}` (all ≥ 0)
  - `Waiting []WaitingRound` (never nil)
  - `Nodes []NodeLoad` (never nil)
- **`WaitingRound`:**
  - `ID RoundID`
  - `Since time.Time` (the round's `QueuedAt`)
  - `Candidate string`
  - `Harness string` (the candidate's harness kind)
- **`RoundID`:** a string, `<owner8>/<binding>#<round>`. `owner8` is the
  first 8 characters of the owner's `ClientID.Dir()` form.
- **`NodeLoad`:**
  - `ID string`
  - `Running, Queued, Unacked int`
  - `ForwardedAt *time.Time`
  - `Problem string` (`""|unreachable|fingerprint|key`)
  - `Rounds []NodeRound{ID RoundID, Candidate string, Started time.Time}`
- **`Response`:**
  - `Version int`
  - `Nodes []NodeOffer`
  - `Preparing *Preparing{Slots int, Since time.Time}`
  - `Denied *Denied{Reason string, RetryAfter Duration}`
  - `ReportEvery Duration`
- **`NodeOffer`:**
  - `ID string`: required, matches `^b[0-9]+$`, unique in the response
  - `Name string`
  - `URL string`: required, https
  - `Fingerprint string`: required, `sha256:<64 hex>`
  - `Cap int`: ≥ 1
  - `State string`: `ready|draining`
  - `DrainAt, KillAt time.Time`: required, `KillAt >= DrainAt`
- **`Validate(Response) error`:**
  - It rejects the whole response on any violation. The call counts as failed (R2).
  - `ReportEvery` is clamped to [10 s, 5 min]; a missing value means 30 s.

### 4.2 `internal/burst` node table (persisted by serve)

**`Node`**, one per node the head holds:
- `Offer NodeOffer`: the last offered facts
- `Verified bool`: true once WhoAmI succeeded
- `VerifiedAt time.Time`
- `Problem string`: the last verification failure
- `UnreachableSince time.Time`: zero while reachable
- `ForwardedAt time.Time`: the last forward to it
- `Gone bool`: missing from the last good response, or past `KillAt`. A gone
  node is kept only until every round on it is re-queued, then dropped.

**`NodeTable`:**
- `Nodes map[string]Node`
- `LastReportAt time.Time`
- `LastGoodAt time.Time`
- `LastErr string`
- `Preparing *Preparing` and `Denied *Denied`: from the last good response
- `ReportEvery time.Duration`

It is persisted as one JSON value in the serve DB kv, key
`serve.burst.nodes`, so it survives a daemon restart.

### 4.3 `store.BurstFacts` (on the head's binding; nil everywhere else)

- **`Forward *Forward`:** non-nil while the current round is on a node.
  - `Node string`: the node id
  - `NodeBinding string`: `<owner8>-<name>`, the binding's name on the node
  - `Round int`: the head round being forwarded (== `b.Round` while open)
  - `Phase string`: `sending|running|closed`
    - `sending`: the head claimed the round; the node has not confirmed.
    - `running`: the node accepted it.
    - `closed`: the node closed it and the head has not finished applying it.
  - `ClaimedAt, SentAt time.Time`
  - `QueuedSince time.Time`: the original `QueuedAt`, restored on re-queue
  - `StopRequested bool`: `relevo stop` arrived and the forwarder owes the node a Stop
- **`Shipped map[string]string`:** node id → the last out SHA shipped to that
  node. It is the per-node `LastShipped`. An `ErrSinceUnknown` falls back to
  a full bundle, as `sendRemote` does.
- **`NodeBindings []string`:** node ids that hold a binding for this head
  binding. `done` and `unbind` fan out to these.
- **`LocalOnly bool`:** set when a round was lost to a node's age kill. That
  round runs on the head only, which is the ping-pong guard. It is cleared
  when the round closes.

Adding `Burst` to `Binding` and `RemoteNode` to `Endpoint` bumps
`BindingFormat` from 6 to 7. `internal/store/testdata/binding-shape.golden`
is regenerated with `-update`.

### 4.4 Other additions

- **`policy.ServePolicy.Burst *BurstPolicy`:**
  - `Command []string` (json `command`): the provider argv. Empty or nil
    means bursting is off.
  - `NodeLostAfter *Duration` (json `node_lost_after`): default 10 m.
  - `MinRunway *Duration` (json `min_runway`): default 30 m. A round is
    forwarded only to a node with at least this long left before
    `kill_at` (R1).
- **`remote.BindingView.Node string`** (json `node,omitempty`): the node id
  while the round is forwarded. Additive; old clients ignore it.
- **`store.Endpoint.RemoteNode string`** (json `remote_node,omitempty`): the
  laptop's copy of `view.Node`, cleared when the view has none.
- **`view.BindingStatus.Node string`:** filled from `Builder.RemoteNode`.
- **`serve.Config` gains:**
  - `ClientKey []byte`: the head's client key, from `L.ClientKey`
  - `Burst *policy.BurstPolicy`
  - `Provider burst.Provider`: injectable for tests; nil means built from
    `Burst.Command`
- **`serve.StatusJSON.Burst *BurstStatus` (json `burst,omitempty`):**
  - `LastReportAt`, `LastErr`
  - `Preparing`, `Denied`
  - `Nodes []{ID, Name, State, Verified, Problem, Running, Cap, DrainAt, KillAt}`

## 5. Interfaces

### 5.1 `burst.Provider`
Single responsibility: one exchange with the external command.

```
interface Provider {
  Report(ctx, Report) -> (Response, error)
}
```
- Errors: `ErrProviderExit{Code, Stderr tail}`, `ErrProviderTimeout`,
  `ErrProviderOutput{cause}` (bad JSON, `version` != 1, or failed `Validate`).
- Precondition: the Report has `Version` 1 and non-nil slices.
- Postcondition: a returned Response has passed `Validate`.
- `CommandProvider{Argv []string, Timeout (10 s)}`: the process runs in its
  own process group, and a timeout kills the group.

### 5.2 `burst.Place` (pure)
Single responsibility: decide, for each waiting round, whether it stays
queued or goes to a node.

```
Place(now, minRunway, localFree int, waiting []Candidate, nodes []Node, load map[nodeID]NodeLoad)
  -> []Placement{Round RoundID, Node string}      // only rounds that go to a node
```
- **A node is eligible when all of these hold:**
  - `Verified`
  - `!Gone`
  - `Problem == ""`
  - `State == ready`
  - `now < DrainAt`
  - `KillAt - now >= minRunway`
  - `free = Cap - Running - Queued > 0`
- **Order:**
  - Waiting rounds are taken FIFO.
  - The first `localFree` rounds are skipped: they start locally, because
    local capacity always comes first.
  - A round with `LocalOnly` is never placed.
  - Each remaining round goes to the eligible node with the latest `DrainAt`,
    then the most free seats, then the lowest id. That node's free count then
    drops by one.
- It never places more rounds than the free seats. It never re-picks the candidate.

### 5.3 `burst.BuildReport` (pure)
```
BuildReport(now, reason, head HeadFacts, local LocalLoad, waiting []WaitingRound,
            table NodeTable, forwards []ForwardFact) -> Report
```
- Per node, `Running`, `Queued` and `Unacked` are counted from the head's
  `Forward` records:
  - `Phase sending|running` counts as running, or as queued when the node's
    view is `queued`.
  - `Phase closed` counts as unacked.
- Every non-gone node in the table appears, including all-zero ones.

### 5.4 `burst.Due` (pure)
```
Due(now, table, anyWaiting, anyNodes bool, outOfCycle bool) -> bool
```
This is R3: interval = `ReportEvery` while anything is waiting or any node is
held, else 5 min; an out-of-cycle call is always due. The one exception is a
failed previous call: then the next call waits a full interval, and an
out-of-cycle trigger does not bring it forward.

### 5.5 `burst.NodeTable.Merge` (pure)
```
Merge(now, Response) -> (NodeTable, []Event)
```
- A new id is added unverified. For a known id, the offer is updated;
  `Verified` survives unless `URL` or `Fingerprint` changed.
- An id missing from the response is marked `Gone`.
- An `Offer.KillAt <= now` is marked `Gone`.
- Events: `node_added`, `node_draining`, `node_gone`, `denied`, `preparing`.
  They become log lines and hooks.

### 5.6 Serve-side contracts (package `serve`, unexported)
| method | responsibility | lock |
|---|---|---|
| `reportCycle(ctx)` | gather facts under `s.mu`, call the provider without the lock, then merge and persist the table under `s.mu` | split |
| `verifyNodes(ctx)` | WhoAmI to every unverified node without the lock, then record the result under the lock | split |
| `placeForwards()` | run `Place` over the census and claim the placements (`Phase=sending`) | under `s.mu` |
| `sendForwards(ctx)` | for every `sending` forward: create the node binding if missing, then StartRound | none, then under the lock to confirm |
| `observeForwards(ctx)` | GetBinding and, on close, fetch files and bundle into temp; apply under the lock | split |
| `requeueLost()` | a forward on a gone or lost node goes back to queued | under `s.mu` |
| `fanOut(ctx, op)` | stop, unbind, unavailable and available to nodes, best effort | none |

Every network call runs without `s.mu`. A fetched fact is applied only if
the binding still has the same `Forward` (node, round, phase). This is the
`remoteFetch.matches` rule, and it prevents a stale apply.

## 6. Flows (pseudocode)

### 6.1 The serve loop
```
Run loop, every interval:
  Tick(ctx)                                  # as today: owners, collectSettled, prune, admit
  if burst enabled:
     requeueLost()                           # under s.mu
     placeForwards()                         # under s.mu, after admit, so local seats fill first
     sendForwards(ctx)                       # network
     observeForwards(ctx)                    # network
     verifyNodes(ctx)                        # network
     if Due(...): reportCycle(ctx)           # network (the provider exec)
```
The burst steps run in the same goroutine as Tick, after it and never under
its lock. A slow node or provider therefore delays the next tick; it never
holds `s.mu` for the HTTP handlers. Each network step has its own per-call
timeout: 10 s for the provider, the client's default for nodes.

**Startup:**
1. Load the table from the kv.
2. Run `adoptSending()`: every `Phase=sending` forward is resolved by
   GetBinding on its node binding.
   - The node's round == `Forward.Round` and it is open → `running`.
   - Anything else → put back to queued.
3. Send a `reason=startup` report.

### 6.2 Verifying a node
```
for node in table where !Verified and !Gone:
  view, err := client(node).WhoAmI()          # TLS pinned to node.Fingerprint, head key signs
  classify err:
    TLS pin mismatch          -> Problem=fingerprint, log + hook burst_security
    401/403 (key not enrolled) -> Problem=key
    dial/timeout/5xx          -> Problem=unreachable (retried next cycle)
  missing features (idempotent_send, candidate, tier, queue; A4 names) -> Problem=key, logged with the missing names
                                                            (the provider seeds the head's build, so this
                                                            means a mis-seeded node)
  ok -> Verified=true, Problem=""
```
The head's client for nodes is built per call from the table:
`remote.Servers{node.ID: {URL, Fingerprint}}`, `serve.Config.ClientKey`.

If bursting is on but there is no client key, serve logs one error at
startup ("burst: no client key; run relevo config server key") and bursting
stays off.

### 6.3 Forwarding a round
```
placeForwards (under s.mu):
  local free = cap - census.Running             # after admit ran
  for p in Place(...):
     b := load(p.round)
     b.Burst.Forward = {Node, NodeBinding: owner8+"-"+b.Name, Round: b.Round,
                        Phase: sending, ClaimedAt: now, QueuedSince: b.QueuedAt}
     b.QueuedAt = zero                           # admit will not start it locally
     log "forwarded to <node>" ; save

sendForwards (no lock):
  for f in forwards where Phase == sending:
     if node binding missing on node: CreateBinding(node, NodeBinding, repo facts of b)
     since := b.Burst.Shipped[node]
     snap := transport.Snapshot(bare repo, out ref, since)   # ErrSinceUnknown -> since ""
     view, err := StartRound(node, NodeBinding, f.Round, plan, bundle, tier, b.BuilderCandidate,
                             tags, idempotent=true)
     under s.mu, if Forward still matches:
        ok or 409 CodeRoundStarted -> Phase=running, SentAt=now, Shipped[node]=snap head,
                                     add node to NodeBindings, table.ForwardedAt=now
                                     mark report due (reason=forwarded)
        definite refusal (4xx)     -> requeue(b, "forward refused by <node>: <msg>")
        network error              -> leave Phase=sending; the next cycle retries idempotently,
                                      and node_lost_after bounds it (6.5)
```
- The candidate and tier are the head's: the node never re-picks.
- The plan text is the head's saved plan for that round.
- A forwarded round has no local process and does not count in
  `census().Running`. Local seats are for local builders only.
- `reconcileHeadless` (`internal/relevo/headless.go`) returns the binding
  unchanged when `b.Burst != nil && b.Burst.Forward != nil`. The check goes
  right after the queued-round guard, before `escapeCheck` and `markerClose`.
  The forwarder owns that round, and the owner daemon must never see it as a
  failed spawn or a lost process.

### 6.4 Observing and closing a forwarded round
```
observeForwards (no lock):
  for f in forwards where Phase in {running, closed}:
     view, err := GetBinding(node, NodeBinding)
     err -> UnreachableSince = first failure time; continue
     view.RoundState:
       queued / running -> cache view.Live for the head's ServedLive
       closed           -> fetch report, diff, log, stream, and the round bundle into temp
       needs_you        -> fetch whatever report exists
  under s.mu, per fetched forward still matching:
     running  -> nothing
     closed   -> absorb the bundle into b.Serve.BareRepo:
                   refs/heads/<branch> = view.ResultCommit
                   refs/relevo/<name>/round-<N> = view.DirtyCommit when set
                 write NNN-report / diff / log / stream into the head store for round N
                 close the round through the head's normal served-close path
                   (report entry, Round++, Serve.ClosedRound / ResultCommit / DirtyCommit)
                 Forward = nil
                 mark report due (reason=closed)
     needs_you -> the head binding becomes NEEDS YOU with the node's halt reason
                  (and its report if any); Forward = nil
  after the head's save (no lock): Ack(node, NodeBinding, N); an ack failure is retried
     next cycle and keeps the node's unacked count > 0
```
- **Invariant:** the head never closes a round it has not fetched.
- The node binding is kept after the ack, so the next forward of the same
  binding to the same node ships incrementally. It is released by done or
  unbind (§6.6), or when the node goes.
- **The worktree:** the head's worktree for that binding is not touched at
  close. The next local round's start syncs it to the branch, as
  `syncRoundWorktree` does for every round.

### 6.5 Losing a node
```
requeueLost (under s.mu):
  for f in forwards:
    lost := node Gone
         or now >= node.KillAt
         or UnreachableSince set and now - UnreachableSince >= node_lost_after
    if lost:
       b.QueuedAt = f.QueuedSince                  # keeps its FIFO place
       b.Burst.LocalOnly = now >= node.DrainAt     # lost at the end of the node's life (age kill):
                                                   # the round ran past min_runway once already, so
                                                   # re-forwarding it could lose it again
       b.Burst.Forward = nil
       log "re-queued (node lost: <id>, <why>)" ; hook burst_node_lost
  drop gone nodes that no forward references
```
- A late result from a lost node is never applied, because the Forward no
  longer matches.
- Re-queuing is not a builder switch. It does not count against the
  candidate and writes no RoundExcluded.

### 6.6 Fan-out (best effort, no lock)
| head event | to the node |
|---|---|
| `relevo stop` on a forwarded round | Stop(node, NodeBinding). The round then closes through §6.4 with the node's stopped report. `handleStop` sets `StopRequested` under the lock and the forwarder sends it. |
| `done` / `unbind` of the head binding | Unbind(node, NodeBinding) for every id in `NodeBindings`. A failure is logged; the node's release cleans it up. The head's own done or unbind never waits on a node. |
| `unavailable` (per binding or server-wide) | POST `/v1/unavailable` to every verified live node |
| `available` | Available(subject) to every verified live node |

### 6.7 The laptop and the statusline
- The head's `ServedView` sets `Node = b.Burst.Forward.Node` while
  `Phase != sending`.
- While a round is forwarded, the head's `ServedLive` returns the cached
  node `Live` view.
- The laptop's `applyRemoteView` copies `view.Node` into
  `b.Builder.RemoteNode`, and clears it on an empty value.
- `status.go` sets `row.Node`.
- `statusLineRowOf` builds the suffix as `"@" + Server` and appends
  `"→" + Node` when `Node` is set. For example: `opencode@contabo→b12`.
- `relevo status` (text) shows `on opencode@contabo→b12` through the same
  `Harness` string.

## 7. Error handling

| failure | category | relevo does |
|---|---|---|
| provider exit ≠ 0, timeout, bad output | recoverable | keep the last list minus nodes past `drain_at` (R2); log kind `burst`; hook `burst_failed`; retry at the next interval |
| `denied` in the response | informational | log it and show it in `serve status`; no retry logic in relevo |
| node fingerprint mismatch | security | `Problem=fingerprint`, never forwarded to; hook `burst_security`; reported to the provider |
| node key refused, missing features | recoverable | `Problem=key`; reported; re-verified if the offer's URL or fingerprint changes |
| node unreachable | recoverable until `node_lost_after` | the node is kept, and forwards stop only after it is lost (§6.5) |
| StartRound refused (4xx) | recoverable | the round goes back to the queue with the node's message in the log |
| StartRound network error | recoverable | stays `sending`; retried idempotently; §6.5 bounds it |
| round `needs_you` on a node | handed to the human | the head binding goes NEEDS YOU with the node's reason |
| ack failure | recoverable | retried each cycle; the node stays unacked > 0 |
| no client key while bursting is configured | configuration | one startup error; bursting off |

**Observability:**
- Log kind `burst`, one line per report (reason, waiting, the nodes'
  running/cap, and the outcome).
- Hooks: `burst_failed`, `burst_node_added`, `burst_node_draining`,
  `burst_node_lost`, `burst_security`, `burst_forwarded`, `burst_closed`.
- Every forward, close and re-queue is a binding log entry, so
  `relevo show` tells the story.

## 8. Testing (CI: no harness binary, no network)

- **Pure table tests in `internal/burst`:**
  - `Validate`
  - `Place`: FIFO, local first, draining, `drain_at`, `min_runway`,
    `LocalOnly`, latest-`drain_at` preference, and seat counting
  - `BuildReport`: all-zero nodes included, unacked counting
  - `Due`: interval, heartbeat, out of cycle, and the backoff after a failure
  - `Merge`: added, gone, `kill_at`, and a URL change that clears `Verified`
- **`CommandProvider` against `testdata/fake-provider.sh`:** ok, exit 1,
  sleep past the timeout, bad JSON, version 2.
- **`internal/serve`:**
  - forward, observe and requeue against an in-process worker `serve.Server`
    over `httptest` TLS, with the fake runner, so there is no network
  - the stale-apply guard
  - `adoptSending` after a simulated restart
- **`make e2e`:** a head and a worker in process with the fake harness. A
  round is forwarded, closed, fetched and delivered to a client, whose
  branch holds the commit.
- **Mutation tests:**
  - drop the `now < DrainAt` term from `Place`, and a named test fails;
  - drop the `min_runway` term from `Place`, and a named test fails;
  - drop the `Forward` match check before apply, and a named test fails.

## 9. Scope boundary

- **Not built:**
  - a grants table, reservations, or ask/list/release;
  - an `up_after` knob in relevo;
  - static-node config: a static worker is a three-line provider script
    that echoes a fixed node;
  - moving a round already running on a node;
  - per-round pinning;
  - forwarding from the laptop (#359);
  - multi-round detached runs (#360).
- **A node never bursts:** its config has no `serve.burst`. The provider
  strips it, and the head does not check.
- **No change to the OpenCode plugin.**

## 10. Landing order

1. **Contract and pure core:**
   - `internal/burst` (types, `Validate`, `Place`, `BuildReport`, `Due`,
     `Merge`, `CommandProvider`)
   - `policy.serve.burst`
   - `config server key --enroll-line`
2. **Forwarding:**
   - `BurstFacts` and `RemoteNode`, with BindingFormat 7
   - `serve.Config.ClientKey`
   - the reporter loop and node table
   - verify, forward, observe, close, requeue, adopt
   - the reconcileHeadless guard
   - in-process head and worker tests
3. **Surfaces and fan-out:**
   - `BindingView.Node`, the laptop's `RemoteNode`, and the statusline
   - `serve status` burst block
   - stop, done, unbind and gate fan-out
   - `make e2e`
4. **Live on contabo.** Not a code round. First point
   `policy.serve.burst.command` at `["/home/fuad/.local/bin/burst-sim","report"]`.
   That is the provider's `sim` cloud:
   - Its nodes are real `relevo serve` workers on 127.0.0.1:7801-7804.
   - Caps are 1-2. Timers are short: a 10-min "billing hour", mark at 8m, drain_at +13m, kill_at +18m.
     Set `min_runway` to 5m, the sim's drain-to-kill gap, or no
     round is ever forwarded.
   - It costs nothing, and `srv.fish burst sim on|off` switches it.

   Once drain, idle teardown, the silence rule and a lost node have all been
   seen working, switch to `["/home/fuad/.local/bin/burst","report"]`.
   `burst release-all` tears every node down.
