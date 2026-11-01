# The database

relevo keeps a Turso-engine database at `$XDG_STATE_HOME/relevo/relevo.db`
(defaulting to `~/.local/state/relevo/relevo.db`), mode 0600, and it is the
record: the configuration sections and secrets, every binding with its round
log and rounds, gates, MasterMind records, and every file a closed round produced
(artifact rows, and transcript rows holding a builder round's rendered stream).
relevo never reads or records the MasterMind's own harness transcript. Nothing
else is a source of truth, and no verb needs it closed. The first start
converts a pre-Turso SQLite file in place and keeps the backup at
`relevo.db.pre-turso`.

One process opens the file: `relevo daemon`. Every other process -- every verb,
the lifecycle hooks, `mcp`, `wait`, the ui and `relevo serve` -- reaches it
through the owner socket `$XDG_STATE_HOME/relevo/relevo.sock` beside it, and a
process that finds no daemon starts one ("Running the daemon" below). The file
at another path (a test's root, a serve root) is still opened directly.
`relevo doctor` prints the database's `database` row, which names the route the
reader reached it by: `via owner <socket>` or `via file`.

The state root holds only what cannot be a row:

- `relevo.db`, with its `-wal` and `-shm`;
- `installation.json`, this installation's id and label (the origin stamped on
  the shared rows it writes), authoritative and never synced;
- `.daemon.lock` and the per-root `.lock`, the two flocks the daemon and every
  command take;
- `.worktrees/`, the git worktrees relevo creates;
- one directory per binding, holding the files of a round that is still open;
- on a server, `serve/repos/` (the bare repos), `serve/tmp/` (in-flight request
  bodies) and `serve/bindings/<owner>/` (one directory per owner).

Which tables and keys may leave this machine, and which paths must be read
together with their origin, is classified in
[`internal/db/migrations/SCOPES.md`](../internal/db/migrations/SCOPES.md).

Nothing else is used. A file dropped into `~/.config/relevo` is imported on the
next command and removed. `relevo doctor`
prints the database's `database` row: its path, its size, the schema version,
the live and archived binding counts, and the route it was read by.
`relevo db query` reads the database ad hoc, and `relevo db rename-repo` and
`relevo db relabel` repair a renamed remote and finished labels;
`relevo help --json db` lists the family.

`relevo history` and `relevo show` read the database for anything that is not a
live binding's open round, so a round from months ago renders the same way a
live one does (see [relevo history](commands.md#relevo-history) and
[relevo show](commands.md#relevo-show)).

## Cloud sync

Cloud sync keeps this machine's rows in a shared remote log, so several
machines share the same bindings. It is off until joined:
`relevo db sync enable` joins this machine to the remote,
`relevo db sync status` shows the exchange, and `push` and `pull` drive each
half by hand. `relevo help --json db sync` lists every verb (`enable`,
`disable`, `retry`, `status`, `push`, `pull`, `reconcile`, `unlock`, `invite`).

## Running the daemon

`relevo daemon` is the reconciler and the one process that opens `relevo.db`:
it watches builders, queues reports back to the MasterMind, and flags stalled
rounds. Every other process reaches the database through the daemon's owner
socket, so a command that finds no daemon starts one itself:

- `systemctl --user start --no-block relevo` when the user unit is installed;
- `launchctl kickstart` on macOS when the LaunchAgent is installed;
- otherwise a `relevo daemon` in a session of its own, appending its output to
  `$XDG_STATE_HOME/relevo/daemon.log`.

So the CLI works on its own -- you never have to start the daemon first -- and
reports are delivered even when you run a single verb by hand instead of
`relevo wait`.

You can just run `relevo daemon` in any spare terminal. To have it start with
your session:

```
make service     # systemd user unit on Linux, LaunchAgent on macOS
make uninstall   # stop it and remove both the binary and the unit
```

On Linux that installs `dist/relevo.service` to
`~/.config/systemd/user/relevo.service` and enables it. On macOS it renders
`dist/com.github.fuad-daoud.relevo.plist.in` into `~/Library/LaunchAgents/` and
loads it, logging to `~/Library/Logs/relevo.log`.

Only one daemon runs at a time. `relevo daemon` takes an exclusive lock on
`$XDG_STATE_HOME/relevo/.daemon.lock` and refuses to start if another one holds
it, so starting a second by hand next to the service is an error rather than
two reconcilers racing. `relevo daemon --check` exits 0 if a daemon is running
and 1 if not, printing nothing. `relevo daemon stop` sends SIGTERM to the
daemon's recorded pid and waits for it to exit, or says so when none is
running; a service-managed daemon is refused with the service command instead.
