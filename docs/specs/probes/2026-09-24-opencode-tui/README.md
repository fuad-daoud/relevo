# OpenCode 2.0.14 TUI plugin probe (#393)

Throwaway probe for relevo issue #393. Findings: `docs/specs/2026-09-24-opencode-tui-probe.md`.

## Re-running

```sh
bash docs/specs/probes/2026-09-24-opencode-tui/run.sh
```

`run.sh` needs `opencode`, `tmux` and `sqlite3` on `PATH`. It creates a throwaway
config directory under `.tmp/` (deleted on exit) containing:

- `opencode.jsonc` -- a symlink to the user's, for providers/permissions only
  (read-only use);
- `cli.json` -- the probe's own TUI settings (sidebar visible);
- `plugins/relevo-probe/` -- a copy of `relevo-probe.tsx` as `tui.tsx` plus a
  `package.json` with an `exports["./tui"]` entrypoint. On OpenCode 2.0.14 a
  file TUI plugin is a **package directory** under `<config>/plugins/` whose
  `exports["./tui"]` module default-exports `{ id, setup }`.

It then starts a **private** TUI in the single tmux session `relevo-probe` with
`opencode --standalone` (so the user's shared service and live sessions are
never touched), drives that TUI with `tmux send-keys`, and captures the pane
into `out/*.txt` (plain) and `out/*.ansi` (coloured). `out/` is wiped and
rebuilt on every run. Only slash commands are typed -- never a chat prompt --
so no model is ever called. The session and `.tmp/` are killed/removed on exit.

If `loaded.json` does not appear within 20 s under `OPENCODE_CONFIG_DIR`, the
driver retries once with `XDG_CONFIG_HOME` pointed at the throwaway directory,
and halts (recording why) if neither isolation method loads the plugin. It
never edits anything under `~/.config/opencode/`.

## Files

| file | purpose |
|------|---------|
| `run.sh` | the driver: temp config, tmux, keys, captures, cleanup |
| `relevo-probe.tsx` | the probe plugin (variant A, JSX `.tsx`) |
| `relevo-probe.js` | variant B, no JSX -- only written if A fails to render |
| `tui.jsonc` | the probe's TUI config as §2 of the plan describes it |
| `out/` | everything the run produced, committed |

The captures and JSON evidence (`out/`) are not in main: they live on the
local branch `relevo/oc-tui-probe` (commits 4e3f0978, d577e7d1, b818e47e,
a7d0757a). The findings doc cites them by file name.
