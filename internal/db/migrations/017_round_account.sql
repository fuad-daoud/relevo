-- Schema v17: the login a round drew from. A round's candidate names the
-- provider and model; several logins can serve one provider, and a limit is
-- enforced per login, so the account is a fact about the round the candidate
-- column cannot carry. Nullable: every round recorded before this migration,
-- and every round on a host with no accounts, carries none.
--
-- Compatibility rules: internal/db/migrations/README.md. Add-only and
-- Turso-safe like 001-016: ALTER TABLE ADD COLUMN only, relying on
-- applyOneMigration's version guard because ADD COLUMN has no IF NOT EXISTS.

ALTER TABLE round ADD COLUMN account TEXT;
