# Dev sandboxes as separate Unix users

The default sandbox needs no root at all: `scripts/relevo-sandbox.sh` points
`XDG_STATE_HOME`, `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`,
`CLAUDE_CONFIG_DIR` and `CODEX_HOME` at a directory under
`~/.local/share/relevo-sandboxes`, and
[env-sandbox.md](env-sandbox.md) has the recipe. It isolates paths, not uids.

Reach for the per-user flow below when the test needs uid separation -- two
sandboxes that must not see each other's files through permissions at all, or
harness logins that must not be shareable. It costs an account, a systemd unit
and root, so it is not the default.

The per-user flow is this: `rv-<name>` is a real Unix account with its own home at
`/home/rv-<name>`, its own clone, its own state root, its own config, its own
systemd user unit and its own harness credentials.
`scripts/relevo-dev-user.sh` creates and destroys them.

Two sandboxes cannot collide on a branch, a socket, a port or a login, because
none of those are shared.

## Setup

```
sudo scripts/relevo-dev-user.sh create demo --port 7801
```

Options:

| Flag | Meaning |
| --- | --- |
| `--port N` | the `relevo serve` port this sandbox owns. Refused when it is 7777 or another sandbox's recorded port. Without it the script picks the next free number from 7801. |
| `--repo URL` | the clone URL. Defaults to the invoking checkout's `origin`. |
| `--ref REF` | the branch or ref to check out after cloning. Defaults to `origin/main`. |
| `--dry-run` | print the commands, run nothing, need no root. Works on every subcommand and may appear anywhere in the arguments. |

The name is a short lowercase token matching `^[a-z][a-z0-9-]{0,11}$`; the
account is `rv-<name>`.

`create` runs, in order:

1. `useradd -m`, making the home.
2. `loginctl enable-linger`. This comes before the service is installed,
   because without lingering the user manager that would run the unit does not
   outlive the session installing it.
3. Wait for `/run/user/<uid>`. Without it there is nowhere to put the user's
   units and `systemctl --user` fails with "Failed to connect to bus".
4. As the user, under a clean environment: clone, `git checkout <ref>`,
   `make service`. That installs `~/.local/bin/relevo` and
   `~/.config/systemd/user/relevo.service`, then reloads, enables and restarts
   it.
5. Write the sandbox marker `<stateRoot>/sandbox`, recording the name and the
   port, owned by the user.
6. Install `dist/relevo-serve.service` with `--port` as a drop-in, and print
   the `relevo serve --listen :N` line.

Every step except the credential ones is root work the script does. These it
prints instead, because they need your own logins:

```
sudo -iu rv-demo
# then, as rv-demo:
#   opencode's permission.external_directory allowlist must include:
#     /home/rv-demo/.local/state/relevo
#   relevo config agents
#   relevo doctor
```

## Why the checkout is never shared

The sandbox clones its own tree. Sharing one work tree between two accounts
means sharing one `.git`, which means the accounts fight over it:

- a branch checked out in one is checked out in the other
- `git fetch` refuses when another checkout holds the ref it wants to update
- `git remote prune` and `git push --delete` can remove a ref the other
  account is working from
- hooks execute as whichever account triggered them
- `safe.directory` has to be set for every account that touches the tree

`git clone` from `--repo` avoids all of it.

## The clean environment

The clone and build run under `env -i` with only `HOME`, `USER`, `LOGNAME`,
`PATH` and `XDG_RUNTIME_DIR`. An inherited environment would carry the parent's
`XDG_STATE_HOME`, `XDG_CONFIG_HOME`, `RELEVO_*` and `CLAUDE*` values into the
sandbox, and the sandbox would resolve its state root under the parent's home
and write there. The `xdg` doctor row exists to catch exactly that if it ever
happens another way: it warns when a set XDG variable resolves to a root owned
by another uid.

## Port plan

- The production `relevo serve` keeps 7777. It is the default, and it is the
  one port this host is assumed to already own.
- Sandboxes start at 7801 and go up, one number each. `create` refuses 7777 and
  refuses any number another sandbox's marker already records.

`relevo daemon` opens no TCP port at all -- it serves only the database socket
and a pprof socket -- so the port question applies to `relevo serve`. A
sandbox that runs no `relevo serve` has nothing to collide with.

The `port` doctor row warns when a sandbox's serve is on 7777, and when it is
on a port other than the one the sandbox was created for. The fix is
`relevo serve --listen :<marker port>`.

## The socket length limit

The owner's socket path is
`/home/rv-<name>/.local/state/relevo/relevo.sock`, and it has to fit the
kernel's `sun_path` (104 bytes on Linux, 104 on macOS). A path that does not
fit can never bind, so `create` refuses the name up front, naming the path and
the limit, rather than leaving you with an account whose daemon cannot start.
This is why the home is the short `/home/rv-<name>`.

## Lingering

`loginctl enable-linger rv-demo` keeps the user manager running with nobody
logged in, which keeps the sandbox's daemon running. Without it the daemon
stops at logout and the sandbox looks like a random broken install next time
you log in.

The `linger` doctor row warns when systemd holds no linger file for the
account. The fix is `loginctl enable-linger rv-demo`.

## Credentials

Each sandbox has its own. `relevo config agents` run as `rv-demo` logs the
harnesses in against `rv-demo`'s own config root, and the tokens land under
`/home/rv-demo/.config`. The developer's own logins are untouched, and so are
the other sandboxes'.

opencode needs its `permission.external_directory` allowlist to include the
sandbox's state root (`/home/rv-demo/.local/state/relevo`), because that is
where it stages plans and reports. The opencode `permission` row in
`relevo doctor` checks that allowlist.

## Rollback

```
sudo scripts/relevo-dev-user.sh destroy demo
```

In order: `systemctl --user disable --now` for `relevo.service` and
`relevo-serve.service`, `loginctl terminate-user`, `loginctl
disable-linger`, then `userdel -r`. The `-r` removes the home, which is the
whole rollback -- a sandbox's state, its clone and its credentials all live
under it. Nothing outside that home is touched, so the production install and
every other sandbox are untouched.

`destroy` refuses a name that is not a sandbox token, so it can never be aimed
at an ordinary account, and it refuses the account running the script.

## Listing

```
sudo scripts/relevo-dev-user.sh list
```

One row per `rv-*` account with its port, whether systemd is lingering it, and
whether it has a `relevo.service` installed.