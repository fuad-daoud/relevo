# #680 owner lifecycle — full report

**Method.** Read at `8e0b0ed` in the throwaway worktree `.scratch/db-owner-r3-lifecycle-z-002` (same commit as round 1's `-001` tree; `git log -3` = `8e0b0ed`, `e7b2485`, `442fc59`). The issue body was fetched this round: `curl -sS -o /tmp/opencode/r2/issue680.json https://api.github.com/repos/fuad-daoud/relevo/issues/680` → **HTTP 200** (`gh issue view 680` fails: "no git remotes found"). All measurement roots are under `/tmp/opencode/dbowner-r3` (round 1's root, kept) and `/tmp/opencode/r2` (this round); no real `relevo.db` was opened. Round 1's two experiments were re-run to n=3 at `/tmp/opencode/r2/reverify.sh` (output `/tmp/opencode/r2/reverify.out`); everything else is code reading at HEAD and is marked where it is inferred. Host: Linux 7.1.11-zen x86_64, 16 cores, go 1.27.1.

---

## 1. Start — verdict: systemd/launchd or a human start it, no plugin hook and no CLI does; and a CLI cannot auto-start it *safely* yet because the installation.json mint runs before the lock, so 2/6 cold double-starts die with a JSON decode error instead of "already running".

**Every starter that exists**
- `make service` → `install` (atomic rename onto `~/ .local/bin/relevo`, `Makefile:96-99`) then, Linux: install `dist/relevo.service`, `systemctl --user enable` + `restart` (`Makefile:115-119`); macOS: render `dist/com.github.fuad-daoud.relevo.plist.in` and `launchctl load -w` (`Makefile:101-107`).
- `dist/relevo.service:14-16`: `ExecStart=%h/.local/bin/relevo daemon --interval 2s`, `Restart=on-failure`, `RestartSec=5`, plus `StartLimitIntervalSec=0` (`:5-7`) and `OOMPolicy=continue` (`:23`).
- `dist/com.github.fuad-daoud.relevo.plist.in`: `ProgramArguments` = `@BIN@ daemon --interval 2s`, `RunAtLoad true`, `KeepAlive{SuccessfulExit=false}`.
- `dist/relevo-serve.service`: `ExecStart=… relevo serve --listen :7777` — the serve host's long-running process, and no local daemon.
- README:166 (`relevo daemon &`), README:2046-2058 (`make service`), README:2059-2064 ("only one daemon runs at a time… `.daemon.lock`").
- `relevo doctor` never starts it: the daemon row is `warn  not running  Fix: relevo daemon` (`internal/doctor/doctor.go:186-196`).
- The plugin does **not** start it: `claude-plugin/hooks/hooks.json` has only `SessionStart → relevo mastermind init --hook claude` and `UserPromptSubmit → relevo mastermind notice --hook claude`. But the SessionStart path **does open and can migrate the database**: `cmdMasterMindInit` → `mastermindInitHook` (`cmd/relevo/mastermind.go:104-114`) → `newRuntime()` (`cmd/relevo/mastermind_hook.go:82`) → `openDB` (`cmd/relevo/wire.go:168`, minting `installation.json` at `wire.go:228`).

**AcquireDaemonLock** — `internal/store/daemonlock.go:24-41`, non-blocking `flock(LOCK_EX|LOCK_NB)` in `internal/store/lock_unix.go:14-24`, file `<state root>/.daemon.lock` (`internal/store/store.go:40`, created at `daemonlock.go:60-71`). Kernel releases it on death, so there is no stale lock. `DaemonRunning` is a take-and-release probe (`daemonlock.go:45-58`). **Only one caller exists**: `cmd/relevo/daemon.go:143`. `relevo serve`, every CLI verb, and `relevo mcp` take it never.

**Two at once, cold, one fresh root (n=6, `--interval 5s`, both stdout/stderr captured)** — 2/6 trials printed `relevo: installation: decode …/installation.json: unexpected end of JSON input` (exit 1); the other 4 lost cleanly with `relevo daemon already running` (exit 1). Mechanism, from the code: `newRuntime` opens and migrates the DB (`wire.go:162-217`) and `openDB` calls `installation.Load` (`wire.go:224-233`) **before** the lock; `mint` creates with `O_EXCL` and *then* writes (`internal/installation/installation.go:91-105`), so a loser's `read` (`:62-75`) can see a 0-byte file. (Attribution inferred from code + the error text; the count is measured.)

**Cold-path side effects (n=1 each, fresh `XDG_STATE_HOME` root):**
| command | rc | files created |
|---|---|---|
| `status` | 0 | `.daemon.lock`, `installation.json`, `relevo.db`(+`-wal`,`-shm`) |
| `daemon --check` | 1 | `.daemon.lock` only; stderr `INFO scopes=on`, **stdout empty** |
| `daemon --preflight` | 0 | nothing at all |
| `doctor` | 0 | same as `status` |

So `--check` does not create the DB (matches the intent at `cmd/relevo/daemon.go:47-48`), but it does create the state root and the lock file; `--preflight` is genuinely side-effect-free.

**Timings (round-1 script `/tmp/opencode/dbowner-r3/measure.sh`, binary built from this commit, n=5, min / median):**
- `daemon --preflight`: 7.2 / 7.4 ms (7.7,7.4,7.2,7.6,7.2), rc 0, prints `ok v0.14.1-…-8e0b0edd7216`.
- `status` cold (fresh root): 34.5 / 35.3 ms (35.3,35.6,34.5,35.7,34.9), rc 0, `no bindings`.
- `status` warm (same root): 11.9 / 12.0 ms (12.0,12.0,11.9,12.0,12.6), rc 0.
- daemon start→lock (spawn `daemon --interval 1s`, poll `daemon --check` every 5 ms): 53.6 / 55.4 ms (53.9,53.6,55.5,55.4,72.9).

**Can a CLI auto-start safely?** Yes, with a bound and a loser rule: spawn `relevo daemon` (or let systemd), then poll `daemon --check` (exit 0/1, stdout silent); the observed start-to-lock is 54–73 ms, so a 1–2 s wait has >20× headroom and the measured cold `status` (35 ms) fits inside it. The loser must be a reader of `--check`, never a second opener — which is exactly what today's pre-lock `newRuntime` breaks (the 2/6 above). Fix ordering, not retries: **preflight → lock → open/migrate → mint**.

---

## 2. Re-exec — verdict: today's re-exec deliberately hands **no** fd over; a unix listener *can* cross it with zero dropped requests, but only by inheriting the fd — without that, requests already queued in the backlog are **reset**, not merely refused.

**Mechanism.** `upgrade.ResolveExe` (strip `" (deleted)"`, `internal/upgrade/exe.go:15-26`) + `ExeIdentity` (dev/ino/size/mtime, `internal/upgrade/exe_unix.go:14-33`); `upgrade.Watcher` debounces the new identity over two ticks (`internal/upgrade/watch.go:58-83`), then runs `<candidate> daemon --preflight` with a 30 s cap (`watch.go:13`, `cmd/relevo/daemon.go:310-319`); success → `ErrReexec` from `internal/relevo/daemon.go:132-134`; then `cmd/relevo/daemon.go:361-373`: `closeDB()` → `stop()` (signal ctx) → `lock.Close()` → `reexec(exe, [exe, os.Args[1:]…], env+RELEVO_REEXEC_FROM)` where `reexec` is `syscall.Exec` (`cmd/relevo/reexec_unix.go:9-11`). A refused build is recorded in `daemon.json` (`watch.go:88-92`, `daemon.go:330-345`, `internal/store/daemoninfo.go:19-25`). Because the upgrade hook runs only after a *completed* tick (`internal/relevo/daemon.go:119-134`, tick under `WithoutCancel`), no reconcile is torn in half.

**Which fds survive.** None, by design: the DB handle and the lock are closed explicitly before `Exec` (`daemon.go:362-367`), everything else Go opens is `O_CLOEXEC`, and only 0/1/2 cross. The code says so in as many words ("rather than relying on CLOEXEC", `:363-364`).

**Experiment — unix listener across `syscall.Exec`** (`/tmp/opencode/dbowner-r3/sockprobe/main.go`; parent listens on a socket path, client dials at t≈0.4 s, parent gets `SIGUSR2` at t≈0.6 s and execs itself as `child`; the child sleeps a deliberate 300 ms before accepting). Re-run n=3 per arm:
- **handoff arm** (parent dups the listener via `(*net.UnixListener).File()` and clears CLOEXEC): 3/3 `CHILD: served "hello" over the inherited fd`, `CLIENT: reply "ok" after 502/503/502 ms`. The connection that arrived *before* the exec sat in the backlog and was served by the new image. **Zero drops.**
- **no-handoff arm** (child unlinks and rebinds): 3/3 `CLIENT: read … connection reset by peer` at ~200 ms, child `accept …: i/o timeout`. A *queued* connection is RSTed at exec — not politely refused.

**Exact conditions for a socket to survive with zero drops** (probe + code reading): (a) the listening fd must be inherited (dup with `F_SETFD 0`, or `ExtraFiles` via `os/exec`); (b) the child must adopt it (`net.FileListener`) and must not unlink/rebind — rebinding discards queued connections and changes the inode; (c) the backlog absorbs connections that arrive during the gap, so the adopt must happen before it fills; (d) the DB and the lock must still be closed/re-acquired in the current order — and here the lock must be *released* before the exec because `flock` is per open-file-description and the new image must acquire it itself (`daemon.go:143`); (e) with Turso's single-opener mode the exec is fine because the **pid survives**, but the parent's DB handle must be closed before the child opens (it is, `daemon.go:365`) — the race window is the few tens of ms between exec and the child's `openDB`.

**What a mid-transaction client sees.** Today: nothing — every verb is a one-shot that opens, does its work in a `Tx`, exits; there is no cross-process transaction. With an endpoint, a transaction is pinned to a connection; the owner drains the tick before exec, so the honest policy is "in-flight transactions are refused at the exec boundary and retried by the client", with the retry budget ≥ debounce+preflight (up to 2 ticks ≈ 4 s + up to 30 s preflight).

**The windows that still drop a request:** (i) no fd handoff → RST (measured); (ii) fd handoff but a full backlog during adoption (inferred); (iii) fd handoff impossible across a `os/exec`-style restart (Makefile's atomic rename means the path is stable, so the watcher's re-exec is the correct vehicle); (iv) a client whose connect lands *after* exec and *before* the child listens, if the child must rebind → `ECONNREFUSED` (inferred; the probe's no-handoff arm hit this as RST because the client had already connected).

---

## 3. Serve hosts — verdict: today **both** the daemon and `relevo serve` open the machine DB (plus every admin verb), and neither takes the daemon lock; the rule that actually holds is "the lock, not the role, names the owner" — on a serve host the owner must be `relevo serve` itself.

**Who opens what, today.**
- `relevo serve` run: `cmdServeRun` → `loadServeConfig` (`cmd/relevo/serve.go:343-354`) → `openMachineDB` (`serve.go:98-112`) = `store.DefaultRoot()/relevo.db`, opened **and migrated**, then `installation.Load` (`:427`), `serve.New(cfg)` with `DB: d` (`:513`), listener at `:566`. `--state` moves only the serve root (`serveRoot`/`defaultServeRoot`, `serve.go:66-83`); README:731 says the same.
- Per-owner stores borrow that handle: `internal/serve/serve.go:141-148` → `store.NewShared(root, ownerID, s.cfg.DB)` (`internal/store/store.go:92-97`), and it is the same handle `LoadClients` reads (`serve.go:114`).
- `relevo serve` **is** the reconciler on that host: `internal/serve/daemon.go:16-53` (Tick) and `:60-105` (Run), each owner driven by `relevo.NewDaemon(...).Tick` (`:37-41`). It takes no `AcquireDaemonLock` and has no re-exec path (grep: no callers outside `cmd/relevo/daemon.go:143`).
- The admin verbs (`serve status|clients|show --owner|gc|unbind`) each call `openMachineDB`/`adminRootFor` (`serve.go:117-157`), i.e. each one opens and can migrate the same file.

**Options, and what each breaks.**
- *daemon owns, serve proxies*: correct on a laptop, wrong on contabo — there is no local daemon there, so `relevo serve` would have to start one, changing the deployment (dist/relevo-serve.service runs only serve).
- *serve owns*: correct on a serve host, wrong on a laptop; and it would need a second owner implementation.
- *lock decides*: one rule for both hosts — the process that holds `.daemon.lock` owns; a serve that cannot take it must either run as a client or refuse to start. Breaks only in that `serve` has no client mode today.
- *split roots* (serve keeps its own DB): breaks the invariant the docs and code build on — "it opens the one machine database" (README:731), `openMachineDB`'s TLS/clients/gates reads (`serve.go:98-112`), the daemon pointer (`serve.go:528-536`), and the doctor's serve rows which read secrets from the machine DB and the root from `serve.daemon` (`internal/doctor/serve.go:19-38`). It also strands `relevo gate --serve`, which writes the `serve.`-prefixed view of the same DB (`serve.go:226-239`, `internal/serve/serve.go:126`).

**Recommendation.** Owner = lock holder, on every host; on a serve host that is `relevo serve`, and it must take the lock (making today's silent co-opener impossible); the plain daemon stays the owner on a laptop, and `relevo serve` on a laptop refuses to start when the lock is held. All admin verbs go through the endpoint like any other client. No split roots.

---

## 4. Version skew — verdict: the current contract is the **file** plus advisory features; an endpoint needs a protocol/version handshake and the schema answer moved owner-side, or the transition period (old CLI + new owner) is two writers by construction.

**What exists.**
- Schema: `maxVersion`/`maxEmbedded` (`internal/db/migrate.go:43-80`), `ErrNewerSchema` (`internal/db/errors.go:15-17`), enforced in `store.dbForWrite` (`internal/store/db.go:41-44`); `OpenReadOnly` never migrates (`internal/db/config.go:188-224`) and is what `--check`/`--preflight` use (`cmd/relevo/wire.go:239-271`); the old daemon pauses ingest and says so once: `relevo.db schema v%d is newer than this relevo (v%d); ingest paused until relevo is upgraded` (`cmd/relevo/daemon.go:174-180`); a CLI verb prints `relevo: schema is newer than this relevo` (measured, §6).
- Binding format: `store.BindingFormat = 10` (`internal/store/format.go:22`), `ErrNewerFormat` on read (`internal/store/lifecycle.go:172-173`, `:344-345`), and the tick skips such a binding (`internal/relevo/daemon.go:305-316`).
- Remote: additive `WhoAmI.Features` tokens (`internal/remote/proto.go:79-96`, `:336-390`) and the informational `Relevo-Client-Version` header (`proto.go:24`, set at `cmd/relevo/main.go:105-108`).
- Owner identity/version: `daemon.json` = version, pid, exe, exe-id, reexec_from, reexec_failed (`internal/store/daemoninfo.go:27-40`); consumers: `status` notice (`cmd/relevo/daemon_notice.go:13-23`, `cmd/relevo/status.go:149-155`), doctor's daemon row (`internal/doctor/release.go:104-139`), `relevo mcp` notice with a 30 s cache (`cmd/relevo/mcp.go:121-129`).

**Handshake shape (proposal).** On connect the client sends `Hello{proto int, version string, exe_id, want_schema bool}`; the owner answers `Welcome{proto, min_client int, features []string, version, pid, started_at, schema_have, schema_know}` or a typed refusal (`wrong_proto`, `schema_newer`, `shutting_down`). Ship `proto=1` like `remote.Version` (`remote/proto.go:17`), and reuse `DaemonInfo` verbatim as the identity payload — no new truth about "what the daemon runs" should exist twice.

**Matrix (client × owner), with the policy each cell forces:**
| | old owner (no endpoint) | new owner (endpoint) |
|---|---|---|
| **old client** | today's world | file opened directly by the client while the owner holds it → **two openers**; safe on modernc, fatal on Turso ⇒ the owner must not assume exclusivity during the transition; the fallback dies at the Turso swap |
| **new client** | dial fails (`ENOENT`/`ECONNREFUSED`) → auto-start or direct-open fallback + one quiet line | full protocol; owner answers schema; a `proto`/`min_client` mismatch prints "relevo daemon runs an older build — restart it (relevo doctor)" and falls back to direct open until the swap |

Skew policy that survives the swap: the owner is authoritative for schema and protocol; a client that cannot speak to it must **refuse**, never open the file; the direct-open fallback exists only while the driver is still multi-opener-safe (modernc), i.e. steps 0–1, and is removed in step 2.

---

## 5. Tests — verdict: every tier exists and **none** uses a socket; the work is a new carrier (in-process owner over a socketpair/pipe), a unix-only build tag plus a non-unix stub, and one macOS path-length rule; direct-open unit tiers stay untouched.

- `cmd/relevo` isolation: `TestMain` moves `HOME/XDG_*` into a temp root and unsets harness identity (`cmd/relevo/main_test.go:29-47`), then `dbtest.Install` (`:48-55`).
- `internal/db/dbtest`: one migrated template per test binary, copied instead of migrated per test (`internal/db/dbtest/dbtest.go:9-31`, `Main` at `:34-46`; `db.SetFreshTemplate`, `internal/db/template.go:15-18`).
- `internal/e2e`: in-process daemon — `relevo.NewDaemon(rt, …)` + `Tick` (`internal/e2e/headless_test.go:143-144`, `:866-871`; `reader_test.go:116-117`; `remote_test.go:329,466,553`), no network, no real harness (`headless_test.go:9-11`).
- CI (`.github/workflows/ci.yml`): lint + darwin `go vet` (`:62-121`), test shards on ubuntu 1.25/stable (`:123-156`) running `scripts/test-shard.sh` = `go test -race -count=1 -cover` (`scripts/test-shard.sh:143,155`), macOS legs (`:189-243`), coverage guard (`:249-278`), and the portability cross-compile incl. **windows/amd64** (`:280-305`). `make check-test` is `-race -count=1 -cover` (`Makefile:59-61`).
- Per tier: (a) *direct open* — `internal/db`, `internal/store`, `dbtest` unit tests, unchanged; (b) *in-process owner* — `internal/e2e` and `cmd/relevo`: start the owner in-process over a unix socketpair (or `net.Pipe` if the protocol is a stream), point a client at it, keep the existing in-process daemon tests as the owner-side tier; (c) *pipe* — the stage-0 driver seam: protocol tests (pinning, cancellation, client death → rollback) run over an in-memory pair with no process, which is where today's `database/sql` pool semantics (`internal/db/db.go:245-296`) get pinned.
- Platform rules: macOS `sun_path` is 104 bytes — bind test sockets under a short root (`/tmp/…`), never under `t.TempDir()`'s long path (inferred from the limit; nothing in-tree binds today — the only `net.Listen` is TCP, `internal/serve/listen.go:41`). Windows keeps compiling: the tree already uses build tags for exactly this (`internal/store/lock_unsupported.go:1-18`, `cmd/relevo/reexec_other.go`, `internal/upgrade/exe_other.go`), so the socket code needs `//go:build unix` and a stub that refuses, and ci.yml:302's windows leg stays green.

---

## 6. Failure modes — verdict: a dead daemon is invisible to `status` and the hooks, a crashed/unreadable DB produces exactly one stderr line (stdout stays clean, measured), and a **wedged writer** is today a 30 s hang then one line — the number the endpoint's timeouts must beat.

| case | detection today | client action | printed line (measured unless marked) |
|---|---|---|---|
| owner crashed | nothing in `status`/hooks; only `relevo doctor`: daemon row `warn  not running  Fix: relevo daemon` (`internal/doctor/doctor.go:186-196`) | CLI opens the DB directly (12 ms warm) | `status` prints `no bindings`, rc 0, **no daemon notice** (`cmd/relevo/daemon_notice.go:13-16` returns "" when not running) |
| stale socket | n/a today — no socket exists (only TCP: `internal/serve/listen.go:41`) | (target) dial `ECONNREFUSED` → auto-start | (target) one line naming the owner it started |
| wrong permission | DB open fails at `newRuntime` | abort rc 1 | `relevo: db: open <path>: ping: open failed: unable to open database file (14)`; `status --line` prints the same on **stderr**, rc 0, **stdout empty** |
| wedged owner (writer holds `BEGIN IMMEDIATE`) | only the deadline: writes retry `beginRetryFor = 30 s` (`internal/db/db.go:37-38`, `:266-268`) | abort rc 1 after ~30 s | reads: 13/12/12 ms, rc 0 (n=3); writes: **30389/30352/30394 ms** (min 30352, med 30389), rc 1, `relevo: db: tx begin: busy` |
| newer schema | owner-side: `store.dbForWrite` → `ErrNewerSchema` (`internal/store/db.go:41-44`); daemon logs the ingest-paused line (`cmd/relevo/daemon.go:174-180`) | abort rc 1 (writes and store reads); `--check` still exits 1, `--preflight` still exits 0 | `relevo: schema is newer than this relevo` (+ a WARN `relevo.db schema is newer; config import skipped`) |
| re-exec refused | `daemon.json.ReexecFailed` (`internal/store/daemoninfo.go:19-25`) | none; reported | `status`: `relevo daemon refused the new binary -- relevo doctor` (`cmd/relevo/daemon_notice.go:20-22`); doctor: `runs %s; the relevo binary at %s failed preflight (%s) and was not loaded` (`internal/doctor/release.go:119-126`) |
| daemon predates version tracking | lock held, no `daemon.json` | none | `relevo daemon predates version tracking -- restart it once (relevo doctor)` (`daemon_notice.go:17-19`) |

**The 2 s hook budget is narrower than it looks.** The only 2 s cap is `mastermindPriorIDTimeout` (`cmd/relevo/mastermind.go:28`) around a *second, timed* open of the same file (`mastermind_consent.go:18-38`, used at `mastermind_hook.go:90`); the hook's first open is `newRuntime()` (`mastermind_hook.go:82`) and is unbounded — and it is the open that creates and migrates the DB on a cold machine (§1). Any owner-wait the hook gains must fit that budget or the hook must move fully onto the endpoint.

**Statusline stays silent.** `runStatusline` prints failures to **stderr** and returns nil (`cmd/relevo/status.go:175-179`, `:192-196`, `:213-218`); measured: unreadable DB → stdout empty rc 0; healthy root with no mastermind → stdout empty rc 0; with a mastermind, `RenderMasterMindLine` is printed even if `MasterMindStatus` fails (`:187-191`). A wedged writer does not block the line: reads were unaffected (12 ms).

---

## 7. What the issue gets wrong

The issue's *diagnosis* is right (no local endpoint exists; the issue's own grep is confirmed here: the only `net.Listen`/`net.Dial` in the tree is `internal/serve/listen.go:41`, TCP, and none under `internal/relevo`/`cmd/relevo`). Its *supporting facts* are stale and, in three places, wrong:

1. **"Cold-start races are serialized by `AcquireDaemonLock`"** (Risks 1). False at HEAD, and measurably so: `newRuntime()` opens + migrates the DB and mints `installation.json` **before** the lock (`cmd/relevo/wire.go:162-233`; `internal/installation/installation.go:80-105`), and 2/6 cold two-daemon starts fail there — exactly the race the lock is claimed to serialize. The lock must move ahead of the first open, or the mint must become atomic (temp file + rename, or retry a zero-length read).
2. **"Socket handoff must survive the daemon's re-exec"** (§3 staging, step 1) is stated as a checkbox; it is a design item. Today's re-exec closes every fd on purpose (`cmd/relevo/daemon.go:361-373`), and measured: *without* fd inheritance a request already queued in the backlog is RST, not refused. So step 1 needs explicit fd passing (clear CLOEXEC / `ExtraFiles`), adoption by the child before the backlog fills, and a client retry budget covering debounce+preflight.
3. **"`--check`/`--preflight` … must keep never opening the database"** (What exists today). `--preflight` is indeed side-effect-free, but `--check` opens the lock file and creates the state root (measured), and both read the DB read-only when one exists (`wire.go:239-271`) — "never *migrate*" is true; "never open" is not.
4. **Line citations are from an older revision** (`dd2abcb6`): `cmd/relevo/wire.go:225` is `openDB`, not `db.Open`; `wire.go:249` is not `OpenReadOnly` (it is called at `wire.go:256`); `internal/store/db.go:28` is an error string (the open is `internal/store/db.go:36`); `internal/db/db.go:41` is the `DB` struct. Counts drift too: at HEAD, non-test `newRuntime()` call sites = **53** (matches), `rt.DB` = **44** (issue: 41), `rt.Store` = **325** (issue: 321), `func (d *DB)` = **65** (matches), `func (s *Store)` = **84** + `func (t *Tx)` = **19** (issue: 119 — consistent if Tx methods were folded in).
5. **The blast radius under-counts openers.** Besides `newRuntime`/`openDB`, every process can also open via `store.DB()` (`wire.go:323` in `buildRuntime`), `captureAgyEnv` (`wire.go:105-123`, runs on **every** verb when `ANTIGRAVITY_*` is set), `newDeliverers` (`wire.go:132-157`), `cmdUI` (`main.go:272`), `history` (`history.go:93`), `show` (`show.go:235`), and the serve path (`serve.go:107`). A seam at `db.OpenWith`+`OpenReadOnly` covers them, but the plan must say so.
6. **"Performance: negligible"** hides the one measured pathology: a wedged writer costs a CLI write **30.3 s** (min 30352 ms, med 30389 ms, n=3) and then a bare `db: tx begin: busy`, while reads stay at 12 ms. The endpoint changes this shape; the failure policy must be designed against those two numbers, not against "negligible".
7. **The serve-host sentence is right but underspecified.** "It must either expose the same local endpoint or be the owner on its host" — at HEAD `relevo serve` opens the machine DB (`serve.go:98-112`, `:343-354`, `:418-422`), writes the daemon pointer (`:528-536`), and runs its own tick (`internal/serve/daemon.go:60-105`) **without taking the lock**, so "the other process becomes a client" is not a topology change, it is a new requirement (see §8). The issue also does not name the serve host's admin verbs (`serve status/clients/show --owner`), each of which is another opener (`serve.go:117-157`).
8. **The "option (b)" staging loses the hook.** Step 1 says "the daemon serves the socket, the CLI defaults to it with auto-start". The SessionStart hook is not a CLI one-shot: it already opens and can migrate the DB synchronously (`mastermind_hook.go:82`), and its 2 s budget covers only the prior-id read (`mastermind.go:28`). Step 1 must move the hook onto the endpoint (or the auto-start must fit the hook budget), or the hook stays the one process that still opens the file.

---

## 8. Recommended lifecycle

**Owner.** Exactly one process per machine opens `relevo.db`, and it is the process that holds `<state root>/.daemon.lock` — not a role. The plain daemon is the owner on a laptop; `relevo serve` is the owner on a serve host (it already runs the reconciler there) and must take the lock; on a laptop `relevo serve` refuses to start when the lock is held rather than silently co-opening. No split roots.

**Start order (the fix for the 2/6 race).** Owner path: resolve root → `--preflight`-equivalent read-only config load → **AcquireDaemonLock** → open+migrate the DB → `installation.Load` (mint, ideally temp+rename) → serve the endpoint → write `daemon.json` → tick. Today's `newRuntime()` at `cmd/relevo/daemon.go:51` must be replaced by a lock-first variant (or split into peek+open), and the same order applies to `relevo serve`.

**Who starts it.** systemd/launchd (`make service`, the two units), or a human (`relevo daemon`). A CLI may auto-start it as a convenience: spawn `relevo daemon …`, then poll `daemon --check` for at most 2 s (measured start-to-lock: 53.6–72.9 ms, n=5); if the wait expires, print one line and fall back to direct open during the transition. The plugin hooks must **not** start it (keep them silent), and the SessionStart hook must stop being an opener by moving its reads onto the endpoint.

**Endpoint, handshake, re-exec.** A unix socket under the state root (0700 dir, 0600 socket, peer-uid check), protocol `proto=1` with `Hello/Welcome` as in §4; the owner answers schema and protocol. Re-exec: pass the listening fd across `syscall.Exec` with CLOEXEC cleared (or `os/exec` + `ExtraFiles` + exit), keep the path, do **not** unlink/rebind, adopt in the child before serving; keep the current drain order (finish the tick, close DB, close lock, exec) and have clients retry for the debounce+preflight window. This is feasible: measured 3/3 zero-drop handoffs with a deliberately widened 300 ms gap.

**Serve-host rule.** Owner = lock holder on that host; `relevo serve` takes the lock, serves its own requests through the endpoint (it is the owner, so locally that is a function call), and the admin verbs become clients. A second `relevo serve`/daemon on the same host is a client or refuses.

**Skew policy.** Owner-authoritative: protocol version, build version, and schema answer. `min_client` gates admission; a mismatch prints one actionable line ("relevo daemon runs an older build; restart it — relevo doctor", same shape as `daemon_notice.go`/`release.go:104-139`) and, only until the Turso swap, falls back to direct open; after the swap it refuses.

**Failure policy.** Auto-start on a missing endpoint; a bounded dial+handshake timeout (≈ the poll budget, not 30 s); on a wedged owner, fail fast with the owner named (never the 30 s `db: tx begin: busy`); keep stdout clean on every failure (the statusline contract, ss §6); keep the existing newer-schema line and the `daemon.json` notices. The existing lock-file semantics (kernel-released, no stale lock) already give free crash recovery — reuse them, do not invent a pid file.

**Does the issue's staging hold?** The three-step shape (driver seam over a pipe → daemon serves + CLI flips with hidden direct-open fallback → Turso) is the right order and stays reversible, with three amendments: (i) step 0 must also make the owner path **lock-first** and the install-file mint atomic — otherwise step 1 inherits a live 2/6 cold-start failure; (ii) step 1's "daemon serves the socket" must include fd inheritance across re-exec and a client retry budget, because without handoff queued requests are reset, not refused; (iii) the "hidden fallback" must not be the plugin hook's permanent mode: the SessionStart hook is already an opener today and has to move onto the endpoint inside step 1, not at the swap.

---
