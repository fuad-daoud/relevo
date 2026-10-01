Scan the branch for security problems.

{{if .BranchDiffPath}}Branch diff, the whole branch against the chain's base: {{.BranchDiffPath}}.
{{.Branch}} against {{.Base}}.
{{else if .Base}}No branch diff was captured; diff the branch yourself from {{.Base}}.
{{else}}No branch diff was captured.
{{end}}{{if .PlanPaths}}Plan copies:
{{range .PlanPaths}}Plan copy: {{.}}.
{{end}}{{end}}Read the whole diff and report what you find. Do not edit the tree. End your
output with this block:

```relevo
findings: 0
```
