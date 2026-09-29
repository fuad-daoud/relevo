# Research for #680: the daemon owns relevo.db

Three read-only research rounds behind `docs/specs/2026-09-29-db-owner-design.md`,
run 2026-09-29 by lite-planner and builder runners and kept as written.

- `usage.md` -- every database open, transaction, SQLite dependency and value
  size; ends with the requirements a proxy must meet. Base `442fc598`.
- `protocol.md` -- Hrana, rqlite, PG/MySQL wire and a custom protocol against
  those requirements; recommends the custom protocol. Base `f74996fa`.
- `lifecycle.md` -- daemon start, re-exec, serve hosts, version skew, tests and
  failure modes, with measurements. Base `8e0b0edd`.

Where a report and the spec disagree, the spec holds. In particular,
`lifecycle.md` recommends "the lock holder owns the database"; the spec chose
"the daemon always owns it" (decision D3), because a host can run both
`relevo serve` and a daemon on one machine database.
