# Plan T2: tmp-root-and-store-helpers

Part of the disk lane (#1314 runner temp dirs, #1308 Go build cache).

**Behaviour.** A runtime knows the relevo tmp root its rounds and checks write under. The store has safe helpers to create and remove entries beneath it, including for a tenant-owned root. Nothing consumes them yet.

**Seams.**
- `internal/relevo/runtime.go`: add `Runtime.TmpRoot string`. Empty means no per-process dirs, which is the case in tests and in container mode.
- `cmd/relevo/wire.go` (~394-403): set `rt.TmpRoot` to the `tmpDir` already computed by `store.TempDir(root)`. Check that `ConfigWatcher.Refresh` (`internal/relevo/reload.go` ~108) preserves the field.
- `internal/serve/serve.go`, `runtimeAt` (~371):
  - Every non-container mode sets `TmpRoot` to `s.ownerTmpDir(root)`. It is per owner, so one owner's sweep can never see another owner's entries.
  - `applyContainer` (~411) leaves it empty, because the container has its own `/tmp` and host paths mean nothing inside it.
  - Say why in a comment.
- New `internal/store/tmpdirs.go`, with these methods:
  - `(*Store) EnsureTmpEntry(tmpRoot, rel string) (string, error)` creates `<tmpRoot>/<rel>` (for example `rounds/x-001`).
    - It works through `os.OpenRoot(tmpRoot)`, so a planted symlink at `rounds/` or `checks/` is refused rather than followed.
    - It calls the store's `chownCreated` (`store.go` ~139) on every directory it creates. In user mode that hands them to the tenant.
  - `(*Store) RemoveTmpEntry(tmpRoot, rel string) error` removes through the same root, and a missing entry is success.
  - `(*Store) ListTmpEntries(tmpRoot, sub string)` returns the direct children of `<tmpRoot>/<sub>`, with mtimes.

**Steps.**
1. Add `store/tmpdirs.go`. Deliverable: `go test ./internal/store/...` is green.
2. Add the `Runtime.TmpRoot` field and set it in `wire.go` and `runtimeAt`. Deliverable: `go test ./internal/serve/... ./cmd/relevo/...` is green. The `cmd` test only asserts the field is set, with no process spawned.
3. Run `make check-affected`.
4. Save the plan to `docs/plans/2026-10-23-tmp-root-and-store-helpers.md` in the same commit.

**Tests.**
- Store tests:
  - Create then remove round-trips.
  - Removing a missing entry is nil.
  - A symlinked `rounds` pointing outside the root is refused, and the target is left alone.
  - The chown callback sees each created directory, and only those.
  - A `rel` containing `..` is refused.
- Serve tests:
  - `TestRuntimeAtTmpRootPerOwner`: two owners get different roots.
  - Container mode gets an empty root.

**Mutation targets.**
- Replace the `os.Root` creation with `os.MkdirAll`: the symlink test must fail.
- Skip `chownCreated`: the chown test must fail.
- Make container mode set a root: the container test must fail.

**Deleted.** None.

**Report must include.**
- Whether `Refresh` kept `TmpRoot`.
- The exact root chosen for each serve isolation mode.
