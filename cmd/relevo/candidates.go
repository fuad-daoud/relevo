package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// notePick prints why relevo chose the candidate it spawned. Silent for an
// explicit token and for adoption; the gated note, if any, is printed
// separately.
func notePick(rt relevo.Runtime, role string, res resolve.Resolution) {
	if res.How == "" || res.How == resolve.HowExplicit {
		return
	}
	fmt.Fprintln(os.Stderr, resolve.PickText(role, res, rt.Candidates))
}

// roleOrBuilder is the role name a flag value means: "builder" for "", else the
// value.
func roleOrBuilder(r string) string {
	if r == "" {
		return "builder"
	}
	return r
}

// builderWhere is how the bound/added lines name the builder's place.
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

		// The column is sized from the names FormatProbe prints, not from argv.
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

func formatCandidates(rt relevo.Runtime) string {
	deps := relevo.AvailabilityDeps(rt)
	h := availability.LatencyHistory{}
	if loaded, err := availability.LoadLatency(deps); err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read latency: %v\n", err)
	} else {
		h = loaded
	}
	h = h.Prune(rt.Now())

	lat := make(map[string]availability.Summary)
	for _, ref := range rt.Candidates.Refs() {
		lat[ref] = h.Summary(ref)
	}

	return view.FormatCandidatesLatencyFor(rt.RoleRegistry(), rt.Candidates, availability.Gates(deps), lat)
}

// loadHistory reads the availability history for display, treating an unreadable
// record as empty after one stderr line.
func loadHistory(rt relevo.Runtime) availability.History {
	h, err := availability.LoadHistory(relevo.AvailabilityDeps(rt))
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not read history: %v\n", err)
		return availability.History{}
	}
	return h
}

func formatPolicy(rt relevo.Runtime) string {
	return resolve.FormatPolicyFor(rt.RoleRegistry(), rt.Accounts, rt.Candidates, rt.Policy,
		pickGates(rt), liveLedgerEntries(rt), loadHistory(rt), rt.Now(), time.Local)
}

// pickGates includes the skill gates a bind from the working directory would
// apply.
func pickGates(rt relevo.Runtime) []availability.Gate {
	wd, _ := os.Getwd()
	return resolve.PickGates(relevo.ResolveDeps(rt), wd)
}

// liveLedgerEntries is the live availability ledger. A read failure reads as
// none.
func liveLedgerEntries(rt relevo.Runtime) []availability.Entry {
	if rt.Gates == nil {
		return nil
	}
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		return nil
	}
	return l.Prune(rt.Now()).Entries
}
