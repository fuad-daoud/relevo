# Plan: placement preference S4 — the `:servers` config view and the servers edit API

Seed: `docs/specs/2026-09-30-placement-preference-design.md` §7 (in this
worktree). Read it first. Where this plan and the spec disagree, halt and say
so; the spec is authoritative.

**Scope:** the cockpit's server config. This round adds the servers section to
the editable config doc, the edit APIs, and a `:servers` view: list, detail,
add, remove, and an on-demand health probe. It does not edit an actor's
placement list (the next round, S5), and it changes no bind/server behavior.

**Preconditions:** S1 (config field + cross-check) and S2 (probe states) are
merged; this branch starts from a main that carries both.

## 1. What changes

1. **`internal/relevo/configedit.go`** (+ its test file)
   - `ConfigDoc` gains `Servers map[string]remote.ServerEntry` (nil/empty when
     the section is absent); `LoadConfigDoc` reads `config.Servers` through the
     store and unmarshals it.
   - `AddServer(d, name string, e remote.ServerEntry) (ConfigEdit, error)`:
     refuse an empty name, refuse `local` (reserved), validate the entry by
     running `remote.ParseServers` on a one-entry body, then encode the whole
     map with `client.EncodeServers` (already exists; check its package rules —
     if importing it from `internal/relevo` is awkward, encode with
     `json.MarshalIndent` the way `roles.EncodeActors` does and say so).
   - `DeleteServer(d, name)`: refuse when an actor's `placement` names the
     server, naming the actors (the config cross-check is the backstop; this is
     the form's early, human message).
   - `EditServer(d, name string, e remote.ServerEntry)`: same validation,
     replacing one entry.
   - All three return a `ConfigEdit` whose `Sections` holds only
     `config.Servers` and whose `Message` follows the house shape ("add server
     zen", "delete server zen").
2. **`internal/ui/actions.go`** (+ `actions_test.go`'s `fakeActions`)
   - `Actions` gains `ServerProbes(ctx context.Context) []relevo.ServerProbe`.
   - `mastermindActions.ServerProbes` loads the servers from the runtime's
     config store and calls `relevo.ProbeServers(ctx, a.runtime(), servers,
     "")`. `fakeActions` returns a canned slice. A nil `Config` or runtime
     answers an empty slice; the view must render that as "no servers
     configured", not as an error.
3. **`internal/ui/view_servers.go`** (new; follow `view_agents.go` for the
   table, `form.go` for the add form, `confirm.go` for delete, and
   `view_log.go`'s fetch command for the probe):
   - table rows: name, url, state from the probe (enrolled as <label> /
     not enrolled / unreachable / cert changed / no key / error), fingerprint
     abbreviated; a row with no probe yet shows "—".
   - `newServersView(env)` loads the doc and returns a probe command; a
     `r` key re-probes on demand (the probe is network work and only ever runs
     in a `tea.Cmd`, never in `Body`).
   - `a` opens the add form (name, url, fingerprint, ca, insecure), `d`
     deletes with confirmation, `e` edits the selected entry's url and trust
     fields.
   - `writeError`/`Result` handling exactly like the agents view: one
     `ApplyConfig` call per edit, then reload the view.
   - Key help and crumbs (`servers`) follow the other views.
4. **`internal/ui/cmdline.go`** (+ its test): a `servers` entry in the
   `commands` table, help "remote builders' servers, and their health".
5. **`internal/ui/view_rounds.go`**: dispatch `:servers` to `newServersView`
   like `:agents` is dispatched; a nil `Actions` hides the edit keys exactly
   as the other config views do.
6. **README**: where the cockpit commands are listed (`:actors`, `:agents`,
   ...), add `:servers` and one sentence; document that the client key stays
   `relevo config server key`, the view never edits key material.
7. **Plan file**: save this plan as
   `docs/plans/2026-09-30-placement-s4-servers.md` and commit it.

## 2. Ordered steps (done-when)

1. `ConfigDoc.Servers` + the three edit APIs + tests (`internal/relevo`).
2. `Actions.ServerProbes` + fake + tests (`internal/ui`).
3. `view_servers.go` + tests; `:servers` in the command table and dispatch.
4. README.
5. `make check` once; mutation pins; commit (message like
   `feat(ui): a servers view, and the servers section is editable`).

## 3. Tests

Follow the existing UI test harnesses (`view_agents_test.go`,
`actions_test.go`'s `fakeActions`, `cmdline_test.go`) and the configedit
tests.

- **configedit**: add/remove/edit a server round-trips through `ConfigEdit`
  and the written body; `local` refused; invalid url/trust refused with a
  `FieldError`; delete refused while an actor names the server and accepted
  once it does not; editing one entry leaves the others byte-identical.
- **view_servers**: rows render name/url/state from a canned probe; "no
  servers" and "no key" render as text, not errors; add submits an
  `ApplyConfig` with the servers section alone; delete asks for confirmation;
  a nil `Actions` hides `a`/`d`/`e` (the pane-without-actions convention).
- **cmdline**: `:servers` completes and dispatches.
- **actions**: the fake's canned probes reach the view; `ServerProbes` on a
  nil runtime is empty, not a panic.

## 4. Mutation pins

Each must fail its named test, then be reverted.

- Remove `Servers` from `LoadConfigDoc` → the view's row test fails.
- Drop the delete guard that names actors → the delete-refused test fails.
- Make `ServerProbes` run inline in `Body` instead of a `tea.Cmd` — pin this
  by asserting the load command exists / the body renders the "—" state
  before the probe message; if that is not observable in the harness, say so
  in the report instead of forcing it.

## 5. Files this round touches (closed)

1. `internal/relevo/configedit.go`, `internal/relevo/configedit_test.go`
2. `internal/ui/actions.go`, `internal/ui/actions_test.go`
3. `internal/ui/view_servers.go` (new), `internal/ui/view_servers_test.go` (new)
4. `internal/ui/cmdline.go`, `internal/ui/cmdline_test.go`
5. `internal/ui/view_rounds.go`
6. `README.md`
7. `docs/plans/2026-09-30-placement-s4-servers.md` (new, this plan)

If a file outside this list needs a change (pipeline pins, a shared UI helper),
halt and name the minimal addition rather than improvising.

## 6. Rules

- `make check` is the gate; run it once at the end. On this server host it may
  take longer than on the laptop.
- Comments say why, never what; no issue numbers, no "round N", no spec
  citations in code.
- Functions ≤ 70 lines, non-test files ≤ 600; no new exclusions.
- Coverage: report whether the baseline moved; never lower it.
- No key material in the view: never decode, display or write `client.key`.

## 7. What the report must include

- `git diff --stat` compared against §5; anything outside is a halt.
- One rendered `:servers` table (name, url, state) from a test.
- For each mutation in §4: the mutation and the named failing test.
- `make check` result and coverage numbers.
- Confirmation that the plan is committed and the tree is clean.
