# Remote builders: the server

`relevo serve` runs a remote-builder server: an HTTPS listener over enrolled clients and an autonomous daemon loop that runs headless builders on the server host without a local MasterMind or GUI. It reaches the one machine database -- enrolled clients, the server's TLS key and certificate, its gates and every served binding are records in relevo.db -- through the local daemon's owner socket like every other client. `--state` only says where the server keeps its bare repos, its temp files and its worktrees. The service unit pulls that daemon in and starts after it (`Wants=relevo.service`, `After=relevo.service`); a daemon `relevo serve` had to start itself would live in serve's own cgroup, so the unit dependency is the supported shape on a server host.

On a fresh server host, the first run looks like:
1. `relevo serve init --host <hostname>` generates a server private key and self-signed certificate, printing the SHA-256 fingerprint that clients pin.
2. Copy the fingerprint to share with clients.
3. Enrol each client's public key: `relevo serve enroll --label <client-label> --key "<public key line>"`.
4. Copy the service unit to `~/.config/systemd/user/relevo-serve.service` and enable it:
   ```
   cp dist/relevo-serve.service ~/.config/systemd/user/
   systemctl --user daemon-reload
   systemctl --user enable --now relevo-serve
   ```
5. If any client reaches this server by `--ca system` or `--insecure`, start it with `--public-host <the exact host in that client's URL>` (repeatable; the port is dropped and the host is lower-cased). A client that pins the certificate needs nothing: its audience is the pinned fingerprint. Run `relevo serve --public-host serve.example.com` (or add the flag to the unit's `ExecStart`). Every accepted audience is logged once at startup, so the log names what the server will accept.

`relevo serve` refuses to start with neither a certificate nor a `--public-host` value: "no audience: run relevo serve init or pass --public-host". A server with no accepted audience refuses every signed request rather than guessing which of its identities a request was meant for.

On the server machine, the admin runs these on the server host. No `--state` is needed: an admin verb reads the running daemon's root from the database's `serve.daemon` record (an explicit `--state` still wins, and a stale record falls back to the default root with a note):

- `relevo serve status [--json]` prints active bindings across all owners as JSON (top-level keys `builders`, `last_contact` and `owners`), sorted by owner label. A binding's contact is recorded at most once an hour, so `last_contact` can trail a polling client by up to an hour.
- `relevo show <name> --owner <label|id>` prints one round's prompt, report, diff, drift, log or transcript, with `relevo show`'s flags. Read-only, and it reads live bindings only: a non-live binding reads as "binding not found".
- `relevo show <name> --owner <label|id> --log` prints that owner's binding log, with `relevo show --log`'s `--round`, `--after`, `--json` and `--follow`. Read-only: `--owner` is an exact label or an exact client id, and nothing is stamped or created.
- `relevo serve clients` lists enrolled clients and their revocation status.
- `relevo serve gc --abandoned <duration>` prunes abandoned bindings whose last activity is older than the threshold by archiving them (running rounds are never touched). Contact is recorded hourly, so a threshold under an hour can archive a binding its client still polls.
- A **DONE** served binding is collected once its last round has been acked by the client, or after seven days without an ack: the daemon removes its worktree, deletes its branch and every `refs/relevo/<name>/*` ref in the owner's bare repo, and archives its record in the database (this cleanup lands in the server's next round). Every server unbind releases the binding's branch and refs as well. A bare repo is deleted once no live binding uses it, and the next bind of that repository recreates it.

What `relevo serve` does not do: it runs no MasterMind and provides no administrative verbs over the network (administration happens on the server host). Tenants are protected from each other over the wire and from a passive network, but not from the server admin or from each other at the OS level where all builders run under the same unix user. Unix-user isolation is available as `serve.isolation: "user"` (above); rootless-container isolation is available as `serve.isolation: "container"` (below).

## Tenant isolation: `user` mode

By default (`serve.isolation: "none"`, and no config change needed) every
builder runs as the serve uid. `serve.isolation: "user"` runs each owner's
builders as that owner's declared unix user instead, so tenants cannot read
each other's worktrees, logins, binding state or repos. It needs the server to
run as **root**, and the fixed, root-owned state root `/var/lib/relevo` (the
unit below does both). A server that is not root refuses to start, and a
half-done switch fails closed: a round halts **NEEDS YOU** naming the missing
piece (the user, or the exact `chown`/`chmod`), never running unisolated.

To switch a host from `none` to `user` (spec §10):

1. `useradd --create-home <unix-user>` once per tenant, then log each harness
   in under that user's own HOME (per-user logins are the recommended setup).
2. Enrol (or re-enrol) each client with its user:
   `relevo serve enroll --label <client-label> --key "<public key line>" --user <unix-user>`.
   An unknown user is refused with the exact `useradd` line; re-enrolling an
   active key fills `unix_user` without revoking it.
3. Set `serve.isolation: "user"` in the policy. Leave
   `serve.isolation_shared_logins` off unless the host deliberately shares one
   group-readable login; when it is on the doctor warns.
4. Move to the root system unit, `dist/relevo-serve-system.service` (a system
   unit with `User=root` and `--state /var/lib/relevo`):
   ```
   sudo cp dist/relevo-serve-system.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now relevo-serve-system
   ```
5. `relevo doctor` reports the mode in force (`scopes=off (isolation=user)`)
   and every unmet prerequisite with its fix: not root, an active client with
   no `unix_user`, a user missing on the host, an owner root with the wrong
   owner, group or mode.

Switching back to `none` keeps root (or chowns the owner roots back). User mode
runs its builders without a scope — system-manager `--uid` scopes are a
follow-up — while the `max_builders` cap still applies.

## Tenant isolation: `container` mode

`serve.isolation: "container"` runs every owner's builders in a rootless
`podman` container instead of as a unix user. It needs no root and keeps the
default user unit (`dist/relevo-serve.service`): the container runs as the
serve uid through `--userns=keep-id`, so bind-mounted ownership and git's file
checks match the host. Tenants cannot reach the server's database, TLS key or
another owner's files because those paths are not mounted.

To switch a host from `none` to `container`:

1. Build an image containing `git`, a POSIX `sh` and the harness binaries on
   `PATH`. `dist/Containerfile` is the reference build:
   `podman build -t relevo-builder:local -f dist/Containerfile .`
2. Set `serve.isolation: "container"` and `serve.isolation_image` to the full
   image reference in the policy. The image is required; there is no default.
3. `relevo doctor` reports the mode in force (`isolation=container`) and fails
   with the exact fix when `podman` is not on `PATH` or the image is missing.

A container round binds the round tree, the binding's `out/` directory, the
owner's `repos/<hex>` and each owner harness home at their host absolute paths,
with a private `/tmp` tmpfs. `serve.scope`'s memory, CPU and task limits render
as `--memory`, `--cpus` and `--pids-limit`; `Slice`, `CPUWeight` and
`AllowedCPUs` have no podman equivalent and are dropped, and `Rusage` reports no
measurement. A round whose `podman` or image is missing halts **NEEDS YOU**
naming the piece instead of running unisolated. The `podman` flags are reasoned
from the `podman run` contract, not observed on every host; a host without
`podman` cannot start a container server, and `relevo serve` refuses at startup.

## Remote builders: the client

A remote binding is an ordinary binding whose builder runs on someone else's
machine, over a signed, pinned HTTPS connection instead of a local process.
It has no worktree of its own: `relevo send` ships a bundle of your branch's
history alongside the prompt, and the daemon polls the server for the round's
state the same way it polls a local builder.

Set up once per machine:
1. `relevo config server key` generates this client's ed25519 keypair (stored
   in relevo.db) and prints two lines: the client's id, and an enrollment line
   (`ed25519 <base64> <user>@<host>`) to hand the server admin.
2. The admin runs `relevo serve enroll --label <you> --key "<enrollment
   line>"` on the server, and shares back that server's certificate
   fingerprint (printed by `relevo serve init` there).
3. `relevo config server add <name> <url> --fingerprint sha256:<hex>` records
   the server and, having a fingerprint to pin the connection with, checks
   enrollment immediately: "enrolled as
   `<label>`", or "not enrolled on `<name>`: give the admin: `<enrollment
   line>`" if step 2 has not happened yet. `--ca system` trusts the system CA
   pool instead of pinning a fingerprint; `--insecure` allows plain HTTP, for
   a server reachable only over an already-trusted tunnel. With `--ca system`
   or `--insecure`, this client signs every request for the URL's host, so the
   server admin must have started the server with `--public-host <that host>`;
   with `--fingerprint` it signs for the pinned fingerprint and the server
   needs no extra flag.
4. `relevo config server list` lists every configured server and this client's
   enrollment on each: `enrolled as <label>`, `not enrolled`, `unreachable`,
   or `cert changed` (the pinned fingerprint no longer matches -- a hard
   refusal the client never overrides silently).

**Version skew.** Signatures are bound to the server's
identity, so the two ends upgrade together -- no window accepts
both the old and the new signed string. A pre-upgrade client against an
upgraded server gets HTTP 426, and `relevo config server list` prints
`error: server rejected protocol version (426)`. An upgraded client against a
pre-upgrade server prints `server predates audience-bound signatures; upgrade
relevo on the server`. Upgrade both ends, then check `relevo config server
list` shows `enrolled as <label>` for every server.

Then, from any repository:
```
relevo bind --name api --server zen --no-feature        # creates the server binding and
                                           # the local branch relevo/api, no worktree
relevo send --name api --file plan.md      # ships plan.md and a bundle of relevo/api
relevo status                              # round state comes from the server, polled
```

What comes back as `relevo/<name>`: the result of a closed round is fetched
into your repository's own `refs/heads/relevo/<name>` branch -- fast-forward
only, exactly like a local builder's worktree branch. If the round closed with
uncommitted changes on the server, they land on a side ref,
`refs/relevo/<name>/round-<N>`, whose parent is that round's commit on
`relevo/<name>`; the report names it. If that fast-forward collides with a
branch you have checked out locally, relevo retries quietly next tick --
check out something else, then `relevo wait`.

`relevo send` also ships your repository's tags as data beside the bundle, and
the server sets each one whose commit it already has, so a tagged server
worktree can `git describe --tags`. Catch-up fetches the builder's own stream
file (`NNN-runner.jsonl`) alongside the report, diff and log, so a remote
round's failure carries its detail to the client.

What is refused: `--cwd` cannot be combined with `--server` (a remote binding
is always created fresh, never bound to an existing directory); and
`relevo bind --resume --rebind` (or
`--candidate`) against a remote binding ("cannot change a remote
builder; unbind and re-create" -- a binding's mode is fixed at creation, the same
rule a headless binding follows). `relevo done` and `relevo unbind` tell the
server first, and only change anything locally once it agrees (a 404 from
the server is treated as already gone, and proceeds).

What `status` and `doctor` show: `relevo status` and `relevo ui` name a
remote binding's builder by its server (`zen`), with the
last round state the daemon observed there (`running`, `idle`, `closed`,
`needs_you`, `unreachable`, `cert`) in the status column -- read from the
store, never over the network, so it costs nothing extra. `relevo status`
and each `relevo wait` poll additionally sync every remote
binding first, so a round the server closed while your daemon was not
running (or was never started) is collected without it -- a laptop closed
overnight still shows the finished round on the next `relevo status`.
`relevo doctor` adds one row per configured server: reachable and enrolled
(`enrolled as <label>`), not yet enrolled (with the line to give the
admin), unreachable, or a certificate that no longer matches the pinned
fingerprint.
