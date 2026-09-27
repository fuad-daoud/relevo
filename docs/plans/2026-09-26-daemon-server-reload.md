# A server added while the daemon runs is used without a restart (issue #615)

Facts below are verified against `0c8da53745d44aa9e64050adea3b10862b961c92` (the round's base,
`origin/main`). Every step names the file, function and line range it changes. Any file not named
here is out of scope: if a step seems to need one, halt and report instead of editing it.

## 1. System overview

The client daemon builds its remote client exactly once, from the config `servers` section and the
stored client key, while the runtime is constructed:

`cmd/relevo/wire.go:335`
```go
	remoteClient, transport, err := newRemoteClient(L.Servers, L.ClientKey, gitClient)
```

Its per-tick reload does not touch that section, and says so:

`internal/relevo/reload.go:26-28`
```go
// ConfigWatcher reloads the candidates, policy and roles sections when the
// config store's version changes. Prices, servers and hooks keep today's
// "loaded once at start" behaviour.
```

So `relevo config server add zen ...` writes the section, bumps the config version
(`db.Tx.ConfigPut` calls `bumpConfigVersion`, `internal/db/config.go:47-60`), and the running
daemon never sees it. Every tick's `GetBinding` answers `ErrUnknownServer`
(`internal/remote/client/client.go:126-129`), `applyRemoteErr` falls through to the generic warn

`internal/relevo/remote_sync.go:245-246`
```go
	slog.Warn("remote get binding failed", "server", server, "binding", name, "err", err)
	return b, false, nil
```

and the binding keeps the `RemoteStatus` it had, so `relevo status` keeps reporting a round the
server has already halted and `relevo wait` times out. Restarting the daemon is the only cure.
The premise holds on this base; nothing is fixed already.

The fix is the issue's first option: the watcher reloads `servers` (and the client key) with the
sections it already reloads, and installs a client rebuilt from them when they changed. Two
supporting changes are required, both small:

- `Runtime.Transport` must stop depending on the `servers` section. It never really did — it is
  `remote.NewBundleTransport(gitClient, "")`, a function of the git client alone
  (`internal/remote/bundle.go:21-26`) — but today it is built and dropped together with the client,
  so a daemon that started with no servers has a nil transport, and the first closed round on a
  newly added server would panic in catch-up at `rt.Transport.Absorb`
  (`internal/relevo/remotefetch.go:532`). A nil-transport panic is worse than the bug being fixed,
  so this is part of the fix, not a cleanup.
- A binding whose server is not in this machine's config must stop reading as a running round.
  A reload can fail (a servers section whose key does not parse, an unreadable store), and the
  watcher then keeps the installed client — leaving exactly the silent "still running" the issue
  describes. This is the issue's floor and it is cheap.

Out of scope, deliberately: `relevo config server add|rm` gets no "restart the daemon" notice,
because after this change a restart is never needed; `ReloadConfig` (`internal/relevo/configedit.go:466`,
the cockpit's reload) is not extended — the cockpit builds its runtime per process and re-reads
config when the store version moves (`internal/ui/live.go:42-63`), and it is not the daemon the
issue reports; prices and hooks stay loaded once at start.

## 2. File structure

```
internal/relevo/
  remoteclient.go        NEW  ErrNoClientKey and NewRemoteClient: the one rule turning a
                              servers section plus a stored key into a client.
  remoteclient_test.go   NEW  TestNewRemoteClient: every arm of that rule.
  reload.go              ~    ConfigWatcher installs the reloaded client: doc (26-28),
                              fields (29-46), constructor (51-62), Refresh (74-110).
  reload_test.go         ~    four watcher tests plus the daemon-level test.
  remote_sync.go         ~    applyRemoteErr classifies ErrUnknownServer (181-247).
  remote_test.go         ~    TestReconcileRemoteUnknownServerIsNotRunning.
cmd/relevo/
  wire.go                ~    Transport always wired (348-375); newRemoteClient delegates
                              to relevo.NewRemoteClient (402-424).
  wire_test.go           NEW  TestNewRuntimeAlwaysWiresTheTransport.
docs/plans/
  2026-09-26-daemon-server-reload.md  NEW  this plan, committed in the last step.
```

No file is deleted. Nothing else is expected to change; `git diff --stat` should name exactly the
paths above (nine entries: eight code and test files plus the plan document of step 8).

Behaviour that changes, as a closed list — everything not on this list survives unchanged:

1. `Runtime.Transport` is non-nil on every runtime `buildRuntime` builds, servers configured or not.
2. A remote binding whose server is not in the runtime's client reports `RemoteStatus`
   `"unknown server"` instead of keeping its previous text, and warns once per binding rather than
   every tick. It is still not halted.
3. The daemon's runtime gets a client rebuilt from the reloaded `servers` and key, on the first
   successful reload and whenever either differs from the installed client's.

No test asserts the old form of (1)-(3): `grep -rn "Transport ==" --include='*_test.go'` and
`grep -rn "no remote transport configured" --include='*_test.go'` are both empty on this base.

## 3. Data structures and type definitions

All existing unless marked NEW.

`remote.ServerEntry` (`internal/remote/servers.go:12-17`) — unchanged.
- `URL string` — required; a URL with a scheme and host.
- `Fingerprint string` — `sha256:<hex>`; required for https unless `CA` is set.
- `CA string` — `""` (pin) or `"system"`.
- `Insecure bool` — allows plain http.

`remote.Servers` (`internal/remote/servers.go:20`) — unchanged: `map[string]ServerEntry`, keyed by
the server's short name. `config.Loaded.Servers` (`internal/config/config.go:85`) is this type, and
an absent section loads as `remote.Servers{}` (`internal/config/config.go:266-281`), never nil.

`relevo.RemoteClient` (`internal/relevo/runtime.go:406-425`) — unchanged; the interface
`client.New` satisfies.

`config.Loaded` (`internal/config/config.go:79-93`) — unchanged; the reloaded `Servers`, `ClientKey`
and `Version` fields are what this change consumes.

**NEW** `relevo.ErrNoClientKey` (`internal/relevo/remoteclient.go`) — a sentinel error whose text is
the whole answer to a human:
```
servers configured but no client key; run relevo config server key
```
It is returned (never wrapped) by `NewRemoteClient`, matched with `errors.Is` by both callers, and
printed verbatim: `cmd/relevo` prefixes `"relevo: "`, the daemon warns it as the message.

**NEW** `ConfigWatcher` fields (`internal/relevo/reload.go`, in the struct at 29-46):
- `servers remote.Servers` — the section the installed client was built from; nil before the first
  install. Compared against each load's `Servers`.
- `key []byte` — the client key the installed client was built from; nil before the first install.
- `remoteFor func(servers remote.Servers, key []byte) (RemoteClient, error)` — the construction
  seam. Production value: `NewRemoteClient`, set by `NewConfigWatcher`; tests replace it to count
  calls and hand back a `fakeRemote` (`internal/relevo/remote_test.go:51`). A nil seam leaves
  `rt.Remote` untouched, which is what a test that does not care about servers gets.

## 4. Interface definitions and component contracts

### NEW `func NewRemoteClient(servers remote.Servers, key []byte) (RemoteClient, error)`

Single responsibility: turn a `servers` section and a stored client key into the client every remote
path uses. Package `internal/relevo`, file `internal/relevo/remoteclient.go`. It makes no network
call and keeps no reference to the key beyond the parsed keypair, so the daemon may call it on every
config change.

Contract, in order:

1. `len(servers) == 0` → `(nil, nil)`. No configured server means no client, exactly as
   `cmd/relevo` wires today (`cmd/relevo/wire.go:409-411`).
2. `len(key) == 0` → `(nil, ErrNoClientKey)`. Servers without a key cannot sign; a fresh CLI run
   behaves the same way (nil `Runtime.Remote` → every remote path reports `ErrRemoteUnavailable`,
   `internal/relevo/runtime.go:403`).
3. `remote.ParsePrivate(key)` fails → `(nil, err)` unwrapped, so a caller can only treat it as "the
   stored key is unusable".
4. Otherwise → `(client.New(servers, kp, time.Now), nil)`. `client.New` is
   `internal/remote/client/client.go:57`; it touches no network at construction.

Preconditions: `servers` and `key` come from one `config.Store.Load`. Postcondition: a non-nil
`RemoteClient` is returned if and only if the machine has both a server and a usable key. It never
returns a nil client with a nil error when `len(servers) > 0`.

### `ConfigWatcher.Refresh(rt Runtime) Runtime` — extended postcondition

`internal/relevo/reload.go:74-110`, documented at 64-73. Unchanged in signature and in its existing
postconditions (Candidates, Policy, Registry, ConfigWarnings, Classify). Added:

- After a successful load whose servers or key differ from the installed client's (§3), `rt.Remote`
  is the client `remoteFor` returned, and the watcher remembers the section and key it installed.
- `ErrNoClientKey` installs a nil `rt.Remote` (the config says there is no key) and warns once with
  the sentinel's text.
- Any other build error leaves `rt.Remote` as given and warns once with the error.
- The version is advanced and `loaded` set exactly as today, whether or not a client was installed:
  a rebuild is attempted on the next version change, not on every 2s tick.
- An unchanged version still returns `rt` untouched (reload.go:87-89); so does a failed import or
  load (reload.go:79-94) — the installed client survives a bad reload.

### `func newRemoteClient(servers remote.Servers, key []byte) (relevo.RemoteClient, error)`

`cmd/relevo/wire.go:402-424`, now a thin printing wrapper over `relevo.NewRemoteClient`: it keeps
the once-per-process stderr notice on `ErrNoClientKey` (the message becomes
`"relevo: " + ErrNoClientKey.Error()`; the old literal "servers configured but no client key; run
relevo config server key" is preserved word for word inside the sentinel), passes every other error
through, and no longer returns a transport. Precondition: `L.Servers`/`L.ClientKey` as loaded.
Postconditions: nil client with nil error for an empty section, nil client with the notice for a
missing key, an error for an unusable one, the client otherwise.

### `func applyRemoteErr(...)` — one new arm

`internal/relevo/remote_sync.go:185-247`. Existing arms (401 revoked, 401 transient, 404,
`ErrUnreachable`, `ErrCertChanged`) keep their order and behaviour. New arm, placed after the
`ErrCertChanged` arm and before the generic warn at 245: `errors.Is(err, client.ErrUnknownServer)`
(a) sets `b.Builder.RemoteStatus = "unknown server"`, (b) logs through `warnOnce(name,
"unknown-server", msg, ...)` (`internal/relevo/reconcile.go:117`), so a 2s tick warns once, (c)
returns `b, false, nil` — no halt, because the round may be running fine on the server and only this
machine's config is wrong. `"unknown server"` equals `client.ErrUnknownServer.Error()` and is not a
`remote.RoundState` (`internal/remote/proto.go:63-69`), so `status.go:140-152` prints it instead of
claiming a running round.

## 5. High-level pseudocode

### 5.1 `NewRemoteClient` (new file)

```
if servers is empty: return nil, nil
if key is empty: return nil, ErrNoClientKey
keypair = remote.ParsePrivate(key); if it fails: return nil, that error
return client.New(servers, keypair, time.Now), nil
```

### 5.2 `ConfigWatcher.Refresh`, the tail (insert between reload.go:103 and 105)

```
... existing replacements of Candidates, Policy, Registry, ConfigWarnings, Classify ...

if remoteFor is not nil and (servers changed OR key changed):
    client, err = remoteFor(L.Servers, L.ClientKey)
    if err is nil:
        rt.Remote = client
        remember L.Servers and L.ClientKey
    else if errors.Is(err, ErrNoClientKey):
        rt.Remote = nil
        remember L.Servers and L.ClientKey
        warn(ErrNoClientKey.Error())
    else:
        keep the installed client and the remembered servers
        warn("config reload: remote client not built; keeping the installed one", "err", err)

advance version, mark loaded, clear lastErr, stamp loadedAt   (unchanged, 105-108)
return rt
```

`sameServers(a, b)` is an unexported helper next to the struct: equal lengths, then every name of
`a` present in `b` with an equal `ServerEntry` (`ServerEntry` is comparable; all fields are string
or bool). Two absent sections and one empty section are the same set, so a machine with no servers
never builds a client.

### 5.3 `newRemoteClient` (cmd/relevo)

```
client, err = relevo.NewRemoteClient(servers, key)
if errors.Is(err, relevo.ErrNoClientKey):
    print "relevo: " + err to stderr
    return nil, nil
return client, err        // includes the empty-section (nil, nil) case
```

### 5.4 `buildRuntime` (cmd/relevo)

```
remoteClient, err = newRemoteClient(L.Servers, L.ClientKey)     // was 3 args, 3 returns
...
rt = relevo.Runtime{
    ...
    Remote:    remoteClient,
    Transport: remote.NewBundleTransport(gitClient, ""),   // unconditional, was `transport`
    ...
}
```

### 5.5 `applyRemoteErr`, the new arm

```
... ErrCertChanged arm (238-244) ...

if errors.Is(err, client.ErrUnknownServer):
    b.Builder.RemoteStatus = "unknown server"
    warnOnce(name, "unknown-server",
             name+": server "+server+" is not in this machine's config; run relevo config server list",
             "server", server, "binding", name)
    return b, false, nil

slog.Warn("remote get binding failed", ...)   // unchanged fallback, 245
return b, false, nil
```

## 6. Error handling strategy

- `ErrNoClientKey` (`relevo.ErrNoClientKey`) — recoverable and expected: servers configured, key
  absent. Both callers install/keep a nil client and say the fix ("run relevo config server key").
  `cmd/relevo` prints it once at startup; the daemon warns it once per reload.
- Unusable stored key — recoverable: `remote.ParsePrivate` error. Startup returns it (fatal for the
  CLI, as today, `cmd/relevo/wire.go:418-421`); the daemon keeps the client it has and warns with the
  error. It does not retry per tick; the next config version change retries the build.
- Failed import / version / load — unchanged (`reload.go:138-151`): rt is returned as given and one
  warning per distinct error text is logged; the installed client (and everything else) survives.
- `client.ErrUnknownServer` from a fetch — classified as a status, not a halt: `RemoteStatus` becomes
  `"unknown server"`, one warning per binding per process, the binding keeps its state. A CLI
  one-shot sees the same status; `relevo wait` still ends in its timeout rather than pretending.
- Observability: the daemon already logs `"config reload failed; keeping the copy loaded at ..."`;
  this change adds only the two client warnings above. No new log on the success path.

## 7. Working efficiently (read before step 1)

- Batch independent reads and edits into one step: the files this plan names are small and their
  line ranges are given; read each once from those ranges, and do not re-find anything.
- Make each file's change in one edit call. Steps 1, 4 and 5 are one file each; steps 2 and 3 are
  reload.go plus reload_test.go — edit each of those two files in one call per step, and never
  re-read a file you just wrote.
- Focused test command (from the repo root, one run per step, fix everything it reports before the
  next run):
  `go test ./internal/relevo/ ./cmd/relevo/ -run 'TestNewRemoteClient|TestRefresh|TestDaemonUsesAServerAddedWhileRunning|TestNewRuntimeAlwaysWiresTheTransport|TestReconcileRemoteUnknownServer' -count=1`
- Full check, once at the end: `make check` (it is `gofmt -l` over tracked files, `go vet`,
  golangci-lint, `scripts/check-comments.sh`, `scripts/check-filesize.sh`, `go mod tidy` check, the
  shell checks, then `go test -race -count=1 -cover ./...` and `scripts/check-coverage.sh`).
  Also run `gofmt -l .` and `sh scripts/check-comments.sh` directly and report both as empty/ok.
  `make check` failing on coverage: `internal/relevo`'s baseline is 86.2 and `cmd/relevo`'s is 50.1
  (`testdata/coverage-baseline.txt:2,29`). Every new statement here is covered by a new test; if a
  package still dips, add the missing assertion — never edit the baseline and never add an
  exclusion.
- Comments and tests: no issue numbers, no `§`, no "used to"/"pre-" history; a comment says why
  only. Tests use invented generic names (`zen`, `api`, `webshop`, `opus-5`) — never a real provider
  key, model or host. A `cmd/relevo` test must not run a subcommand that spawns a harness or reaches
  the network (`cmd/relevo/wire_test.go` builds a runtime only).
- Functions at most 70 lines, non-test files at most 600. Nothing here is close.

## 8. Ordered implementation steps

### Step 1 — the client rule: `internal/relevo/remoteclient.go` (NEW) + `internal/relevo/remoteclient_test.go` (NEW)

Deliverable: `ErrNoClientKey` and `NewRemoteClient` exactly as §4 specifies, plus
`TestNewRemoteClient`.

- `internal/relevo/remoteclient.go`: package doc comment stays in the existing
  `internal/relevo/runtime.go`; this file carries the two declarations and one comment each saying
  why (the key is the only thing that makes a server usable; construction touches no network so a
  running daemon can rebuild it). Imports: `errors`, `time`,
  `internal/remote`, `internal/remote/client`.
- `internal/relevo/remoteclient_test.go`: `TestNewRemoteClient`, table-driven, one case per arm of
  §4, each case naming what it pins:
  1. "no servers is no client": `nil` servers → nil, nil.
  2. "servers without a key name the fix": `Servers{"zen": {...}}`, nil key →
     `errors.Is(err, ErrNoClientKey)`, client nil, and `ErrNoClientKey.Error()` contains
     "run relevo config server key".
  3. "an unusable key is an error": a non-PEM key → err non-nil, client nil, and
     `!errors.Is(err, ErrNoClientKey)`.
  4. "a keyed server builds a client": key from `remote.Generate()` + `remote.MarshalPrivate` →
     nil err, non-nil client.
  No network: only construction is exercised.

Verification: `go test ./internal/relevo/ -run TestNewRemoteClient -count=1` passes; `gofmt -l
internal/relevo/remoteclient.go internal/relevo/remoteclient_test.go` prints nothing.

Depends on: nothing.

### Step 2 — the watcher installs the client: `internal/relevo/reload.go` + `internal/relevo/reload_test.go`

Deliverable: `ConfigWatcher` rebuilds and installs `rt.Remote` when the loaded servers or key differ
from the installed client's, with the three outcomes of §5.2, and three tests pin it.

- `internal/relevo/reload.go`:
  - 26-28: the doc becomes "reloads the candidates, policy, roles and servers sections"; drop
    "servers" from the "loaded once at start" list (prices and hooks stay).
  - 29-46: add `servers remote.Servers`, `key []byte` with a two-line why comment (they are what the
    installed client was built from), and `remoteFor func(remote.Servers, []byte) (RemoteClient, error)`
    in the seams block at 39-41. Add `sameServers` beside the struct.
  - 51-62: `NewConfigWatcher` sets `remoteFor: NewRemoteClient` — that one line is what makes the
    daemon reload servers with no change in `cmd/relevo/daemon.go`.
  - 74-110: insert the §5.2 block between line 103 (`}` of the `resolve` guard) and line 105
    (`w.version = v`). Update the `Refresh` doc at 64-73 with the added postcondition.
  - Imports: add `bytes`, `errors`, `github.com/fuad-daoud/relevo/internal/remote`.
- `internal/relevo/reload_test.go` (append; existing tests and the `fakeSource`/`loaded` helpers at
  18-62 stay as they are):
  1. `TestRefreshInstallsRemoteClientOnServersChange` — pins that a reloaded servers section reaches
     `rt.Remote`: override `w.remoteFor` with a recorder that returns a fresh `&fakeRemote{}` per
     call; the first `Refresh` installs a client and is handed the loaded servers and key; a version
     bump with a second server in `src.loaded.Servers` calls the recorder again with the new map and
     leaves `out.Remote` a different pointer than after the first refresh.
  2. `TestRefreshKeepsTheClientWhenServersAreUnchanged` — pins the comparison: after a first refresh
     that installs, a version bump whose servers and key are identical calls `remoteFor` no second
     time (recorded call count stays 1) and `out.Remote` is the same pointer.
  3. `TestRefreshKeepsTheInstalledClientWhenTheBuildFails` — pins the failure arm: `remoteFor`
     returns `errors.New("bad key")` on the second load; `out.Remote` is still the first client and
     exactly one warning containing "keeping the installed one" was captured through `w.warn`.

Verification: `go test ./internal/relevo/ -run 'TestRefresh|TestNewRemoteClient' -count=1` passes and
the three existing reload tests (`TestRefreshSkipsWhenVersionUnchanged`,
`TestRefreshKeepsLastGoodOnBadLoad`, `TestRefreshCarriesAndLogsConfigWarnings`) still pass unchanged.
Since these tests replace the seam, they do not touch `NewRemoteClient` and need no key material.

Depends on: step 1 (`NewRemoteClient` is the constructor default and the seam's type).

### Step 3 — the default seam and the daemon-level proof

Deliverable: tests that use `NewConfigWatcher` exactly as `cmd/relevo/daemon.go:124` does — no seam
override — including the issue's own scenario end to end at the tick boundary.

`internal/relevo/reload_test.go` (append):

1. `TestRefreshBuildsTheClientForAStoredKey` — `fakeSource` with one server and a real key
   (`remote.Generate` + `remote.MarshalPrivate`); plain `NewConfigWatcher(src, dir, nil)`; assert
   `out.Remote != nil`. Pins that the production default is installed by the constructor.
2. `TestRefreshWithoutAKeyWarnsAndCarriesNoClient` — same, key nil; assert `out.Remote == nil` and
   the captured warning contains "run relevo config server key". Pins that a keyless config never
   leaves a stale client behind.
3. `TestDaemonUsesAServerAddedWhileRunning` — the issue's headline, one tick apart:
   - open a config store over a real db (`db.Open(filepath.Join(t.TempDir(), "relevo.db"))`,
     `config.Open(d)`, as `TestReloadConfig` does at `configedit_test.go:537-540`), store a
     generated client key with
     `st.As("cli", "test key").PutSecret(config.SecretClientKey, pem)`;
   - `rt := Runtime{Store: store.New(t.TempDir()), Config: st}`;
     `w := NewConfigWatcher(st, t.TempDir(), nil)`; `daemon := NewDaemon(rt, time.Second).WithRefresh(w.Refresh)`;
   - `daemon.Tick(context.Background())` → `daemon.rt.Remote` is nil (no server yet);
   - write one server: `st.As("cli", "config server add zen").Put(config.Servers, body)` with
     `body` from `client.EncodeServers` for `{"zen": {URL: "https://zen:7777", Fingerprint: "sha256:00…"}}`;
   - `daemon.Tick(context.Background())` → `daemon.rt.Remote` is non-nil.
   This pins that the write the CLI performs is what the daemon's tick consumes; no subprocess, no
   network (a client is constructed, never used).
   Note for the builder: `NewDaemon(rt, …).Tick` runs `d.refresh(d.rt)` first
   (`internal/relevo/daemon.go:141-144`) and returns after an empty binding list, so the fixture
   needs no bindings.

Imports `reload_test.go` will need by the end of this step (it already has `errors`, `strings`,
`testing`, `time`): `context`, `path/filepath`, `github.com/fuad-daoud/relevo/internal/db`,
`github.com/fuad-daoud/relevo/internal/remote`,
`github.com/fuad-daoud/relevo/internal/remote/client`, `github.com/fuad-daoud/relevo/internal/store`.

Verification: `go test ./internal/relevo/ -run 'TestRefresh|TestDaemonUsesAServerAdded' -count=1`
passes; `go vet ./internal/relevo/` clean.

Depends on: steps 1 and 2.

### Step 4 — the transport is independent of the servers section: `cmd/relevo/wire.go` + `cmd/relevo/wire_test.go` (NEW)

Deliverable: `buildRuntime` always wires a transport; `newRemoteClient` delegates to
`relevo.NewRemoteClient`; one test pins the invariant.

- `cmd/relevo/wire.go`:
  - 335: `remoteClient, transport, err := newRemoteClient(L.Servers, L.ClientKey, gitClient)` →
    `remoteClient, err := newRemoteClient(L.Servers, L.ClientKey)`.
  - 368: `Transport: transport,` → `Transport: remote.NewBundleTransport(gitClient, ""),` with a
    one-line why comment (the transport depends only on the git client, so a server added while the
    daemon runs needs no rebuild of it).
  - 402-424: replace the body with the §5.3 wrapper and rewrite the comment to say why the keyless
    case prints (unchanged reason) and that the daemon's reload uses the same helper. The function
    no longer returns a transport.
  - Imports: drop `"github.com/fuad-daoud/relevo/internal/remote/client"` (line 24) — 423 was its
    only use; add `"errors"`. `remote` and `time` stay.
- `cmd/relevo/wire_test.go` (NEW): `TestNewRuntimeAlwaysWiresTheTransport` — `rt, err :=
  newRuntime()` with no servers configured; assert `err == nil`, `rt.Transport != nil` and
  `rt.Remote == nil`. Pins that the catch-up path has a transport the moment a server appears, and
  that an empty section still means no client. (The package's TestMain already points HOME and XDG
  at a temp root; this test reads and writes only that root, spawns nothing and reaches no network.)

Verification: `go test ./cmd/relevo/ -run TestNewRuntimeAlwaysWiresTheTransport -count=1` passes;
`go build ./...` clean.

Depends on: step 1.

### Step 5 — a binding on a server this machine does not know stops reading as running: `internal/relevo/remote_sync.go` + `internal/relevo/remote_test.go`

Deliverable: the new `applyRemoteErr` arm of §4/§5.5 and its test.

- `internal/relevo/remote_sync.go`: extend the doc at 181-184 with the new classification, and insert
  the arm between line 244 (`}` closing the `ErrCertChanged` block) and line 245 (the generic warn).
- `internal/relevo/remote_test.go` (append):
  `TestReconcileRemoteUnknownServerIsNotRunning` — `store.New(t.TempDir())`, a `remoteBinding("zen")`
  (2045) whose `Builder.RemoteStatus` is pre-set to `string(remote.RoundRunning)` (the stale text the
  bug leaves), `fr := &fakeRemote{getBindingErr: client.ErrUnknownServer}`, `rt := Runtime{Store: st,
  Remote: fr, Now: func() time.Time { return baseTime }}`; through the `reconcile(t, rt, b)` helper
  (fixture_test.go:176) assert the returned binding's `State` is still `store.StateActive` (not
  `store.StateNeedsYou`, no halt) and `Builder.RemoteStatus == "unknown server"`. Pins both halves:
  the status stops claiming a running round, and a config mistake never halts a round that may be
  fine on the server.

Verification: `go test ./internal/relevo/ -run TestReconcileRemoteUnknownServer -count=1` passes, and
the neighbouring classification tests (`TestReconcileRemote401Halts`,
`TestReconcileRemoteUnreachableIsNotHalt`, `TestReconcileRemote404Halts`) still pass.

Depends on: nothing (independent of steps 1-4); do it after step 4 only to keep the diff order.

### Step 6 — mutation checks

Deliverable: proof that the new tests fail without the new logic, with the tree restored.

1. In `internal/relevo/reload.go`, remove the install block added in step 2 (the whole
   `if w.remoteFor != nil && …` block) and run
   `go test ./internal/relevo/ -run 'TestRefreshInstallsRemoteClientOnServersChange|TestRefreshBuildsTheClientForAStoredKey|TestDaemonUsesAServerAddedWhileRunning' -count=1`.
   All three must fail. Restore the block.
2. In `internal/relevo/remote_sync.go`, remove the `ErrUnknownServer` arm added in step 5 and run
   `go test ./internal/relevo/ -run TestReconcileRemoteUnknownServerIsNotRunning -count=1`.
   It must fail (the status keeps the pre-set "running"). Restore the arm.
3. Re-run the focused command from §7 and confirm green; report the two mutations and the test names
   that caught them.

Depends on: steps 2, 3 and 5.

### Step 7 — the gate

Deliverable: the full check, green, with its output quoted in the report.

- Run, in this order, and fix everything each reports before moving on:
  `gofmt -l .` (empty), `sh scripts/check-comments.sh` (ok), `sh scripts/check-filesize.sh` (ok),
  then `make check` (must pass; if `check-coverage.sh` fails, see §7 — add the assertion, never the
  baseline or an exclusion).
- `git status --porcelain` names only the eight paths of §2 plus `docs/plans/2026-09-26-daemon-server-reload.md`
  from step 8; anything else is a halt-and-report.

Depends on: steps 1-6.

### Step 8 — the plan file and the commit

Deliverable: this plan saved verbatim and one commit.

- Write this plan text, verbatim and unedited, as `docs/plans/2026-09-26-daemon-server-reload.md`.
- Commit everything (the six source changes, the four test files, the plan file) as one commit, e.g.
  `fix: the daemon reloads its servers section, so a new server needs no restart`. Do not amend
  earlier commits, do not push.
- Report the commit sha, `git diff --stat` against the base and the name of the plan file.

Depends on: step 7.

## 9. If a step is impossible

Halt and report rather than improvise. Specifically: if `reload.go`'s lines 74-110, `wire.go`'s
335/368/402-424, or `remote_sync.go`'s 181-247 do not match what is quoted here; if a test named
here already exists; if a build or coverage failure would need an exclusion, a baseline edit, or a
test weakened to go green; or if the diff would touch a file not listed in §2.
