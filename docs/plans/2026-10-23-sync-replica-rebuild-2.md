# Plan: rebuild a corrupt sync replica by itself -- member 2, `relevo db sync retry --rebuild`

Member 2 of two. Member 1 owns the mechanism (`internal/sync`): the classifier,
the aside folder, the record, the cap and the latch. This member is a surface:
one flag, one wire field, one call into member 1's exported
`(*sync.Runner).RebuildReplica`. The dependency is real: this member lands after
member 1, on the same branch. It touches `internal/db/wire`,
`cmd/relevo/db_sync.go` and `internal/relevo/syncverb.go`, apart from the one
seam it calls.

The seam consumed, exactly:

```go
// internal/sync/rebuild.go (member 1)
func (r *Runner) RebuildReplica(now time.Time) (RebuildResult, error)

type RebuildResult struct {
    At    time.Time // when the rebuild ran
    Aside string    // the folder the copy was moved into
    Moved []string  // every file that went with it
}
```

## Behaviour and cases

1. **`relevo db sync retry --rebuild` does by hand what the machine does by
   itself.** It clears the latch and the death count exactly as `retry` does
   today, then stops the worker, moves the replica and its driver files into the
   aside folder, and records the rebuild. The next attempt bootstraps a fresh
   replica from the remote with the stored token and key.
2. **It ignores the cap.** `--rebuild` never consults `RebuildDue`. A machine
   latched by the second corruption in the window is recovered by this one
   command. It does write the record, so the facts status shows stay true and
   the window restarts from the human's rebuild.
3. **Plain `retry` is unchanged.** Without the flag, the verb's behaviour, its
   refusal classes and its printed line are byte-identical to today.
4. **The answer says where the copy is.** The line and the `--json` document
   carry the aside folder.
5. **A machine with no replica succeeds without a move.** The plain retry part
   has already cleared the latch, and the next attempt bootstraps a fresh
   replica anyway, so `ErrNoReplica` is answered OK with no aside and no
   record, and the line says there was nothing to move. (A sandbox run showed
   the refusal this plan first asked for surfacing as `internal`, next
   `relevo bugreport`, for a machine that was already recovering.)
6. **The flag reaches no new secret and no new verb.** `--rebuild` rides the
   existing `retry` frame as a bool; the wire's closed sets of verbs and codes
   are unchanged.

## Seams

- `internal/db/wire/msg.go` (`SyncVerb`) -- `Rebuild bool` with `json:"rebuild,omitempty"`;
  (`SyncResult`) -- `Aside string` with `json:"aside,omitempty"`.
- `cmd/relevo/db_sync_call.go` (`dbSyncVerbOptions`) -- `Rebuild bool`, carried
  into the `wire.SyncVerb`.
- `cmd/relevo/db_sync.go` -- `dbSyncRetryFlagValues.rebuild`, the `--rebuild`
  flag, `cmdDBSyncRetry` sending it and printing the aside line, the usage line
  `relevo db sync retry [--rebuild] [--json]` and its prose, and
  `dbSyncCallDoc.Aside`.
- `internal/relevo/syncverb.go` -- the retry case passes `verb.Rebuild`;
  `retry(rebuild bool)` calls `Runner.Retry()` first, then, only when `rebuild`
  is set, `Runner.RebuildReplica(time.Now())` and answers
  `wire.SyncResult{OK: true, Aside: ...}`. A failure of either is
  `verbRefusal(verbClassify(err), err)`.

## Ordered steps

1. **The wire.** The two `internal/db/wire/msg.go` fields.
   *Works when:* `go build ./...` succeeds and no existing wire test changes.
2. **The flag and the frame.** The `cmd/relevo` seams above.
   *Works when:* the usage assertions in `cmd/relevo/db_sync_test.go` include
   `retry [--rebuild]` and pass, and `cmdDBSyncRetry([]string{"--rebuild"})`
   against the test verb hook prints the aside line.
3. **The verb.** `retry(rebuild bool)` and the four cases in
   `internal/relevo/sync_rebuild_test.go`: `TestRetryRebuildMovesTheReplicaAside`,
   `TestRetryRebuildIgnoresTheWindow`, `TestRetryWithoutTheFlagMovesNothing`,
   `TestRetryRebuildWithNoReplicaClearsTheLatch`; `TestDBSyncRetryLineSaysWhenNothingWasMoved` pins the line. No Turso file is opened and nothing
   dials a remote.
4. **The CLI text.** `TestDBSyncRetryRebuildOutput` pins the plain and the
   `--rebuild` output and `--json` documents through the scripted hook.
5. **The plan file.** This file, committed in the same PR.

Focused loop: `go test ./internal/db/wire/... ./internal/relevo/ ./cmd/relevo/`.
Full gate: `make check-affected`.

## Wire compatibility

Two `omitempty` fields added, no version bump, no new verb, no new code.
`internal/sync/rebuild.go`, `internal/sync/sync.go` and the status files are
untouched by this member.
