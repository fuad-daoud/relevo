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
  [Claude Code plugin](plugin.md#claude-code-plugin)). The `relevo wait` command is
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
  round without a report. See [Stopping a round](rounds.md#stopping-a-round).
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
  [Claude Code plugin](plugin.md#claude-code-plugin).
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

## relevo history

`relevo history` reads the database, not the filesystem: one line per round
across every binding relevo has ever recorded -- live or archived -- newest
first.

```
relevo history [--here]                    filter to a repo: --here resolves the current directory's
                                           origin url (or its git common dir with no remote)
              [--feature LABEL]           filter to a --feature label
              [--ticket REF]              filter by ticket: a bare N or #N matches any stored ticket
                                           ending in #N, anything else matches exactly
              [--binding NAME]            filter to one binding name
              [--mastermind SESSION]         filter to one MasterMind session id
              [--since D]                 only rounds started after this: 24h, 7d, or YYYY-MM-DD
              [--limit N]                 max rows to print; 0 = all (default 200)
              [--json]                    a JSON array of RoundRow, `[]` when empty
              [-q "<query>"]              filter with the query language below
              [--by <axis>]               regroup the result: none, binding, repo, feature, ticket,
                                           candidate, actor, harness, provider, model, day, outcome
              [--rows]                    with --json --by, include each group's Rows
```

Two switches turn the same query into an aggregate instead of a
row-per-round list:

- `--tab` — tokens and cost across bindings, archived ones included (see
  "Round usage" below).
- `--stats` — rounds, outcomes, switches, gate results and consults across
  bindings, archived ones included, with the last 30 days of provider blocks
  (see "Usage stats" below).

Every flag is one field of the shared `Filter` the ui's coming `all` scope
and dashboard query too. `--archived` and `--live` together, and an
`--outcome` outside the enum above, are usage errors. A plain line looks
like:

```
2026-09-15 14:02  api-auth      r3  agy/antigravity/opus                      reported        +2 commits  clean  $0.42  (archived)
```

started time in the local zone; the binding name, truncated with `…` past
12 characters; the round; the builder candidate; the outcome; commits
(`-` when unknown); the worktree's tree state at close; cost (`$0.42`
measured, `~$0.42` estimated, `unknown`, or `-` when the round recorded no
usage at all); `(archived)` for a round from a binding `unbind --done` has archived.

`-q` takes the query language the dashboard's filter line shares:

```
query  := token*                              whitespace separated
token  := key ":" value                       equality; "quoted" for spaces
        | numkey op number                    op in > < >= <= = 
        | word                                case-insensitive substring of binding, repo or feature
key    := binding repo feature ticket MasterMind harness provider model candidate outcome
          report state gate basis server mode since until archived by
numkey := cost tokens commits duration round
values : outcome reported|halted|exited|switched|done_no_report|open
         report  done|halted|blocked|deferred|unstructured
         gate    pass|fail|timeout|error
         basis   measured|estimated|unknown
         mode    pane|headless|remote   (pane: history only)
         archived true|false
         since/until 24h|7d|YYYY-MM-DD
         by      none|binding|repo|feature|ticket|candidate|actor|harness|provider|model|day|outcome
         duration minutes; tokens = in+cache+write+out; cost in USD
```

`by:` (or `--by`) regroups the result instead of printing one line per
round: one row per axis value with rounds, reported, halted, commits,
tokens, cost and the last round's date. A flag overrides the same key in
`-q` and prints `note: --<flag> overrides <key>:<value> from -q` on stderr.
An unknown key, a bad enum value or a bad number is a usage error (exit 2).

```
$ relevo history -q "harness:agy outcome:halted since:30d"
$ relevo history -q "auth cost>1" --by builder
$ relevo history --by day --since 14d
```

## relevo show

`relevo show` prints one round's prompt, report, diff, drift, log or
transcript. A live binding is read straight from its files, exactly as
today; anything not live -- an archived binding, or one this machine's
database otherwise knows about -- is read from the database instead, so a
round from months ago renders the same way a live one does.

```
relevo show <name> [--round N]                      the round to read; default: the newest completed one
                   [--prompt|--report|--diff|--drift|--log|--transcript|--output]
                                                     which section; default: --prompt; only one may be given
                   [--json]                         the ShowResult as JSON (Events included for --log)
```

A header line goes to stderr -- `<name> round <N> of <Rounds> · <section>`,
with `· archived <date>` appended for a non-live binding -- so stdout is
always just the section itself and safe to pipe. A round with no such
section (an open round with no diff yet, say) prints `no <section> for
round N` and exits 0 rather than erroring. `--log` prints the round's
events; `--transcript` prints the builder's
rendered stream. Reading `--round N` outside the binding's round count, or
naming a binding neither live nor in the database, exits 1:

```
$ relevo show api-auth --report
api-auth round 3 of 4 · report
report text here
```

## relevo board

`relevo board [path] [--board NAME] [--mastermind M] [--theme NAME] [--no-open]`
opens a board. A bare `relevo board` opens this MasterMind's live board; naming
a path ending in `/board.html` opens one board in the repository. Either way it
binds `127.0.0.1:0`, prints one line,
`board: http://127.0.0.1:<port>/#t=…  (Ctrl-C to stop)`, and opens that URL in
the browser unless `--no-open` is given. Ctrl-C stops it, draining an in-flight
save through `http.Server.Shutdown`.

```
relevo board                                       opens this MasterMind's live board
relevo board --board api                           opens the live board "api"
relevo board --mastermind architect-2              another MasterMind's live board
relevo board docs/boards/api/board.html            opens a named repo board
relevo board url                                   prints a live board's URL
relevo board promote                               copies a live board into the repo
relevo board docs/boards/api.excalidraw            edits a named Excalidraw scene
relevo board --theme blueprint                     a palette for new scene elements
relevo board comments                              lists a scene's comments
relevo board comment --text "check state 3"        appends one comment
relevo board text docs/boards/api.excalidraw       lists the scene's text elements
relevo board annotate docs/boards/api.excalidraw --text "step 1"
                                                   appends one text element
```

A board is one HTML file. A live board is
`<state root>/boards/<mastermind-id>/<name>/board.html` and a repo board is
`<repo>/docs/boards/<name>/board.html`; the default name is `board`. The board
name is `--board`, else the `current` pointer file in the live directory --
written when a board is opened or selected, and never for a repo board -- else
`board`. `--mastermind <id|name>` selects whose board; without it
`RELEVO_MASTERMIND` is used, and with neither the single registered MasterMind is
used, else a usage error lists them. An `mm_`/`pl_`/ULID id or a path opens no
database at all; a name, or the single-MasterMind fallback, reads the registry
read-only. A live directory is created `0700` and its board, pointer and
`server.json` are written `0600`.

The page is served under a content security policy that names no remote origin:
`default-src 'none'`, so nothing loads unless the policy allows it, and no entry
is an `http` or `https` host. The browser loads a small shell that needs no
token; the shell reads the token from the URL fragment, asks `GET /api/board`
for the board's bytes, and renders them in a `blob:` iframe sandboxed without
`allow-same-origin`. The board's own scripts therefore run in an opaque origin
where the API is unreachable and the token is unreadable. The API is `GET` only,
requires the per-run token compared in constant time, refuses a `Host` header
that is not the bound `127.0.0.1:<port>`, and never caches. A board that names an
external resource -- an `http`, `https`, `ws`, `wss` or `ftp` URL, or a
protocol-relative `//host` -- is still served: the shell banners the references
and `relevo board` names the first one on stderr, and the policy is what blocks
the load. A missing `board.html` is not an error; the shell says there is no
board at that path yet, and nothing is created until you save one.

`relevo board promote [--board NAME] [--mastermind M] [--to SLUG] [--force]`
copies a live board into `<repo>/docs/boards/<SLUG>/board.html`, atomically.
`--to` renames it; `--force` overwrites an existing repo board. A missing source
and an existing target without `--force` are both refused (exit 2) naming what to
do about it; a bad slug and a run outside a repository are usage. It is a peek
verb: no daemon, no server, and no pointer write -- promoting is not opening, so
the pointer still names the board you are looking at.

A running live board writes `server.json` (`scene`, `url`, `port`, `pid`,
`started_at`) beside the board and removes it on shutdown only while it is still
its own; a second server on the same board is allowed and the most recent writer
wins. The advertised `scene` is the board's slug, so `board url` and the
statusline find a live HTML board the same way they find any other.
`relevo board url [--board NAME] [--mastermind M]` prints that URL -- its token
included -- for a shell copy, and exits 1 when no live server holds it.
A live board's URL names its owner in the path,
`http://127.0.0.1:<port>/<mastermind>/#t=<token>`, so a copied URL says whose
board it is; the token still rides only the fragment. The URL also appears in the
statusline (below).

An explicit `.excalidraw` path opens the
[Excalidraw](https://excalidraw.com) whiteboard instead, with `--theme` choosing
the palette for new elements.

`relevo board comments [path|--board NAME] [--json]` reads the resolved scene
and prints every comment in scene order -- `id, x, y, text, by, at` -- one
tab-separated row each, or one compact JSON array with `--json` (`[]` when
there are none). A comment is a text element carrying
`customData.relevo` `{comment: true, by, at}`; both writers use the theme's
`comment` colour and `by` distinguishes them. `relevo board comment
[path|--board NAME] --text S [--x X --y Y] [--by B]` appends one comment, at
`--x`/`--y` when both are given and otherwise below the scene's bounds so it
never lands on a drawing, authored by `--by` (default `RELEVO_MASTERMIND`, else
`human`); every other byte of the scene is unchanged. A missing scene is a new
one carrying the comment for `comment`, and simply no comments for `comments`.
A malformed marker, or a file that is not a scene, is `refused` (exit 2) naming
the element or the path; an author that is empty, longer than 64 bytes or holds
a control character is `usage` (exit 2). Both verbs resolve like `relevo board`
and are peek verbs: no daemon, no database, and `comments` never writes the
pointer. Comments are flat -- no replies, resolve, delete or thread -- and the
CLI never deletes.

Both formats are confined the same way. A repo path must end in `/board.html`
(or `.excalidraw`), its parent directory must be a board slug, and after symlinks
are resolved on its deepest existing ancestor the whole path must sit under the
git top level, so neither `..` nor a symlinked parent escapes. A live path must
be exactly `<live root>/<mastermind-id>/<slug>/board.html` under the state root.
A missing file is a new board or a new scene, and nothing is created on disk
until the first save. An explicit repo path outside a repository is a usage
error, exit 2; a bare `relevo board` is the live board and never requires a
repository. A live board refuses a state root that sits inside a repository (or a
repository inside it), because the two scopes may not nest. Because every path
must name its file, a bare slug directory is never a path, so a board directory
can never collide with a subverb name.

The theme chooses the colours of **new** elements only -- an existing scene
keeps the colours it stores. Every palette lives in the `themes` config
section, keyed by name: the release writes `cockpit` and `blueprint` there
once per database when relevo starts (so `relevo config get themes.cockpit
--json` prints cockpit's palette on a fresh install), and after that every
lookup reads the section -- `relevo config set themes.<name> '{...}'` changes
what the board paints, `config unset themes.<name>` is what unsets one, and a
palette you unset stays unset: the seed runs once, not on every open. A user
palette is whole -- all eleven colour fields (`bg ink muted faint line panel
accent good warn bad comment`) are required, and each is a `#` followed by 3,
4, 6 or 8 hex digits -- so a missing or unknown field, and any other colour
form, is refused at write time naming the entry and the field. Precedence is
`--theme`, then the repository's own stored setting (`relevo config repo set
board_theme <name>`), then the repo-local git config key `relevo.boardTheme`
(`git config relevo.boardTheme blueprint`, kept so existing setups keep
working), then `cockpit`. A `relevo board` that runs before the state root
has a database at all -- a fresh install that has never opened it writable --
paints the palettes the embedded seed file carries. An unknown name is a
usage error, exit 2, that lists the names the consulted set holds and says
how to add one: `relevo config set themes.<name> '{...}'`.

On save the page writes the scene and a companion `<name>.svg` beside it,
exported for the cockpit, agents and PR diffs; both are committed. The export
runs with dark mode off and the view background set from the palette, so what
is stored, shown and exported is the same colour. `Ctrl-S` (or the Save button)
writes the scene and its companion `.svg`.

The page and its assets (bundle, stylesheet and self-hosted fonts) are vendored,
committed and embedded, so no CDN is contacted. The page talks to three
loopback-only routes: `GET /` and `GET /assets/*` are the page, `GET /api/scene`
returns the scene, and `PUT /api/scene` saves it. A per-run token, carried in
the URL fragment and sent on every `/api/*` call, plus a `Host` check, guard the
API; `If-Match` carries the scene's etag, and a stale one is a 409 the page
reports as `the file changed on disk; reload to continue`. `make board-assets`
regenerates the vendored assets and their integrity manifest; it is dev-only,
and CI only reads them.

The agent leg is pure Go, so an agent reads and writes a scene without the
page: `relevo board text <file>` prints one row per non-deleted text element --
id, x, y, then the raw text -- and `--json` prints one line,
`[{"id":"…","x":10,"y":20,"text":"…"}]` (`[]` for a scene with none, never
`null`). `relevo board annotate <file> --text S [--x X --y Y]` appends one text
element and prints `board: annotated <path> (id <id>)`; it never unmarshals and
re-marshals the scene, so every other byte keeps its position and a diff shows
only the insertion. Without `--x/--y` the element lands below the scene's
current bounds, left-aligned with its leftmost element, so an agent's note
never lands on a drawing.

Both subverbs require a scene path that ends in `.excalidraw` and sits under
the repository root, and both refuse rather than create one: a missing scene is
`no scene at <path>` (exit 2), and an invalid scene is refused with the
validator's own words. The other usage refusals are exit 2 as well: a path that
is missing or repeated, `--text` absent or empty, `--x` without `--y` (they
come together), and a non-finite coordinate.

Annotate's element is built for Excalidraw 0.18.1: `fontFamily` 3 (Cascadia),
`lineHeight` 1.2, `fontSize` 20, `roughness` 0, and `strokeColor` from the
resolved theme's ink. Font safety is explicit -- `autoResize` is true and the
width is an estimate (twelve pixels per byte of the widest line, so it errs
upward) that the emitted element never falls below, which is what keeps
CLI-made text from rendering clipped before the font loads. Only the id (16
random bytes), the seed and the timestamp are random.

In the cockpit, an `.excalidraw` artifact opens its companion `.svg` in the
browser when one sits beside it; without one it opens in `$EDITOR` and the tab
shows the faint line `run relevo board <path> to edit`, so the scene can be
opened on the board. The OPENS IN cell and the open action share one decision,
so they cannot disagree.

## done and unbind are the destructive verbs

`relevo done` and `relevo unbind` both require a binding name (`relevo done ai`, or
`--name ai`) or `--pick`, which lists the bindings and runs the verb on the one
you choose. Neither resolves the current directory for you: a bare `relevo done` once ended a live
loop by accident, and the recovery is `relevo bind --resume --name <name>`.
`--pick` is explicit for the same reason -- a bare verb never opens a picker,
so a MasterMind agent can never fall into one.
And because a popup takes focus the instant it opens, `Enter` on a binding
that is not `DONE` asks first -- `mark webshop done? it is ACTIVE in round 5`
-- and only `y` proceeds; any other key returns to the list.

## Display states

`relevo status` collapses the binding's internal state into four:

- **ACTIVE** — someone is working (MasterMind or builder), nothing needs a human
  yet.
- **NEEDS YOU** — relevo has stopped and a person must act. Covers a dead
  builder process, a round that ran past its timeout, and a binding that hit
  its round cap.
- **PAUSED** — the binding's worktree was released between rounds, keeping the
  branch and the round log; nothing sets this state today, but a binding a
  pre-housekeeping relevo left paused still reads as PAUSED, and
  `relevo bind --resume` restores it. Nothing needs a human, and
  `relevo unbind --done` leaves it alone.
- **DONE** — the MasterMind declared the work verified via `relevo done`, and
  relaying has stopped deliberately, not because anything went wrong: unlike
  NEEDS YOU, nothing needs a human here. `Reconcile` returns immediately for
  a done binding — no reports are queued and no timeouts are flagged. The binding and its round log stay in the database (`relevo show <name> --log`
  still works as an audit trail) until `relevo unbind` or `relevo unbind --done` archives or removes them;
  a clean worktree is released at `done` so the branch is free to review.

## Recovering a broken binding

If the builder's process dies without a report, relevo switches to the next
candidate or, when none serves the binding, goes `NEEDS YOU` — see "Headless
builders". A binding whose builder is gone between rounds is `BROKEN` and
relaying stops until you point it at a new builder:

```bash
relevo bind --resume --name N --rebind                                       # start a fresh builder, picked by the actor's order
relevo bind --resume --name N --candidate agy/google/gemini-3.8-flash-high     # start a fresh builder, naming it
```

The binding keeps its name, round number, round log, working directory, and diff
baseline. The replacement builder is started with its agent on the launch line,
like any builder relevo spawns. With `--rebind` the candidate is resolved through
the builder actor's candidate list and the ledger, and the pick is logged, exactly as a fresh
bind with `--candidate` omitted. Relevo does not automatically re-send the current
prompt: it prints the `relevo send` command pointing at the staged prompt so you
can hand over the round when ready.

If only the MasterMind moved or restarted, `relevo bind --resume --name N` re-points
the MasterMind without touching the builder. If you want to start over from scratch,
use `relevo unbind N` and bind fresh.

A headless binding is never `BROKEN` for lack of a process: between rounds
there is none. If its process died mid-round the daemon already switched or
halted it (see "Headless builders"). To move a binding to a fresh process by
hand, `relevo send` the staged prompt again once `relevo status` shows the
builder `exited`.
