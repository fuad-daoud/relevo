# A5 R3: a reader actor can be bound (local only)

Spec: `docs/specs/2026-09-24-cockpit-design.md`, §3.2, §3.4 and D6. A5 also has these
owner decisions:
- **a binding keeps one actor shape**, recorded at bind;
- **readers are local-only in A5** (remote readers come later);
- **a reader may share a writer's tree**;
- `relevo ask` is removed later in A5 (R6), so do not extend it.

**Vocabulary:**
- an **actor** is config;
- a **runner** is the live process and plays one actor;
- a **reader** actor's agent has shape `reader` (reviewer, researcher, architect, and
  custom reader agents);
- a **writer** actor's agent has shape `writer` (plan-executor).

This round lets a reader actor be **bound**. It does **not** run reader rounds: R4
does. Until R4, `send` to a reader binding is refused with a clear message.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. The binding records its shape (internal/store, format 8 → 9)

- Add `Binding.Shape string` with `json:"shape"`. Its values are `"writer"` or
  `"reader"`, and it is **always written**.
- Decoding a record with no `shape` (format ≤ 8) gives `"writer"`. Every binding before
  A5 is a writer, because readers could not be bound.
- `BindingFormat` becomes **9**, and `recordFormat` returns 9. The format.go comment
  says why: the shape decides which tree a round runs in, and an older binary would
  erase it.
- Regenerate `binding-shape.golden` with the sanctioned `-update`.
- Test `TestBindingShapeDefaultsToWriter`: a format-8 record decodes to
  `Shape == "writer"`, and encoding writes `"shape":"writer"` at format 9.

## 2. Bind accepts readers (internal/relevo)

- In `writer_role.go`:
  - `checkWriterRole` becomes `actorShape(reg, role) (string, error)`. It returns
    `"writer"` or `"reader"` for a known actor (`harness.ShapeBuilder` is writer,
    anything else reader).
  - An unknown actor keeps today's `ErrUnknownRole` error.
  - Delete `ErrNotAWriterRole` and its "runs through relevo ask" text.
  - `CheckWriterRole` (exported, used by `internal/serve`) becomes `ActorShape(rt,
    role) (string, error)`, with the same rule.
- `bind.go` `create` (~line 518) and `add.go` (~line 120): record
  `b.Shape = actorShape(...)`.
- **Readers refuse what only makes sense for a writer.** Each of these is an error at
  bind or send whose text names the flag:
  - `--gate` given for a reader: "a reader round has no check";
  - `--regate` given for a reader;
  - `send --verify` on a reader binding.
- **Policy defaults do not apply to a reader:** `gate.default` does not set `b.Gate`,
  and `verify.default` does not set `RoundVerify`.
- **Send to a reader binding** (`internal/relevo/send.go`, before any spawn): refuse
  with `ErrReaderRoundsNotYet`, "reader rounds are not available yet: <binding> is
  bound to reader actor <actor>". R4 deletes this refusal.
- **Remote readers are refused, local-only:**
  - The client refuses `bind --server … --actor <reader>` before contacting the
    server (`add.go`'s remote path, ~line 113, or wherever the remote add begins),
    with "reader actors run locally only; bind without --server".
  - The server's check (`internal/serve/bindings.go:~158-164`, via `ActorShape`)
    refuses a reader with the same words.
  - The server check needs no wire change: `CreateBindingRequest.actor` exists.

## 3. A reader may share a writer's tree (internal/store, internal/relevo, cmd/relevo)

- `store.assertCWDFree` (lifecycle.go:~263) and the pre-checks in `bind.go` (~551) and
  `add.go` (~279) apply **only between writers**:
  - a reader binding never blocks, and is never blocked by, another binding on the same
    CWD;
  - two writers on one CWD are still refused, exactly as today.
- `store.FindByCWD` (lifecycle.go:~64) resolves a verb's binding from its cwd:
  - it returns the **writer** on that CWD when there is one;
  - with no writer and exactly one reader, it returns that reader;
  - with no writer and several readers, it returns a new `ErrAmbiguousCWD`, "several
    readers are bound to <cwd>: pass --name".

  Every caller (cmd/relevo/args.go:~41 and ~126, and any other; grep for `FindByCWD(`)
  surfaces that error as is. DONE bindings are ignored, as today.
- `bind --actor <reader>` from inside a writer's tree therefore succeeds. The
  reader's CWD is the writer's tree, which R4 copies into a scratch worktree.
  `--worktree` with a reader stays allowed.

## 4. Text

- `bind --actor` help: "the actor this binding runs (default builder); a reader actor
  leaves artifacts and never changes the tree".
- `cmd/relevo/doctor_checks.go:~205` and any other text pointing a reader at `ask`:
  point at `bind --actor <reader>` instead.

## 5. Tests

1. `TestBindAReaderRecordsItsShape`: bind `--actor reviewer` locally, stored
   `Shape == "reader"`.
2. `TestReaderSharesAWritersTree`: a writer is bound on CWD X; binding a reviewer on X
   succeeds; a second writer on X is still refused with `ErrCWDTaken`.
3. `TestFindByCWDPrefersTheWriter`, and `TestFindByCWDAmbiguousReaders`.
4. `TestReaderRefusesGateRegateAndVerify`, plus that `gate.default` and
   `verify.default` do not apply to a reader.
5. `TestSendToAReaderIsNotYetAvailable`.
6. `TestRemoteReaderIsRefusedLocallyAndOnTheServer`: the client refusal as a pure
   function or through the add path with a fake remote, **no network**; the server
   refusal through the existing httptest serve tests.
- Port every test that asserted `ErrNotAWriterRole` or the "runs through relevo ask"
  text (writer_role_test.go:~58-77 and ~188, and the serve tests) to the new behaviour.
  Report each one.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) `assertCWDFree` treats readers as writers: test 2 fails.
  - (b) `FindByCWD` returns the first match: test 3 fails.
  - (c) Decoding leaves `Shape` empty for old records: `TestBindingShapeDefaultsToWriter`
    fails.
- No test spawns a harness or reaches the network (CLAUDE.md). cmd/relevo tests only
  parse flags.

## 6. Working efficiently

- Batch-read these:
  - internal/relevo/{writer_role,bind,add,send}.go;
  - internal/store/{binding,format,lifecycle}.go;
  - internal/serve/bindings.go;
  - cmd/relevo/{bind,args,doctor_checks}.go;
  - and the tests next to each.
- Focused loop: `go build ./... && go test -count=1 ./internal/store/ ./internal/serve/ && go test -count=1 ./internal/relevo/ -run 'Bind|Add|Role|Actor|Send|CWD|Reader|Remote' && go test -count=1 ./cmd/relevo/ -run 'Bind|Args|Contract|Doctor'`
- Full, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a5-r3-bind-reader.md`. Commit as
**one new commit**: `feat(a5): a reader actor can be bound, locally, and share a writer's tree`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the ported tests;
- the goldens that moved;
- the mutations;
- `make check`'s last lines;
- anything that did not match.
