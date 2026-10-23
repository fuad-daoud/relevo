# Refuse a claude builder at tier edit

Owner decision (2026-10-23): refuse `edit` for claude builders, the way agy
and opencode already refuse it, and keep `edit` for readers.

Live check (env sandbox, the owner's real settings without
`additionalDirectories`): at `--permission-mode acceptEdits`, headless claude
allowed `git status`, `git log`, `git diff`, `ls`, Read and Write, and denied
`go version`, `make --version`, `go vet` and `git add && git commit`.

1. `internal/harness/tier.go`: `claudeBuilderEdit(kind, role, tier)` returns
   `ErrTierUnsupported` for claude + `ShapeBuilder` + `TierEdit`, naming
   `--tier yolo` and `--tier harness`. `Launch` calls it after
   `PermissionArgs`, so bind, send and switch all refuse through the existing
   `ErrTierUnsupported` paths. A consult-shaped reader is unaffected.
2. Tests: `TestLaunchRefusesClaudeBuilderAtEdit`,
   `TestLaunchKeepsClaudeReaderAtEdit`, `TestBindClaudeBuilderTierEditRefused`.
   Tests that exercised the claude edit cell through the builder role move to
   the reviewer role; tests that bound a claude builder at edit only for its
   prompt and chain read grants bind at read, which carries the same grants.
3. Wording: the README tier table and a paragraph beside agy's; the setup
   wizard's tier prompt and its edit choice, and `config init`'s tier line, say
   only codex builders bind at edit.
