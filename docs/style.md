# Code style

The rules every Go change in this repository follows. `make check` enforces
most of them; the rest are for the author and the reviewer.

The dexpace Go styleguide applies, with two
exceptions: there is no "two assertions per function" rule, and an exported
name does not need a doc comment.

## Comments

A comment says **why**, and only where the code cannot: a constraint that is
not obvious, where a number comes from, an ordering that matters, a hazard. It
never restates the code.

A good comment carries a decision the code cannot show:

```go
// An unreadable store refuses the forget: deleting a record a live binding may
// name is worse than making the human retry.
func mastermindInUse(rt relevo.Runtime) func(id string) bool {
```

A comment that only repeats the name adds nothing, so leave it out:

```go
// dialUDS is one plain unix dial.
func dialUDS(ctx context.Context, sock string) (net.Conn, error) {
```

- **Package comments.** One to three lines saying what the package owns. If a
  package has a boundary, say where it is:

  ```go
  // Package text holds the display-text helpers the CLI, the cockpit views,
  // the daemon and the listings share: first-line cuts, small plural and
  // fallback sentences, and millisecond counts.
  ```

- **Doc comments on exported names.** Write one only when it adds something
  the name and the signature do not.
- **Length.** A comment as long as the function it sits on is usually telling
  a story. Keep the reason and drop the story.
- **Trimming.** Cut the restatement, not the definition. A doc comment that
  stays begins with its name, and a field, option or constant whose name does
  not say what it holds keeps the clause that does. Never leave a fragment
  such as "It is also..." or "Nil when..." behind.
- **Reasons hiding in a story.** These stay when a story is cut: two copies
  that must agree, the consequence that makes a hazard a hazard, why a lock or
  transaction is passed in, what `""`, nil or zero means for a parameter or
  field of an exported API, and where a number or a file shape comes from.

## No history

Code describes what is true now. Git and the issues hold how it got there, so
a comment never carries:

- issue or pull-request numbers (`#123`), or spec section marks (`§4.1`);
- "round N", step or phase names from a plan;
- stories about earlier behaviour: "used to", "previously", "before the fix",
  "pre-#NNN", "no longer does X".

"No longer" is fine when it describes the present state, not a change:

```go
// A subject with no live rate-limit entry is absent, which the exporter reads
// as a key the document no longer holds and sends as a delete.
```

When a history comment carries a real reason, keep the reason and drop the
history. Tests follow the same rules.

## Size

- A function is at most 70 lines, comments not counted, with cognitive
  complexity at most 30. Test functions are exempt from both: a table-driven
  test reads best as one long function.
- A non-test `.go` file is at most 600 lines; aim for about 500. Test files
  have no limit.
- One package per concept. When a file grows past its concept, split it into
  files named for what they hold, moving whole functions.

## Shared helpers

Look before writing a helper: wording helpers live in `internal/text`, the
shapes a human reads in `internal/view`, the cockpit's layout and palette in
`internal/ui/widget`. [Architecture](architecture.md#new-code-goes-here) lists
the home for each kind of code. Never copy a helper into a second package;
call it, or move it down to a package both callers can import.

## Tests

- A test's name says what it pins: `TestConnCountExcludesAClientThatSawItsClose`,
  not `TestConnCount2`.
- A subtle change gets a mutation check: break the condition the change turns
  on and confirm a named test fails. A test that passes with and without the
  logic pins nothing.
- A test in `cmd/relevo` never runs a subcommand that starts a harness or
  reaches the network; CI runners have neither. Test the rule as a pure
  function in `internal/relevo` instead.

## What `make check` enforces

| Check | Fails on | Skips |
| --- | --- | --- |
| `golangci-lint` (`.golangci.yml`) | a function over 70 lines (`funlen`), cognitive complexity over 30 (`gocognit`), and the findings of `errcheck`, `errorlint`, `unused`, `govet`, `staticcheck` and `ineffassign` | `funlen` and `gocognit` in `_test.go` files |
| `scripts/check-comments.sh` | a `//` comment containing `#` followed by a digit, or `§` | the files in `scripts/check-comments.allow` |
| `scripts/check-filesize.sh` | a non-test `.go` file over 600 lines | the files in `scripts/check-filesize.allow` |

The allow-lists hold only the scripts' own test fixtures. Nothing is ever
added to them, or to the exclusions in `.golangci.yml`, to get a change green. The comment check is a text match, so it
cannot see "used to" or "round N": those are for the author and the reviewer.

Run the two scripts on their own from the repository root:

```sh
sh scripts/check-comments.sh
sh scripts/check-filesize.sh
```

## Coverage

CI's coverage job fails when a package's statement coverage drops more than
one point below `testdata/coverage-baseline.txt`. On a pull request it holds
only the packages whose own files changed; a push to `main` holds every
package. A change that moves code between packages regenerates the baseline
with `sh scripts/check-coverage.sh --write` and says so. No change lowers a
baseline to get green.

The baseline is tied to the Go minor version and platform in its header, and
only CI's job enforces it (`RELEVO_REQUIRE_COVERAGE=1`); elsewhere `make check`
prints `skipped`, because another build of the same Go minor can measure a
package several points apart. Set `RELEVO_REQUIRE_COVERAGE=1` to compare
locally. A Go upgrade on CI's go-stable leg means regenerating the baseline.
