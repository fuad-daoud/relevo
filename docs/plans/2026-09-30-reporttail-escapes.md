# Plan: escape-aware flow scanning in reporttail (#730)

One round on current `main`, one commit. The bug: `internal/reporttail`'s four quote-aware scanners (`flowListDepth`, `splitListElements`, `StripComment`, `UnquoteScalar`) track a single active quote character but do not honour YAML escapes. A `commands_run` item like `"git commit -m \"fix(...)\""` (with the apostrophe in `gh's`) desyncs `flowListDepth`, so the closing `]` reads as quoted and the tail errors `commands_run list is not closed` -- a valid report ingests as `unstructured`. Reproduced on `main`:

```
$ go run ./tmp_parsecheck ~/.local/state/relevo/bugreport-gh/001-report.md
ok=false reason="tail: line 267: commands_run list is not closed" status="" halted_at="" commands=0 not_done=0
```

## Behaviour

- **One scanner.** A shared helper walks a string and identifies the end of a quoted scalar, honouring YAML escapes: inside a `"` scalar a backslash escapes the next rune (`\"`, `\\`); inside a `'` scalar `''` is an escaped quote. It returns bytes/runes outside scalars to the caller. Design its signature to serve all four callers; do not duplicate the escape rules.
- **`flowListDepth`** counts `[`/`]` outside scalars only, using the scanner. Balanced brackets must return 0 even when a scalar contains `\"`, apostrophes, or commas.
- **`splitListElements`** splits on commas outside scalars only; an escaped quote or comma never splits (and the raw scalar, quotes included, is still written into the element exactly as today, because `ParseListValue` unquotes it next).
- **`StripComment`** keeps today's rule -- `#` outside quotes and brackets starts a comment; inside a bracketed flow list it is content -- with the scanner making the quote tracking escape-aware.
- **`UnquoteScalar`** still removes exactly one layer of matching quotes, and additionally unescapes: in a `"` scalar `\"` to `"` and `\\` to `\`; in a `'` scalar `''` to `'`. A scalar without escapes is byte-identical to today.
- **No format, signature or output-shape change** for reports that parse today. `ParseWithReason` keeps its contract; only previously rejected valid reports now parse.

## Steps

1. **Reproduce first.** In `internal/reporttail/reporttail_test.go`, add `TestParseEscapedQuotesInCommandsRun`: a minimal fenced tail extracted from #730's real report (read `~/.local/state/relevo/bugreport-gh/001-report.md`, line 267, for the exact command text; embed only the eight-line tail, never the whole file):
   ```
   ```relevo
   status: done
   halted_at: ""
   changed_paths: ["cmd/relevo/bugreport.go", "cmd/relevo/bugreport_test.go"]
   commands_run: ["git status", "git commit -m \"fix(bugreport): --gh prints the issue URL and surfaces gh's reason (#721)\"", "go test -count=1 ./cmd/relevo/"]
   not_done: []
   ```
   ```
   Assert `ok`, `Status == OutcomeDone`, `len(CommandsRun) == 3`, `CommandsRun[1] == "git commit -m \"fix(bugreport): --gh prints the issue URL and surfaces gh's reason (#721)\""` (unescaped quotes), and `NotDone` empty. Run it **before** the fix and keep the failure output: it must fail with `commands_run list is not closed`.
2. **Table rows for the helpers**, observing the bug before the fix where practical: `flowListDepth` on a balanced value containing `\"` (want 0) and on a genuinely continued value (want 1); `splitListElements` on `"a, b", "c\"d"` (want 2 elements, no split inside quotes); `StripComment` on `["a # kept"] # cut` (want `["a # kept"] `) and on `"x\" # "` (the `#` stays inside the scalar); `UnquoteScalar` on `"git commit -m \"x\""` (want `git commit -m "x"`) and `'it''s'` (want `it's`).
3. **Implement the shared scanner** and rewire the four functions. Keep each function under the repo's line limit and the comments why-only (the escape rules are the why).
4. Focused: `go test -count=1 ./internal/reporttail/ -v`, then its consumers: `go test -count=1 ./internal/reporttail/ ./internal/ingest/ ./internal/relevo/`.
5. **Mutation:** remove the double-quote escape handling from the scanner (or make the `'` pair ordinary) and confirm `TestParseEscapedQuotesInCommandsRun` fails; restore and re-run green.
6. `make check` from the committed tree, exit 0; `gofmt -l $(git ls-files '*.go')` empty.
7. `docs/plans/2026-09-30-reporttail-escapes.md`: this plan in the repo's shape. One commit; do not push.

## The report must include

- The pre-fix failure output of `TestParseEscapedQuotesInCommandsRun`.
- The helper table rows added, by name, and their pre/post status.
- The pointer to the single scanner and every caller rewired to it.
- The mutation result (failing test name, reverted).
- The full `make check` output (exit 0) and the commit hash.