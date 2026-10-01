package relevo

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/db"
)

// OpencodeAuth is the install-global opencode credential row. opencode keeps
// one active login per integration for the whole install, so relevo cannot
// select a login per process the way claude and codex select a home; it reads
// which login is active and, when a limit leaves that login unusable, flips the
// row. The seam exists so tests drive a fake and nothing is spawned.
type OpencodeAuth interface {
	// Active returns the label of the credential row opencode currently uses
	// for integration, or "" when none is recorded.
	Active(ctx context.Context, integration string) (label string, err error)
	// Switch makes label the active credential for integration.
	Switch(ctx context.Context, integration, label string) error
}

// OSOpencodeAuth returns the production seam, backed by the opencode binary on
// PATH and its own database. It runs `opencode auth switch` to flip a row and
// `sqlite3 -readonly` to see which row is active; nothing else on the host
// changes.
func OSOpencodeAuth(dbPath string) OpencodeAuth { return osOpencodeAuth{dbPath: dbPath} }

// osOpencodeAuth runs the opencode CLI. Active reads the credential table: the
// list command reports every row but not the active one, so the database is the
// only surface that names it.
type osOpencodeAuth struct{ dbPath string }

// opencodeActiveQuery names the active credential row of one integration. The
// sqlite3 CLI cannot bind parameters, so the integration is quoted as a
// literal.
func opencodeActiveQuery(integration string) string {
	return "select label from credential where active = 1 and integration_id = " + sqlQuote(integration) + " limit 1"
}

// sqlQuote renders a value as a single-quoted SQL literal.
func sqlQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func (o osOpencodeAuth) Active(ctx context.Context, integration string) (string, error) {
	if o.dbPath == "" {
		return "", fmt.Errorf("opencode auth: no database path")
	}
	out, err := exec.CommandContext(ctx, "sqlite3", "-readonly", o.dbPath, opencodeActiveQuery(integration)).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("opencode auth: read %s: %w: %s", o.dbPath, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (osOpencodeAuth) Switch(ctx context.Context, integration, label string) error {
	_, err := opencodeRun(ctx, "auth", "switch", "--standalone", integration, label)
	return err
}

// opencodeRun runs one opencode subcommand and returns its combined output.
func opencodeRun(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "opencode", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("opencode %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// opencodeActiveKey is the kv row relevo keeps the active account in, one per
// integration: opencode's row is global to the install, so this is the only
// per-account state relevo can hold for it.
func opencodeActiveKey(integration string) string { return "opencode-active:" + integration }

// opencodeActiveRow is the row's body. The label is opencode's own name for
// the credential, so it compares directly with what the list command reports.
type opencodeActiveRow struct {
	Label string `json:"label"`
}

// readActiveAccount returns the label relevo last recorded as active for
// integration, or "" when nothing is recorded or the row cannot be read.
func readActiveAccount(kv db.KV, integration string) string {
	if kv == nil {
		return ""
	}
	raw, ok, err := kv.KVGet(opencodeActiveKey(integration))
	if err != nil || !ok {
		return ""
	}
	var row opencodeActiveRow
	if json.Unmarshal(raw, &row) != nil {
		return ""
	}
	return row.Label
}

// writeActiveAccount records label as the active account for integration. A
// missing kv store drops the write: the record is bookkeeping, never a
// reason to refuse the flip that already happened.
func writeActiveAccount(kv db.KV, integration, label string) error {
	if kv == nil {
		return nil
	}
	raw, err := json.Marshal(opencodeActiveRow{Label: label})
	if err != nil {
		return err
	}
	return kv.KVPut(opencodeActiveKey(integration), raw)
}

// switchOpencodeActive points the install-global opencode row at to, when the
// login opencode is actually using is gated. It re-reads the active row first,
// because a human running `auth switch` beside relevo desynchronises anything
// relevo remembered; the drift is logged and the row is left alone unless the
// login in use is itself gated, so a healthy round is never moved.
func switchOpencodeActive(ctx context.Context, rt Runtime, to account.Account, pool []account.Account, gates []string) error {
	if rt.OpencodeAuth == nil || to.Harness != account.OpenCode {
		return nil
	}
	actual, err := rt.OpencodeAuth.Active(ctx, to.Integration)
	if err != nil {
		// The flip is what makes the rotation usable; an unreadable row must
		// not block it. The drift check is skipped, and the row relevo set is
		// still recorded below.
		slog.Warn("opencode active account unreadable; flipping anyway", "integration", to.Integration, "err", err)
		actual = ""
	}
	recorded := readActiveAccount(rt.Gates, to.Integration)
	if actual != "" && actual != recorded {
		slog.Warn("opencode active account drifted", "integration", to.Integration, "recorded", recorded, "active", actual)
	}
	// An empty row has nothing healthy on it; any other row is moved only
	// when it is gated, because every round on a gated login is already
	// limited and no healthy round is.
	if actual != "" {
		current, ok := accountByLabel(pool, actual)
		if !ok || !account.Gated(current, gates) {
			return nil
		}
	}
	if err := rt.OpencodeAuth.Switch(ctx, to.Integration, to.Label); err != nil {
		return err
	}
	return writeActiveAccount(rt.Gates, to.Integration, to.Label)
}

// ensureOpencodeActive restores the recorded account before a round resumes on
// it: a session id only resolves under the login that wrote it, so a row a
// human moved meanwhile must be put back. A gated recorded account is left
// alone -- resuming onto a limited login would only re-limit the round.
func ensureOpencodeActive(ctx context.Context, rt Runtime, rec account.Account, gates []string) {
	if rt.OpencodeAuth == nil || rec.Harness != account.OpenCode {
		return
	}
	if account.Gated(rec, gates) {
		return
	}
	actual, err := rt.OpencodeAuth.Active(ctx, rec.Integration)
	if err != nil {
		slog.Warn("opencode active account unreadable on resume", "integration", rec.Integration, "err", err)
		return
	}
	if actual == rec.Label {
		return
	}
	if err := rt.OpencodeAuth.Switch(ctx, rec.Integration, rec.Label); err != nil {
		slog.Warn("could not restore opencode account on resume", "integration", rec.Integration, "label", rec.Label, "err", err)
		return
	}
	if err := writeActiveAccount(rt.Gates, rec.Integration, rec.Label); err != nil {
		slog.Warn("could not record restored opencode account", "integration", rec.Integration, "err", err)
	}
}

// accountByLabel finds the pool account opencode reports by its label, so the
// drift check can ask whether the row in use is gated.
func accountByLabel(pool []account.Account, label string) (account.Account, bool) {
	for _, a := range pool {
		if a.Label == label {
			return a, true
		}
	}
	return account.Account{}, false
}

// accountByName finds the pool account a binding recorded by its name.
func accountByName(pool []account.Account, name string) (account.Account, bool) {
	for _, a := range pool {
		if a.Name == name {
			return a, true
		}
	}
	return account.Account{}, false
}
