Review the round and give a verdict.

Plan: {{.PlanPath}} (plan {{.Plan}} of {{.Plans}}).
Builder's report: {{.ReportPath}}.
Round diff: {{.DiffPath}}.
Check result: {{.GateResult}}; its output is at {{.GateLogPath}}.

Read all four, judge the diff against the plan and against the check, and do
not edit the tree. End your output with this block:

```relevo
verdict: pass        # or: changes
```
