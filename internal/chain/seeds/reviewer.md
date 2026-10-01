Review the round and give a verdict.

Plan {{.Plan}} of {{.Plans}}: {{.PlanPath}}.
{{if .RoundPromptPath}}This round's prompt: {{.RoundPromptPath}}.
{{end}}Builder's report: {{.ReportPath}}.
This round's diff: {{.DiffPath}}.
{{if .PlanDiffPath}}Plan diff, every round of this plan so far: {{.PlanDiffPath}}.
{{else}}No cumulative plan diff was captured for this plan.
{{end}}{{if .GateLogPath}}Check result: {{.GateResult}}; its output: {{.GateLogPath}}.{{else}}No check ran for this round.{{end}}

Read the plan, the round's prompt when it is named, the report and the diffs,
judge the diff against the plan and against the check, and do not edit the tree.
End your output with this block:

```relevo
verdict: pass        # or: changes
```
