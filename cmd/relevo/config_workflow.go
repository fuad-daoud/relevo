package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
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
	name, err := relevo.WorkflowAdd(rt, rest[0], *v.replace, *v.force)
	if err != nil {
		return workflowError(err)
	}
	fmt.Printf("stored workflow %s\n", name)
	return nil
}

// workflowError maps a workflow operation's class onto the catalog code that
// class earns, keeping the message the operation wrote. The classes are one
// place in internal/relevo so the cockpit can map them to text; this is the CLI's
// side of the same table, and the code is picked from the class rather than
// parsed out of the message.
func workflowError(err error) error {
	code := codeConfigInvalid
	switch {
	case errors.Is(err, relevo.ErrWorkflowShipped), errors.Is(err, relevo.ErrWorkflowSaved):
		code = codeConflict
	case errors.Is(err, relevo.ErrWorkflowNotSaved):
		code = codeConfigPathNotSet
	case errors.Is(err, relevo.ErrWorkflowEncode):
		code = codeInternal
	}
	return failWrap(code, err, "%s", err)
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
	if err := relevo.WorkflowRemove(rt, name); err != nil {
		return workflowError(err)
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
	if *v.asJSON {
		def, err := relevo.WorkflowDefinition(rt, name)
		if err != nil {
			return workflowError(err)
		}
		raw, err := json.Marshal(def)
		if err != nil {
			return fail(codeInternal, "%v", err)
		}
		return printJSON(os.Stdout, raw)
	}
	source, shipped, err := relevo.WorkflowSource(rt, name)
	if err != nil {
		return workflowError(err)
	}
	if shipped {
		return fail(codeConfigPathNotSet, "workflow %q is not saved", name)
	}
	fmt.Print(source)
	if !strings.HasSuffix(source, "\n") {
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
	source, shipped, err := relevo.WorkflowSource(rt, name)
	if err != nil {
		return workflowError(err)
	}
	if shipped {
		return fail(codeConfigPathNotSet, "workflow %q is not saved", name)
	}
	def, err := relevo.WorkflowDefinition(rt, name)
	if err != nil {
		return workflowError(err)
	}
	return editWorkflowLoop(rt, name, config.StoredWorkflow{Source: source, Definition: def})
}

// editWorkflowLoop is the $EDITOR half of the update loop. It writes the buffer,
// runs the editor, and lets WorkflowEditRound decide: a valid workflow is saved,
// an unchanged or empty buffer is a no-op, and any other result reopens the
// editor with the problems on top. The round returns the definition to store,
// so the saved source and the embedded file: seeds stay in step.
func editWorkflowLoop(rt relevo.Runtime, name string, saved config.StoredWorkflow) error {
	actors := rt.RoleRegistry().WorkflowActors()
	prev := []byte(saved.Source)

	root := ""
	if rt.Store != nil {
		root = rt.Store.Root()
	}
	tmp, err := store.CreateTemp(root, "relevo-workflow-*.yaml")
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

		stored, def, problems, done := relevo.WorkflowEditRound(prev, edited, saved, actors)
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

		if def.Name != name {
			problem := relevo.WorkflowNameProblem(def.Name, name)
			prev = reopenWith([]string{problem}, stored)
			continue
		}
		if err := relevo.WorkflowSave(rt, name, stored, def, "config workflow edit "+name); err != nil {
			return workflowError(err)
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
// the user last wrote with any earlier problem block stripped, so the next
// editor pass opens on something fixable and the comments never accumulate.
func reopenWith(problems []string, edited []byte) []byte {
	return relevo.WorkflowEditReopen(problems, edited)
}
