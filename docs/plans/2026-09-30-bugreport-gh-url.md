# Plan: `bugreport --gh` prints the issue URL and surfaces gh's reason (#721)

One round on current `main` (`a15912e`), one commit. Small and surgical: `cmd/relevo/bugreport.go` and its tests only.

## Behaviour

**A. The exec seam.** In `cmd/relevo/bugreport.go`, change `bugreportExec` to run with `cmd.CombinedOutput()`:

```go
var bugreportExec = func(ctx context.Context, argv []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	return cmd.CombinedOutput()
}
```

Its `([]byte, error)` signature stays, so `fakeBugreportExec` and every test keep their shape; the recorded argv contract is unchanged.

**B. Success prints the URL.** In `runBugreport`, after `fmt.Println(line)` and the `if !opts.gh { return nil }` guard:

```go
out, err := bugreportExec(context.Background(), argv)
if err != nil {
	if errors.Is(err, exec.ErrNotFound) {
		return fail(codeNotAvailable, "%s", line)
	}
	return fail(codeNotAvailable, "gh issue create: %s", firstLine(string(out)))
}
if text := strings.TrimSpace(string(out)); text != "" {
	fmt.Println(text)
}
return nil
```

Notes:
- `firstLine` is `cmd/relevo/args.go:55`; `strings` may need importing.
- A non-zero exit is `not_available`, not `internal`: gh's auth, permission or network refusal is the user's environment, not a relevo bug, and `internal` would make `recordInternal` leave a last-error slot and hint `relevo bugreport` for it. If gh printed nothing, `firstLine` returns the empty string; then fall back to `err.Error()` in the message so it is never blank (`preflightReason` in `cmd/relevo/update.go:210` is the precedent for that fallback, but it lives for the update path -- inline the two-line choice rather than exporting it).
- The trimmed output is printed as-is: gh prints the new issue URL and nothing else on success.

**C. Untouched.** The default (no `--gh`), `--stdout`, `--json`, `--out`, `--raw`, `--logs`, the argv from `bugreport.IssueArgv`, the printed path-and-line order, and the missing-`gh` branch all keep their current behaviour byte-for-byte.

## Steps

1. Write the failing tests first, in `cmd/relevo/bugreport_test.go`:
   - Change `TestBugreportGhRunsExactArgv` to have the fake return `[]byte("https://github.com/fuad-daoud/relevo/issues/999\n")`, still assert the exact argv, and assert stdout ends with that URL line after the printed command line.
   - Add `TestBugreportGhFailureSurfacesTheReason`: the fake returns `[]byte("gh: To use GitHub CLI, run gh auth login\n")` and a non-nil error (an `*exec.ExitError`-shaped error is fine; construct one or use `errors.New("exit status 1")` if the fake contract only needs non-nil); assert the error code is `not_available` and the message contains gh's line, and that the returned `*cliError` is not `internal`.
   - Keep `TestBugreportGhMissingIsNotAvailable` as it is (fake returns `exec.ErrNotFound`).
2. Implement A and B.
3. Focused: `go test -count=1 ./cmd/relevo/ -run 'Bugreport' -v`; then the package: `go test -count=1 ./cmd/relevo/`.
4. `make check` from the committed tree, exit 0; `gofmt -l $(git ls-files '*.go')` empty.
5. `docs/plans/2026-09-30-bugreport-gh-url.md`: this plan in the repo's shape. One commit; do not push.

## Deleted behaviour

- A failed `--gh` no longer renders as bare `internal: exit status 1` and no longer records a `last-error.json` slot.
- Nothing else: no flag, output line, argv entry, or code path is removed or reordered.

## The report must include

- The test names added/changed with the focused output.
- The success stdout, verbatim (path, command line, URL), from the test.
- The failure message and its code, verbatim.
- The full `make check` output (exit 0) and the commit hash.