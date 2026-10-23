HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 2 — refuse an unknown candidate key where it is typed

Deliverable: `config set` refuses a candidate key the shape does not declare,
and every reader warns about one it finds.

1. Add `internal/candidate/unknown_keys.go`: `KnownFields() []string` and
   `UnknownKeys(body []byte) []string` (paths `<name>.<key>`, nested keys
   included), built on `jsonshape.Keys(reflect.TypeOf(Candidate{}))` — the
   pattern `internal/policy/validate.go:122-176` and
   `internal/roles/unknown_keys.go` already use.
2. Have `candidate.Parse` append one warning per unknown key, in the wording
   `policyUnknownKeyWarnings` uses, and add the tests
   `TestParseWarnsOnUnknownCandidateKey` and `TestUnknownKeysNamesThePath`.
3. Add the `config.Candidates` case to `guardUnknownFields`
   (`cmd/relevo/config_unknown.go:13-52`): compare the updated body's unknown
   keys against the stored body's and refuse only the added ones, with
   `codeConfigInvalid`, the full path and the known keys.
3b. Add the `config.Policy` case the same way: `relevo config set policy.round_cap 40`
   (issue #1318's repro) must be refused with `codeConfigInvalid`, naming the
   path and the known keys. Reuse the known-key set behind
   `policyUnknownKeyWarnings` (`internal/policy/validate.go:122-176`); do not
   write a second list. Add `TestConfigSetRefusesUnknownPolicyKey`.
4. Extend `cmd/relevo/config_unknown_test.go` with
   `TestConfigSetRefusesUnknownCandidateKey` for `candidates.<n>.maxx` and
   `candidates.<n>.windows.onlyy`, asserting the stored body is unchanged.
5. Run `go test ./internal/candidate/... ./cmd/relevo/ -run
   'UnknownKey|ConfigSet|Parse'` and then `go test ./internal/config/...` to
   prove the tolerance test `TestEnsureCandidateNamesWritesOnce` still passes.

Commit: `config: refuse an unknown candidates or policy key where it is typed`.
