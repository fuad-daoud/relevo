# Plan: a busy BEGIN IMMEDIATE must not poison the retry (#1284)

## Finding

Reproduced on the default (Turso) build:

- Lock held still (one handle holds a write tx, the other runs BEGIN IMMEDIATE
  with a 20 ms busy_timeout), 30 of 30 attempts: the first BEGIN fails
  `database is locked`, the second is busy again, and ROLLBACK returns
  `cannot rollback - no transaction is active`. No poisoning.
- Commit storm (one handle commits nonstop, the other loops BEGIN IMMEDIATE on
  one conn), 3,982 tries: 985 busy and 1 poisoned. The next BEGIN failed
  `cannot start a transaction within a transaction`; ROLLBACK returned nil and
  cleared the conn. The engine can leave a conn inside a transaction when a busy
  BEGIN races a commit. It is rare and timing-dependent.
- busy_timeout=0 hangs the probe; no test uses it.

## Fix

After every BEGIN IMMEDIATE that fails busy, run ROLLBACK on the same conn
before retrying or giving up. Only the engine's "no transaction is active" reply
is ignored; any other ROLLBACK error ends the tx with `db: tx begin: ...`. One
helper, `beginImmediate` in `internal/db/db.go`, serves `DB.tx`, the migration
and the utf8 repair, which all hand a conn back to the pool after a failed BEGIN.

## Tests

A package-level `beginHook` makes the busy-begin failure deterministic: it
starts a plain BEGIN on the conn and reports busy. Tests pin the retry, the
deadline give-up, the migration and the utf8 repair, and
`TestTxRetriesABusyBegin` releases its holder on a channel instead of a sleep.
`flake-hunt.yml` lists the new tests.
