# relevo gate --help names the --for value "duration", not "relevo gate --clear"

## Goal

`relevo gate --help` prints

    -for relevo gate --clear
        how long to gate the provider (Go duration, e.g. 2h); omit to leave it gated until relevo gate --clear

because Go's `flag` package takes the first back-quoted word in a usage string as the value's
placeholder name (`flag.UnquoteUsage`). It should print `-for duration`.

## Facts (verified against origin/main 35d855d3)

`cmd/relevo/gate.go:46`:

```go
	forFlag := fs.String("for", "", "how long to gate the provider (Go duration, e.g. 2h); omit to leave it gated until `relevo gate --clear`")
```

It is the only flag usage string in `cmd/relevo` with back-quotes inside it (`git grep` over
`fs.String|Bool|Int|Duration|Var|Func` calls). No golden or test pins this help text.

## Steps

1. Change the usage string so the back-quoted word is the placeholder, e.g.
   `"how long to gate the provider, as a Go \`duration\` (e.g. 2h); omit to leave it gated until relevo gate --clear"`
   (a Go string literal cannot hold a back-quote inside a raw string, so keep it an interpreted
   string with the back-quotes as plain characters, exactly as the current line does).
2. Add a test in `cmd/relevo` that pins the placeholder: find the gate verb's flag set or run the
   `gate --help` path the way existing cmd/relevo help tests do (look for one first and follow it),
   and assert the output contains `-for duration` and does not contain `-for relevo`. The test must
   not spawn a harness or reach the network (help returns before any runtime work -- confirm that
   in the code; if `--help` would build a runtime, test `flag.UnquoteUsage` on the flag instead).
3. Also add a test (or extend the one above) that walks every verb's flag set in `cmd/relevo` if the
   package already has a registry of verbs you can iterate, asserting no placeholder name contains a
   space. If there is no such registry, skip this step and say so in the report.
4. Mutation: restore the old usage string and confirm the new test fails; restore the fix.
5. `make check` must pass; also `gofmt -l .` and `sh scripts/check-comments.sh`.
6. Last step: save this plan verbatim as `docs/plans/2026-09-27-gate-for-placeholder.md` and
   commit it with the change: `fix(gate): --help names the --for value duration`.

## Scope

`cmd/relevo/gate.go`, one `cmd/relevo/*_test.go` file, the plan file. Anything else: halt and
report. CLAUDE.md code style applies (comments say why only, no issue numbers or history). If a
step is impossible as written or contradicts the code, stop and report.
