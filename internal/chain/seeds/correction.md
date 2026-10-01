Write a correction plan for the builder.

Reviewer's output: {{.OutputPath}}.
Plan: {{.PlanPath}}.
{{if .RoundPromptPath}}This round's prompt: {{.RoundPromptPath}}.
{{end}}Builder's report: {{.ReportPath}}.
This round's diff: {{.DiffPath}}.
{{if .PlanDiffPath}}Plan diff, every round of this plan so far: {{.PlanDiffPath}}.
{{else}}No cumulative plan diff was captured for this plan.
{{end}}{{if .GateLogPath}}Check result: {{.GateResult}}; its output is at {{.GateLogPath}}.{{else}}No check ran for this round.{{end}}

This is correction {{.Corrections}} on this plan. Read the reviewer's output,
the round's prompt when it is named and the diffs, then write a plan that
answers every change it asks for and nothing else. Do not edit the tree.
