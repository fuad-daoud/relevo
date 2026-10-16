# Plan: comment judgement for every language a small lexer can read

## Behaviour

`actors.<name>.scope` with `comments: true` lets a writer change an out-of-scope
source file when the old and new contents differ only inside comments. Today that
holds for `.go` only (`internal/pathscope/judge.go:70` refuses any other extension
before any comment rule is reached). This plan extends the same contract to a
compiled-in table of extensions, judged by a small lexer that knows each
language's string literals.

Judging order for one changed path (unchanged cases first, so the Go path is
untouched):

1. A symlink or gitlink on either side, or a mode change between two present
   sides -- refused. Unchanged.
2. The path matches the scope's patterns -- in scope. Unchanged.
3. `.go` -- the existing `go/scanner` rule (`internal/pathscope/golang.go`),
   reached exactly as today: `Comments` false refuses, `M` compares token
   streams then directive lists, `A`/`D` is new/deleted source, any other status
   is outside scope. Unchanged.
4. An extension in the comment table:
   - `Comments` false -- refused, `outside scope; cannot judge` (same wording as
     a `.go` file with comment judgement off).
   - `M` -- read both blobs; a read error, or either side unlexable -- refused,
     `cannot judge`; the two files' code lines differ -- refused, `code change`;
     the ordered directive lists differ -- refused, `directive comment`;
     otherwise in scope.
   - `A`/`D` -- refused, `new/deleted source file`. A new file has no old side to
     judge, and the reason name is not Go-specific.
   - any other status -- refused, `outside scope; cannot judge`.
5. An extension not in the table (`.json`, `.txt`, `.rb`, `.php`, a binary, a
   path with no extension such as `Makefile`) -- refused,
   `outside scope; cannot judge`, exactly as today.

No new reason constants: the four in `judge.go:26-34` are reused, so the README's
refusal wording, `scopeHaltText` and the payload line stay as they are.

### The rule for a table language (the code-line rule)

Two sources differ only in comments when:

- every comment is removed, and
- the remaining **lines** match one for one, in order.

A line is compared after its comment spans are cut out and its trailing
whitespace is trimmed; a line that is empty, or empty once its comments are cut
out, is dropped from both sides. Consequences, all of them intended:

- comment text, a comment's own line, a comment added, removed or moved, and
  blank lines are free;
- everything else, **including leading whitespace**, is code. Indentation is
  significant in Python and YAML, so it is never free -- this is where the rule
  is deliberately stricter than the Go token comparison, which drops whitespace
  because `gofmt` moves it;
- a Python docstring, a `"""` block, a `$'...'` body or any other string literal
  is code: a change inside one is `code change`, which is the seed's decision
  that docstrings stay out of scope.

Directives are separate from the free text, as for Go: a comment whose text
matches the language's directive prefixes goes into an ordered list instead, and
the two lists must be equal, else `directive comment`. The list is compiled into
the package, never config -- an editable list would let an actor loosen its own
check.

`cannot judge` is reported when either side fails to lex: an unterminated string,
block comment, long bracket or heredoc, or an unterminated `{-`.

## The language table

Keyed by `filepath.Ext(path)` lowercased. `.go` is not in the table; it keeps the
scanner path. Extensionless files are not in the table.

`#` line comments:

| extensions | strings the lexer must handle | notes |
| --- | --- | --- |
| `py`, `pyi` | `'`, `"`, `'''`, `"""`, with `r`/`b`/`f`/`u` prefixes and backslash escapes | `#` anywhere outside a string |
| `sh`, `bash`, `zsh` | `'`, `"`, `$'`, `$"`, heredocs (`<<WORD`, `<<-WORD`, `<<'WORD'`, `<<\WORD`) | `#` only at the start of a word -- line start, or after whitespace or one of `;&|()<>`; so `echo a#b`, `$#` and `${#x}` are code. `<<` inside `$(( ))`/`(( ))` is a shift, not a heredoc |
| `yaml`, `yml` | `'`, `"` | `#` only at the start of a word; block scalars (`\|`, `>`, with chomping and an indentation digit) are strings, so a body line starting `#` is code |
| `toml` | `'`, `"`, `'''`, `"""` | `#` anywhere outside a string |
| `r` | `'`, `"` | `#` anywhere outside a string |

`//` line comments and `/* */` block comments:

| extensions | strings | notes |
| --- | --- | --- |
| `js`, `jsx`, `mjs`, `cjs`, `ts`, `tsx` | `'`, `"`, backtick templates | `${...}` stays inside the string token, so a marker in an interpolation is code |
| `java`, `kt`, `kts`, `scala` | `'`, `"`, `"""` | |
| `cs` | `'`, `"`, `@"..."`, `"""` | |
| `swift` | `"`, `"""`, `#"..."#` | `'` is not a string in Swift |
| `dart` | `'`, `"`, `'''`, `"""`, `r'...'`, `r"..."` | |
| `c`, `h`, `cc`, `cpp`, `hpp` | `'`, `"`, and a backslash before a newline continues the string | |
| `rs` | `"`, `r"..."`, `r#"..."#` (any number of `#`), `b"..."`, `c"..."` | block comments nest; `'` is a lifetime, so it is not lexed as a string |
| `sql` | `'` with `''` doubling, `$tag$...$tag$` | block comment `/* */`; `#` is deliberately not a comment, so a MySQL `#` comment edit is refused rather than a `#` in another dialect being read as one |
| `lua` | `'`, `"`, `[[...]]`, `[=[...]=]` | block comments `--[[...]]`/`--[=[...]=]` |
| `hs` | `"` with backslash escapes and string gaps | `--` is a comment only when the next character is not a symbol character (`!#$%&*+./<=>?@\^|-~:`), so `-->` stays code; `{- ... -}` nests; `{-# ... #-}` is a directive |

Directive prefixes, per family, matched against the comment's text with the
markers stripped and surrounding space trimmed (Go keeps its own table):

- `py`: `#!` (first line), `# -*- coding`, `# coding:`, `# type:`, `# noqa`,
  `# pyright:`, `# pylint:`, `# ruff:`, `# fmt:`, `# isort:`, `# mypy:`,
  `# pragma:`, `# flake8:`.
- `sh`, `bash`, `zsh`: `#!` (first line), `# shellcheck`, `#shfmt:`, `# shfmt:`.
- `yaml`, `yml`: `# yamllint`, `# yaml-language-server:`, `# yamlfmt`.
- `toml`: `#:schema`, `# taplo:`.
- `js`, `jsx`, `mjs`, `cjs`, `ts`, `tsx`: `// @ts-`, `/// <reference`,
  `// eslint-`, `/* eslint-`, `// prettier-ignore`, `// biome-ignore`,
  `// deno-lint-ignore`, `// istanbul ignore`, `// @flow`, `//#region`,
  `// @vitest-environment`, `// @jest-environment`, `// webpack`.
- `java`, `kt`, `kts`, `scala`, `cs`, `swift`, `dart`: `//noinspection`,
  `// language=`, `// swiftlint:`, `// ignore:`, `// ignore_for_file:`,
  `// dart format`, `// coverage:ignore`, `// region`, `// endregion`,
  `// ReSharper`.
- `c`, `h`, `cc`, `cpp`, `hpp`: `// NOLINT`, `//NOLINT`, `// clang-format`,
  `/* clang-format`, `// IWYU pragma:`, `// cppcheck-suppress`, `// clang-tidy`,
  `// ReSharper`.
- `sql`: `-- noqa`, `-- sqlfluff:`, `-- name:`, `-- +migrate`, `/*!`.
- `lua`: `---@`, `-- luacheck:`, `-- stylua:`, `-- selene:`.
- `hs`: `{-#`.
- `r` and `rs`: none.

### Deliberately excluded, and why

The rule is: include a language only when every string form that can contain a
comment marker is lexable by a small, deterministic lexer; leave the rest out
rather than guess. Excluded:

- `rb`: `%q`/`%w`/`%i`/`%r` literals, `?x` character literals and
  `=begin`/`=end`. A `#` inside a `%w[a#b]` word would be read as a comment, so a
  change to that word would pass as a comment-only edit.
- `php`: heredoc/nowdoc, and a `.php` file mixes `#` and `//`.
- `pl`, `pm`: heredocs and `q//`.
- everything else, including `Makefile`/`Dockerfile` (no extension), `.vue`,
  `.svelte`, `.jsonc`, `.xml`, `.html`, `.css`. These keep today's refusal.

## Decisions settled (one each)

1. **The rule for a table language is the code-line rule above**, not a token
   stream: without a parser or a formatter, whitespace cannot be assumed
   insignificant, and Python and YAML prove it. Only `.go` keeps the
   `go/scanner` token comparison, so its behaviour and its tests are unchanged.
2. **`.go` is not a table entry.** The table is consulted only after the `.go`
   branch, so the cgo and `parsesCleanly` rules cannot be bypassed.
3. **Directives are a compiled-in table per language**, compared as an ordered
   list of raw comment texts with trailing whitespace trimmed, in file order.
4. **The extension lookup lowercases** `filepath.Ext(path)`; `.PY` is Python.
   Basenames are not consulted, so `Makefile` and `Dockerfile` are refused as
   today.
5. **`A`/`D` on a table language is `new/deleted source file`.** Today it is
   `outside scope; cannot judge`; both refuse, and the new reason is the accurate
   one. Pinned by a test.
6. **`Comments` false refuses a table language the same way it refuses `.go`**:
   `outside scope; cannot judge`, no blob read.
7. **A lex error on either side is `cannot judge`**, never a pass. Refuse, never
   guess.
8. **Where the code lives** -- three new files in `internal/pathscope`, so the
   package keeps one concept (judging a scope) and no file approaches 600 lines:
   `languages.go` (the table, the lookup, the directive table),
   `lex.go` (the lexer), `comments.go` (the code-line rule and the judge). The
   lexer must be split into small functions: `funlen` is 70 lines and
   `gocognit` 30, and `internal/pathscope` is not in `.golangci.yml`'s
   exclusions.
9. **The prose that says "Go" becomes wrong and is corrected**:
   `internal/pathscope/scope.go`'s package comment and `Comments` field comment,
   `internal/view/actors_list.go:94`, `internal/ui/view_actor.go:124` (dropping
   its `(#801)` citation while the line is rewritten), and README's scope
   paragraph. No comment gains a history citation; `internal/ui/view_actor.go`
   stays on `scripts/check-comments.allow` for its `§4` citations.
10. **The librarian's four definitions say a docstring is a string**, so the
    writer does not promise something the scope refuses. `sh
    scripts/agents-shipped.sh --write` regenerates `shipped.sha256`; the script
    is append-only, so the file only grows.

## Seed against the code

The seed's constraint "keep the Go judge's behaviour identical (its tests must
pass unchanged)" cannot hold literally, and this is the one place it does not.
`internal/pathscope/judge_test.go:147` (case 8, "other extension refused") lists
`docs/specs/probes/x.ts` among the refused paths, and `blobs(nil)` returns empty
blobs for both sides, so once `.ts` is judgeable that path becomes a comment-only
pass and the test fails. That entry pins exactly the behaviour this round deletes.

Resolution, and the only test change in the round: the `.ts` entry leaves case 8's
list (replaced by an extension outside the table, `.rst`), and the `.ts` comment
rule is pinned by the new table tests. Cases 1-7 and every Go assertion are
untouched, `golang.go` is untouched, and no test is deleted. The report must call
this out as the deviation it is.

## Seams

- **`internal/pathscope/judge.go:70-101`** -- replace the `strings.HasSuffix(...,
  ".go")` gate with the table lookup and a second status switch; `judgeOne`'s
  case order 1 and 2 stays first. `Change`, `Violation` and the four reason
  constants (`:26-34`) unchanged.
- **`internal/pathscope/golang.go`** -- untouched, including `goDirective`.
- **New `internal/pathscope/languages.go`** -- the syntax table, `langFor(path)`
  and the directive table.
- **New `internal/pathscope/lex.go`** -- the lexer: comment spans, the ordered
  directive list, and the error for an unterminated form.
- **New `internal/pathscope/comments.go`** -- the code-line rule and
  `judgeComments(spec, oldSrc, newSrc) (ok bool, reason string)`, mirroring
  `judgeGo`'s signature and reason strings.
- **`internal/pathscope/scope.go:4-5` and `:23-25`** -- the package and field
  comments stop saying "Go".
- **`internal/pathscope/judge_test.go:147`** -- the `.ts` entry.
- **New `internal/pathscope/comments_test.go`** -- every table test.
- **`internal/harness/agents/librarian.claude.md:18,20`**,
  **`librarian.opencode.md:18,20`**, **`librarian.codex.toml:16,18`**,
  **`librarian.agy.md:33,35`** -- the in-scope bullet and the out-of-scope
  paragraph. The anchors `doc comments`, `//go:build`, `//nolint`, `@ts-ignore`
  and "A round that changes code is a failed round" must survive
  (`internal/harness/agents_test.go:250-273`).
- **`internal/harness/agents/shipped.sha256`** -- regenerated by
  `sh scripts/agents-shipped.sh --write`; `make check` runs
  `scripts/agents-shipped_test.sh`, which fails until it is.
- **`internal/view/actors_list.go:94`**, **`internal/ui/view_actor.go:124`** --
  "comment-only Go edit" becomes "comment-only edit".
- **`README.md:1703-1709`** -- the scope paragraph's comment rule.
- **New `docs/plans/2026-10-15-comment-judging-languages.md`** -- this plan,
  saved verbatim in the same commit as the code.
- No change to `Scope`, `Validate`, `Match`, `DocsSet`, the roles validation,
  `internal/relevo/scopecheck.go`, the views' rendered text or any golden.

## Steps

Each step is one or more new commits. Never amend or rebase a commit already on
the remote binding's branch.

1. **`languages.go`.** The syntax table, the directive table and `langFor`.
   Done when `go test ./internal/pathscope/ -run TestLanguageTable -count=1`
   passes: lowercase keys, `.go` absent, every extension in the table above
   resolving, `.rb`/`.php`/`.pl`/`.json`/`.txt`/`Makefile` resolving to nothing,
   and every entry carrying at least one comment marker.
2. **`lex.go`.** Comment spans, the ordered directive list, the unterminated-form
   error. Done when `go test ./internal/pathscope/ -run TestLex -count=1` passes:
   one case per string form in the table, plus heredocs, YAML block scalars, Lua
   long brackets, nested Rust block comments, the Haskell `--` operator, and an
   unterminated form per family.
3. **`comments.go` and the `judge.go` dispatch.** Done when
   `go test ./internal/pathscope/ -run 'TestJudge|TestComments' -count=1` passes,
   including the untouched Go cases.
4. **The tests below**, in the new `comments_test.go` and the one-line change to
   `judge_test.go:147`. Done when every named test passes and every mutation
   fails the test named beside it.
5. **The four librarian definitions**, plus `sh scripts/agents-shipped.sh
   --write`. Done when `sh scripts/agents-shipped.sh --check` exits 0 and
   `go test ./internal/harness/ -run TestLibrarian -count=1` passes. The report
   states the line count of `shipped.sha256` before and after.
6. **The Go prose** (the four comments in `scope.go`, `actors_list.go`,
   `view_actor.go`). Done when `grep -rn "comment-only Go" internal/` is empty
   and `go test ./internal/view/ ./internal/ui/ -count=1` passes with no golden
   changed.
7. **README.** Done when the scope paragraph names the rule, every supported
   extension, the directive examples, that a docstring or other string is code,
   and that an extension outside the table is refused as before.
8. **Save this plan** as `docs/plans/2026-10-15-comment-judging-languages.md`.
9. **Full check.** Focused: `go test ./internal/pathscope/ ./internal/harness/
   ./internal/view/ ./internal/ui/ -count=1`. Final: `make check`.
   `internal/pathscope`'s coverage baseline is 86.1 and must not be lowered; add
   tests if the measurement falls more than a point below it
   (`RELEVO_REQUIRE_COVERAGE=1 sh scripts/check-coverage.sh`). No new exclusion,
   no baseline edit.

## The tests, and the mutation each must catch

In `internal/pathscope/comments_test.go`:

1. `TestCommentOnlyEditPassesEveryTableLanguage` -- one old/new pair per
   extension, differing only inside a comment, no violation. Mutation: delete any
   table entry.
2. `TestCodeChangeRefusedEveryTableLanguage` -- the same pairs with one byte
   changed outside a comment, `code change`. Mutation: compare comment spans
   instead of code lines.
3. `TestCommentMarkerInsideStringIsCode` -- a `//` in a JS string, a `#` in a
   Python string, `` `//` `` in a template, `--` in a Lua long string, each
   changed, `code change`. Mutation: drop string lexing for that family.
4. `TestDirectiveCommentRefused` -- `# noqa`, `# type: ignore`,
   `# -*- coding`, `// @ts-ignore`, `// eslint-disable-next-line`, `#!` on line 1,
   `---@type`, `{-# LANGUAGE #-}`, each changed, `directive comment`. Mutation:
   drop one prefix from the table.
5. `TestCannotJudgeUnterminated` -- an unclosed `/*`, `{-`, `"""`, `[[`, heredoc,
   `code change`'s sibling `cannot judge`. Mutation: treat a lex error as a pass.
6. `TestHashNotAtWordStartIsCode` -- `echo a#b` and `key: a#b`, one character
   changed after the `#`, `code change`. Mutation: treat every `#` as a comment.
7. `TestShellHeredocBodyIsCode` -- a heredoc body line starting `#`, changed,
   `code change`. Mutation: drop heredoc handling.
8. `TestShellArithmeticShiftIsNotAHeredoc` -- a file containing `$((1 << 2))`
   with an unrelated comment edit, no violation. Mutation: drop the arithmetic
   guard, which turns the file into `cannot judge`.
9. `TestYamlBlockScalarBodyIsCode` -- a `|` body line starting `#`, changed,
   `code change`. Mutation: drop block-scalar handling.
10. `TestIndentationChangeRefused` -- a Python and a YAML indentation change,
    `code change`. Mutation: trim leading whitespace before comparing.
11. `TestCommentAddedRemovedAndMovedPasses` -- a whole-line comment added,
    another deleted, a third moved, no violation. Mutation: require equal comment
    counts or positions.
12. `TestNestedBlockCommentEditPasses` -- `/* /* old */ */` in Rust, no
    violation. Mutation: drop the nesting flag.
13. `TestNewOrDeletedTableLanguageRefused` -- `A` and `D` on `.py`,
    `new/deleted source file`. Mutation: fall through to `outside scope`.
14. `TestCommentsDisallowedTableLanguageRefused` -- `Comments` false on `.py`,
    `outside scope; cannot judge`. Mutation: judge the comments anyway.
15. `TestExtensionOutsideTableRefused` -- `.json`, `.txt`, `.rb`, `.php`, `.pl`,
    `Makefile`, `Dockerfile`, `outside scope; cannot judge`. Mutation: make the
    lookup default to a language.
16. `TestExtensionLookupIsCaseInsensitive` -- `x.PY` judged as Python. Mutation:
    drop the lowercase.
17. `TestLanguageTableMatchesDocumentedSet` -- the table's extension set equals
    the documented set, so a language cannot be added without the README and the
    tests moving with it. Mutation: add an extension.

In `internal/pathscope/judge_test.go`:

18. Case 8's path list loses `docs/specs/probes/x.ts` and gains `docs/specs/probes/x.rst`.
    Mutation: nothing to mutate here; it is the seed's one deviation, and case 3's
    `.go` pass and case 8's other two paths keep pinning the old behaviour.

## Deleted

1. `internal/pathscope/judge_test.go:147` -- the `docs/specs/probes/x.ts` entry in
   case 8's path list. No test and no test case is deleted.
2. `internal/pathscope/judge.go:70-73` -- the blanket `!strings.HasSuffix(path,
   ".go")` refusal, replaced by the table lookup.
3. `README.md:1708-1709` -- "as is any other extension, a binary or a symlink",
   replaced by the table rule.
4. `internal/pathscope/scope.go:4-5`, `:23-25` and `internal/view/actors_list.go:94`,
   `internal/ui/view_actor.go:124` -- "Go" as the language of a comment-only edit,
   and the `(#801)` citation on the last of them.
5. `internal/harness/agents/librarian.claude.md:18`, `.opencode.md:18`,
   `.codex.toml:16`, `.agy.md:33` -- `docstrings` from the in-scope bullet, and
   nothing else from those files.
6. Nothing else: `golang.go` in full, `goDirective`, `scanGo`, `judgeGo`,
   `parsesCleanly`, the four reason constants, `Scope`, `Validate`, `Match`,
   `DocsSet`, the roles plumbing, `internal/relevo/scopecheck.go`, the views'
   output and every golden are kept as they are.

## Not done in this round

- `.rb`, `.php`, `.pl`, `.html`, `.xml`, `.css`, `.vue`, `.svelte`, `.jsonc` and
  extensionless files: refused as today, for the reasons above.
- A tree-sitter or per-language parser; the rule stays lexical.
- Making the table configurable, or exposing it through `relevo config`.
- Migrating existing DBs, or changing the seeded `{"paths": ["@docs"], "comments":
  true}`.
- Any change to the remote/server path: a served binding is judged by the
  server's own binary, so the server must be released for the new languages to
  apply there.

## The report must include

- Per-step status and the new commits, with `git diff --stat` against these seams.
- The final language table, including the excluded languages and each exclusion's
  reason.
- The one-line `judge_test.go` change called out as the seed's only deviation,
  with the reason the entry could not stay.
- Each mutation, run, with the test that failed.
- The `make check` result, the `internal/pathscope` coverage measurement, and a
  statement that no baseline or exclusion was touched.
- `shipped.sha256`'s line count before and after `--write`, and that `--check`
  passes.
- The README paragraph as written.
- A halt instead of improvising if any seam above contradicts the code.
