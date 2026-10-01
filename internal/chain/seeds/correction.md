Write a correction plan for the builder.

Reviewer's output: {{.OutputPath}}.
Plan {{.Plan}} of {{.Plans}}: {{.PlanPath}}.
{{if .RoundPromptPath}}This round's prompt: {{.RoundPromptPath}}.
{{end}}Builder's report: {{.ReportPath}}.
This round's diff: {{.DiffPath}}.
{{if .PlanDiffPath}}Plan diff, every round of this plan so far: {{.PlanDiffPath}}.
{{else if .DiffFrom}}No cumulative plan diff was captured; diff the plan yourself from {{.DiffFrom}}.
{{else}}No cumulative plan diff was captured for this plan.
{{end}}{{if .BuilderRoundKind}}This closing round is {{.BuilderRoundKind}}, on top of round {{.BuilderRoundOn}}.
Builder rounds of this plan:
{{range .BuilderRounds}}- round {{.Round}} prompt: {{.PromptPath}}
- round {{.Round}} report: {{.ReportPath}}
{{end}}{{end}}{{if .GateLogPath}}Check result: {{.GateResult}}; its output: {{.GateLogPath}}.{{else}}No check ran for this round.{{end}}

This is correction {{.Corrections}} on this plan. Read the reviewer's output,
the round's prompt when it is named and the diffs. You review plan {{.Plan}} of {{.Plans}} as a whole
against the plan text: the plan's cumulative diff is the primary input and this
round's diff is its latest increment. Write a plan that answers every change it
asks for and nothing else. Do not edit the tree.
