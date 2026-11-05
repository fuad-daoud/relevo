package relevo

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// DryRunDoc is what a dry run resolved: workflow, params, step graph,
// members, cleanup and every validation problem. Rendering it starts nothing
// and writes nothing.
type DryRunDoc struct {
	Workflow string
	Origin   string
	Params   []DryRunParam
	Steps    []DryRunStep
	Members  []DryRunMember
	// Cleanup is every cleanup step that runs a command, in step order; empty
	// when the chain runs no cleanup.
	Cleanup []DryRunCleanup
	// Cap is the longest chain name the members allow.
	Cap int
	// Problems is every validation failure, one line each, in validator order.
	Problems []string
}

type DryRunParam struct {
	Name  string
	Value string
	// Source is the tier the gate's value fell back from, so the printed
	// line tells a flag's own command from the repository's or the
	// policy's. Only the gate param carries one, and only when a tier
	// supplied its value: a custom workflow's own default names no tier.
	Source string
}

type DryRunStep struct {
	ID        string
	Kind      string
	Actor     string
	Placement string
	Children  []string
	Refs      []string
	Edges     []string
}

type DryRunMember struct {
	Name   string
	Actor  string
	Writer bool
}

// DryRunCleanup is one cleanup step that runs a command: its resolved
// command, where the command came from, when it runs, and the step it
// precedes.
type DryRunCleanup struct {
	Step    string
	Command string
	Source  string
	When    string
	Before  string
}

// ChainDryRun resolves a chain's workflow without starting anything. A
// workflow that does not validate comes back in the document, so a caller
// prints every problem at once rather than only the first.
func ChainDryRun(ctx context.Context, rt Runtime, opts ChainOptions) (DryRunDoc, error) {
	def, origin, err := chainDryRunWorkflow(rt, opts.Workflow)
	if err != nil {
		return DryRunDoc{}, err
	}
	// The repository is resolved before the params, on a start's terms: a
	// directory git cannot identify reads as "no settings", since a dry run
	// starts nothing and must not fail on it.
	repo, err := os.Getwd()
	if err != nil {
		return DryRunDoc{}, fmt.Errorf("resolve working directory: %w", err)
	}
	repoSet, err := RepoSettingsFor(ctx, rt, repo)
	if err != nil {
		return DryRunDoc{}, err
	}
	values, cleanupRes, err := chainParamsFor(def, rt.Policy, opts, repoSet)
	if err != nil {
		return DryRunDoc{}, err
	}
	applied, err := workflow.WithParams(def, values)
	if err != nil {
		return DryRunDoc{}, refusal.New("%v", err)
	}

	// The graph renders pruned: a chain with no cleanup prints the shape an
	// old binary reads, while a chain with one shows the cleanup steps. The
	// problems still read the unpruned definition, as before.
	pruned := workflow.PruneEmptyCleanup(applied)
	actors := rt.RoleRegistry().WorkflowActors()
	// The gate line carries the tier its value fell back from, on the start
	// line's terms: an explicit --param gate wins and is named as such, and
	// a value no tier supplied -- a custom workflow's own default, or no
	// gate at all -- names none. Resolution itself is untouched.
	checks := resolve.RoleChecks(rt.RoleRegistry(), "builder")
	gateRes := resolveGateFor(opts.Gate, opts.NoGate, rt.Policy, checks, repoSet)
	doc := DryRunDoc{Workflow: pruned.Name, Origin: origin, Params: dryRunParams(pruned, dryRunGateSource(pruned, opts, gateRes))}
	doc.Steps = dryRunSteps(pruned, actors, rt, opts)
	doc.Cleanup = dryRunCleanup(pruned, cleanupRes, opts.Params)
	for _, p := range workflow.Validate(applied, dryRunEnv(rt, opts)) {
		doc.Problems = append(doc.Problems, p.String())
	}
	members, err := chain.MemberNames(opts.Name, applied, actors)
	if err != nil {
		doc.Problems = append(doc.Problems, err.Error())
		return doc, nil
	}
	doc.Members = make([]DryRunMember, 0, len(members))
	for _, m := range members {
		doc.Members = append(doc.Members, DryRunMember{Name: m.Name, Actor: m.Actor, Writer: m.Writer})
	}
	doc.Cap = chain.NameCap(opts.Name, members)
	return doc, nil
}

// chainDryRunWorkflow resolves the workflow a dry run names; no name is the
// shipped default.
func chainDryRunWorkflow(rt Runtime, name string) (workflow.Definition, string, error) {
	if name == "" {
		return workflow.Default(), "shipped", nil
	}
	return resolve.ResolveWorkflow(rt.Config, name)
}

// dryRunEnv is the workflow environment a dry run validates against.
func dryRunEnv(rt Runtime, opts ChainOptions) workflow.Env {
	return workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  &workflow.Given{Plans: len(opts.Plans) > 0, Task: opts.Task != ""},
		Seeds:  workflow.ShippedSeeds(),
		Workflow: func(name string) (workflow.Definition, bool) {
			def, _, err := resolve.ResolveWorkflow(rt.Config, name)
			return def, err == nil
		},
	}
}

// dryRunParams is the workflow's effective params, sorted by name. The gate
// param carries the tier its value fell back from; every other param carries
// none.
func dryRunParams(def workflow.Definition, gateSource string) []DryRunParam {
	names := make([]string, 0, len(def.Params))
	for name := range def.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DryRunParam, 0, len(names))
	for _, name := range names {
		p := DryRunParam{Name: name, Value: workflow.RenderParams(def, "{{params."+name+"}}")}
		if name == "gate" {
			p.Source = gateSource
		}
		out = append(out, p)
	}
	return out
}

// dryRunGateSource names the tier the dry run's effective gate fell back
// from, following the start line's precedence: an explicit --param gate is
// named as such, then the resolved tier when it supplied the value. A gate
// the workflow's own definition supplied, and no gate at all, name no tier.
func dryRunGateSource(def workflow.Definition, opts ChainOptions, res resolve.GateResolution) string {
	if !chain.HasParam(def, "gate") {
		return ""
	}
	gate := workflow.RenderParams(def, "{{params.gate}}")
	if gate == "" {
		return ""
	}
	if param, ok := opts.Params["gate"]; ok && param == gate {
		return "--param gate"
	}
	if gate == res.Command {
		return res.Source
	}
	return ""
}

// dryRunSteps lists every step of the resolved graph, sorted by id.
func dryRunSteps(def workflow.Definition, actors map[string]workflow.ActorInfo, rt Runtime, opts ChainOptions) []DryRunStep {
	ids := make([]string, 0, len(def.Steps))
	for id := range def.Steps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]DryRunStep, 0, len(ids))
	for _, id := range ids {
		step := def.Steps[id]
		ds := DryRunStep{
			ID:    id,
			Kind:  strings.Join(step.Kinds(), "+"),
			Refs:  dryRunRefs(step),
			Edges: dryRunEdges(step),
		}
		if step.Run != "" {
			ds.Actor = workflow.RenderParams(def, step.Run)
			ds.Placement = dryRunPlacement(rt, ds.Actor)
		}
		if step.Fork != nil {
			ds.Children = dryRunForkChildren(opts.Name, step.Fork, opts)
		}
		out = append(out, ds)
	}
	return out
}

func dryRunForkChildren(name string, f *workflow.Fork, opts ChainOptions) []string {
	if name == "" {
		name = "<chain>"
	}
	var out []string
	if len(f.Children) > 0 {
		for i := range f.Children {
			out = append(out, fmt.Sprintf("%s.%d", name, i+1))
		}
	} else if f.Each == "plans" {
		for i := range opts.Plans {
			out = append(out, fmt.Sprintf("%s.%d", name, i+1))
		}
	}
	return out
}

// dryRunRefs returns one step's references, deduped and sorted, in source form.
func dryRunRefs(step workflow.Step) []string {
	seen := map[string]bool{}
	var out []string
	add := func(text string) {
		refs, err := workflow.Refs(text)
		if err != nil {
			return
		}
		for _, ref := range refs {
			text := "{{" + ref.Root + "}}"
			if ref.Attr != "" {
				text = "{{" + ref.Root + "." + ref.Attr + "}}"
			}
			if !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	}
	add(step.Run)
	add(step.Check)
	add(step.When)
	add(step.Seed)
	if step.Budget != nil {
		add(step.Budget.Max.Ref)
	}
	sort.Strings(out)
	return out
}

// dryRunEdges returns a step's edges as "match=target", budget's then last.
func dryRunEdges(step workflow.Step) []string {
	keys := make([]string, 0, len(step.On))
	for key := range step.On {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys)+1)
	for _, key := range keys {
		out = append(out, key+"="+dryRunTarget(step.On[key]))
	}
	if step.Budget != nil {
		out = append(out, "budget="+dryRunTarget(step.Budget.Then))
	}
	return out
}

func dryRunTarget(t workflow.Target) string {
	switch t.Kind {
	case workflow.TargetStep:
		return t.Step
	case workflow.TargetHalt:
		return "halt: " + t.Reason
	default:
		return "done"
	}
}

// dryRunPlacement names where an actor's placement puts its round, or local
// when it names none.
func dryRunPlacement(rt Runtime, actor string) string {
	if r, ok := rt.RoleRegistry().Role(actor); ok && len(r.Placement) > 0 {
		return strings.Join(r.Placement, ",")
	}
	return resolve.LocalPlacement
}

// dryRunCleanup lists every cleanup step that runs a command, in step order:
// its resolved command, the tier that supplied it, when it runs, and the
// step it precedes.
func dryRunCleanup(def workflow.Definition, cleanupRes CleanupResolution, optParams map[string]string) []DryRunCleanup {
	ids := make([]string, 0, len(def.Steps))
	for id, step := range def.Steps {
		if step.Cleanup == "" {
			continue
		}
		if workflow.RenderParams(def, step.Cleanup) == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []DryRunCleanup
	for _, id := range ids {
		step := def.Steps[id]
		cmd := workflow.RenderParams(def, step.Cleanup)
		out = append(out, DryRunCleanup{
			Step:    id,
			Command: cmd,
			Source:  dryRunCleanupSource(step, cmd, cleanupRes, optParams),
			When:    dryRunCleanupWhen(step, cleanupRes.After),
			Before:  dryRunCleanupBefore(step),
		})
	}
	return out
}

// dryRunCleanupSource names the tier that supplied a cleanup step's command:
// a --param override wins, then the resolved cleanup, then the workflow's
// own default or literal. A literal step never takes the resolution's
// source, since it runs whatever it names.
func dryRunCleanupSource(step workflow.Step, cmd string, cleanupRes CleanupResolution, optParams map[string]string) string {
	refs, err := workflow.Refs(step.Cleanup)
	if err != nil {
		return "workflow"
	}
	seen := false
	for _, ref := range refs {
		if ref.Root != "params" || (ref.Attr != "cleanup_round" && ref.Attr != "cleanup_chain") {
			continue
		}
		seen = true
		if v, ok := optParams[ref.Attr]; ok && v == cmd {
			return "--param " + ref.Attr
		}
	}
	if seen && cleanupRes.Command == cmd {
		return cleanupRes.Source
	}
	return "workflow"
}

// dryRunCleanupWhen names when a cleanup step runs: the phase its cleanup
// param fills, or the resolution's own after word for a literal step.
func dryRunCleanupWhen(step workflow.Step, after string) string {
	refs, err := workflow.Refs(step.Cleanup)
	if err != nil {
		return after
	}
	round, chain := false, false
	for _, ref := range refs {
		if ref.Root != "params" {
			continue
		}
		switch ref.Attr {
		case "cleanup_round":
			round = true
		case "cleanup_chain":
			chain = true
		}
	}
	switch {
	case round && chain:
		return "both"
	case round:
		return "round"
	case chain:
		return "chain"
	}
	return after
}

// dryRunCleanupBefore names the step a cleanup precedes: its done edge, or
// its else when done is absent.
func dryRunCleanupBefore(step workflow.Step) string {
	if t, ok := step.On["done"]; ok {
		return dryRunTarget(t)
	}
	if t, ok := step.On["else"]; ok {
		return dryRunTarget(t)
	}
	return ""
}

// RenderChainDryRun renders a dry-run document as text. The name carries
// "Chain" because the send verb's RenderDryRun owns the plain name.
func RenderChainDryRun(doc DryRunDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow: %s (%s)\n", doc.Workflow, doc.Origin)
	if len(doc.Params) > 0 {
		b.WriteString("params:\n")
		for _, p := range doc.Params {
			if p.Source != "" {
				fmt.Fprintf(&b, "  %s=%s (%s)\n", p.Name, p.Value, p.Source)
				continue
			}
			fmt.Fprintf(&b, "  %s=%s\n", p.Name, p.Value)
		}
	}
	b.WriteString("steps:\n")
	for _, s := range doc.Steps {
		line := "  " + s.ID + " [" + s.Kind + "]"
		if s.Actor != "" {
			line += " actor=" + s.Actor + " placement=" + s.Placement
		}
		if len(s.Children) > 0 {
			line += " children=" + strings.Join(s.Children, ",")
		}
		if len(s.Refs) > 0 {
			line += " refs=" + strings.Join(s.Refs, " ")
		}
		if len(s.Edges) > 0 {
			line += " edges=" + strings.Join(s.Edges, " ")
		}
		b.WriteString(line + "\n")
	}
	if len(doc.Cleanup) > 0 {
		b.WriteString("cleanup:\n")
		for _, c := range doc.Cleanup {
			line := "  " + c.Step + ": " + c.Command + " (" + c.Source + ") [" + c.When
			if c.Before != "" {
				line += ", before " + c.Before
			}
			b.WriteString(line + "]\n")
		}
	}
	if len(doc.Members) > 0 {
		fmt.Fprintf(&b, "members (chain name cap %d):\n", doc.Cap)
		for _, m := range doc.Members {
			role := "reader"
			if m.Writer {
				role = "writer"
			}
			fmt.Fprintf(&b, "  %-24s %s (%s)\n", m.Name, m.Actor, role)
		}
	}
	if len(doc.Problems) > 0 {
		b.WriteString("problems:\n")
		for _, p := range doc.Problems {
			fmt.Fprintf(&b, "  %s\n", p)
		}
	}
	return b.String()
}
