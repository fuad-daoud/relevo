package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// HandoverDoc is `relevo handover --json`.
type HandoverDoc struct {
	Name            string `json:"name"`
	To              string `json:"to"`
	Branch          string `json:"branch,omitempty"`
	Already         bool   `json:"already,omitempty"`
	WorktreeRemoved string `json:"worktree_removed,omitempty"`
	WorktreeKept    string `json:"worktree_kept,omitempty"`
	KeptReason      string `json:"kept_reason,omitempty"`
}

type handoverFlagValues struct {
	name   *string
	to     *string
	remote *string
	asJSON *bool
}

func handoverFlagSet(fs *flag.FlagSet) *handoverFlagValues {
	v := &handoverFlagValues{}
	v.name = fs.String("name", "", "binding to hand over")
	v.to = fs.String("to", "", "installation id or label of the machine that takes the binding")
	v.remote = fs.String("remote", "origin", "git remote the branch is pushed to")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

func cmdHandover(args []string) error {
	fs := flag.NewFlagSet("handover", flag.ContinueOnError)
	v := handoverFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	target, ok := explicitBinding(*v.name, fs.Args())
	if !ok {
		return fail(codeUsage, "usage: relevo handover <name> --to <installation id or label>%s\n"+
			"handover gives a binding to another machine; it will not guess which one you meant", bindingHint("handover"))
	}
	if *v.to == "" {
		return fail(codeUsage, "usage: relevo handover <name> --to <installation id or label>")
	}
	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}
	res, err := relevo.Handover(context.Background(), rt, relevo.HandoverOptions{Name: target, To: *v.to, Remote: *v.remote})
	if err != nil {
		return writeError(err)
	}
	if *v.asJSON {
		return printDoc(HandoverDoc{
			Name: target, To: res.Target, Branch: res.Branch, Already: res.Already,
			WorktreeRemoved: res.WorktreeRemoved, WorktreeKept: res.WorktreeKept, KeptReason: res.KeptReason,
		})
	}
	fmt.Print(relevo.HandoverText(target, res))
	return nil
}
