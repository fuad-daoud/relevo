# A2 round 4, part 3: the detail block's path column fits the longest path

**The defect.** `agentFileLine` (internal/ui/view_agents.go:363-372) pads the path to a
fixed 46 cells. A longer path runs straight into the state with no gap. The golden
`agents-custom-132` shows it:

    opencode  ~/.config/opencode/agents/security-reviewer.mdup to date

**The fix.**
- `agentFileLine` takes the path width as a parameter: `pathW int`.
- `agentDetailLines` computes `pathW = max(46, longest tilde path in the block + 2)`
  over the lines it draws, and passes it to every `agentFileLine` call. There is one
  width per block, so the state column stays aligned.
- Change no other caller's output. If another caller exists, pass it 46, which is
  today's width.

**Steps.**
1. Make the edit in internal/ui/view_agents.go, as one edit.
2. Regenerate only `agents-custom-132` (`go test ./internal/ui/ -run 'TestGoldenViews/agents-custom-132' -update`).
   Every other golden must pass unchanged. If one moves, stop and report.
   In the new golden, the opencode line must read
   `...security-reviewer.md  up to date`, with two spaces, and every state in the block
   must start in the same column.
3. Required mutation: pass 46 unconditionally, and confirm `agents-custom-132` fails;
   then restore it.
4. Checks: `gofmt -l $(git ls-files '*.go')` prints nothing;
   `go vet ./internal/ui/`, `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh`
   and `go test -count=1 ./internal/ui/` all pass.
5. Commit as a **new** commit:
   `fix(cockpit): the agent detail's path column fits the longest path`.
   Never amend, and never rebase.

If a step does not match the code, stop and report.

The report covers the new golden's detail block lines, the mutation result and the
check outputs.
