package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/fuad-daoud/relevo/internal/relevo"
)

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
	asJSON    *bool
}

// sendFlagSet defines send's flags, so cmdSend and the tests read one flag
// surface.
func sendFlagSet(fs *flag.FlagSet) *sendFlagValues {
	v := &sendFlagValues{}
	v.file = fs.String("file", "", "path to the plan file to hand the runner")
	v.name = fs.String("name", "", "binding name (default: the binding for this cwd)")
	v.tier = fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then the actor's tier, then harness)")
	v.candidate = fs.String("candidate", "", "candidate name or harness/provider/model token to run this round and later ones on; refused while a live round is open (a halted served round may be re-pointed)")
	v.allowYolo = fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	v.dryRun = fs.Bool("dry-run", false, "check every precondition and print what send would do, without sending")
	v.regate = fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	v.verify = fs.Bool("verify", false, "run a read-only reviewer in a throwaway worktree when the round closes")
	v.noVerify = fs.Bool("no-verify", false, "do not run a reviewer when the round closes (default: config policy verify.default)")
	v.force = fs.Bool("force", false, "send a planner actor's seed even when it is over the 4 KiB cap")
	v.asJSON = fs.Bool("json", false, "print the result as a JSON document")
	return v
}

// resolveSendFlags refuses the flag combinations send cannot run, before the
// runtime is built. The refusal touches neither the state directory nor a
// harness, and must not depend on argv order.
func resolveSendFlags(fs *flag.FlagSet, v *sendFlagValues) (*int, *bool, error) {
	if *v.verify && *v.noVerify {
		return nil, nil, fail(codeRefused, "relevo send --verify and --no-verify are exclusive")
	}
	if *v.file == "" {
		return nil, nil, fail(codeUsage, "relevo send requires --file")
	}
	regateOpt, err := regateFlag(fs, v.regate)
	if err != nil {
		return nil, nil, err
	}

	var verifyOpt *bool
	if *v.verify || *v.noVerify {
		b := *v.verify
		verifyOpt = &b
	}
	return regateOpt, verifyOpt, nil
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	v := sendFlagSet(fs)
	file, name, tier, candidate := v.file, v.name, v.tier, v.candidate
	allowYolo, dryRun := v.allowYolo, v.dryRun
	force, asJSON := v.force, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	// Before newRuntime: the refusal must not depend on argv order and touches
	// neither the state directory nor a harness.
	regateOpt, verifyOpt, err := resolveSendFlags(fs, v)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
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
			return writeError(err)
		}
		if *asJSON {
			return printDoc(d)
		}
		fmt.Print(relevo.RenderDryRun(d))
		return nil
	}

	res, err := relevo.Send(context.Background(), rt, target, *file, opts)
	if err != nil {
		return writeError(err)
	}

	// The pick and drift lines are notices, not the result: --json moves them
	// to stderr, where stdout carries the document alone.
	notices := noticeWriter(*asJSON)
	if res.Pick != "" {
		_, _ = fmt.Fprintln(notices, res.Pick)
	}
	if res.Drift != "" {
		_, _ = fmt.Fprintln(notices, res.Drift)
	}
	if *asJSON {
		// The round's candidate and tier: the flags when given, else the
		// binding's own values, which the send just stored.
		cand, roundTier := *candidate, *tier
		if b, lerr := rt.Store.Load(target); lerr == nil {
			cand = b.BuilderCandidate
			if roundTier == "" {
				roundTier = b.Tier
			}
		}
		return printDoc(sendDocOf(target, res.Round, candidateLabel(rt, cand), roundTier, false))
	}
	fmt.Printf("sent round %d to %s's runner\n", res.Round, target)
	warnWaitingOnYou(rt, target)
	return nil
}
