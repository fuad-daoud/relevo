package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// configWorkflowFlagSet declares the bare `config workflow` dispatcher's flags:
// none today. It exists so the registry's parity test finds exactly one
// installer per verb.
func configWorkflowFlagSet(*flag.FlagSet) {}

// configWorkflow is the workflow half of `relevo config`: the saved workflows a
// chain can be started with.
func configWorkflow(args []string) error {
	const usage = `usage: relevo config workflow add <file> [--replace] [--force]
       relevo config workflow rm <name>
       relevo config workflow show <name> [--json]
       relevo config workflow edit <name>`

	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return exitCodeErr{code: 2}
	}

	switch args[0] {
	case "add":
		return cmdWorkflowAdd(args[1:])
	case "rm":
		return cmdWorkflowRm(args[1:])
	case "show":
		return cmdWorkflowShow(args[1:])
	case "edit":
		return cmdWorkflowEdit(args[1:])
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fail(codeUsage, "relevo config workflow: unknown command %q", args[0])
	}
}

// formatWorkflows renders the `workflows` block of `relevo config`: the shipped
// workflow first, then every saved one, each with the word that says where it
// comes from.
func formatWorkflows(L config.Loaded) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  shipped\n", workflow.Default().Name)
	names := make([]string, 0, len(L.Workflows))
	for name := range L.Workflows {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&b, "%s  saved\n", name)
	}
	return b.String()
}

// workflowAddFlagValues holds the pointers `config workflow add` parses into.
type workflowAddFlagValues struct {
	replace *bool
	force   *bool
}

// workflowAddFlagSet defines those flags on fs and returns what they parse into.
func workflowAddFlagSet(fs *flag.FlagSet) *workflowAddFlagValues {
	v := &workflowAddFlagValues{}
	v.replace = fs.Bool("replace", false, "overwrite a saved workflow of the same name")
	v.force = fs.Bool("force", false, "allow the name to shadow a shipped workflow")
	return v
}

// cmdWorkflowAdd validates and stores a workflow read from a file.
func cmdWorkflowAdd(args []string) error {
	fs := flag.NewFlagSet("relevo config workflow add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := workflowAddFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config workflow add wants <file>")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	return addWorkflow(rt, rest[0], *v.replace, *v.force)
}

// addWorkflow reads path, validates the workflow it holds against the current
// actors, and saves its source beside the definition. file: seeds are embedded
// relative to the file.
func addWorkflow(rt relevo.Runtime, path string, replace, force bool) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fail(codeConfigInvalid, "workflow add: %v", err)
	}
	def, err := workflow.Parse(raw)
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	if err := checkWorkflowName(rt, def.Name, replace, force); err != nil {
		return err
	}
	def, err = relevo.EmbedFileSeeds(def, filepath.Dir(path))
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	if err := validateWorkflow(rt, def); err != nil {
		return err
	}
	if err := saveWorkflow(rt, def.Name, raw, def, "config workflow add "+def.Name); err != nil {
		return err
	}
	fmt.Printf("stored workflow %s\n", def.Name)
	return nil
}

// checkWorkflowName refuses a name that is shipped, or already saved, unless the
// caller asked for the one flag that allows it.
func checkWorkflowName(rt relevo.Runtime, name string, replace, force bool) error {
	if name == workflow.Default().Name && !force {
		return fail(codeConflict, "workflow %q is shipped; pass --force to shadow it", name)
	}
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	if _, ok := L.Workflows[name]; ok && !replace {
		return fail(codeConflict, "workflow %q is already saved; pass --replace to overwrite it", name)
	}
	return nil
}

// validateWorkflow runs the full format and rule checks against the actors this
// machine has. given is nil, so a rule about the chain's inputs is left to chain
// start.
func validateWorkflow(rt relevo.Runtime, def workflow.Definition) error {
	env := workflow.Env{
		Actors: rt.RoleRegistry().WorkflowActors(),
		Given:  nil,
		Seeds:  workflow.ShippedSeeds(),
		Workflow: func(name string) (workflow.Definition, bool) {
			d, _, err := relevo.ResolveWorkflow(rt, name)
			return d, err == nil
		},
	}
	problems := workflow.Validate(def, env)
	if len(problems) == 0 {
		return nil
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		lines = append(lines, p.String())
	}
	return fail(codeConfigInvalid, "%s", strings.Join(lines, "; "))
}

// saveWorkflow writes name's source and definition into the workflows section,
// leaving every other saved workflow in place.
func saveWorkflow(rt relevo.Runtime, name string, source []byte, def workflow.Definition, message string) error {
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	saved := make(map[string]config.StoredWorkflow, len(L.Workflows)+1)
	for key, w := range L.Workflows {
		saved[key] = w
	}
	saved[name] = config.StoredWorkflow{Source: string(source), Definition: def}
	body, err := config.EncodeWorkflows(saved)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if _, err := rt.Config.As("cli", message).Put(config.Workflows, body); err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	return nil
}

// workflowRmFlagSet declares `config workflow rm`'s flags: none today.
func workflowRmFlagSet(*flag.FlagSet) {}

// cmdWorkflowRm removes a saved workflow, leaving the shipped ones alone.
func cmdWorkflowRm(args []string) error {
	fs := flag.NewFlagSet("relevo config workflow rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workflowRmFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config workflow rm wants <name>")
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	if _, ok := L.Workflows[name]; !ok {
		return fail(codeConfigPathNotSet, "workflow %q is not saved", name)
	}
	saved := make(map[string]config.StoredWorkflow, len(L.Workflows))
	for key, w := range L.Workflows {
		if key != name {
			saved[key] = w
		}
	}
	body, err := config.EncodeWorkflows(saved)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if _, err := rt.Config.As("cli", "config workflow rm "+name).Put(config.Workflows, body); err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	fmt.Printf("removed workflow %s\n", name)
	return nil
}

// workflowShowFlagValues holds the pointer `config workflow show` parses into.
type workflowShowFlagValues struct {
	asJSON *bool
}

// workflowShowFlagSet defines that flag on fs and returns what it parses into.
func workflowShowFlagSet(fs *flag.FlagSet) *workflowShowFlagValues {
	v := &workflowShowFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the parsed definition instead of the source")
	return v
}

// cmdWorkflowShow prints a saved workflow's source, comments kept, or its
// stored definition under --json.
func cmdWorkflowShow(args []string) error {
	fs := flag.NewFlagSet("relevo config workflow show", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := workflowShowFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config workflow show wants <name>")
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	w, ok := L.Workflows[name]
	if !ok {
		return fail(codeConfigPathNotSet, "workflow %q is not saved", name)
	}
	if *v.asJSON {
		raw, err := json.Marshal(w.Definition)
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		return printJSON(os.Stdout, raw)
	}
	fmt.Print(w.Source)
	if !strings.HasSuffix(w.Source, "\n") {
		fmt.Println()
	}
	return nil
}

// workflowEditFlagSet declares `config workflow edit`'s flags: none today.
func workflowEditFlagSet(*flag.FlagSet) {}

// cmdWorkflowEdit opens a saved workflow's source in $EDITOR and stores what the
// edit loop accepts.
func cmdWorkflowEdit(args []string) error {
	fs := flag.NewFlagSet("relevo config workflow edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workflowEditFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config workflow edit wants <name>")
	}
	name := rest[0]

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	L, err := rt.Config.Load()
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	saved, ok := L.Workflows[name]
	if !ok {
		return fail(codeConfigPathNotSet, "workflow %q is not saved", name)
	}
	return editWorkflowLoop(rt, name, saved.Source)
}

// editWorkflowLoop is the $EDITOR half of the update loop. It writes the buffer,
// runs the editor, and lets WorkflowEditRound decide: a valid workflow is saved,
// an unchanged or empty buffer is a no-op, and any other result reopens the
// editor with the problems on top.
func editWorkflowLoop(rt relevo.Runtime, name, source string) error {
	actors := rt.RoleRegistry().WorkflowActors()
	prev := []byte(source)

	tmp, err := os.CreateTemp("", "relevo-workflow-*.yaml")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	for {
		if err := os.WriteFile(tmpPath, prev, 0o600); err != nil {
			return err
		}
		code, err := runEditor(tmpPath)
		if err != nil {
			return err
		}
		if code != 0 {
			fmt.Fprintf(os.Stderr, "relevo config workflow edit: editor exited %d; nothing changed\n", code)
			return exitCodeErr{code: 1}
		}
		edited, err := os.ReadFile(tmpPath)
		if err != nil {
			return err
		}

		stored, problems, done := relevo.WorkflowEditRound(prev, edited, actors)
		if !done {
			prev = reopenWith(problems, edited)
			continue
		}
		if stored == nil {
			if strings.TrimSpace(string(edited)) == "" {
				fmt.Println("aborted; nothing changed")
			} else {
				fmt.Println("no changes")
			}
			return nil
		}

		def, perr := workflow.Parse(stored)
		if perr != nil {
			prev = reopenWith([]string{"# " + perr.Error()}, stored)
			continue
		}
		if def.Name != name {
			problem := fmt.Sprintf("# workflow name %q does not match %q", def.Name, name)
			prev = reopenWith([]string{problem}, stored)
			continue
		}
		if err := saveWorkflow(rt, name, stored, def, "config workflow edit "+name); err != nil {
			return err
		}
		version, err := rt.Config.Version()
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		fmt.Printf("saved (config version %d)\n", version)
		return nil
	}
}

// reopenWith puts the problem lines at the top, then a blank line and the text
// the user last wrote, so the next editor pass opens on something fixable.
func reopenWith(problems []string, edited []byte) []byte {
	var b strings.Builder
	b.WriteString(strings.Join(problems, "\n"))
	b.WriteString("\n\n")
	b.Write(edited)
	return []byte(b.String())
}
