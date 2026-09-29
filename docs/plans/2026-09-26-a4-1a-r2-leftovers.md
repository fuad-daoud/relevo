# A4-1a, round 2: the user-facing words round 1's list missed

Round 1 was right to leave these untouched, because they were outside its closed list.
This round finishes them. Same spec, same vocabulary:
- an **actor** is config;
- a **runner** is the live process;
- a **candidate** is the model;
- `builder` stays only as the seeded actor's name.

The files A4-1b owns are still off limits: architect.*, handoff.md, shipped.sha256,
mcp/instructions.go, delivery/origin.go, relevo/wait.go, claude-plugin/, README.md,
docs/design.md and CLAUDE.md.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## Closed list

These are the items from your round-1 report. Find each by its quoted text.

1. `cmd/relevo/serve.go` log lines "candidate roles missing; those candidates will be
   skipped" and "roles present" become "candidate agent definitions missing; those
   candidates will be skipped" and "agent definitions present".
2. `internal/doctor/roledefs.go`: "could not check the role definitions: %s" becomes
   "could not check the agent definitions: %s", and "role definitions are stale; …"
   becomes "agent definitions are stale; …". Port the test at
   `internal/doctor/doctor_test.go:~260`.
3. `internal/availability/gates.go` `GateKindText(RolesMissing)`: "roles missing"
   becomes "agents missing". Port every test asserting "roles missing (…)":
   roles_views, candidate and off tests, plus any golden. Leave the Go constant
   `RolesMissing` alone (D3).
4. `internal/view/candidates_list.go`: "(no role)" becomes "(no actor)".
5. The bare process word `builder`, meaning the runner, in user-facing text:
   - `internal/relevo/send.go`: "sent round %d to %s's builder" becomes "sent round %d
     to %s's runner".
   - `cmd/relevo/ask.go`: "ask the builder that built this closed round" becomes "ask
     the runner that ran this closed round".
   - `internal/relevo/remote_send.go`: "cannot change a binding's builder" becomes
     "cannot change a binding's candidate". The value there is the candidate, not the
     process.
   - `cmd/relevo/main.go` usage lines, about lines 36, 43, 46, 47 and 55. Where a line
     means the process, use "runner"; where it means the flag, use `--candidate`; where
     it means the seeded actor, keep "builder". Report every line, old and new.
   - The text-mode status row's `builder` label in `internal/view/render.go`: the
     label for the runner's state becomes `runner`. **Text mode only**; A4-2a owns
     the JSON.
   - The other `internal/view/render.go` process-sense uses, by the same rule.
     Report each one.
6. Comments that name a **removed** flag or verb (`--builder`, `--role` on bind or ask,
   `config agents --role`, `--no-roles`, `config roles-init`) are now wrong. Update
   them to the new flag. Leave comments that use "builder" or "role" in other senses;
   D3 keeps internal words.

Port every test asserting a changed string, and delete none. Regenerate only the
goldens that print a changed string, and list each one with a reason.

## Checks, on this server

- Focused loop: `go build ./... && go test -count=1 ./cmd/relevo/ ./internal/doctor/ ./internal/availability/ ./internal/view/ && go test -count=1 ./internal/relevo/ -run 'Send|Roles|Candidate|Off|Remote'`
- Full check, once at the end:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`.

## Commit

Commit as **one new commit**: `feat(a4): the remaining runner, actor and agent words`.
Never amend, and never rebase. This round needs no plan copy; the owner adds it.

## Report

The report covers:
- every changed string, old and new;
- the goldens that moved;
- the tests ported;
- `make check`'s last lines.
