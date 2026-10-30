package doctor

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
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// databaseCheck is doctor's `database` row: the path, the file
// size, the schema version, the binding_record live/archived counts and the
// route this process reached the row by -- `via file` for a direct open, `via
// owner <sock>` for a dialled handle (stage 1b). It is OK unless the open
// fails; there is no migrate row, because every open migrates.
func databaseCheck(st *store.Store) Check {
	path := st.DBPath()
	c := Check{Name: "database", Severity: SevOK}

	d, err := st.DB()
	if err != nil {
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		c.Fix = "fix the state root, or move the database aside"
		return c
	}
	version, err := d.Version()
	if err != nil {
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		return c
	}
	live, archived, err := d.RecordCounts()
	if err != nil {
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("%s · %v", path, err)
		return c
	}

	size := int64(0)
	if info, serr := os.Stat(path); serr == nil {
		size = info.Size()
	}
	c.Detail = fmt.Sprintf("%s · %s · schema v%d · %d live, %d archived · via %s",
		path, view.HumanBytes(size), version, live, archived, d.Route())
	return c
}

// engineCheck is doctor's `engine` row: the driver this binary opens databases
// with, and whether its library is usable. A missing library warns -- a fresh
// root has none until something opens a database there -- and a library the
// loader refuses fails, because nothing can open until it is removed.
func engineCheck(status db.EngineState) Check {
	c := Check{Name: "engine", Severity: SevOK}
	switch {
	case status.Err != nil:
		c.Severity = SevFail
		c.Detail = fmt.Sprintf("%s · %s · %v", status.Name, status.Library, status.Err)
		c.Fix = fmt.Sprintf("remove %s and restart the daemon", status.CacheDir)
	case status.Missing:
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("%s · no library under %s yet", status.Name, status.CacheDir)
	default:
		c.Detail = status.Name
		if status.Library != "" {
			c.Detail = status.Name + " · " + status.Library
		}
	}
	return c
}

// ownerCheck is doctor's `owner` row: whether the daemon serves relevo.sock,
// and what it answers. A refused dial or an absent socket warns with the fix,
// since a daemon that predates the socket still runs.
func ownerCheck(status db.OwnerStatus, probeErr error) Check {
	c := Check{Name: "owner", Severity: SevOK}
	if probeErr != nil {
		c.Severity = SevWarn
		detail := probeErr.Error()
		if status.Socket != "" {
			detail = status.Socket + " · " + detail
		}
		c.Detail = detail
		c.Fix = "run any relevo command: it starts the daemon when the socket is missing"
		return c
	}
	c.Detail = fmt.Sprintf("socket %s · pid %d · protocol %s v%d · %d connections",
		status.Socket, status.PID, wire.Proto, status.Version, status.Conns)
	return c
}

// ledgerChecks turns live gates into doctor rows under the candidate's
// harness. They warn, never fail: a gated provider is a fact about right
// now, not a broken install, and must not change doctor's exit code.
func ledgerChecks(gates []availability.Gate) []Check {
	checks := make([]Check, 0, len(gates))
	for _, g := range gates {
		if g.Kind != availability.SpawnFailed && g.Kind != availability.RateLimited {
			continue
		}
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

		checks = append(checks, Check{
			Group:    group,
			Name:     "ledger",
			Severity: SevWarn,
			Detail: fmt.Sprintf("%s: %s since %s (%s)",
				g.Token, availability.GateKindText(g.Kind), availability.GateTimeText(g.Since), g.UntilText()),
			Fix: fix,
		})
	}
	return checks
}

// serverChecks turns each configured server's probe (relevo.ProbeServers)
// into a doctor row: reachable and enrolled is
// ok; not enrolled and unreachable warn (an unreachable probe is
// ProbeFailed -- relevo could not establish the fact, not that anything is
// wrong); a changed certificate fails, since the client hard-refuses it.
func serverChecks(probes []relevo.ServerProbe) []Check {
	checks := make([]Check, 0, len(probes))
	for _, p := range probes {
		c := Check{Group: "", Name: "servers"}
		switch p.State {
		case "enrolled":
			c.Severity = SevOK
			c.Detail = fmt.Sprintf("%s: enrolled as %s", p.Name, p.Label)
			if p.QueueAware && p.Builders != nil {
				c.Detail += fmt.Sprintf(", %s", buildersText(p))
			}
			checks = append(checks, c)
			if warning := relevo.ServerTierWarning(p); warning != "" {
				checks = append(checks, Check{
					Group:    "",
					Name:     "servers",
					Severity: SevWarn,
					Detail:   fmt.Sprintf("%s: %s", p.Name, warning),
					Fix:      "set tier.builder in the server's config policy",
				})
			} else if !p.TierAware {
				checks = append(checks, Check{
					Group:       "",
					Name:        "servers",
					Severity:    SevWarn,
					Detail:      fmt.Sprintf("%s: builder tier unknown (pre-tier server)", p.Name),
					ProbeFailed: true,
				})
			}
			if p.QueueAware && p.Builders != nil && !p.Builders.Scopes {
				checks = append(checks, Check{
					Group:    "",
					Name:     "servers",
					Severity: SevWarn,
					Detail:   fmt.Sprintf("scopes unavailable on %s: a daemon restart kills its builders", p.Name),
				})
			}
			continue
		case "not enrolled":
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s: not enrolled", p.Name)
			c.Fix = "give the admin: " + p.Detail
		case "unreachable":
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s: unreachable: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		case "cert changed":
			c.Severity = SevFail
			c.Detail = fmt.Sprintf("%s: certificate changed", p.Name)
			c.Fix = fmt.Sprintf("relevo config server add %s <url> --fingerprint <new>", p.Name)
		case "no key":
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		default:
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s: %s", p.Name, p.Detail)
			c.ProbeFailed = true
		}
		checks = append(checks, c)
	}
	return checks
}

// buildersText is a queue-aware, enrolled probe's runner census, the same
// words RenderServers appends to its row: "runners %d/%d, %d
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
// a broken machine.
func policyChecks(warnings []relevo.PolicyWarning) []Check {
	checks := make([]Check, 0, len(warnings))
	for _, w := range warnings {
		checks = append(checks, Check{
			Group:    "",
			Name:     "policy",
			Severity: SevWarn,
			Detail:   w.Text,
			Fix:      "run relevo config edit",
		})
	}
	return checks
}

// refusalChecks turns the roles an omitted candidate would be refused for
// into doctor rows. Warnings, not failures: the machine is fine, the
// configuration is not. The fix is a literal policy.json built
// from the tokens that serve the role, so it can be pasted as is.
func refusalChecks(refusals []relevo.RoleRefusal) []Check {
	checks := make([]Check, 0, len(refusals))
	for _, r := range refusals {
		detail := r.Text + " -- bind --actor " + r.Role + " without --candidate would refuse"
		if r.Role == "builder" {
			detail = r.Text + " -- add/bind without --candidate would refuse"
		}
		fix := "relevo config set policy '" + policyExample(r.Role, r.Serving) + "'"
		if !r.NoOrder {
			fix = "relevo gate --clear <provider>"
			if len(r.Gated) > 0 {
				fix = "relevo gate --clear " + strings.Join(r.Gated, " && relevo gate --clear ")
			}
			switch r.Cause {
			case relevo.CauseWindow:
				fix = "wait for the window to open, or widen it with relevo config edit"
			case relevo.CauseCap:
				fix = "wait for a round to finish, or raise max_concurrency with relevo config edit"
			}
		}
		checks = append(checks, Check{
			Group:    "",
			Name:     "policy",
			Severity: SevWarn,
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
// stale-record note names it.
const staleMasterMindAge = 7 * 24 * time.Hour

// mastermindCheckInput gathers the mastermind-row facts: which masterminds exist,
// whether this process runs inside Claude Code or muse, and whether the
// resolved mastermind has a live push claim. Every read is best-effort -- a
// fact relevo cannot establish reads as absent, and the checks say "not
// checked" rather than guessing. Home comes from $HOME so the row reads the
// same directory cmd/relevo's TestMain isolated. The cmd-owned facts arrive
// through deps; everything else is read here.
func mastermindCheckInput(rt relevo.Runtime, kinds []string, deps Deps) MasterMindCheckInput {
	in := MasterMindCheckInput{Home: os.Getenv("HOME"), ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), Running: deps.Version}
	if wd, err := os.Getwd(); err == nil {
		in.Repo = wd
	}
	mastermindConsent(rt, &in, deps)
	for _, k := range kinds {
		gatePluginRowsFor(k, &in)
	}

	live := liveMasterMindIDs(rt)
	cutoff := rt.Now().Add(-staleMasterMindAge)
	for _, rec := range mastermindRecords(rt) {
		gatePluginRowsFor(rec.HarnessKind, &in)
		if isStaleMasterMind(rec, live, cutoff) {
			in.Stale = append(in.Stale, rec.Name)
		}
	}

	mastermindClaudeSession(rt, &in, deps)
	museSessionGate(rt, &in, deps.ChatResolver)

	return in
}

// hostChildProcesses reads the child processes of pid: /proc/<pid>/task/*/children
// names them, and each child's /proc/<pid>/cmdline its argv. On a host with no
// /proc (macOS) it falls back to `ps -o pid=,args= --ppid`. A pid that has gone
// or a read that fails yields nil, which reads as "no relevo mcp child" -- the
// FAIL the mastermind row asks for when a Claude session has no push route.
func hostChildProcesses(pid int) []ChildProcess {
	if pid <= 0 {
		return nil
	}
	paths, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	if err != nil || len(paths) == 0 {
		return psChildren(pid)
	}
	seen := make(map[int]bool)
	var out []ChildProcess
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
				out = append(out, ChildProcess{PID: cpid, Args: args})
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
func psChildren(pid int) []ChildProcess {
	out, err := exec.Command("ps", "-o", "pid=,args=", "--ppid", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil
	}
	return ParsePSChildren(out)
}

// ParsePSChildren parses `ps -o pid=,args=` output: one line per process, the
// pid first, the argv as the rest of the line. Pure, so the parsing is
// testable without a process tree.
func ParsePSChildren(out []byte) []ChildProcess {
	var procs []ChildProcess
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
		procs = append(procs, ChildProcess{PID: pid, Args: args})
	}
	return procs
}

// InsertGlobalCheck puts c after the last global row, so render order stays
// "globals first, then one block per kind".
func InsertGlobalCheck(checks []Check, c Check) []Check {
	last := 0
	for i, existing := range checks {
		if existing.Group == "" {
			last = i + 1
		}
	}
	out := make([]Check, 0, len(checks)+1)
	out = append(out, checks[:last]...)
	out = append(out, c)
	return append(out, checks[last:]...)
}

// BindPreflightDefs runs the bind-time preflight for one kind and renders its
// warning lines, checking the definitions the caller resolved for the kind --
// the registry's builder definitions when roles.json is loaded, the shipped
// ones otherwise. The timeout lives here, not at the call site, so
// it cannot be dropped by accident; adopted is passed through to Run,
// which owns what an adopted pane is and is not checked for.
func BindPreflightDefs(ctx context.Context, env Env, kind string, adopted bool, defs []string) []string {
	if defs == nil {
		defs = builderDefinitions()
	}
	ctx, cancel := context.WithTimeout(ctx, bindPreflightTimeout)
	defer cancel()
	return bindWarningLines(Run(ctx, env, []string{kind}, WithAdopted(adopted), WithDefinitions(map[string][]string{kind: defs})))
}

// bindPreflight is BindPreflightDefs with the builder's shipped definitions:
// the behaviour every pre-roles.json caller had.
func bindPreflight(ctx context.Context, env Env, kind string, adopted bool) []string {
	return BindPreflightDefs(ctx, env, kind, adopted, builderDefinitions())
}

func bindWarningLines(rep Report) []string {
	var warnings []string
	for _, c := range rep.Checks {
		if c.Severity == SevOK {
			continue
		}
		// A row relevo could not establish is not actionable, so it stays off the
		// hot path -- unless it is a SevFail, which means relevo cannot run at
		// all and the user needs to hear it even when the cause was a bad probe.
		if c.ProbeFailed && c.Severity != SevFail {
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

// scopeBlock is one ScopeBlock for a policy scope that sets allowed_cpus.
// MaxCPU is the highest core the pool names, computed with
// policy.ParseCPUList so internal/doctor never imports policy.
func scopeBlock(key string, sc *policy.ScopePolicy) ScopeBlock {
	b := ScopeBlock{Key: key, AllowedCPUs: sc.AllowedCPUs}
	if cpus, err := policy.ParseCPUList(sc.AllowedCPUs); err == nil && len(cpus) > 0 {
		b.MaxCPU = cpus[len(cpus)-1]
	}
	return b
}
