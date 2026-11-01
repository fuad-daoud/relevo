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
live one does. See "relevo history" and "relevo show" below.

## Cloud sync

Cloud sync keeps this machine's rows in a shared remote log, so several
machines share the same bindings. It is off until joined:
`relevo db sync enable` joins this machine to the remote,
`relevo db sync status` shows the exchange, and `push` and `pull` drive each
half by hand. `relevo help --json db sync` lists every verb (`enable`,
`disable`, `retry`, `status`, `push`, `pull`, `reconcile`, `unlock`, `invite`).
