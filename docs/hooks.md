# Lifecycle hooks

relevo supports user-defined hook commands dispatched during binding lifecycle
events. When state changes or a new round begins, `relevo daemon` runs the argv
lists in the `hooks` section, keyed by event type. Set one with:

```
relevo config set hooks.state_changed '[["/path/to/script"]]'
```

A directory of executable scripts dropped into
`$XDG_CONFIG_HOME/relevo/hooks/<event_type>.d/` (default
`~/.config/relevo/hooks/<event_type>.d/`) is imported into the `hooks` section
on the next command and removed; the scripts themselves are never removed.

### Supported events

- `state_changed` — fires whenever a binding transitions between states (`active`, `needs_you`, `broken`, `done`, `paused`).
- `round_started` — fires whenever a new round starts.
- `builder_stalled` — fires once when a live local builder's tree and stream or screen have been quiet for `stall_after_ms` (#252, generalised by #135). Clearing the stall fires nothing.
- `binding_stale` — fires once when a `NEEDS YOU` binding has sat unacted for `stale_after_ms` (#135). Clearing the stamp fires nothing.

### Hook execution & environment

Each hook command is executed asynchronously in a detached process with a 10-second timeout. relevo injects the following environment variables:

- `RELEVO_EVENT`: The event type name (`state_changed`, `round_started`, `builder_stalled`, `binding_stale`).
- `RELEVO_BINDING`: The name of the binding.
- `RELEVO_STATE`: The current state of the binding.
- `RELEVO_OLD_STATE`: The previous state of the binding.
- `RELEVO_ROUND`: The current round number.

Hook stdout, stderr, and execution failures are recorded in the database's hook run log. `relevo doctor` prints a `hooks` row -- `hooks: N runs, M failed in the last 24h (last: <event>: <err>)`.

Each hook script must have its executable bit set (`chmod +x`). An event with no argv list in the `hooks` section is a silent no-op.

### Webhooks

Beside hook scripts, the policy section's `notify.webhooks` posts lifecycle events straight to a URL -- a Slack incoming webhook, a Discord webhook, or any endpoint that accepts a JSON POST -- with no script required:

```json
"notify": { "webhooks": [
  { "url": "https://hooks.slack.com/services/…", "format": "slack", "events": ["state_changed:needs_you", "binding_stale", "builder_stalled"] }
] }
```

- `url` (required) -- where the event is POSTed; must be `http://` or `https://`.
- `events` -- which events reach this webhook; omit (or leave empty) to receive every event. `state_changed:<state>` matches only a `state_changed` event whose new state is `<state>` -- `state_changed:needs_you` is the one most people want.
- `format` -- `json` (default: the event and its rendered text as a JSON object), `slack` (`{"text": ...}`), or `discord` (`{"content": ...}`).

One sentence per event name, for a webhook filter:

- `state_changed` -- a binding transitioned between states; filter to one target state with `state_changed:<state>`.
- `round_started` -- a new round began.
- `fork_created` -- a new binding was branched from an earlier round of an existing binding.
- `builder_stalled` -- a live headless builder's stream went quiet for `stall_after_ms`.
- `binding_stale` -- a NEEDS YOU binding sat unacted for `stale_after_ms`.

Each matching webhook POSTs in its own goroutine with a 5-second timeout and never blocks a daemon tick. A failure -- a non-2xx response or a transport error -- is logged to the hook run log with the URL's host only, never the full URL, since a webhook URL is a secret. There are no retries and no queue: a webhook is best-effort, exactly like a hook script.

## Setting up your agent harnesses

A harness needs nothing installed beyond its own binary on `PATH` and the agent
definitions relevo installs (`relevo config agents`). relevo starts each builder
as a non-interactive process and reads the round from the harness's own stream,
so there is no lifecycle hook to install for it.

### opencode permission allowlist

relevo stages prompts and reports under `~/.local/state/relevo/<binding>/`, outside
the repo the builder is working in, so a fresh opencode builder blocks on an
"Access external directory" dialog on its first round. To skip it entirely, add
this to `~/.config/opencode/opencode.jsonc`:

```jsonc
"permission": {
  "external_directory": {
    "/home/you/.local/state/relevo/*": "allow",
    "/home/you/.local/state/relevo/**": "allow"
  }
}
```

Substitute your real home directory: opencode does not expand `~` or `$HOME`
in these patterns. Claude builders (`claude/anthropic/sonnet`) have their own permission model
and are not covered by that entry.

relevo doctor warns when this entry is missing (row external_directory under opencode).
