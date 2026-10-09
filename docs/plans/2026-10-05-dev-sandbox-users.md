# Plan: separate Unix users as relevo dev sandboxes (#1049)

## What I could and could not read

I could not read the five research outputs (rs-state, rs-scopes, rs-daemon, rs-sharing, rs-creds). `relevo show` needs approval on both the command line and the MCP tool, and finished bindings have no files left under `~/.local/state/relevo/rs-*`. This plan uses the seed's summary of their decisions and checks it against the code.

Two places where the seed and the code differ:

1. **The daemon opens no TCP port.** `dist/relevo.service` runs `relevo daemon --interval 2s`. That process serves only the database socket at `<stateRoot>/relevo.sock` (`internal/db/wire/owner/listen.go:15-25`) and the pprof socket. `--listen` is a `relevo serve` flag (`cmd/relevo/serve.go:60`, default `:7777`), and the board uses `127.0.0.1:0`. So the "unique `--listen` port" decision only matters for a sandbox that also runs `relevo serve`. The port plan below keeps the port but applies it to serve only, and the default-port doctor row reads serve's recorded listen address.
2. **"Scopes are already off in user mode" is not checked.** Nothing in this plan relies on it, so the builder does not need to either.

The seed's "keep the socket path short" is real: `SocketPath` refuses a path over the `sun_path` limit. That is why the sandbox home is short (`/home/rv-<name>`).

## Behaviour

**`scripts/relevo-dev-user.sh` (POSIX sh, run as root)** has four subcommands:

- **`create <name> [--port N] [--repo URL] [--ref REF]`**
  - Checks `<name>` against `^[a-z][a-z0-9-]{0,11}$`. The user is `rv-<name>` with home `/home/rv-<name>`.
  - Refuses if the user already exists.
  - Refuses `--port 7777` and any port another sandbox already recorded.
  - Steps, in order:
    1. `useradd -m`.
    2. `loginctl enable-linger`.
    3. Waits for `/run/user/<uid>`.
    4. As the user, under a clean environment (`env -i HOME=… USER=… LOGNAME=… PATH=… XDG_RUNTIME_DIR=/run/user/<uid>`, with no inherited `XDG_*`, `RELEVO_*` or `CLAUDE*`): `git clone` its own checkout from `--repo` (default: the invoking checkout's `origin` URL), checks out `--ref` (default `origin/main`), then `make service`. That installs `~/.local/bin/relevo` and `~/.config/systemd/user/relevo.service`, then reloads, enables and restarts it.
    5. Writes the sandbox marker `<stateRoot>/sandbox`, recording name and port, owned by the user.
    6. Prints the credential steps it cannot do itself: `sudo -iu rv-<name>`, then the harness logins, `relevo config agents`, the opencode `external_directory` allowlist for the user's own state root, and `relevo doctor`.
  - With `--port`, it also prints the `relevo serve --listen :N` line, or installs `dist/relevo-serve.service` with that port as a drop-in.
- **`destroy <name>`**: in order, `systemctl --user disable --now relevo.service` (and relevo-serve if present), `loginctl terminate-user`, `loginctl disable-linger`, `userdel -r`. This is the rollback; prod state is never touched.
- **`list`**: every `rv-*` user with its port, linger state and unit state.
- **`--dry-run`** on every subcommand prints the commands without running them and without needing root. The script test relies on it.

Cases:

- Refuses to run as non-root unless `--dry-run`.
- Refuses a name that would push `/home/rv-<name>/.local/state/relevo/relevo.sock` over 104 bytes.
- `destroy` of an unknown user is an error. `destroy` never touches the invoking user or any non-`rv-` user.

**Doctor (pure functions in `internal/doctor`, wired in `cmd/relevo`)**: three new global rows.

- **`xdg` row: runs for every user.**
  - Warn if `XDG_STATE_HOME` or `XDG_CONFIG_HOME` is set and the resolved relevo state root or config root exists and is owned by another uid. That is the inherited-parent-environment case. Fix: `unset XDG_STATE_HOME XDG_CONFIG_HOME`, or log in as the user.
  - OK otherwise.
- **`linger` row: only when the sandbox marker exists.**
  - Warn if `/var/lib/systemd/linger/<user>` is missing. Fix: `loginctl enable-linger <user>`.
  - OK otherwise.
- **`port` row: only when the sandbox marker exists and the serve pointer (`serve.ReadDaemonPointer`) is present.**
  - Warn if its listen port is 7777. Fix: `relevo serve --listen :<marker port>`.
  - Also Warn if the port differs from the marker's port.
- With no marker there is no linger row and no port row, so prod machines gain at most the `xdg` row.
- None of the three rows reaches bind preflight: `bindPreflightDefs` calls `doctor.Run` alone, and these rows are added in `doctorReport`.

## Seams

- `internal/doctor/env.go:17-41`: the `Env` interface. Add nothing to it if you can avoid it. `Stat` and `ReadFile` cover the linger file and the marker. Owner uid needs a new `Env` method (e.g. `StatOwner(path)`) built on `statOwner` in `internal/doctor/stat_unix.go`, plus a stub in `stat_other.go`, and the fakes in `doctor_test.go` updated.
- New file `internal/doctor/sandbox.go`: `XDGCheck`, `LingerCheck` and `PortCheck`, each taking plain inputs (env values, uid, roots, marker contents, pointer listen string) and returning a `Check`. Also `ParseSandboxMarker`.
- `cmd/relevo/doctor.go:256-…` (`doctorReport`): add one call to a new helper `sandboxChecks(stateRoot)` in `cmd/relevo/doctor_checks.go`. The helper reads the marker, the pointer (via `rt.Store`'s DB, if open) and `user.Current()`, then appends the rows with `insertGlobalCheck`. `doctorReport` is already long; do not inline the logic. `doctor_checks.go` is at 577 lines and must stay at or under 600, so if the helper does not fit, put it in a new file `cmd/relevo/doctor_sandbox.go`.
- `internal/store/store.go:149` (`DefaultRoot`) and `cmd/relevo/main.go:310` (`userConfigRoot`): read them, do not change them. Compose the config root through `userConfigRoot()`.
- `Makefile:132-155` (`install` / `service`): unchanged. The script calls `make service` as the sandbox user.
- `scripts/`: new `relevo-dev-user.sh` and `relevo-dev-user_test.sh`. `check-scripts` picks up `scripts/*_test.sh` and shellcheck covers `scripts/*.sh` without any Makefile edit.
- Docs:
  - New `docs/dev-sandbox.md`: setup, per-user clone and why the checkout is never shared (branch collision, fetch refusal, prune/ref deletion, hook exec, safe.directory), port plan (prod serve 7777, sandboxes 7801+), lingering, credentials, rollback (`destroy`, i.e. `userdel -r`), socket-length limit.
  - One line in `docs/runbook.md` §"Installing and deploying" (line 52) pointing to it.
- Plan file: `docs/plans/2026-10-05-dev-sandbox-users.md` (this plan) ships in the same PR.

## Steps

1. **`internal/doctor/sandbox.go` and its tests.** Add the three check functions and the marker parser. Done when `go test ./internal/doctor -run 'XDG|Linger|Port|Sandbox'` passes and each Warn branch fails a named test if mutated.
2. **`Env` owner lookup.** Add `StatOwner` to `Env`, `realEnv` and the test fakes. Done when `go test ./internal/doctor` is green.
3. **Wiring.** Add `sandboxChecks` in `cmd/relevo` and call it from `doctorReport`. Done when a cmd/relevo test writes a marker under the TestMain temp state root and sees the linger and port rows, and a test without the marker sees neither. These tests run `doctorReport`/`sandboxChecks` directly and never execute a subcommand that spawns a harness or reaches the network.
4. **The script.** Write `scripts/relevo-dev-user.sh` with `create`, `destroy`, `list` and `--dry-run`. Done when `shellcheck scripts/relevo-dev-user.sh` is clean.
5. **The script test.** `scripts/relevo-dev-user_test.sh` runs `--dry-run` only and asserts:
   - a bad name is rejected;
   - port 7777 is refused;
   - a too-long socket path is refused;
   - the `create` plan runs as the target user under `env -i` with no `XDG_` passthrough;
   - linger comes before `make service`;
   - `destroy` order;
   - non-root without `--dry-run` refuses.

   Done when `sh scripts/relevo-dev-user_test.sh` passes.
6. **Docs.** Write `docs/dev-sandbox.md` and the runbook pointer. Done when every flag the script accepts appears in the doc.
7. **Plan file.** Save this plan to `docs/plans/2026-10-05-dev-sandbox-users.md`.
8. **Full check.** Done when `make check` is green: gofmt, vet, lint, comments (the new files are not on any allow list, so no history or issue numbers in comments or test names), file size, coverage. Do not touch the coverage baseline; no code moves between packages.

**Commands:**
- Focused, while iterating: `go test ./internal/doctor ./cmd/relevo -run 'XDG|Linger|Port|Sandbox' && sh scripts/relevo-dev-user_test.sh`
- Full check, once at the end: `make check`
- A real `create`/`destroy` needs root and a live machine. That is the MasterMind's job, not the builder's.

All work goes in new commits. Never amend or rebase anything already pushed.

## Deleted behaviour

None.

## What the report must include

- `git diff --stat`, matching the scope: `internal/doctor/{sandbox.go,sandbox_test.go,env.go,stat_*.go,doctor_test.go}`, `cmd/relevo/{doctor.go,doctor_checks.go or doctor_sandbox.go,*_test.go}`, the two scripts, `docs/dev-sandbox.md`, `docs/runbook.md`, and the plan file.
- The `make check` result, and confirmation that the allow lists and the coverage baseline are unchanged.
- One mutation per new doctor row, each with the named test that failed.
- The `--dry-run` output of `create demo --port 7801` and `destroy demo`.
- Which doctor rows a plain (no-marker) user now sees.
- Any deviation from the two seed differences above.
