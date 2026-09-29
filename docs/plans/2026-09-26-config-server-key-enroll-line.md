# Plan: `relevo config server key --enroll-line`

## 1. System overview

`relevo config server key` prints two lines: `client id <id>` and the
enrollment line `ed25519 <pubkey> <comment>`. The burst provider in
fuad-daoud/servers (#391) seeds each worker by enrolling the head's key. It
needs the enrollment line alone, in a stable form. It parses stdout, so the
output must be exactly one line.

This round adds `--enroll-line`. With the flag, the command prints only the
enrollment line followed by a newline. The key is generated if it is absent,
exactly as it is without the flag. Without the flag, the output does not change.

## 2. Working efficiently

- Batch the reads and edits below as parallel tool calls. Every location is
  named here, so there is nothing to search for.
- Make each file's change in one edit call.
- Focused test: `go test -count=1 -run 'TestConfigServerKey' ./cmd/relevo/`
- Full check, once, at the end: `make check`. If a hook blocks `make check`
  locally, run these instead and say so in the report:
  - `go vet ./cmd/relevo/`
  - `gofmt -l cmd/relevo`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
  - `go test -count=1 ./cmd/relevo/`
- If a step is impossible as written, or contradicts the code, stop and
  report. Do not improvise.

## 3. File structure (all edits, no new files except the plan copy)

```
cmd/relevo/config_server.go   configServerKey: parse --enroll-line; printClientKey gains a mode
                              usage text for `server key` (line ~23)
cmd/relevo/config_test.go     one new test next to TestConfigServerKeyIsStable (~line 314)
README.md                     the `relevo config server key` bullet (~line 432): mention the flag
docs/plans/2026-09-26-config-server-key-enroll-line.md   this plan, saved verbatim (last step)
```

## 4. Contracts

- **`configServerKey(args []string) error`**
  (`cmd/relevo/config_server.go`, ~lines 49-69).
  - Registers a bool flag `enroll-line` on the existing `fs`, with the help
    text "print only the enrolment line (ed25519 <pubkey> <comment>)".
  - Any positional argument after the flags is a usage error: exit code 2,
    through the existing `exitCodeErr{code: 2}` path.
  - Behaviour is otherwise unchanged: `newRuntime`, then `ensureClientKey`,
    then print.
- **`printClientKey(pem []byte, enrollOnly bool) error`**
  (~lines 229-238). With `enrollOnly` it prints only
  `client.EnrollLine(kp)` plus `\n`. Without it, it prints the two lines as
  today. Update its one caller. Its doc comment must still say what it
  prints, and nothing more.
- **The `server key` usage line** (~line 23) becomes
  `relevo config server key [--enroll-line]`.
- **Stability promise.** State it once, as the flag's help text or a
  one-line comment on the flag: "the provider parses this line; its format
  is `remote.MarshalPublic`'s". Do not add more commentary than that. The
  repo's comment rules apply (CLAUDE.md "Code style").

## 5. Pseudocode

```
configServerKey(args):
  fs := FlagSet; enrollOnly := fs.Bool("enroll-line", false, ...)
  parse; on help -> return; on error or fs.NArg() > 0 -> exit 2
  rt := newRuntime(); pem := ensureClientKey(rt)
  return printClientKey(pem, *enrollOnly)
```

## 6. Error handling

There are no new error types. A bad flag or an extra argument exits 2, as
today. A key or runtime error is returned unchanged.

## 7. Test (in `cmd/relevo/config_test.go`)

Add `TestConfigServerKeyEnrollLinePrintsOnlyTheLine`. It uses `initRoot(t)`
and `captureOutput`, as `TestConfigServerKeyIsStable` does.
- Run `config server key` without the flag, then
  `config server key --enroll-line`.
- Assert that the flag output is exactly one line ending in `\n`.
- Assert that the line starts with `ed25519 `.
- Assert that the line equals the second line of the plain output, so both
  forms refer to the same key.
- Also assert that running `--enroll-line` FIRST on a fresh root generates the
  key. Either use a fresh `initRoot` in a subtest, or order the calls
  flag-first and then compare against the plain output.

The test runs no harness and contacts no network. `initRoot` isolates it
from the user's real config (CLAUDE.md "Merging and CI").

## 8. Ordered steps

1. Edit `cmd/relevo/config_server.go` (flag, arg check, `printClientKey`
   mode, usage line).
   - Verify: `go build ./cmd/relevo/`.
2. Add the test to `cmd/relevo/config_test.go`.
   - Verify: the focused test passes.
   - Mutation check: temporarily make `enrollOnly` print both lines; the new
     test must fail. Revert.
3. README bullet: add one sentence saying that `--enroll-line` prints only
   the `ed25519 ...` line, for scripts.
4. Full check (§2).
5. Save this plan verbatim as
   `docs/plans/2026-09-26-config-server-key-enroll-line.md` and include it in
   the commit.
6. Commit on the binding's branch:
   `feat(config): config server key --enroll-line prints only the enrolment line (#391)`.

**Scope:** exactly the four files in §3. Anything else in the diff is out of
scope, so report it rather than make it.
