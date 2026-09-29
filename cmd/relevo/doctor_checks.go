package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// databaseCheck is doctor's `database` row (P3d §4.7): the path, the file
// size, the schema version and the binding_record live/archived counts. It is
// OK unless the open fails; there is no migrate row, because every open
// migrates.
func databaseCheck(st *store.Store) doctor.Check {
	path := st.DBPath()
	c := doctor.Check{Name: "database", Severity: doctor.SevOK}

	d, err := st.DB()
	if err != nil {
		c.Severity = doctor.SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		c.Fix = "fix the state root, or move the database aside"
		return c
	}
	version, err := d.Version()
	if err != nil {
		c.Severity = doctor.SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		return c
	}
	live, archived, err := d.RecordCounts()
	if err != nil {
		c.Severity = doctor.SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		return c
	}

	size := int64(0)
	if info, serr := os.Stat(path); serr == nil {
		size = info.Size()
	}
	c.Detail = fmt.Sprintf("%s · %s · schema v%d · %d live, %d archived",
		path, view.HumanBytes(size), version, live, archived)
	return c
}

// ledgerChecks turns live gates into doctor rows under the candidate's
// harness. They warn, never fail: a gated provider is a fact about right
// now, not a broken install, and must not change doctor's exit code.
func ledgerChecks(gates []availability.Gate) []doctor.Check {
	checks := make([]doctor.Check, 0, len(gates))
	for _, g := range gates {
		ref, err := candidate.ParseRef(g.Token)
		group := ""
		provider := ""
		if err == nil {
			group = ref.Harness
			provider = ref.Provider
		}

		fix := "wait until " + availability.GateTimeText(g.Until)
		if g.Kind == availability.RateLimited {
			fix = "relevo gate --clear " + provider
		}

		checks = append(checks, doctor.Check{
			Group:    group,
			Name:     "ledger",
			Severity: doctor.SevWarn,
			Detail: fmt.Sprintf("%s: %s since %s (%s)",
				g.Token, availability.GateKindText(g.Kind), availability.GateTimeText(g.Since), availability.GateUntilText(g.Until)),
			Fix: fix,
		})
	}
	return checks
}

// serverChecks turns each configured server's probe (relevo.ProbeServers)
// into a doctor row (remote-builders spec §5.5): reachable and enrolled is
// ok; not enrolled and unreachable warn (an unreachable probe is
// ProbeFailed -- relevo could not establish the fact, not that anything is
// wrong); a changed certificate fails, since the client hard-refuses it.
func serverChecks(probes []relevo.ServerProbe) []doctor.Check {
	checks := make([]doctor.Check, 0, len(probes))
	for _, p := range probes {
		c := doctor.Check{Group: "", Name: "servers"}
		switch p.State {
		case "enrolled":
			c.Severity = doctor.SevOK
			c.Detail = fmt.Sprintf("%s: enrolled as %s", p.Name, p.Label)
			if p.QueueAware && p.Builders != nil {
				c.Detail += fmt.Sprintf(", %s", buildersText(p))
			}
			checks = append(checks, c)
			if warning := relevo.ServerTierWarning(p); warning != "" {
				checks = append(checks, doctor.Check{
					Group:    "",
					Name:     "servers",
					Severity: doctor.SevWarn,
					Detail:   fmt.Sprintf("%s: %s", p.Name, warning),
					Fix:      "set tier.builder in the server's config policy",
				})
			} else if !p.TierAware {
				checks = append(checks, doctor.Check{
					Group:       "",
					Name:        "servers",
					Severity:    doctor.SevWarn,
					Detail:      fmt.Sprintf("%s: builder tier unknown (pre-tier server)", p.Name),
					ProbeFailed: true,
				})
			}
			if p.QueueAware && p.Builders != nil && !p.Builders.Scopes {
				checks = append(checks, doctor.Check{
					Group:    "",
					Name:     "servers",
					Severity: doctor.SevWarn,
					Detail:   fmt.Sprintf("scopes unavailable on %s: a daemon restart kills its builders", p.Name),
				})
			}
			continue
		case "not enrolled":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: not enrolled", p.Name)
			c.Fix = "give the admin: " + p.Detail
		case "unreachable":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: unreachable: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		case "cert changed":
			c.Severity = doctor.SevFail
			c.Detail = fmt.Sprintf("%s: certificate changed", p.Name)
			c.Fix = fmt.Sprintf("relevo config server add %s <url> --fingerprint <new>", p.Name)
		case "no key":
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		default:
			c.Severity = doctor.SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		}
		checks = append(checks, c)
	}
	return checks
}

// buildersText is a queue-aware, enrolled probe's runner census, the same
// words RenderServers appends to its row (#285): "runners %d/%d, %d
// queued, scopes %s".
func buildersText(p relevo.ServerProbe) string {
	scopes := "off"
	switch {
	case p.Builders.Scopes && p.Builders.Slice != "" && p.Builders.Quota != "":
		scopes = fmt.Sprintf("on (%s, %s)", p.Builders.Slice, p.Builders.Quota)
	case p.Builders.Scopes && p.Builders.Quota != "":
		scopes = fmt.Sprintf("on (%s)", p.Builders.Quota)
	case p.Builders.Scopes && p.Builders.Slice != "":
		scopes = fmt.Sprintf("on (%s)", p.Builders.Slice)
	case p.Builders.Scopes:
		scopes = "on"
	}
	return fmt.Sprintf("runners %d/%d, %d queued, scopes %s",
		p.Builders.Running, p.Builders.Cap, p.Builders.Queued, scopes)
}

// policyChecks turns policy/candidates inconsistencies into doctor rows.
// Warnings, not failures: an unlisted candidate is a degraded order, not
// a broken machine (spec §4.8).
func policyChecks(warnings []relevo.PolicyWarning) []doctor.Check {
	checks := make([]doctor.Check, 0, len(warnings))
	for _, w := range warnings {
		checks = append(checks, doctor.Check{
			Group:    "",
			Name:     "policy",
			Severity: doctor.SevWarn,
			Detail:   w.Text,
			Fix:      "run relevo config edit",
		})
	}
	return checks
}

// refusalChecks turns the roles an omitted candidate would be refused for
// into doctor rows. Warnings, not failures: the machine is fine, the
// configuration is not (#165). The fix is a literal policy.json built
// from the tokens that serve the role, so it can be pasted as is.
func refusalChecks(refusals []relevo.RoleRefusal) []doctor.Check {
	checks := make([]doctor.Check, 0, len(refusals))
	for _, r := range refusals {
		detail := r.Text + " -- bind --actor " + r.Role + " without --candidate would refuse"
		if r.Role == "builder" {
			detail = r.Text + " -- add/bind without --candidate would refuse"
		}
		fix := "relevo config set policy '" + policyExample(r.Role, r.Serving) + "'"
		if !r.NoOrder {
			provider := "<provider>"
			if len(r.Gated) > 0 {
				provider = r.Gated[0]
			}
			fix = "relevo gate --clear " + provider
		}
		checks = append(checks, doctor.Check{
			Group:    "",
			Name:     "policy",
			Severity: doctor.SevWarn,
			Detail:   detail,
			Fix:      fix,
		})
	}
	return checks
}

func policyExample(role string, serving []string) string {
	// json.Marshal cannot fail on a map of string slices.
	b, _ := json.Marshal(map[string]map[string][]string{
		"order": {role: serving},
	})
	return string(b)
}

// staleMasterMindAge is how old a mastermind record's seen_at must be before the
// stale-record note names it (§4.8, row 5).
const staleMasterMindAge = 7 * 24 * time.Hour

// mastermindCheckInput gathers §4.8's mastermind-row facts: which masterminds exist,
// whether this process runs inside Claude Code, and whether the resolved
// mastermind has a live channel claim. Every read is best-effort -- a fact relevo
// cannot establish reads as absent, and the checks say "not checked" rather
// than guessing. Home comes from $HOME so the row reads the same directory
// cmd/relevo's TestMain isolated.
func mastermindCheckInput(rt relevo.Runtime, kinds []string) doctor.MasterMindCheckInput {
	in := doctor.MasterMindCheckInput{Home: os.Getenv("HOME"), Running: buildVersion()}
	if wd, err := os.Getwd(); err == nil {
		in.Repo = wd
	}
	// The repository's consent answer (#632). Each read is best-effort like the
	// rest of the row: a store or git read that fails leaves the row off.
	if rt.Git != nil && in.Repo != "" {
		ref := mastermindRepoOf(context.Background(), rt, in.Repo)
		in.RepoKnown = mastermindRepoKnown(ref)
		if rt.Store != nil {
			if d, err := rt.Store.DB(); err == nil {
				in.ConsentKnown = true
				if in.RepoKnown {
					if c, err := d.RepoConsent(ref); err == nil {
						in.Consent = c
					}
				}
			}
		}
	}
	for _, k := range kinds {
		if k == "claude" {
			in.Claude = true
		}
	}

	records := []mastermind.Record{}
	if rt.MasterMinds != nil {
		if recs, err := rt.MasterMinds.List(); err == nil {
			records = recs
		}
	}

	live := map[string]bool{}
	if rt.Store != nil {
		if bindings, err := rt.Store.List(); err == nil {
			for _, b := range bindings {
				if b.State != store.StateDone && b.MasterMindID != "" {
					live[b.MasterMindID] = true
				}
			}
		}
	}

	cutoff := rt.Now().Add(-staleMasterMindAge)
	for _, rec := range records {
		if rec.HarnessKind == "claude" {
			in.Claude = true
		}
		if rec.SeenAt.Before(cutoff) && !live[rec.ID] {
			in.Stale = append(in.Stale, rec.Name)
		}
	}

	if ident, ok := mastermind.Detect(os.Getenv, os.Getppid()); ok && ident.Kind == "claude" {
		in.Detected = true
		in.MCPChild = HasMCPChild(hostChildProcesses(ident.HostPID))
		if rt.MasterMinds != nil {
			rec, _, err := mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
				Env:       os.Getenv,
				PPID:      os.Getppid(),
				ProcStart: rt.ProcStart,
				Now:       rt.Now(),
			})
			if err == nil {
				in.Resolved = &rec
				// #386: the mastermind's chat label, read here so the row can
				// name the mastermind as the harness does. An empty label
				// leaves the detail byte-identical.
				if lbl := chatResolver().Resolve(context.Background(), rec.HarnessKind, rec.SessionID, rec.TranscriptLocator); lbl != (chatlabel.Label{}) {
					in.Chat = lbl.String()
				}
				if rt.Channels != nil {
					if c, cerr := rt.Channels.Live(rec.ID, rt.Now()); cerr == nil && c != nil {
						in.ClaimLive = true
					}
				}
			}
		}
	}

	return in
}

// HasMCPChild is doctor.HasMCPChild, re-exported so the pure rule is visible
// at this call site without importing internal/doctor into a test's mind.
func HasMCPChild(children []doctor.ChildProcess) bool { return doctor.HasMCPChild(children) }

// hostChildProcesses reads the child processes of pid: /proc/<pid>/task/*/children
// names them, and each child's /proc/<pid>/cmdline its argv. On a host with no
// /proc (macOS) it falls back to `ps -o pid=,args= --ppid`. A pid that has gone
// or a read that fails yields nil, which reads as "no relevo mcp child" -- the
// FAIL #4.8 asks for when a Claude session has no push route.
func hostChildProcesses(pid int) []doctor.ChildProcess {
	if pid <= 0 {
		return nil
	}
	paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	if err != nil || len(paths) == 0 {
		return psChildren(pid)
	}
	seen := make(map[int]bool)
	var out []doctor.ChildProcess
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, f := range strings.Fields(string(raw)) {
			cpid, err := strconv.Atoi(f)
			if err != nil || seen[cpid] {
				continue
			}
			seen[cpid] = true
			if args := processArgs(cpid); len(args) > 0 {
				out = append(out, doctor.ChildProcess{PID: cpid, Args: args})
			}
		}
	}
	if len(out) == 0 {
		return psChildren(pid)
	}
	return out
}

// processArgs reads one process's argv from /proc/<pid>/cmdline, which is
// NUL-separated. A process that has gone yields nil.
func processArgs(pid int) []string {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(raw) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return parts
}

// psChildren is hostChildProcesses' portable fallback: `ps -o pid=,args=
// --ppid <pid>`, which the tree's other process probes already use. An
// unsupported flag or a missing ps reads as no children.
func psChildren(pid int) []doctor.ChildProcess {
	out, err := exec.Command("ps", "-o", "pid=,args=", "--ppid", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return ParsePSChildren(out)
}

// ParsePSChildren parses `ps -o pid=,args=` output: one line per process, the
// pid first, the argv as the rest of the line. Pure, so the parsing is
// testable without a process tree.
func ParsePSChildren(out []byte) []doctor.ChildProcess {
	var procs []doctor.ChildProcess
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		var args []string
		if len(fields) > 1 {
			args = strings.Fields(fields[1])
		}
		procs = append(procs, doctor.ChildProcess{PID: pid, Args: args})
	}
	return procs
}

// insertGlobalCheck puts c after the last global row, so render order stays
// "globals first, then one block per kind".
func insertGlobalCheck(checks []doctor.Check, c doctor.Check) []doctor.Check {
	last := 0
	for i, existing := range checks {
		if existing.Group == "" {
			last = i + 1
		}
	}
	out := make([]doctor.Check, 0, len(checks)+1)
	out = append(out, checks[:last]...)
	out = append(out, c)
	return append(out, checks[last:]...)
}

// bindPreflightDefs runs the bind-time preflight for one kind and renders its
// warning lines, checking the definitions the caller resolved for the kind --
// the registry's builder definitions when roles.json is loaded, the shipped
// ones otherwise (#374 §3.5). The timeout lives here, not at the call site, so
// it cannot be dropped by accident; adopted is passed through to doctor.Run,
// which owns what an adopted pane is and is not checked for.
func bindPreflightDefs(ctx context.Context, env doctor.Env, kind string, adopted bool, defs []string) []string {
	if defs == nil {
		defs = builderDefinitions()
	}
	ctx, cancel := context.WithTimeout(ctx, bindPreflightTimeout)
	defer cancel()
	return bindWarningLines(doctor.Run(ctx, env, []string{kind}, doctor.WithAdopted(adopted), doctor.WithDefinitions(map[string][]string{kind: defs})))
}

// bindPreflight is bindPreflightDefs with the builder's shipped definitions:
// the behaviour every pre-roles.json caller had.
func bindPreflight(ctx context.Context, env doctor.Env, kind string, adopted bool) []string {
	return bindPreflightDefs(ctx, env, kind, adopted, builderDefinitions())
}

func bindWarningLines(rep doctor.Report) []string {
	var warnings []string
	for _, c := range rep.Checks {
		if c.Severity == doctor.SevOK {
			continue
		}
		// A row relevo could not establish is not actionable, so it stays off the
		// hot path -- unless it is a SevFail, which means relevo cannot run at
		// all and the user needs to hear it even when the cause was a bad probe.
		if c.ProbeFailed && c.Severity != doctor.SevFail {
			continue
		}

		var msg string
		if c.Group == "" {
			msg = fmt.Sprintf("%s %s", c.Name, c.Detail)
		} else {
			msg = fmt.Sprintf("%s %s %s", c.Group, c.Name, c.Detail)
		}
		if c.Fix != "" {
			msg += fmt.Sprintf(". Fix: %s", c.Fix)
		}
		warnings = append(warnings, msg)
	}

	if len(warnings) == 0 {
		return nil
	}

	var lines []string
	for _, w := range warnings {
		wrapped := wrapText(w, 70)
		for _, l := range wrapped {
			lines = append(lines, "relevo: "+l)
		}
	}
	lines = append(lines, "relevo: run `relevo doctor` for the full check")
	return lines
}

func wrapText(text string, maxLen int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	curr := words[0]
	for _, w := range words[1:] {
		if len(curr)+1+len(w) > maxLen {
			lines = append(lines, curr)
			curr = "  " + w
		} else {
			curr += " " + w
		}
	}
	lines = append(lines, curr)
	return lines
}

// enabledScope is a scope block that will actually run: nil for a nil block or
// one whose Enabled is explicitly false, the same rule scopeFromPolicy applies.
func enabledScope(sc *policy.ScopePolicy) *policy.ScopePolicy {
	if sc == nil || (sc.Enabled != nil && !*sc.Enabled) {
		return nil
	}
	return sc
}

// scopeBlock is one doctor.ScopeBlock for a policy scope that sets allowed_cpus
// (#314). MaxCPU is the highest core the pool names, computed with
// policy.ParseCPUList so internal/doctor never imports policy.
func scopeBlock(key string, sc *policy.ScopePolicy) doctor.ScopeBlock {
	b := doctor.ScopeBlock{Key: key, AllowedCPUs: sc.AllowedCPUs}
	if cpus, err := policy.ParseCPUList(sc.AllowedCPUs); err == nil && len(cpus) > 0 {
		b.MaxCPU = cpus[len(cpus)-1]
	}
	return b
}
