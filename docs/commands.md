# Command surface

- `relevo bind [--name N] [--candidate CANDIDATE] [--actor R] [--tier T [--allow-yolo]] [--gate CMD|--no-gate] [--regate N] [--resume [--rebind]] [--local] [--timeout D] --feature L|--no-feature [--ticket REF] [--mastermind P]`
  — start a binding between the calling MasterMind and a builder. A fresh bind
  must name exactly one of `--feature <label>` (a label grouping it with other
  bindings) and `--no-feature` (it serves no feature); naming neither or both is
  refused, exit 2, before any state, worktree or network work. `--ticket REF` is
  optional and allowed with either: a number, `#N`, `owner/repo#N`, an issue
  key like `ABC-123`, or an issue URL, stored as `owner/repo#N` when relevo
  knows the repository; any other http(s) URL is stored whole. `--candidate` is
  a candidate token; `--actor R` is the writer actor the binding runs (default
  `builder`). A name that already exists is refused rather than reused:
  only the binding's record would be rewritten, so a fresh round 1 would
  collide with the previous session's round log. `--resume --name N` re-points that
  existing binding's MasterMind side at the calling MasterMind without touching the
  builder; on a resume `--feature` sets the label, `--no-feature` clears it, and
  naming neither keeps it, while `--ticket` sets one. `relevo unbind N` is the other way out.
- `relevo board [path] [--board NAME] [--mastermind M] [--theme NAME] [--no-open]` — open
  a board: a single HTML page served on `127.0.0.1` with a per-run token. A bare
  `relevo board` opens the live board of the calling MasterMind under the state
  root, and a path ending in `/board.html` opens one board in the repo. A path
  ending in `.excalidraw` opens the
  [Excalidraw](https://excalidraw.com) whiteboard instead, saving the scene and
  (for a repo scene) a companion `.svg` beside it. `relevo board url` prints a
  live board's URL and `relevo board promote` copies a live board into the repo.
  Foreground; Ctrl-C stops. See "relevo board" below.
- `relevo board comments [path|--board NAME] [--json]` — list every comment in the
  resolved scene in scene order (`id, x, y, text, by, at`); `--json` is one compact
  array. `relevo board comment [path|--board NAME] --text S [--x X --y Y] [--by B]`
  — append one comment element, leaving every other byte of the scene unchanged.
  See "relevo board" below.
- `relevo board text <file> [--json]`, `relevo board annotate <file> --text S
  [--x X --y Y]` — list the scene's text elements, or append one without
  touching the rest of the scene. See "relevo board" below.
- `relevo send [NAME|--name N] --file PATH [--dry-run] [--tier T [--allow-yolo]] [--candidate CANDIDATE] [--verify|--no-verify] [--regate N] [--force]` — stage the file as the current round's
  prompt and hand it to the builder as the prompt of a fresh process started in
  the binding's tree. A planner actor's prompt is a seed, capped at 4 KiB, and a
  larger one is refused unless `--force`.
  A headless binding whose previous round's process is still running refuses
  the send; wait for the round to close or `relevo done` it.
  `--dry-run` checks every precondition a send would and prints what it would
  do, writing nothing: no prompt staged, no log entry, no process started.
  A precondition that fails is the same error `relevo send` gives, exit
  1, with nothing written. A local binding names the exact command line it
  would run, and a remote one the server and branch without contacting it:
  ```
  would send round 5 to api-auth
    builder   headless agy/google/gemini-3.8-flash-high
    where     /usr/bin/agy -p
    tier      yolo
    prompt    /home/me/.local/state/relevo/api-auth/005-prompt.md  (staged from ./plan.md, 4.1 KiB)
    report    /home/me/.local/state/relevo/api-auth/005-report.md
    marker    /home/me/.local/state/relevo/api-auth/005-done
    head      relevo: round 5 · to builder "api-auth" · from the MasterMind (not the human)
              Your working tree is: /home/me/.worktrees/api-auth
  ```
- `relevo show NAME --diff [--round R] [--stat] [--drift] [--anchors]` — print a round's
  captured patch to stdout, or its diffstat summary with `--stat`. Pass `--drift`
  to inspect between-rounds drift instead of the round's diff; `--drift` composes
  with `--stat` and `--round`, and defaults to the currently open round where plain
  `relevo show --diff` defaults to the newest completed one. Pass `--anchors` to prefix
  each hunk and each `' '`/`'+'` line with its `path:line`, ready to quote into a
  review comment (see "Reviewing a round" below).
- `relevo status [NAME|--name N] [--json] [--all] [--line]` — one row per binding: round, display state, the builder's own status, the last relayed event and anything pending. `--line` is the one-row-per-binding form Claude Code's status line runs (see [Status line](../README.md#status-line)). Rows are attention-first -- NEEDS YOU, ACTIVE, PAUSED, DONE, stale first within a group, newest last-event first -- the same order `relevo ui` has always used, so the two never disagree. Naming a binding shows only that one. Bindings marked DONE are hidden by default and the footer names how many are hidden.
  While a round is open a row also shows the round's live diff against its baseline (`+120/-30 in 6`, `(shared tree)` for a `--cwd` binding sharing the MasterMind's own working tree), an ACTIVE row's `quiet <age>` since its last progress sample, and `●new` when the binding's newest report is unread (its record's `viewed_at` stamp is older than the newest report).
  `--json` also carries fields the prose above does not spell out:
  ```
  branch      the binding's worktree branch; absent for a --cwd binding
  waiting     set when the binding is stalled on a human: cause, line, since, hint
  last_seq    the Seq of the binding's newest log entry; 0 when the log is empty
  live        the round's live diff stat against its baseline tree (files/added/removed,
              shared true for a --cwd binding); absent when no round is open
  quiet_for   an ACTIVE row's age since its last progress sample; absent otherwise
  unread      true when the binding's newest report is newer than its .viewed stamp
              (or there is no stamp and a report exists)
  ```
- `relevo show NAME [--round N] --log [--after N] [--follow] [--json]` — the binding's append-only round log. Every entry carries a 1-based `seq`, monotonic within the binding and never rewritten; `--round N` shows one round, `--after N` shows only entries with a greater `seq`, `--json` prints one compact JSON object per line (NDJSON, `seq` included), and `--follow` keeps printing new entries until the binding is DONE or gone. A file written before `seq` existed reads back with `seq` equal to the line number, so nothing is rewritten.
  A hook or script that has already seen up to a known `seq` asks only for the rest:
  ```bash
  last_seq=$(relevo status --json | jq -r '.bindings[] | select(.name == "NAME") | .last_seq')
  relevo show NAME --log --after $(last_seq) --json
  ```
- `relevo history [--here] [--binding B] [--mastermind P] [--since D] [--limit N] [-q "<query>"] [--json] [--rows]` —
  round history as a JSON array across every binding relevo has ever recorded, live or archived, newest first. `-q` filters with the query language. See "The database" below.
- `relevo show <name> [--round N] [--prompt|--report|--diff [--stat|--anchors]|--drift|--log [--follow --after N]|--transcript|--gate|--findings <id>|--output] [--json]` —
  one round's prompt, report, diff, drift, log, transcript, gate log, output file or a consult's findings: from a live binding's open round files, or, for anything sealed or archived, from the database. See "The database" below.
- `relevo wait [NAME|--name N] [--any N1 N2 ...] [--round R] [--timeout D] [--peek]` — block
  until the round closes or the binding needs you, reading relevo's own state only.
  Exit 0: closed on the marker, stdout is the report path. 2: closed
  without it (`unmarked`, `noreport` — verify before trusting), report
  path or `-`. 3: needs you, stdout is one line saying what it is waiting on. 4:
  the binding is DONE or was unbound. 5: closed, but the builder's report says
  halted or blocked -- read it before sending again; stdout is the report path.
  6: the round has no prompt entry -- it was never sent, so nothing is in flight;
  stdout says so. 124: `--timeout` (default 10m) elapsed. On every exit but 4 and
  124 the wait then prints a blank line and the oldest pending report's text
  (the report's pointer line, a blank line, then the report itself, capped at
  64 KiB), marked delivered, unless `--peek` asks for the outcome line only.
  `--any` waits on several and prints the
  winner's name first. A Claude Code MasterMind instead calls the blocking
  `wait` tool the `relevo mcp` server exposes and ends its turn: the call
  returns with the report in its output (see
  [Claude Code plugin](../README.md#claude-code-plugin)). The `relevo wait` command is
  the route where there are no MCP tools.
- `relevo ui [--interval D] [:view [args]]` — the cockpit: `:fleet`, `:rounds [query]`,
  `:round <binding> [N]`, `:chains`, `:stats`, `:candidates`, `:actors`, `:workflows`,
  `:agents`, `:servers`, `:sync`, `:settings`, `:audit`, `:log`. `:fleet` is the root table of bindings; `enter` opens its round
  detail, `esc` goes back, `:` the command line, `?` the key list. `relevo ui :rounds`
  opens the rounds grid directly (`:rounds` reaches it from the fleet).
  `:servers` lists the configured remote builders' servers and probes their health on demand
  (`r`); the client key stays with `relevo config server key`, and the view never shows or edits
  key material.
- `relevo bind --worktree --name N --feature L|--no-feature [--ticket REF] [--candidate CANDIDATE] [--actor R] [--cwd DIR]` — attach an
  additional builder to this MasterMind on its own git worktree, starting at
  round 1. This is how one MasterMind drives several builders at once.
  `--actor R` is the writer actor the binding runs (default `builder`).
  `relevo bind --no-feature --name N --server S [--base REF]` runs that builder on a
  configured remote server instead (see "Remote builders: the client" below);
  `--cwd` cannot be combined with `--server`.
  `relevo bind --no-feature --branch B` checks an existing branch out into relevo's own
  worktree instead of cutting `relevo/<name>`: a local `B` is used first, and
  `origin/B` is made a local tracking branch only when no local `B` exists
  (a branch on neither is refused). A branch already checked out in another
  worktree is refused; free it first. `--name` is optional with `--branch`
  and defaults to the branch's last path segment, lowercased and reduced to
  the characters a binding name accepts. The binding records
  `existing_branch: true`, and relevo never deletes a branch it did not
  create. Works with `--server`; not with `--cwd`.
  For a `--server` binding the server's own `refs/heads/relevo/<name>` ref is
  kept in the repository beside the adopted branch: every closed round
  absorbs it and fast-forwards `B` to it; relevo never deletes the adopted
  branch; the server's `relevo/<name>` ref is deleted by `unbind --done` once it is on a
  remote-tracking ref.
- `relevo config` — show the actors, the current pick and the configured
  candidates, in three blocks; `relevo config --probe` runs each candidate
  once and records its time to first output.
- `relevo setup` — the first-run wizard described in
  [First run on a clean machine](../README.md#first-run-on-a-clean-machine): probe, ask,
  write the starter configuration, install the plugin and service, and run
  one real round (`--answer <id>=<value>` repeatable; `--json` prints the
  questions document with exit 3 until every question is answered, then the
  result document).
- `relevo config init` — seed the candidates, policy and actors sections from
  the harnesses on `PATH` (one builder candidate per harness except claude,
  which only plans, plus a `librarian` writer over the same candidates and a
  `planner` and a `lite-planner` reader actor for
  claude and opencode) and install the agent definitions (`--force`,
  `--no-agents`); the scripted-install alternative to `relevo setup`.
- `relevo config agents` — install the per-kind agent definitions
  (`--kind`, `--agent`, `--force`, `--dry-run`).
- `relevo config export|import|get|set|unset|edit` — read and change the
  configuration document section by section.
- `relevo config repo [--repo <path>]` — read this repository's own settings
  (the `repos` config section, keyed by the repository's identity);
  `config repo set <name|gate|board_theme|trimpath> <value>` stores one field,
  `config repo unset <key>` clears it, and `config repo list` prints one row
  per repository that has settings.
- `relevo config secret set|rm|list` — store, forget or list the `typesafe`
  and `client.key` secrets (the value is read from stdin).
- `relevo gate` — list this machine's active gates.
- `relevo gate <harness/provider/model> [--for D] [--reason S]` — record
  that a candidate's provider is rate-limited; gates every candidate on that
  provider until `--for` elapses, or until `relevo gate --clear` lifts it.
- `relevo gate --clear <provider|harness/provider/model>` — clear a recorded rate
  limit on a provider.
- `relevo done NAME|--name N|--pick` — mark a binding done; relaying stops.
  `--pick` chooses from a list in the terminal.
- `relevo stop NAME|--name N` — kill the builder process and close its open
  round without a report. See [Stopping a round](../README.md#stopping-a-round).
- `relevo unbind NAME|--name N|--pick [--archive] [--force]` — forget a binding, deleting its directory or
  archiving its record first. `--pick` chooses from a list in the terminal. A worktree relevo keeps
  (uncommitted changes, or a failed check or removal) keeps the binding too and the verb refuses;
  commit or discard the work and unbind again, or pass `--force` to archive the binding anyway
  (`--force` goes with a named binding or `--done`, not `--pick`). The refusal prints the reason and a
  `next:` line with the `--force` form; for a served binding `relevo serve unbind --force` runs on the server.

- `relevo unbind --done [--dry-run] [--delete] [--force] [--mastermind P | --all-masterminds]` —
  clear the DONE bindings of the calling MasterMind (resolved like every
  MasterMind-scoped verb), in one pass; also deletes each cleared binding's
  `relevo/<name>` branch and `refs/relevo/<name>/*` refs once each is on a
  remote-tracking ref. `--all-masterminds` clears every MasterMind's, including
  bindings with no MasterMind. If no MasterMind resolves, it refuses rather than
  clearing everything, and each line names the binding's MasterMind. Archives by
  default; pass `--delete` to remove each binding's directory instead
  (`relevo unbind --done --archive` is accepted as a no-op). A DONE binding whose worktree is
  kept stays DONE, reported as kept, so running it again retries once the tree is clean;
  `--force` archives or deletes it anyway. `--done` asks a remote binding's server
  first and keeps the binding while the server keeps its worktree; a server that
  cannot be reached does not block it, and the line notes why.
- `relevo unbind --sweep [--dry-run]` — delete `relevo/<name>` branches and `refs/relevo/<name>/*` refs of bindings that no longer exist, once each is on a remote-tracking ref.
- `relevo daemon [--interval D] [--check] [--pprof <path>]` — the long-running reconciler; this is what the
  service unit runs. It reconciles builders, queues reports for the MasterMind
  and syncs remote bindings. `--check` exits 0 when a daemon is running and 1 when not, printing nothing.
  `--pprof` serves `net/http/pprof` on a unix socket (owner-only, off by
  default) for profiling a daemon that is too slow or too large; fetch it with
  `curl --unix-socket <path> http://x/debug/pprof/profile?seconds=30 -o cpu.pprof`.
- `relevo daemon stop` — stop the daemon this CLI started: it reads the recorded
  pid, sends it SIGTERM and waits for it to exit, or says when none is running.
  A service-managed daemon (systemd unit or launchd agent installed) is refused
  with the service command to use instead, so the stop never fights the service
  manager's restart policy.
- `relevo mcp [--kind opencode] [--mastermind P]` — run the MCP server over stdio
  for a Claude Code MasterMind, serving the verbs as tools. See
  [Claude Code plugin](../README.md#claude-code-plugin).
- `relevo push [--mastermind P]` — hold this MasterMind's push claim and write its
  round events to the mod on stdout as NDJSON. The holder reads nothing from
  stdin: `relevo push --ack <binding> <seq>` confirms one entry the holder wrote
  and exits 0, and the holder moves on once it sees that confirm. `--json` is
  accepted on both routes; the holder's stream is already NDJSON, so on the ack
  route it prints one document instead — `{"binding","seq","route",
  "already_confirmed"}`, where `already_confirmed` is true only on an idempotent
  retry. Without `--json` a successful ack stays silent.
- `relevo mastermind init [--name N] [--kind K --session S] [--hook claude]`;
  `relevo mastermind list [--json]`; `relevo mastermind rename <id|name> <new-name>`;
  `relevo mastermind forget <id|name>` — register this MasterMind, or list, rename
  or forget its records.
- `relevo doctor` — preflight check: plugin, daemon, harness binaries, roles,
  the database and hooks. See [First run on a clean machine](../README.md#first-run-on-a-clean-machine).
- `relevo bugreport [--name N] [--round N] [--logs] [--raw] [--out PATH] [--stdout|--json|--gh]` —
  assemble a local, redacted bug-report bundle and print the `gh issue create` line.
  The default writes a new dated file under `<state root>/bugreports/`; `--stdout` and
  `--json` print instead of writing, `--out PATH` writes exactly there, `--logs` adds
  capped report, diff and transcript tails, `--raw` skips the redaction pass, and `--gh`
  runs the printed line. Nothing is sent automatically.
- `relevo serve [--listen :7777] [--state <dir>] [--interval 2s] [--insecure-http] [--max-bundle-bytes N] [--max-builders N] [--public-host <h>]...` — run the remote-builder server (listener + daemon).
- `relevo serve init|enroll|clients|revoke|fingerprint|status|ui|gc|unbind` — server administration, on the server host. `relevo show --owner` reads one owner's round on that host, and `serve ui` is the server's own reader.
- `relevo gate --serve [--state DIR]` — list the gates on the server's own ledger.
- `relevo gate --serve --clear <provider|token> [--state DIR]` — clear a recorded rate limit on the server's ledger.
- `relevo gate --serve <token> [--for D] [--reason S] [--state DIR]` — record a provider rate limit on the server's ledger.
- `relevo config server key [--enroll-line]` — generate this machine's remote-builder identity (an
  ed25519 keypair); prints the enrollment line a server admin runs
  `relevo serve enroll --key "<line>"` with. With `--enroll-line`, it prints
  only the `ed25519 ...` line, for scripts.
- `relevo config server add NAME URL (--fingerprint sha256:HEX | --ca system | --insecure)` —
  record a remote server; with `--fingerprint`, checks enrollment once.
  Generates and prints this machine's key when it has none yet.
- `relevo config server rm NAME` — forget a configured server; refused while
  any binding still names it.
- `relevo config server list` — one row per configured server: name, url, and this
  client's enrollment on it.
- `relevo help` — the command list. `relevo <command> -h` prints that command's
  flags.
- `relevo version` — the build's version.

Every binding-scoped command takes its binding either positionally or as
`--name`; naming it both ways at once is refused. `send` falls
back to whichever binding owns the current working directory, and a bare
`relevo status` lists them all. Naming one is **required** for `done`,
`stop` and `unbind`: those act on a specific loop — `stop` kills a
process, the others end one — and they refuse to guess (see below).

## Renamed commands

Every verb the housekeeping removed, and the form that replaces it. An old
name exits 2 and names its replacement:

| removed | use instead |
| --- | --- |
| `relevo init` | `relevo config init` |
| `relevo candidates` | `relevo config` |
| `relevo policy` | `relevo config` |
| `relevo roles` | `relevo config` |
| `relevo agent` | `relevo config agents` |
| `relevo client` | `relevo config server` |
| `relevo servers` | `relevo config server list` |
| `relevo add` | `relevo bind --worktree` |
| `relevo diff` | `relevo show --diff` |
| `relevo log` | `relevo show --log` |
| `relevo gc` | `relevo unbind --done` |
| `relevo pause` | `relevo done`, then `relevo bind --resume` |
| `relevo statusline` | `relevo status --line` |
| `relevo pull` | `relevo wait (it prints the report)` |
| `relevo unavailable` | `relevo gate <token>` |
| `relevo available` | `relevo gate --clear <provider>` |

## Reviewing a round

`relevo show --diff --anchors` prints a round's patch with a `path:line` gutter on
every hunk header and every `' '`/`'+'` line, so a comment can quote a line
straight off the printed diff instead of counting by hand:

```
$ relevo show api-auth --diff --anchors
diff --git a/auth.go b/auth.go
--- a/auth.go
+++ b/auth.go
auth.go:41  @@ -38,6 +38,7 @@ func Login(ctx context.Context, u string) error {
auth.go:41  	if u == "" {
auth.go:42 +		return errEmptyUser
auth.go:43  	}
```

## The report block

The builder ends its report with a fenced `relevo` block:

```relevo
status: done            # done | halted | blocked | deferred
halted_at: ""           # which step, when halted or blocked
changed_paths: []       # repo-relative files you changed
commands_run: []        # every command you ran, as typed
not_done: []            # adjacent work you deliberately left
```

The four valid statuses are `done`, `halted`, `blocked`, and `deferred`. A missing or malformed block closes the round as `unstructured` without error or refusal. When `changed_paths` does not match git's count of changed files, relevo notes the discrepancy as `paths: report N, diff M` on the diff entry.
