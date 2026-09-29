# A4-1a: flags, the MCP send argument, user-facing texts, gate → check

Spec: `docs/specs/2026-09-26-a4-state-rename-design.md` (read §1-§3.1 and §3.7 first).
Decision D1 is a **clean break**: old flags are removed, not aliased.

**Vocabulary** (spec §2):
- An **actor** is config: an agent, its candidates, a tier and a check.
- A **runner** is the live process slot that plays one actor.
- A **candidate** is the model a round runs on.
- `builder` stays valid only as the name of the seeded writer actor.

This round is one of two parallel A4-1 rounds. The other one (A4-1b) owns:
- `internal/harness/agents/architect.*`, `internal/planner/handoff.md`,
  `internal/harness/agents/shipped.sha256`;
- `internal/mcp/instructions.go`, `internal/delivery/origin.go`, `internal/relevo/wait.go`;
- `claude-plugin/`, `README.md`, `docs/design.md`, `CLAUDE.md`.

**Do not edit those files.**

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. Flags (clean break)

| where | today | A4 |
|---|---|---|
| `cmd/relevo/bind.go` `bindFlagSet` (~line 106) | `--builder` (`v.builderAlias`) | `--candidate` (field `v.candidate`); help "candidate name or harness/provider/model token to run; omit to take the actor's first ungated candidate" |
| `cmd/relevo/send.go` `cmdSend` (~line 17) | `--builder` | `--candidate`, same help as today with the new flag name |
| `cmd/relevo/agent.go` `cmdAgentInstall` (~line 27) | `--role` ("role name") | `--agent` ("agent name"); update the usage line |
| `cmd/relevo/init.go` (~line 23) | `--no-roles` ("do not install role definitions") | `--no-agents` ("do not install agent definitions") |

- Rename the Go variables along with the flags. Keep the `SendOptions.Builder` and
  `BindOptions` field names, which are internal (D3).
- **Tests:** add `TestRemovedFlagsAreUnknown` in cmd/relevo, a table asserting that
  parsing each of these fails with `flag provided but not defined`:
  - `bind --builder x`
  - `send --builder x`
  - `config agents --role x`
  - `config init --no-roles`

  Parse through the verbs' flag sets **only**. Never execute a verb that spawns a
  harness or reaches the network (CLAUDE.md). `bindFlagSet` exists for exactly this; for
  the others, extract a `…FlagSet` function the same way if none exists. Mirror
  `TestBindFlagsHaveActorNotRole` (cmd/relevo/main_test.go).

## 2. The MCP send argument (internal/mcp)

- `SendArgs.Builder` becomes `Candidate`, `json:"candidate,omitempty"` (tools.go ~line 28).
- The send tool schema property `builder` becomes `candidate`, with the description
  "candidate name or token to run this round and later ones on (persists); refused
  while a round is open" (~line 108).
- Update the tool's description: "Hand a binding's runner a new round: stage file as
  the round's plan and prompt the runner. …".
- Fix the verbs.go use (~line 90).
- Regenerate `internal/mcp/testdata/contract/tools-list.golden` with the contract test's
  `-update`. Report its diff.

## 3. User-facing texts

Rule:
- `--builder` becomes `--candidate`, and `--role` becomes `--actor`;
- "role" becomes "actor" where it names an actor, or "agent" where it names a definition
  file;
- "roles.json" / "config roles" becomes "config actors";
- "builder" meaning the running process becomes "runner".

"builder" as the actor's name stays.

Closed site list, from the survey on main 1d3d5e73. Lines have moved since, so find each
by its quoted text:

1. `cmd/relevo/bind.go` help strings: `--rebind` ("replace a gone builder … like bind
   with --builder omitted" becomes "replace a gone runner … like bind with --candidate
   omitted"); `--tier` "policy tier.<role>" becomes "the actor's tier"; `--worktree`
   "attach an additional builder" becomes "attach an additional runner".
2. `cmd/relevo/send.go`: `--file` help "hand the builder" becomes "hand the runner";
   `--tier` as in item 1.
3. `cmd/relevo/ask.go`: the help mentioning `order[<role>]`.
4. `cmd/relevo/doctor_checks.go`: "ask --role" becomes "ask --actor"; "send --builder"
   becomes "send --candidate".
5. `cmd/relevo/doctor.go`: "role source" and "roles.json is the source" become the
   actors wording.
6. `cmd/relevo/config.go`: usage text lines naming roles; `config agents --role`.
7. `internal/doctor/roles.go`: "binding role", and "runs role %q, which config roles no
   longer defines", becoming "runs actor %q, which config actors no longer defines".
8. `internal/doctor/roledefs.go`: fix texts naming `config agents --role`.
9. `internal/relevo/writer_role.go`: "which config roles no longer defines".
10. `internal/relevo/candidate.go`: "config roles", "send --builder" and "bind --builder".
11. `internal/relevo/send.go`: the "--builder" error texts.
12. `internal/relevo/builder_change.go`: the `ErrBadBuilder` text "send --builder"
    becomes "send --candidate".
13. `internal/relevo/remote.go` and the files split out of it (they may now be in
    `internal/relevo/remote_*.go` or `internal/delivery`; find them by text):
    "does not run custom roles" becomes "does not run custom actors", and the
    `--builder` hint.
14. `internal/relevo/status.go`: the text-mode line printing `role <r>` becomes
    `actor <r>`. **Text mode only**; the JSON is A4-2's.
15. `internal/relevo/ledger.go`, or wherever `rolesMissingNote` now lives (the
    availability package): "roles missing for" becomes "agent definitions missing for".
16. `internal/relevo/policy_view.go`: user-facing "role" words in printed lines, not the
    legacy-key warnings (those name old keys on purpose).
17. `cmd/relevo/daemon.go` and `cmd/relevo/serve.go` log lines: "role definition(s)"
    becomes "agent definition(s)".

Port every test that asserts one of these strings to the new text. Delete none.

## 4. gate → check (spec §3.7, internal names only)

- Rename the Go fields `roles.Row.Gate` to `Check` and `roles.Role.Gate` to `Check`;
  `roleGates` becomes `roleChecks`, with all their uses.
- **Keep the legacy roles.json JSON key `"gate"` readable.** A `roles` section exists
  only as the input of the A2 migration, which must still read it. The Go field is
  `Check` with the tag `json:"gate,omitempty"`, plus a one-line comment saying why.
- The printed "gate" in `FormatRoles` (roles_list.go) becomes "check".
- `Binding.Gate`, the `--gate`/`--no-gate` flags and `relevo gate` are **different
  things and stay**.

## 5. Working efficiently

- Batch-read: the files in §1-§4, plus `cmd/relevo/main_test.go` around
  `TestBindFlagsHaveActorNotRole`, and `internal/mcp/contract_test.go`.
- Find every §3 site in one pass:
  `grep -rn -- '--builder\|--role\|config roles\|roles.json\|tier.<role>\|order\[<role>\]\|role definition\|roles missing' --include='*.go' cmd internal | grep -v _test`.
  Anything that grep finds outside §3's list, and outside the files A4-1b owns: stop
  and list it in the report, and do not change it.
- Focused loop: `go build ./... && go test -count=1 ./cmd/relevo/ ./internal/mcp/ ./internal/doctor/ ./internal/roles/ && go test -count=1 ./internal/relevo/ -run 'Send|Bind|Candidate|Doctor|Gate|Roles|Status'`
- Full check, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`.
  Contract goldens that move: only `tools-list.golden`, plus any CLI golden that prints
  a changed help or error string. List each one, with a one-line reason.

## 6. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a4-1a-flags-and-texts.md`. Commit as
**one new commit**: `feat(a4): --candidate, config agents --agent, runner and actor words; gate becomes check`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- a table of every changed user-facing string, old and new;
- the goldens that moved, and why;
- the tests ported;
- `make check`'s last lines;
- anything grep found outside the list.
