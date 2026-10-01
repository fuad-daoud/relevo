Write a plan that fixes the security findings.

Security output: {{.OutputPath}}.
{{if .BranchDiffPath}}Branch diff, the whole branch against the chain's base: {{.BranchDiffPath}}.
{{.Branch}} against {{.Base}}.
{{else if .Base}}No branch diff was captured; diff the branch yourself from {{.Base}}.
{{else}}No branch diff was captured.
{{end}}{{if .PlanPaths}}Plan copies:
{{range .PlanPaths}}Plan copy: {{.}}.
{{end}}{{end}}
Read the findings and the whole diff, then write the plan the builder runs next. It
answers every finding and nothing else. Do not edit the tree.
