# Plan: #204 slice B — user mode (`serve.isolation: "user"`)

Branch `relevo/iso-b`, cut from `origin/main` (`2fa6007f`; slice A `8155747c` and S0 are already in). Make new commits only. Never amend or rebase anything that has been pushed.

## 0. Where the seed and spec disagree with the code (read first)

The owner settled findings 1, 2 and 7 on 2026-10-01. They are now facts of this plan, not open questions.

1. **The layout is the root-owned split (approved).** The seed's tenant-owned `bindings/<owner-hex>` would let a tenant swap a binding directory for a symlink and redirect root's writes. The approved layout:
   - `bindings/<owner-hex>` is `root:<tenant-gid> 0710`.
   - Binding directories `<owner-hex>/<name>/` are root-owned.
   - The only tenant-owned directories under `bindings/` are `<name>/out/` and `<owner-hex>/.worktrees/` (with `.worktrees/.scratch/`), each `0700`.
   - `repos/<owner-hex>` (and `tmp/<owner-hex>`) are tenant-owned `0700`.

   Root still reads, writes and removes inside the tenant-owned directories. Step 11 enumerates every such operation.
2. **In user mode, served rounds run without a scope (approved).** `proc.ScopeArgv` (`internal/proc/scope.go:28`) always uses `systemd-run --user`, and stop, probe and journal use `systemctl`/`journalctl --user`. That is root's own user manager, not the tenant's. The startup line and the doctor say `scopes=off (isolation=user)`. System-manager scopes with `--uid` are a follow-up and out of scope. The caps (`max_builders`) still apply.
3. **Owner-path git starts processes in two places, not one.** They are `internal/git/client.go:run` (lines 56-95) and `internal/git/snapshot.go:diffPatch` (around line 138). Both get the credential. Spec §4.4 and §6.3 are corrected in step 12.
4. **The server uses its git and transport directly in many places, not only through `runtimeAt`.**
   - `s.cfg.Git` is called in `internal/serve/bindings.go:220,524`, `internal/serve/chains.go:291-363` and `internal/serve/rounds.go:255-275,359`.
   - The server-wide `s.transport` is used in `rounds.go:223`, `chains.go:336` and `roundfiles.go:198`.

   Each must use the owner's git client and transport.
5. **Spec §2.1's uid-0 peer on the owner socket is not in the code.** `internal/db/wire/owner/owner.go:268` still compares `int(uid) != s.uid`. Step 9 lands it.
6. **Any `Start` error currently gates the candidate.** `internal/relevo/headless.go:startProcess` (lines 378-381) records `availability.RecordSpawnFailureLocked` on every start error. A tenant setup refusal would therefore gate the candidate for every owner. Step 3 adds a sentinel the spawn paths do not record.
7. **`serve.isolation_shared_logins: bool` is added (approved).** It defaults to false, and the doctor warns when it is on.
8. **The spec's session-reaper anchor is stale.** It points at `cmd/relevo/serve.go:522`. The reaper is built at `cmd/relevo/serve.go:532` and `:246`, and the whole server shares it (`internal/serve/serve.go:287`). Step 12 fixes §4.5.
9. **The stream is not written through a path by the tenant.** The stream, builder log, kill record, gate log, consult stream and prompt all live in the binding directory, which is root-owned under finding 1. Root opens the stream and log in `internal/proc/proc.go:openSpawnFiles` (Lstat plus `O_NOFOLLOW`) and hands the file descriptors to the supervisor. The supervisor, now running as the tenant, never opens a path. None of these passes through `out/` or `.worktrees/`, so step 11 leaves them unchanged and §6 records why.

## 1. Behaviour and cases

- `isolation: "user"` with euid ≠ 0: `relevo serve` refuses with `codeNotAvailable` before any side effect.
- `relevo serve enroll --label L --key K --user U`:
  - An unknown `U` is refused, printing `useradd --create-home U`.
  - A new key stores `unix_user`.
  - An already-enrolled active key given `--user` has its `unix_user` updated; the call does not answer `ErrAlreadyEnrolled`.
  - A revoked key is re-enrolled as today, plus the user.
- User mode, for each owner:
  - The tenant is resolved from the client's `unix_user` with `os/user`: uid, primary gid, home and name.
  - Root sets up the owner's tree in the split layout (step 7).
  - Every owner process runs with `Credential{uid,gid}` and the tenant's `HOME`/`USER`/`LOGNAME`. This covers the round builder, gate shell, consult, session reaper and owner-path git.
  - The daemon's inherited home variables are removed from the child's environment.
- Root's own operations under the tenant-owned `out/` and `.worktrees/` never follow a symlink out of those directories. This holds in every mode (step 11).
- No declared user, a user that no longer exists, or an existing owner root with the wrong owner, group or mode:
  - Wire create is refused (`422`, `CodeInvalid`, naming `relevo serve enroll --user`).
  - A round halts **NEEDS YOU**. The halt text names `relevo serve enroll --label <L> --key … --user <u>` or the exact `chown`/`chmod` line.
  - The candidate's availability record is not touched.
- Shared logins:
  - Off (the default): account-home entries from the server's pool (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`, …) are removed from the round's environment.
  - On: those entries are kept, and the doctor warns.
- Unchanged modes:
  - `none` stays byte-identical, apart from step 11's refusal of escaping symlinks.
  - All slice A tests stay green unchanged.
  - `container` stays refused, as in slice A.

## 2. Ordered steps

Each step names what it delivers and how to know it worked.

1. **Credential and env deny list on the spawn spec.**
   - `internal/spawn/spawn.go:ProcSpec` gains `Credential *Credential` (`UID`, `GID uint32`) and `DenyEnv []string`.
   - In `internal/proc/proc.go:Runner.Start` (lines 190-220), pull the command assembly out into a pure `buildCmd(spec, bin) *exec.Cmd`.
   - Keep `Setsid: true`.
   - Set `SysProcAttr.Credential` (with `Groups` empty) only when `Credential != nil`.
   - Pass `DenyEnv` into `spawnEnv`'s deny list (line 320).

   Proof: `internal/proc/proc_test.go:TestBuildCmdAppliesCredential` and `TestSpawnEnvDeniesSpecDenyEnv`. Existing proc tests stay unchanged.
2. **The user boundary in `internal/isolate/isolate.go`.**
   - `Tenant{User string; UID, GID uint32; Home string}`.
   - `UserSpec(spec, Tenant, sharedLogins bool) spawn.ProcSpec`:
     - sets `Credential`;
     - appends `HOME`, `USER` and `LOGNAME` last;
     - sets `DenyEnv` to `InheritedHomeVars` (`XDG_{CONFIG,DATA,STATE,CACHE}_HOME`, `XDG_RUNTIME_DIR`, plus every `harness.HomeEnv` name);
     - removes account-home entries unless `sharedLogins`;
     - sets `Scope` to nil.
   - `CheckPrivilege(mode, euid int) error`.
   - `Available` covers none and user.
   - `boundary.ForTenant(t *Tenant, setupErr error) spawn.Runner`. A nil tenant refuses with an error that wraps the new `spawn.ErrBoundarySetup` and carries `setupErr`'s text.
   - Update the package comment.

   Proof:
   - `internal/isolate/isolate_test.go:TestUserSpec` (table);
   - `TestInheritedHomeVarsCoverEveryHarness`;
   - `TestCheckPrivilege`;
   - `TestUserBoundaryRefusesWithoutTenant`;
   - `TestAvailable` updated;
   - slice A's `TestWrapNonePassesSpecThrough` and the optional-halves tests stay unchanged.
3. **Setup refusals stay out of the availability ledger.**
   - In `internal/relevo/headless.go:startProcess` (lines 378-381), an error that is `errors.Is(err, spawn.ErrBoundarySetup)` returns without calling `RecordSpawnFailureLocked` and is not wrapped in `spawnFailure`.
   - Check `relevo/gate.go:startGate` (around line 120) and `consult/verify.go:verifyStart.start` (around line 298), and change them if they record such an error.
   - Halt text flows through `send.go:599-620` and `queue.go:68-85` unchanged.

   Proof: `internal/relevo/headless_test.go:TestBoundarySetupErrorIsNotASpawnFailure`.
4. **Owner-path git under the credential, in `internal/git`.**
   - `(*Client) WithCredential(uid, gid uint32, env []string) *Client` returns a copy.
   - A pure `command(ctx, dir, env, args) *exec.Cmd` is shared by `client.go:run` and `snapshot.go:diffPatch`.

   Proof: `internal/git/client_test.go:TestCommandAppliesCredential` and `TestDiffPatchUsesCommand`. The existing git suite passes.
5. **Per-owner bundle transport in `internal/remote/bundle.go`.**
   - `NewBundleTransport` takes an option for a per-owner tmp dir and an owner `(uid, gid)`.
   - Temp files are `Lchown`ed to the owner before git touches them.
   - `Snapshot`'s re-open (line 77) uses `O_NOFOLLOW` and refuses anything that is not a regular file.
   - none: no option, identical behaviour.

   Proof: `internal/remote/bundle_test.go:TestSnapshotRefusesSwappedSymlink` and `TestAbsorbChownsTempToOwner`.
6. **Enrolment declares the user.**
   - `internal/serve/clients.go`: `Client.UnixUser string` with JSON tag `unix_user,omitempty`.
   - `Clients.Add` takes `unixUser`.
   - `CheckUnixUser(lookup, name) (isolate.Tenant, error)` returns the `useradd --create-home <user>` sentence.
   - `cmd/relevo/serve_admin_write.go:cmdServeEnrollRun` (lines 110-144) and `serveEnrollFlagSet` gain `--user`; update the usage string at `cmd/relevo/serve.go:176`.

   Proof:
   - `internal/serve/clients_test.go:TestAddSetsUnixUserOnActiveKey`;
   - `TestCheckUnixUserRefusesUnknownWithUseradd`;
   - `cmd/relevo/serve_test.go:TestServeEnrollRefusesUnknownUser`. It uses a name that cannot exist and only reads `/etc/passwd`, so no harness and no network are involved.
7. **Tenant resolution and the split layout in `internal/serve`.**
   - `serve.Config` gains `LookupUser func(string) (isolate.Tenant, error)`, `SharedLogins bool` and `ReaperFor func(isolate.Tenant) relevo.SessionDeleter`.
   - New file `internal/serve/tenant.go` holds `tenantFor(owner) (*isolate.Tenant, error)` and `ensureTenantRoots(owner, t)`. Both refuse symlinks (Lstat), never follow them, and use `Lchown`. The layout they create or verify:

     | Path | Owner | Mode |
     | --- | --- | --- |
     | `<root>`, `bindings/`, `repos/`, `tmp/` | root | `0711` |
     | `bindings/<hex>` | `root:<gid>` | `0710` |
     | `bindings/<hex>/<name>` | root | as today |
     | `bindings/<hex>/<name>/out` | tenant | `0700` |
     | `bindings/<hex>/.worktrees`, `.worktrees/.scratch` | tenant | `0700` |
     | `repos/<hex>`, `tmp/<hex>` | tenant | `0700` |

   - Add a store option: a tenant chown callback set by `runtimeAt`. Do not import `serve` into `store`.
     - `EnsureOutDir` and a new `Store.EnsureScratchDir` (which replaces the raw `os.MkdirAll` at `relevo/scratch.go:122`) chown what they create through it.
     - With no callback (none mode), behaviour is as today.
   - `runtimeAt` (`serve.go:266-289`) gives every user-mode owner:
     - `Runner = boundary.ForTenant(t, err)`;
     - `Git = s.cfg.Git.WithCredential(...)`;
     - `SessionReaper = ReaperFor(t)`;
     - a per-owner transport;
     - the chown callback.
   - Re-run `ensureTenantRoots` on every resolution, because `PruneWorktreeDirs` and `cleanup.go:222` remove empty tenant directories.
   - Route every `s.cfg.Git` and `s.transport` call listed in §0.4 through the owner's runtime.
   - Wire create refuses an owner with no tenant (`422`).

   Proof:
   - `internal/serve/serve_test.go:TestServedRunnerRunsAsTenant`: a fake `LookupUser` returns the test's own uid and gid. Assert `Credential`, `HOME`, `DenyEnv` and a nil `Scope` on the spec for a round, a gate and a consult.
   - `TestUserModeOwnerGitRunsAsTenant`.
   - `TestUserModeReaperRunsAsTenant`.
   - `TestUserModeRoundWithoutUnixUserHaltsNeedsYou`.
   - `TestUserModeRoundWithForeignOwnerRootHaltsNeedsYou`.
   - `TestEnsureTenantRootsRefusesSymlink`.
   - `TestUserModeCreateRefusedWithoutUnixUser`.
   - `TestServedRunnerPassesSpecThrough` (none) stays unchanged.
8. **`cmd/relevo`.** Put the new logic in `serve_isolation.go`; `serve.go` is 596 lines and must not pass 600.
   - `resolveIsolation` takes the euid and calls `isolate.CheckPrivilege`.
   - In user mode:
     - scope is nil;
     - the serve root defaults to `/var/lib/relevo` (a `serve` constant) when `--state` is unset;
     - `LookupUser` uses `os/user`;
     - `ReaperFor` builds `relevo.NewSessionReaper(binExec{cred})`.
   - `binExec` (`exec.go`) gains an optional credential and tenant env, built with `isolate.UserSpec`'s env rule.
   - `serve init --state` applies the `0711` modes.
   - Callers of `defaultServeRoot` that already load the policy pick the user-mode root; the rest find it through the daemon pointer.

   Proof: `cmd/relevo/serve_test.go:TestServeRunRefusesUserModeWithoutRoot`, with a bounded wait like slice A's and skipped when euid is 0. It refuses before anything is spawned, so no harness or network is involved.
9. **Root peers on the owner socket.** Pull a pure `peerAllowed(peer, own int) bool` (root or own uid) out of `internal/db/wire/owner/owner.go:Server.handle` (line 268).

   Proof: `owner_test.go:TestPeerAllowedAcceptsRoot`. `TestOwnerDropsADifferentUid` stays green.
10. **Doctor and policy.**
    - `internal/doctor/serve.go:serveIsolationCheck` (lines 144-170) handles `user`. It fails, with a fix, on each of:
      - not root;
      - an active client with no `unix_user`;
      - a user missing on this host;
      - an owner root with the wrong owner, group or mode.
    - It warns when shared logins are on, and prints `scopes=off (isolation=user)`.
    - User lookup and stat are injected, so tests stay pure.
    - Policy: `internal/policy/policy.go:ServePolicy.IsolationSharedLogins` plus an accessor, validated in `validate.go:validateThresholds`.

    Proof: `internal/doctor/serve_test.go:TestServeChecksIsolationUser` (one row per prerequisite) and `internal/policy/policy_test.go:TestServeIsolationSharedLogins`.
11. **Root's operations under `out/` and `.worktrees/` stay inside them.** This applies in every mode.
    - Add one helper file, `internal/store/confined.go`, with two exported accessors:
      - `Store.OutRoot(name) (*os.Root, error)` opens `OutDir`;
      - `Store.WorktreeRoot() (*os.Root, error)` opens `WorktreeDir`.

      `os.OpenRoot` plus Go 1.25's `Root.MkdirAll`/`RemoveAll`/`Rename`/`Lchown`/`ReadFile` are available; `go.mod` says `go 1.25.0`.
    - `os.Root` refuses only escapes. Inside the root, keep today's final-component rules:
      - Lstat first;
      - after opening, check `Stat()` on the handle and refuse anything that is not a regular file;
      - open read paths so a planted fifo cannot block.
    - Do not add a `filepath.EvalSymlinks` pre-check.

    The closed list of root-side operations whose path passes through `out/` or `.worktrees/`, and what each does:

    | # | Site | Operation | Treatment |
    | --- | --- | --- | --- |
    | a | `store/read.go:readRunnerOutput`, `readRegularFile` (59-110) | read of `out/<name>`, nested `NNN-<actor>/<rel>` | **os.Root** (`OutRoot`). The old flat home stays raw: binding dir is root-owned |
    | b | `store/read.go:statRunnerOutput` (150-176) | Lstat of nested names | **os.Root** `Lstat` |
    | c | `store/out.go:runnerOutputExists`, `resolveRunnerOutput` (54-75) | Lstat | **os.Root** for the out/ home |
    | d | `store/roundwalk.go:walkRoundDir`, `walkArtifactDir` (79-160) for the out/ home | ReadDir descending `NNN-*` dirs | **os.Root**: open each directory through the root, read entries from the handle |
    | e | `store/seal.go:SealRound` (264-292) | `os.ReadFile` + `os.Stat` + `os.Remove` of each sealed file | **os.Root** for out/ files. Read, handle-Stat and remove through the root. Today's raw `os.ReadFile` follows a final symlink swapped in after the walk, which would seal a root-only file into a row the tenant can fetch |
    | f | `store/seal.go:removeEmptyRoundDirs`, `removeEmptyDirsBelow` (302-339) for the out/ home | ReadDir + rmdir | **os.Root** (`ReadDir` via the root's handle, `Root.Remove`) |
    | g | `store/out.go:MigrateOutLayout` (127-166) | rename `<name>/X` → `<name>/out/X` | **os.Root** opened on `Dir(name)`, `Root.Rename(base, "out/"+base)` |
    | h | `store/out.go:EnsureOutDir` (33-49) | Lstat + MkdirAll of `out` | Raw stays: `out` is an entry of the root-owned binding dir, so no component is tenant-swappable. Gains the tenant chown (`os.Lchown` on the just-created dir) from step 7 |
    | i | `relevo/summary.go:writeReaderSummary` (61-88) | Lstat + MkdirAll of `out/NNN-<actor>` | **os.Root** (`OutRoot`, `Root.MkdirAll`) |
    | j | `relevo/summary.go:writeReaderOutput` (130-140) | create `out/NNN-<actor>/<label>.md` | **os.Root** `OpenFile` with `O_EXCL`; signature takes the root and a relative name |
    | k | `relevo/summary.go:replaceReaderOutput` (149-165) and its callers `summary.go:68`, `reconcile.go:588` | truncate-write | **os.Root**. `reconcile.go` is 721 lines (already excluded); keep its change to the call site, no net growth |
    | l | `relevo/scratch.go:prepareScratchPath` (108-125) | Stat + RemoveAll of the leftover `.scratch/<n>-NNN`, MkdirAll of `.scratch` | **os.Root** (`WorktreeRoot`). `RemoveAll(".scratch/<n>-NNN")`; the MkdirAll becomes `Store.EnsureScratchDir` (through the root, plus tenant chown) |
    | m | `relevo/scratch.go:SweepScratch` (143-180) | ReadDir of `.scratch` | **os.Root** |
    | n | `relevo/scratch.go:removeScratchEntry` (187-198) | `os.RemoveAll(path)` | **os.Root** `RemoveAll`. Today a tenant `.scratch` → symlink makes root delete any `<x>-NNN` directory anywhere |
    | o | `relevo/scratch.go:scratchRepoFromGitFile` (224-) | `os.ReadFile(<scratch>/.git)` | **os.Root** `ReadFile`, with the handle-Stat regular check |
    | p | `store/paths.go:PruneWorktreeDirs` (412-416) | `os.Remove` of `.verify`, `.scratch`, `.worktrees` | Raw stays: rmdir never follows its final component, and every parent (`<hex>`, the `.worktrees` entry in it) is root-owned. The removed tenant dirs are recreated by `ensureTenantRoots`/`EnsureScratchDir` (step 7) |
    | q | `relevo/daemon.go:509-513` | ReadDir + rmdir of `out` and the binding dir | Raw stays: `out` is an entry of the root-owned binding dir; rmdir never follows |
    | r | `store/lifecycle.go:430,461` | `os.RemoveAll(s.Dir(name))` | Raw stays: the parent `bindings/<hex>` is root-owned, and Go's Linux `RemoveAll` descends with `openat(O_NOFOLLOW)`/`unlinkat`, so a symlink inside `out/` is unlinked, never followed |
    | s | `serve/cleanup.go:214,222` | `RemoveAll` of `repos/<hex>/X.git`, rmdir of `repos/<hex>` | Raw stays: the parent's entry lives in root-owned `repos/`, and the descent is at-based as in (r). The rmdir'd owner root is re-ensured (step 7) |
    | t | `remote/bundle.go` temp files in `tmp/<hex>` | CreateTemp + re-open | Handled in step 5 (`O_EXCL` create, `O_NOFOLLOW` re-open, `Lchown`) |
    | u | `git worktree add/remove` into `.worktrees/` (`bindings.go`, `scratch.go:CreateScratchFrom`, `consult/verify.go`) | git writes | Not root in user mode: they run as the tenant through the owner's git (steps 4 and 7) |
    | v | `relevo/headless.go:startProcess` `os.Stat(roundTree)`, `proc.checkSpawnSpec` `os.Stat(spec.Dir)`, `serve/rounds.go:263` | stat that follows symlinks | Raw stays: metadata only. The child it gates runs as the tenant, so a redirected `Dir` only reaches what the tenant already can |

    Proof, one test per redirected group:
    - `internal/store/out_test.go:TestReadRunnerOutputRefusesSymlinkedIntermediateDir`: a nested `NNN-<actor>` linked outside `out/` is refused; covers (a), (b) and (c).
    - `internal/store/seal_test.go:TestSealRefusesFileBehindEscapingSymlink`: the outside file is neither sealed into a row nor removed; covers (d), (e) and (f).
    - `internal/store/out_test.go:TestMigrateOutLayoutStaysInBindingDir`; covers (g).
    - `internal/relevo/summary_test.go:TestWriteReaderOutputRefusesEscapingArtifactDir` and `TestReplaceReaderOutputRefusesEscapingArtifactDir`; cover (i), (j) and (k).
    - `internal/relevo/scratch_test.go:TestSweepScratchDoesNotFollowSymlinkedScratchDir`: `.scratch` is linked to a temp dir holding `x-001`, and that directory survives; covers (l), (m), (n) and (o).
    - `internal/store/paths_test.go:TestEnsureScratchDirChownsThroughCallback`.

    S0's plant tests stay green.
12. **Docs, spec and deployment.**
    - `dist/relevo-serve-system.service`: a system unit with `User=root` and `--state /var/lib/relevo`.
    - README: the `user`-mode enrolment and switch steps (spec §10).
    - Spec `docs/specs/2026-10-01-tenant-isolation-design.md`, edited in place:
      - **§3.1**: add `serve.isolation_shared_logins` (bool, default false, user-mode only, validated beside `serve.max_builders`).
      - **§4.3**: replace the tenant-owned `bindings/<owner-hex>` bullet with the approved split layout, using the step 7 table.
      - **§4.4 and §6.3**: replace "single exec site" with both `internal/git/client.go:run` and `internal/git/snapshot.go:diffPatch`. §4.4 carries the same sentence, so both change.
      - **§4.5**: re-anchor the reaper (`cmd/relevo/serve.go:532`/`:246`, `internal/serve/serve.go:287`).
      - **§4.6**: name `serve.isolation_shared_logins` as the opt-in switch. Off strips account-home entries; on keeps them, and the doctor warns.
      - **§14**: append decisions 6-8, "2026-10-01, this session": the split layout; scopes off in user mode, with system-manager `--uid` scopes as a follow-up; the `isolation_shared_logins` key.
    - Save this plan as `docs/plans/2026-10-01-tenant-isolation-slice-b.md` in the last commit.

    Proof: `make check` (comments, file size).

## 3. Commands

- Focused, after each step:
  - `go test ./internal/isolate ./internal/spawn ./internal/proc ./internal/git ./internal/remote ./internal/policy ./internal/doctor ./internal/store ./internal/db/wire/owner`
  - `go test -race ./internal/serve ./internal/relevo ./internal/consult ./cmd/relevo`
- Full, once at the end: `gofmt -l $(git ls-files '*.go')`, then `make check`. Do not change `make e2e`.
- Goldens that may move, each only by gaining fields: the `serve clients`/enroll JSON and `cmd/relevo/testdata/contract/*`. Update them with the repo's update flag and list every one in the report.
- Do not grow `relevo/reconcile.go` (721) or `relevo/daemon.go` (714). They are over 600 and excluded; do not add new exclusions.

## 4. What is deleted (closed list)

1. `Clients.Add` returning `ErrAlreadyEnrolled` for an **active** key when a non-empty `--user` is given. It now fills the field.
2. Slice A's refusal of `isolation: "user"` as "not available in this build". `container` keeps it.
3. The server-wide `transport` and `cfg.Git` as the git path for owner repos in user mode. none still uses the same client.
4. Root following a symlink that escapes `out/` or `.worktrees/` on the operations in step 11 (a)-(g) and (i)-(o). They are refused now, in every mode.
5. The raw root `os.MkdirAll` of `.worktrees/.scratch` at `relevo/scratch.go:122`, replaced by `Store.EnsureScratchDir`.

## 5. Mutation checks to run and report

- `UserSpec` drops `Credential` → `TestUserSpec` and `TestServedRunnerRunsAsTenant` fail.
- `startProcess` records `ErrBoundarySetup` → `TestBoundarySetupErrorIsNotASpawnFailure` fails.
- `CheckPrivilege` ignores euid → `TestCheckPrivilege` and `TestServeRunRefusesUserModeWithoutRoot` fail.
- `runtimeAt` keeps the server-wide reaper → `TestUserModeReaperRunsAsTenant` fails.
- The ownership check skipped in `ensureTenantRoots` → `TestUserModeRoundWithForeignOwnerRootHaltsNeedsYou` fails.
- `peerAllowed` drops the root case → `TestPeerAllowedAcceptsRoot` fails.
- `SealRound` reads with raw `os.ReadFile(f.path)` again → `TestSealRefusesFileBehindEscapingSymlink` fails.
- `removeScratchEntry` uses raw `os.RemoveAll(path)` again → `TestSweepScratchDoesNotFollowSymlinkedScratchDir` fails.
- `writeReaderOutput` opens the raw joined path again → `TestWriteReaderOutputRefusesEscapingArtifactDir` fails.
- Each test that pins a §4.7 failure case:
  - euid → `TestCheckPrivilege` and `TestServeRunRefusesUserModeWithoutRoot`;
  - unknown user → `TestCheckUnixUserRefusesUnknownWithUseradd` and `TestServeEnrollRefusesUnknownUser`;
  - no user → `TestUserModeRoundWithoutUnixUserHaltsNeedsYou`;
  - wrong owner → `TestUserModeRoundWithForeignOwnerRootHaltsNeedsYou`;
  - doctor → `TestServeChecksIsolationUser`.

## 6. The report must include

- Commands run, with results: the focused commands, `make check` and `gofmt -l`.
- `git diff --stat` against `origin/main`, compared with the files named above, including the spec and plan docs.
- Each mutation, with the failing test and its output line.
- Every golden moved.
- Coverage for the new and changed packages. Regenerate the baseline only if code moved between packages, and say so.
- Any `s.cfg.Runner` or `s.cfg.Git` use in `internal/serve` left unrouted, with the reason (for example, the availability probe's throwaway temp directory).
- Step 11's table row by row: confirm each row's treatment, or name the deviation and why.
- **Residuals: raw-path root operations left on purpose.** The report confirms each still holds, and adds any it found that this list misses:
  1. (h) `EnsureOutDir`: `out` is an entry of the root-owned binding dir.
  2. (p) `PruneWorktreeDirs`: rmdir never follows its final component, and every parent is root-owned.
  3. (q) `daemon.go:509-513`: the same as (h) and (p).
  4. (r) `lifecycle.go:430,461` `RemoveAll`: the parent is root-owned, and Go's Linux descent uses `openat(O_NOFOLLOW)`/`unlinkat`.
  5. (s) `cleanup.go:214,222`: the same as (r); the parent entry lives in root-owned `repos/`.
  6. (v) The `os.Stat` calls that gate a spawn: metadata only, and the child runs as the tenant.
  7. Stream, builder log, kill record, gate log, consult stream and prompt staging: they live in the root-owned binding dir. Root opens them with Lstat plus `O_NOFOLLOW` and hands the supervisor file descriptors, so no path passes through `out/` or `.worktrees/`.
  8. Client-side mirrors (`relevo/remotefetch.go`, `remote_catchup.go`, `remote_artifacts.go`, `remote_sync.go`): they run as the client user, not root on the server.
- What was not proven:
  - no real two-user run, because CI is not root;
  - setgroups with the credential is untested without root;
  - the chown callback is tested only with the test's own uid;
  - scopes are off in user mode.
