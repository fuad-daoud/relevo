# Repair round {{.RepairRound}} for {{.Name}}: round {{.FailedRound}}'s gate failed
Round {{.FailedRound}}'s acceptance check (`{{.Gate}}`) did NOT pass. Fix ONLY what the check reports; do not
restyle or refactor unrelated code. If the failure is not something a code change can fix, halt and report.
Original plan: {{.PlanPath}}   (read it first; the same rules apply)
Acceptance check output: {{.GateLogPath}} -- last {{len .Tail}} lines:
```
{{range .Tail}}{{.}}
{{end}}```
When done: run the same check yourself in the foreground, then write your report and create the done marker as before.
