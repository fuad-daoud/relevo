package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

// sendFlagValues holds the pointers send's flags parse into. sendFlagSet
// defines them on fs; cmdSend and TestRemovedFlagsAreUnknown read the same
// surface (A4-1a).
type sendFlagValues struct {
	file      *string
	name      *string
	tier      *string
	candidate *string
	allowYolo *bool
	dryRun    *bool
	regate    *int
	verify    *bool
	noVerify  *bool
	force     *bool
}

// sendFlagSet defines send's flags on fs and returns the values they parse
// into, so a test can inspect the flag surface without running a send.
func sendFlagSet(fs *flag.FlagSet) *sendFlagValues {
	v := &sendFlagValues{}
	v.file = fs.String("file", "", "path to the plan file to hand the runner")
	v.name = fs.String("name", "", "binding name (default: the binding for this cwd)")
	v.tier = fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then the actor's tier, then harness)")
	v.candidate = fs.String("candidate", "", "candidate name or harness/provider/model token to run this round and later ones on; refused while a round is open")
	v.allowYolo = fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	v.dryRun = fs.Bool("dry-run", false, "check every precondition and print what send would do, without sending")
	v.regate = fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	v.verify = fs.Bool("verify", false, "run a read-only reviewer in a throwaway worktree when the round closes")
	v.noVerify = fs.Bool("no-verify", false, "do not run a reviewer when the round closes (default: config policy verify.default)")
	v.force = fs.Bool("force", false, "send a planner actor's seed even when it is over the 4 KiB cap")
	return v
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	v := sendFlagSet(fs)
	file, name, tier, candidate := v.file, v.name, v.tier, v.candidate
	allowYolo, dryRun, regate := v.allowYolo, v.dryRun, v.regate
	verify, noVerify := v.verify, v.noVerify
	force := v.force
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Before newRuntime, like add's flag pair: the refusal must not depend on
	// argv order and must touch neither the state directory nor a harness.
	if *verify && *noVerify {
		fmt.Fprintf(os.Stderr, "relevo: relevo send --verify and --no-verify are exclusive\n")
		return fmt.Errorf("relevo send --verify and --no-verify are exclusive: %w", exitCodeErr{code: 2})
	}
	if *file == "" {
		return fmt.Errorf("relevo send requires --file")
	}
	regateOpt, err := regateFlag(fs, regate)
	if err != nil {
		return err
	}

	var verifyOpt *bool
	if *verify || *noVerify {
		v := *verify
		verifyOpt = &v
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	target, err := resolveBinding(rt, *name, fs.Args())
	if err != nil {
		return err
	}

	opts := relevo.SendOptions{
		Tier:      *tier,
		AllowYolo: *allowYolo,
		Builder:   *candidate,
		Regate:    regateOpt,
		Verify:    verifyOpt,
		Force:     *force,
	}

	if *dryRun {
		d, err := relevo.SendDryRun(context.Background(), rt, target, *file, opts)
		if err != nil {
			return err
		}
		fmt.Print(relevo.RenderDryRun(d))
		return nil
	}

	res, err := relevo.Send(context.Background(), rt, target, *file, opts)
	if err != nil {
		return err
	}

	if res.Pick != "" {
		fmt.Println(res.Pick)
	}
	if res.Drift != "" {
		fmt.Println(res.Drift)
	}
	fmt.Printf("sent round %d to %s's runner\n", res.Round, target)
	warnWaitingOnYou(rt, target)
	return nil
}
