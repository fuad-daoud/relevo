# Tenant isolation for served builders: `user` and `container` modes

**Status:** draft for owner review 2026-10-01 (issue #204). Not agreed; the
owner's decisions are asked at the end (§14). This document is the round's only
write and exists so the draft can be reviewed; it is not an approved design.
**Issue:** #204.
**Related:** `docs/specs/2026-09-19-remote-builders-design.md` §2.5 (the wire's
threat model), #485 (account pools — it gates the serve half of this design and
names the per-owner account-home gap), #488 (per-round file sandbox — it
consumes the homes this design must hide), #203 (grants), the 2026-09-29 bug
sweep (issue #456; its six findings are §9 here).

## 1. The hole

remote-builders §2.5 states the boundary the wire does **not** draw
(`docs/specs/2026-09-19-remote-builders-design.md:187-207`): `Allowed`
(`internal/serve/bindings.go:Allowed`) protects tenants from each other over the
wire and from a passive network, but "every builder runs as the same unix user
on the server, and a plan that says `cat ../other-owner/...` succeeds". v1's
answer is per-owner directories and an admin who trusts the tenants they enrol.
README.md repeats it: tenants are "not ... from each other at the OS level where
all builders run under the same unix user", and #204 is named as the fix.

The queue-and-scopes design says "the scope is the seam tenant isolation will
hang on" (`docs/specs/2026-09-21-serve-queue-and-scopes-design.md:6-7`). The
tree's answer is narrower, and this draft follows the tree: the substitution
point is `spawn.Runner` (`internal/spawn/spawn.go:Runner`), not the systemd
scope. The scope keeps its resource bounds and nothing else (§6); systemd
scopes are not a security boundary — the daemon and root can write into any
scope's cgroup.

The six scan findings (#653, #654, #655, #656, #660, #661) were each
cross-tenant for the same root reason: one uid sees every owner's tree. Their
fixes have landed (§9), but a fix is not a mode. This design adds the mode.

## 2. Threat model (D0)

Three modes: `none`, `user`, `container`.

**`none` — today.** Every builder is the serve uid. Shared filesystem, shared
logins, trusted tenants (remote-builders §2.5, quoted above).

**`user` and `container` promise**, for every enrolled owner O and every other
owner P≠O:

- a round belonging to P cannot read or write O's worktree, binding state, or
  repo;
- no tenant can read the server's trusted state: the machine database, the TLS
  key, the enrolled-client registry;
- no tenant can read another owner's harness logins;
- no tenant-authored plan makes the server execute tenant-controlled content as
  a more privileged identity — specifically the server's own git and the gate
  shell (§6).

**Not promised** (state plainly so the modes are not oversold):

- the server admin still reads every tenant's plans and runs their code
  (remote-builders §2.5);
- network egress is unrestricted (the seed); the harness's own sandbox is #488,
  not this design;
- the owner-scoped wire (`Allowed`, `internal/serve/bindings.go:Allowed`) is
  unchanged and still owns tenant-vs-tenant over HTTP. The two layers compose:
  the wire says *who may ask*, the mode says *as whom the code runs*.

### 2.1 A root-run `serve` and the machine database (seed-vs-tree 4)

Every non-peek CLI route dials the owner socket (`cmd/relevo/machinedb.go:routeForArgs`,
`cmd/relevo/machinedb.go:openDBRoute`), and the owner refuses any peer whose uid
differs from its own (`internal/db/wire/owner/owner.go:ownerUID`,
`internal/db/wire/owner/owner.go:Server.handle`; test
`internal/db/wire/owner/owner_test.go:TestOwnerDropsADifferentUid`). So a
`user`-mode server, which must run as root, cannot reach the machine DB today.

**Recommendation: accept uid 0 as a peer of the owner socket.** Root can read
the database file and signal the daemon regardless of the socket, so refusing it
buys no secrecy and only breaks the intended deployment. The change is one
comparison in `handle` (`int(uid) != s.uid` becomes `uid != 0 && int(uid) !=
s.uid`).

**Rejected alternatives, stated:**

- **`RELEVO_DB_DIRECT` as the served path.** It opens the machine DB in the
  server process itself; with a root server that is two writers on one SQLite
  file — the exact reason the owner process exists. Rejected.
- **sudoers to let the server dial as the owner uid.** `Kill` and the
  `Credential`-free tree `proc` relies on (`internal/proc/proc.go:Runner.Start`)
  assume the server's own uid; sudo breaks pid, signal and environment
  semantics for every other path. Rejected.

## 3. Config, wire and doctor (D1)

### 3.1 Config

Add to the ordinary `serve` block of `policy.json`:

- `serve.isolation`: `"none"` | `"user"` | `"container"`. Default
  `"none"` (absent = today). Unknown value is refused.
- `serve.isolation_image`: string. Container-only, and **required** when
  `isolation` is `"container"`, so the admin names the image rather than a
  default being run silently.

Anchors: the DB-backed policy section is `policy.ServePolicy`
(`internal/policy/policy.go:ServePolicy`); the new keys are validated beside
`serve.max_builders` in `internal/policy/validate.go:validateThresholds`. The
value is resolved in `cmd/relevo/serve.go:cmdServeRun`, its prerequisites
checked **before** `serve.New`, and `isolation=<mode>` added to the existing
startup line (`cmd/relevo/serve.go:536`, "builders cap=… scopes=…"). The server
config struct is `internal/serve/serve.go:Config` (a new field beside
`MaxBuilders`/`Scope`).

### 3.2 Wire

`remote.BuildersView` (`internal/remote/proto.go:BuildersView`) gains
`isolation` (and `image`). That one field is already carried to both surfaces:
`GET /v1/whoami` fills `who.Builders` in `internal/serve/routes.go:handleWhoAmI`,
and `serve status` prints the same view via `internal/serve/admin.go:StatusJSON`.
A new `FeatureIsolation` token joins the feature constants
(`internal/remote/proto.go`, e.g. beside `FeatureTier`).

### 3.3 Status and doctor

- `relevo serve status` **always prints JSON** today
  (`cmd/relevo/serve_admin.go:cmdServeStatus`); its `--json` flag is read and
  ignored. "Reports it in force" therefore means three things, none of them new
  human output: a field in the status document (`StatusJSON.Builders.isolation`),
  a doctor row, and the startup log line.
- Doctor: a new `serve isolation` row in `ServeChecks`
  (`internal/doctor/serve.go:ServeChecks`); the config is already available to
  the doctor at `cmd/relevo/doctor.go:doctorReport`. It prints the mode in force
  and **every unmet prerequisite with its fix** (a missing user, a wrongly-owned
  owner root, `podman` absent, the image missing). `none` with more than one
  enrolled client is a **warning**, not a failure — it is today's default and
  the admin may be happy with it.

### 3.4 Compatibility

Additive only.

- An old server warns on the unknown key and runs `none`
  (`internal/policy/validate.go:policyUnknownKeyWarnings`).
- A new client reading a server with no `isolation` renders "none (server
  predates isolation)".
- No endpoint, request or response changes semantics. Client-side display of
  the mode is named as a later, optional slice; nothing in this design requires
  the client to understand it.

## 4. User mode (D2)

Serve runs as **root** (a system unit, not the user unit in
`dist/relevo-serve.service`), and every owner process runs as that owner's
declared unix user.

### 4.1 Enrollment declares the user

`relevo serve enroll --label <L> --key <K> --user <unix-user>`.

- `serve.Client` (`internal/serve/clients.go:Client`) gains `unix_user`.
- An unknown user is refused with the exact line to run:
  `useradd --create-home <user>`.
- Re-enrolling an existing key fills the field
  (`internal/serve/clients.go:Clients.Add`, which already clears `RevokedAt` on
  re-enroll).

### 4.2 Privilege boundary

Children get `syscall.SysProcAttr.Credential{Uid, Gid}` from a boundary wrapper
around `proc.Runner`. `internal/proc/proc.go:Runner.Start` keeps its `Setsid`
(the process group is what `Kill` signals) and only the credential fields are
added; `HOME`, `USER` and `LOGNAME` are set to the tenant (§4.5).

### 4.3 Directories

- `<serve root>/bindings/<owner-hex>/` and `<serve root>/repos/<owner-hex>/` are
  `0700` and owned by the tenant. They are created by root and `chown`ed, so the
  tenant can write its own tree and cannot see a sibling's.
- The serve root must be tenant-traversable: `--state /var/lib/relevo` with
  `0711` on the components a tenant must pass through (the root itself and the
  `bindings`/`repos` parents). `EnsureStateRoot`
  (`internal/serve/serve.go:EnsureStateRoot`) is where the root is created; the
  per-owner roots are `internal/serve/serve.go:ownerRoot` and
  `internal/serve/serve.go:repoRoot`.
- The machine DB stays in the admin's own `0700` state root, outside the
  tenant-traversable tree.

### 4.4 Owner-path git runs as the tenant

The server's own git process is the second execution surface after the harness.
There is a single exec site, `internal/git/client.go:run`
(`internal/git/client.go:NewClient`); the per-owner client is injected through
`internal/serve/serve.go:runtimeAt`, beside the per-owner runner. In `user` mode
that client runs with the owner's credential; in `container` mode it runs inside
the container (§5). Either way the server never executes a tenant-writable repo's
`hooks`, `fsmonitor` or filters as a more privileged identity.

### 4.5 Every owner process goes through the boundary

The four spawn sites, all of which must be covered or the boundary has a hole:

| Site | Anchor |
| --- | --- |
| round builder | `internal/relevo/headless.go:startProcess` (reached from `startRound`/`resumeRound`) |
| gate shell | `internal/relevo/gate.go:startGate` |
| consult | `internal/consult/verify.go:verifyStart.start` |
| session reaper | `cmd/relevo/serve.go:cmdServeRun` (`SessionReaper`), `cmd/relevo/exec.go:binExec.Run` |

Seed-vs-tree 5: the session reaper does **not** use `spawn.Runner` today — it
runs `binExec` synchronously (`cmd/relevo/serve.go:522`). It is the one path a
`Runner`-only boundary would miss; in `user` mode it must run as the tenant, or
it deletes sessions in the tenant's store as the daemon user. This design brings
it under the same wrapper.

### 4.6 HOME, logins, accounts

- The boundary sets `HOME`/`USER`/`LOGNAME` to the tenant and strips the
  daemon's inherited account-home variables. The env builders are
  `internal/relevo/headless.go:roundEnv` and
  `internal/relevo/headless.go:roundEnvFor` (the per-account home entry);
  the strip follows the rule `roundEnv` already applies to the runner marker.
- **Per-user logins are the recommended setup** (each tenant logs each harness
  in under its own HOME). A group-readable shared login is an explicit opt-in
  fallback that doctor warns on.
- #100's shared credentials apply to `none` only; under `user` they would be a
  cross-tenant read.
- #485 account homes must be per-owner here. The accounts design names this gap
  itself (`docs/specs/2026-09-30-account-pools-design.md:330-332`): "any account
  home on a host with untrusted tenants — tenant builders run as the same OS
  user and can read every home."

### 4.7 Failures without setup, named one by one

- `serve` refuses to start when `isolation: "user"` is set and `os.Geteuid() != 0`.
- `enroll --user` refuses a user that does not exist, printing the `useradd` line.
- A round whose owner has no declared user, or whose owner root is not owned by
  that user, halts **NEEDS YOU** naming `serve enroll --user` or the `chown`.
- Doctor fails each prerequisite above with its own fix.

## 5. Container mode (D3)

Rootless `podman` per spawn; no root; today's user unit
(`dist/relevo-serve.service`) is the deployment.

### 5.1 Image

The image is admin-built and named by `serve.isolation_image`. It must contain
git, a POSIX `sh`, and the harness binaries/runtimes on `PATH`. Ship a
`dist/Containerfile` and document the build. Doctor checks `podman info`
(rootless) and `podman image exists`.

### 5.2 Command

The boundary renders:

```
podman run --rm --userns=keep-id --name <unit> --cidfile <stream>.cid \
  <mounts> <env> <image> /bin/sh -c <supervisorScript> relevo-supervisor "" <argv>
```

and hands that `spawn.ProcSpec` to the base runner, so `Start` (pid = podman;
the log/stream fds are inherited), `Alive`, and `ExitCode` (the `relevo-exit:`
trailer is unchanged) stay the base implementations. Only the argv and mounts
are translated.

### 5.3 Mounts

Mounts are bound at their **host absolute paths**, so argv, `-C`, and
`CLAUDE_CONFIG_DIR`/`CODEX_HOME` need no rewriting:

| Host path | Mode | Why |
| --- | --- | --- |
| the round tree | rw | the builder commits here |
| the owner's bare repo | rw | git must reach `<repo>.git/worktrees/<name>` to commit (seed-vs-tree 6) |
| the runner-writable `out/` dir | rw | report, done marker, artifacts (S0, §6) |
| the owner's harness home/login | ro for OAuth token dirs; rw where the harness writes | opencode's credential DB is flipped by rotation, so it needs rw (`docs/specs/2026-09-30-account-pools-design.md`) |
| `/tmp` | tmpfs | scratch; nothing else |

Seed-vs-tree 6: a linked worktree's `.git` file points into
`<repo>.git/worktrees/<name>`; git needs the common dir to commit, so
"worktree + out/ + login" alone leaves the harness unable to commit. The bare
repo mount is required.

### 5.4 Bounds

`serve.scope`'s memory/quota/tasks render as `--memory` / `--cpus` /
`--pids-limit`. There is **no systemd scope for container rounds**, so `Rusage`
reports `ok=false` — state that trade rather than faking a measurement.

### 5.5 Kill

The cidfile written beside the stream drives `podman rm -f`, then the base
`Kill` (`internal/proc/proc.go:Runner.Kill`). A stale cidfile is swept the same
way the kill record is.

### 5.6 Honest label: unproven here

**`podman` is not installed on this laptop** (`command -v podman` is empty;
`bwrap` is present). Every podman flag in this section is a design decision to
be proven in slice C, never an observed fact. No flag is claimed as tested.

## 6. Seams and state layout

### 6.1 The `out/` prerequisite (seed-vs-tree 2, S0)

The owner decided on #654 (2026-09-29) that the report, the done marker and the
artifacts move to `<state>/<name>/out/`, and that **only that directory** is the
harness writable root. That layout is decided but **not in the tree**:

- Today report, done marker and stream live in the binding dir
  (`internal/store/paths.go:ReportPath`, `internal/store/paths.go:DonePath`,
  `internal/store/paths.go:StreamPath`).
- Codex's writable root is the whole binding dir:
  `internal/harness/harness.go:writableRootsArg`, filled into the argv by
  `internal/harness/harness.go:PrintArgs` and applied at
  `internal/relevo/headless.go:startProcess`.

This draft writes its mount and writable-root rules **against** the `out/`
layout and lists its landing as prerequisite slice **S0** (§8); it is not yet
shipped. Until S0 lands, the container mount of "the runner-writable `out/`" is
the binding dir, which is strictly weaker.

### 6.2 The substitution point

The boundary wraps `spawn.Runner` (`internal/spawn/spawn.go:Runner`); the four
spawn sites of §4.5 are the complete set. The `ProcSpec`
(`internal/spawn/spawn.go:ProcSpec`) is already the single translation surface:
`Dir`, `Argv`, `Env`, `LogPath`, `StreamPath`, `Scope`. `user` mode adds a
credential and an env rewrite; `container` mode rewrites `Argv` and adds mounts.
Nothing else in the spawn contract changes.

### 6.3 Owner-path git

`internal/git/client.go:run` is the single exec site. In `user` mode it runs as
the tenant; in `container` mode it runs inside the container. This is what binds
the server's own git — repo config, hooks, `fsmonitor`, filters — to the
boundary (seed-vs-tree 7). The server's git is otherwise "repo-config code
execution" on a tenant-writable repo, which is the whole point of the finding.

## 7. CI and tests (D4)

- **New pure package** (suggested `internal/isolate`): `Mode` parse/validate,
  `UserSpec(spec, uid, gid, home)`, `ContainerArgv(spec, opts)`,
  `Bounds(scope)`. Pure: table-tested, no `podman`, no root, no harness, no
  network.
- **Serve tests** keep the fake runner (`scriptRunner`,
  `internal/serve/helpers_test.go:scriptRunner`) as the base and wrap it in the
  boundary, asserting the translated `ProcSpec`. The status/whoami additions get
  `-race` tests beside the existing ones.
- **No new e2e**: `make e2e` is untouched. A podman integration test is
  local-only and **skips when `podman` is absent**.
- `make check` must not need root, `podman`, or network (CLAUDE.md's CI rule).

## 8. Slices and order (D5)

| Slice | Content | Test layer |
| --- | --- | --- |
| **S0** (prerequisite) | state layout `out/`: store paths, prompt paths, migration | store + serve unit tests |
| **A** (ships first) | config + wire + doctor + seam pass-through; `isolation: none` is byte-identical | pure `internal/isolate` tables; serve/whoami `-race` |
| **B** | user mode: enrollment `--user`, root serve, credential wrapper, dirs, git-as-tenant, logins | `internal/isolate.UserSpec` tables; serve wrapper test |
| **C** | container mode: image, argv render, mounts, bounds, kill | `internal/isolate.ContainerArgv` tables; local podman test (skips) |

Order and why:

1. **A ships first.** It is inert — `none` is byte-identical — so it merges
   while S0/B/C are still under review, and it gives both modes their seam.
2. **S0 next.** It is independent of A and B/C's writable-root rules land
   against it.
3. **Then B**, then **C**. B is the only mode that needs nothing installed
   beyond system users and is the fallback where rootless containers are
   unavailable; C is the stronger boundary and becomes the recommended mode for
   genuinely untrusted tenants **once proven** (slice C, per §5.6).

**Rejected alternative, stated:** C before B (strongest guarantee sooner).
Rejected because B is the fallback that must exist before the "recommended" mode
can be advised, and container mode has the larger unproven surface.

## 9. Relation to the six scan findings (D6)

The six findings are **closed, not open**. The fixes have landed; this section
answers the seed: per finding, what the cross-tenant class was, and exactly what
a mode removes. The mode removes the **shared-identity filesystem view** that
made each one cross-tenant; it does not fix any of them. **A mode is not a
substitute for the fix; each fix remains load-bearing.**

| Finding | What it was | Fix | What `user`/`container` remove |
| --- | --- | --- | --- |
| #653 | `repo_id` was not validated and the joined bare path was not confined (`internal/serve/bindings.go:parseCreateRequest`, `buildServedBinding`); a traversal could name a path outside the owner's repo root | `6f9a3d46` (#688) | a traversal reaches another owner's dir only because one uid sees all; under a mode the path is `EACCES` (`user`) or absent (`container`) |
| #656 | `whoami`/`candidates` did not hold the owner-store lock, so a shared store map could race and crash | `6f9a3d46` (#688) | **nothing** — the race is in the single server process, which is shared in every mode; the lock is load-bearing on its own |
| #661 | a tenant-named candidate the server did not serve fell back to `rt.Candidates.Resolve`, so a rogue candidate was accepted | `6f9a3d46` (#688) | nothing at the OS level; the refusal is the fix. The mode does ensure the accepted candidate cannot read another owner's tree |
| #654 | a planted `bind.json`/name at the runner-writable state dir could be read as a record | `e7b24853` (#689) | the state dir is per-owner `0700`, so a plant reaches only its own owner's record |
| #655 | a daemon write could follow a runner-planted symlink out of the runner-writable dir | `e7b24853` (#689) | another owner's dir is `EACCES`/absent, so a symlink cannot traverse into it |
| #660 | the database and state root were created world-readable (a world-readable transcript/DB) | `f74996fa` (#692) | the mode keeps the DB outside the tenant-traversable root, so even the residual read is gone |
| #685 (follow-up) | round log/stream and reader-output strip followed a planted symlink | `48d43889` (#697) | as #655: the target of a cross-owner symlink is unreachable |

`none` remains today's trust model: the fixes above are active, but a plan may
still read a sibling owner's tree.

## 10. Compatibility and rollout (D7)

- **Default `none`.** No config change for existing servers.
- **Mixed versions, both directions.** D1's compat rules: an old server warns on
  the unknown key and runs `none`; a new client renders "none (server predates
  isolation)".
- **Enrolling per mode:**
  - `none`: as today.
  - `user`: `useradd`, `serve enroll --user`, then log each harness in under
    that user.
  - `container`: image present; enrollment unchanged.
- **Switching a live server:**
  - `none → user`: enroll users, chown owner roots, move to the root system unit.
  - `user → none`: chown back, or keep root.
  - `→ container`: no state move.
- Each switch is a doctor-checked step, and a **half-done switch fails closed**:
  rounds halt naming the missing piece (a user, an owner root, the image), never
  silently run unisolated.

## 11. Behaviour cases

One line each; per mode where they differ.

- A plan that reads another owner's worktree/state/repo fails: `EACCES` under
  `user`, the path does not exist under `container`.
- Another owner's harness login is unreadable: `EACCES` under `user`; not
  mounted under `container`.
- The machine database, TLS key and client registry are unreadable to a tenant
  in both modes.
- The server never executes tenant-authored git config or gate shell as a more
  privileged identity (git-as-tenant under `user`; inside the container under
  `container`).
- The wire, round lifecycle, queueing, `done`/`unbind`/`resume`, reports and
  deltas are unchanged in all modes.
- `none` is byte-identical to today.

## 12. Risks

- **S0 not yet shipped.** The container mount rules assume `out/`; until S0
  lands they degrade to the binding dir (§6.1). Stated, not hidden.
- **Podman flags unproven** on this laptop (§5.6). Slice C must prove every flag.
- **Rusage is unavailable in container mode** (§5.4): a policy that relies on
  measured usage loses that signal there.
- **Shared-login fallback** weakens `user` mode to a group-readable login; it is
  opt-in and doctor-warned, not silent.
- **Root serve widens blast radius**: the server process is root to be able to
  chown and set credentials. This is a deliberate trade for `user` mode, named
  in §4.
- **Half-done switches**: mitigated by fail-closed rounds and doctor (§10), not
  by automatic rollback.

## 13. Rejected alternatives

- **`RELEVO_DB_DIRECT` for the root serve** — two writers on one DB; §2.1.
- **sudoers for the owner socket** — breaks `Kill`/pid/signal/env semantics;
  §2.1.
- **Doing the isolation in the systemd scope** — the scope is resource bounds,
  not a security boundary; §1.
- **A `none`-only shared-login path kept as the default under `user`** — a
  cross-tenant read; kept only as an explicit, doctor-warned fallback (§4.6).
- **Container mode before user mode** — §8.

## 14. Open owner questions

These are questions, not decisions buried as recommendations.

1. **The server's own git in container mode.** Should it run inside the
   container alongside the round (one boundary, more mounts), or as a separate
   per-owner process outside it (simpler mounts, a second exec surface to keep
   bound)? §6.3 assumes "inside".
2. **Serve-root placement for root `user` mode.** Is `/var/lib/relevo` (0711
   traversal, DB elsewhere) the intended default, or does the owner prefer a
   different root and a different DB location?
3. **Is the shared-login fallback offered at all?** §4.6 recommends per-user
   logins and permits an opt-in shared login with a doctor warning. Should the
   fallback exist, or should `user` mode refuse a shared login outright?
4. **Image naming and ownership.** Who owns `serve.isolation_image` — the admin
   by path, a registry reference, or both — and is `dist/Containerfile` the
   intended build artifact?
5. **An optional client-side require-isolation.** Should a client be able to
   demand a minimum mode (refusing to send to a server below it), or is the mode
   purely an admin/server-side fact the client only displays?
