package relevo

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/workflow"
)

// DryRunDoc is what a dry run resolved: the workflow it read, the params it
// applied, the step graph from start, the members it would create and every
// validation problem found. Rendering it starts nothing and writes nothing.
type DryRunDoc struct {
	Workflow string
	Origin   string
	Params   []DryRunParam
	Steps    []DryRunStep
	Members  []DryRunMember
	// Cap is the longest chain name the members allow.
	Cap int
	// Problems is every validation failure, one line each, in the order the
	// validator reported them.
	Problems []string
}

// DryRunParam is one effective workflow param: its name and rendered value.
type DryRunParam struct {
	Name  string
	Value string
}

// DryRunStep is one step of the resolved graph: its kind, the actor a run runs
// and where that actor's placement puts it, every reference it carries and
// every edge out of it.
type DryRunStep struct {
	ID        string
	Kind      string
	Actor     string
	Placement string
	Refs      []string
	Edges     []string
}

// DryRunMember is one binding the chain would create: its name, the actor it
// runs and whether that actor writes.
type DryRunMember struct {
	Name   string
	Actor  string
	Writer bool
}

// ChainDryRun resolves a chain's workflow without starting anything: it reads
// the workflow, applies the params, validates it against the actors present
// and the inputs given, and names the members it would create. A workflow it
// cannot resolve or whose params it cannot apply is an error; a workflow that
// does not validate comes back in the document, so a caller prints every
// problem at once rather than only the first.
func ChainDryRun(ctx context.Context, rt Runtime, opts ChainOptions) (DryRunDoc, error) {
	_ = ctx
	def, origin, err := chainDryRunWorkflow(rt, opts.Workflow)
	if err != nil {
		return DryRunDoc{}, err
	}
	values, err := chainParamsFor(def, rt.Policy, opts)
	if err != nil {
		return DryRunDoc{}, err
	}
	applied, err := workflow.WithParams(def, values)
	if err != nil {
		return DryRunDoc{}, refuse("%v", err)
	}

	actors := rt.RoleRegistry().WorkflowActors()
	doc := DryRunDoc{Workflow: applied.Name, Origin: origin, Params: dryRunParams(applied)}
	doc.Steps = dryRunSteps(applied, actors, rt)
	for _, p := range workflow.Validate(applied, dryRunEnv(rt, opts)) {
		doc.Problems = append(doc.Problems, p.String())
	}
	members, err := chainMemberNames(opts.Name, applied, actors)
	if err != nil {
		doc.Problems = append(doc.Problems, err.Error())
		return doc, nil
	}
	doc.Members = make([]DryRunMember, 0, len(members))
	for _, m := range members {
		doc.Members = append(doc.Members, DryRunMember{Name: m.Name, Actor: m.Actor, Writer: m.Writer})
	}
	doc.Cap = chainNameCap(members)
	return doc, nil
}

// chainDryRunWorkflow resolves the workflow a dry run names; no name is the
// shipped default.
func chainDryRunWorkflow(rt Runtime, name string) (workflow.Definition, string, error) {
	if name == "" {
		return workflow.Default(), "shipped", nil
	}
	return ResolveWorkflow(rt, name)
}

// dryRunEnv is the workflow environment a dry run validates against: the actors
// present, the inputs the invocation gives, the shipped seed names and a
// read-only resolver for a fork's children.
func dryRunEnv(rt Runtime, opts ChainOptions) workflow.Env {
	return workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  &workflow.Given{Plans: len(opts.Plans) > 0, Task: opts.Task != ""},
		Seeds:  workflow.ShippedSeeds(),
		Workflow: func(name string) (workflow.Definition, bool) {
			def, _, err := ResolveWorkflow(rt, name)
			return def, err == nil
		},
	}
}

// dryRunParams is the workflow's effective params, sorted by name and rendered
// as the run would read them.
func dryRunParams(def workflow.Definition) []DryRunParam {
	names := make([]string, 0, len(def.Params))
	for name := range def.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DryRunParam, 0, len(names))
	for _, name := range names {
		out = append(out, DryRunParam{Name: name, Value: workflow.RenderParams(def, "{{params."+name+"}}")})
	}
	return out
}

// dryRunSteps lists every step of the resolved graph, sorted by id, with the
// edges, references, actor and placement a reviewer needs to read it.
func dryRunSteps(def workflow.Definition, actors map[string]workflow.ActorInfo, rt Runtime) []DryRunStep {
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
		out = append(out, ds)
	}
	return out
}

// dryRunRefs returns the references one step's templates carry, deduped and
// sorted, in the source form a reviewer recognises.
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

// dryRunEdges returns a step's edges as "match=target", its on entries in
// sorted match order and a budget's then edge last.
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

// dryRunTarget renders a target: the step it names, done, or a halt with its
// reason.
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

// dryRunPlacement names where an actor's placement puts its round: its
// preferred entries joined, or local when the actor names none.
func dryRunPlacement(rt Runtime, actor string) string {
	if r, ok := rt.RoleRegistry().Role(actor); ok && len(r.Placement) > 0 {
		return strings.Join(r.Placement, ",")
	}
	return localPlacement
}

// RenderChainDryRun renders a dry-run document as text: the workflow and its
// origin, the effective params, the step graph, the members and the chain-name
// cap, and every validation problem. The name carries "Chain" because the send
// verb's own RenderDryRun already owns the plain name.
func RenderChainDryRun(doc DryRunDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow: %s (%s)\n", doc.Workflow, doc.Origin)
	if len(doc.Params) > 0 {
		b.WriteString("params:\n")
		for _, p := range doc.Params {
			fmt.Fprintf(&b, "  %s=%s\n", p.Name, p.Value)
		}
	}
	b.WriteString("steps:\n")
	for _, s := range doc.Steps {
		line := "  " + s.ID + " [" + s.Kind + "]"
		if s.Actor != "" {
			line += " actor=" + s.Actor + " placement=" + s.Placement
		}
		if len(s.Refs) > 0 {
			line += " refs=" + strings.Join(s.Refs, " ")
		}
		if len(s.Edges) > 0 {
			line += " edges=" + strings.Join(s.Edges, " ")
		}
		b.WriteString(line + "\n")
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
