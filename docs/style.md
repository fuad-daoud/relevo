# Code style

This guide states the comment, size, helper, test and lint rules each change follows. Every rule names the file that states or enforces it.

## Comments

A package comment is 1-3 lines saying what the package owns (CLAUDE.md "Code style").

Good, `internal/text/text.go:1-3`:

```
// Package text holds the display-text helpers the CLI, the cockpit views,
// the daemon and the listings share: first-line cuts, small plural and
// fallback sentences, and millisecond counts.
```

Good, with an ownership boundary, `internal/view/status.go:1-3`:

```
// Package view renders a binding's state for a human and holds the plain
// types that carry it. The code that computes that state -- probes, live
// usage, git numstat, mastermind routing -- stays in relevo and fills them.
```

A comment says why, and only where the code cannot: a non-obvious constraint, where a number comes from, an ordering that matters, a hazard (CLAUDE.md "Code style"). It never restates the code. A doc comment on an exported name appears only when it adds something the name and signature lack. The worked why-comment example sits under "No history": the fine present-state quote (`internal/availability/gatedoc.go:71-73`) names the constraint the exporter obeys.

## No history

No comment carries history: no issue or pull-request numbers, no spec sections, no round markers, no stories about what the code did at an earlier point (CLAUDE.md "Code style"). Git and the issues hold history. Tests follow the same rules.

Bad (counterexample — this wording fails the rule), from `internal/availability/helpers_test.go` before the cleanup:

```
// failure a hand-edited file used to cause.
```

Fine (present state — this wording passes the rule), `internal/availability/gatedoc.go:71-73`:

```
// Items returns the document as one value per subject this machine holds a live
// rate limit on. A subject with no live rate-limit entry is absent, which the
// exporter reads as a key the document no longer holds and sends as a delete.
```

The first describes what the code did at an earlier point; the second describes what the document holds at read time.

## Size

A function holds at most 70 lines; the funlen gate counts lines rather than statements and ignores comments (`.golangci.yml:15-18`). A non-test `.go` file holds at most 600 lines against a working target near 500; test files carry no limit (`scripts/check-filesize.sh:4-6,27,39`). One package owns one concept (CLAUDE.md "Code style").

## Shared helpers

Shared text and rendering helpers live in few homes; new code cites them instead of duplicating them. `internal/text` holds the display-text helpers the CLI, the cockpit views, the daemon and the listings share (`internal/text/text.go:1-3`). `internal/view` renders a binding's state for a human while the computation stays in `relevo` (`internal/view/status.go:1-3`). The remaining shared homes sit under the layer map in `docs/architecture.md`; this guide does not restate that list.

## Tests

A test's name says what it pins (CLAUDE.md "Code style"). A change earns its test by mutation: break the specific condition the change turns on and confirm a named test fails. A test that passes with and without the logic pins nothing (CLAUDE.md "Verifying a builder's work").

## Dexpace base

The dexpace Go styleguide applies, with two exceptions: no "two assertions per function" rule, and no mandatory doc comment on an exported name (CLAUDE.md "Code style").

## Enforcement

`make check` enforces this guide with three static checks (CLAUDE.md "Code style"):

- golangci-lint (`.golangci.yml:5-20`) runs funlen, gocognit with a minimum complexity of 30, errcheck, errorlint, unused, govet, staticcheck and ineffassign, with one exclusion rule per not-yet-clean package directory (`.golangci.yml:21-46`).
- `scripts/check-comments.sh` fails on a history-citing comment — an issue or pull-request number or a section mark after `//` — in each tracked `.go` file and each untracked, non-ignored one, minus `scripts/check-comments.allow` (`scripts/check-comments.sh:40-55`).
- `scripts/check-filesize.sh` fails on a non-test `.go` file over 600 lines from the same file set, minus `scripts/check-filesize.allow` (`scripts/check-filesize.sh:39-54`).

Each check runs as written from the repository root:

```
sh scripts/check-comments.sh
sh scripts/check-filesize.sh
```

A round that finishes a package removes its entries from the exclusion lists; no round adds an entry to get green (CLAUDE.md "Code style").
