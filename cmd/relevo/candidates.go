package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// notePick prints why relevo chose the candidate it spawned. Silent for
// an explicit token (the mastermind already knows) and for adoption
// (nothing was chosen); the gated note, if any, is printed separately.
// A1 §4.4: the line names the candidate by its short name.
func notePick(rt relevo.Runtime, role string, res relevo.Resolution) {
	if res.How == "" || res.How == relevo.HowExplicit {
		return
	}
	fmt.Fprintln(os.Stderr, relevo.PickText(role, res, rt.Candidates))
}

// roleOrBuilder is the role name a flag value means: "builder" for "", else
// the value. The CLI's --actor and the registry both spell the default builder
// as "".
func roleOrBuilder(r string) string {
	if r == "" {
		return "builder"
	}
	return r
}

// builderWhere is how the bound/added lines name the builder's place: a local
// builder is always headless (a process relevo runs itself, #99, #303).
func builderWhere(ep store.Endpoint) string {
	if ep.Headless() {
		return "headless"
	}
	return ep.PaneID
}

func cmdCandidates(args []string) error {
	fs := flag.NewFlagSet("relevo config", flag.ContinueOnError)
	probe := fs.Bool("probe", false, "run each candidate once with a one-line prompt from this machine and record its time to first output")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if !*probe && len(fs.Args()) > 0 {
		return fmt.Errorf("usage: relevo config --probe [token...]")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *probe {
		host, _ := os.Hostname()
		tokens := fs.Args()
		n := len(tokens)
		if n == 0 {
			n = rt.Candidates.Len()
		}
		fmt.Fprintf(os.Stderr, "probing %d candidate(s) from %s, one at a time\n", n, host)

		// The column is sized from the names FormatProbe prints, not from
		// argv: Probe resolves each argument and FormatProbe prints the
		// resolved candidate's short name (A1 §4.4, round 3 F2).
		var names []string
		if len(tokens) > 0 {
			for _, tok := range tokens {
				c, err := rt.Candidates.Resolve(tok)
				if err != nil {
					names = append(names, tok)
					continue
				}
				names = append(names, rt.Candidates.NameOf(c.Ref().String()))
			}
		} else {
			for _, ref := range rt.Candidates.Refs() {
				names = append(names, rt.Candidates.NameOf(ref))
			}
		}
		width := availability.ProbeNameWidth(names)

		_, err := availability.Probe(context.Background(), relevo.AvailabilityDeps(rt), lineExec{}, tokens, host, func(r availability.ProbeResult) {
			fmt.Println(availability.FormatProbe(r, width))
		})
		return err
	}

	fmt.Print(formatCandidates(rt))
	return nil
}

// formatCandidates renders the candidate table cmdCandidates' non-probe form
// prints -- the candidates block `relevo config` shows.
func formatCandidates(rt relevo.Runtime) string {
	h := availability.LatencyHistory{}
	if rt.Latency != nil {
		loaded, err := availability.LoadLatency(rt.Latency)
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo: could not read latency: %v\n", err)
		} else {
			h = loaded
		}
	}
	h = h.Prune(rt.Now())

	lat := make(map[string]availability.Summary)
	for _, ref := range rt.Candidates.Refs() {
		lat[ref] = h.Summary(ref)
	}

	return view.FormatCandidatesLatencyFor(rt.RoleRegistry(), rt.Candidates, availability.Gates(relevo.AvailabilityDeps(rt)), lat)
}

// loadHistory reads the availability history for display, treating an
// unreadable record as empty after one stderr line -- the same rule Gates
// applies to the ledger. The body moved to relevo.LoadHistory (cockpit C2b
// §4.1); this keeps the stderr line for its other callers.
func loadHistory(rt relevo.Runtime) availability.History {
	h, err := availability.LoadHistory(relevo.AvailabilityDeps(rt))
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read history: %v\n", err)
		return availability.History{}
	}
	return h
}

// formatPolicy renders the per-role pick explanation the `pick` block of
// `relevo config` shows.
func formatPolicy(rt relevo.Runtime) string {
	return relevo.FormatPolicyFor(rt.RoleRegistry(), rt.Candidates, rt.Policy, availability.Gates(relevo.AvailabilityDeps(rt)), loadHistory(rt), rt.Now(), time.Local)
}
