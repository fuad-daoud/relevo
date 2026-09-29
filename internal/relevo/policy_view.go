package relevo

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// PolicyWarning is one inconsistency between order[role] and the configured
// candidates: a listed token that is not configured, a listed token that
// does not serve the role it is listed under, or a candidate that serves a
// role but is missing from that role's order. policy.Load deliberately does
// not check any of this against candidates.json (spec §3.1): an order entry
// naming a removed candidate must not stop every subcommand from starting.
// It is tolerated at resolve time instead, and reported here for `relevo
// policy` and `doctor` to render.
type PolicyWarning struct {
	Role  string
	Index int // index into order[role]; -1 for an unlisted-candidate warning
	Token string
	Text  string // the rendered line
}

// PolicyWarnings is the one source of the three findings: relevo config's
// warnings block and doctor's policy rows both render from this function;
// neither computes its own.
func PolicyWarnings(set *candidate.Set, pol policy.Policy) []PolicyWarning {
	var out []PolicyWarning

	for _, role := range harness.RoleNames() {
		order := pol.OrderFor(role)
		if len(order) == 0 {
			continue
		}

		listed := make(map[string]bool)
		for i, entry := range order {
			// An entry may be a candidate name or a canonical token (A1
			// §4.2). A token that resolves is shown as the candidate's short
			// name; one that does not stays raw, since naming it is the
			// point of the warning (A1 §4.4).
			c, err := set.Resolve(entry)
			if err != nil {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: entry,
					Text:  fmt.Sprintf("order.%s[%d] %q is not a configured candidate", role, i, entry),
				})
				continue
			}
			if !c.Serves(role) {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: entry,
					Text:  fmt.Sprintf("order.%s[%d] %q does not serve %s (its roles: %v)", role, i, c.Name, role, c.Roles),
				})
				continue
			}
			listed[c.Ref().String()] = true
		}

		for _, c := range set.ForRole(role) {
			tok := c.Ref().String()
			if listed[tok] {
				continue
			}
			out = append(out, PolicyWarning{
				Role:  role,
				Index: -1,
				Token: tok,
				Text:  fmt.Sprintf("%s: %s serves the actor but is not in order.%s", role, c.Name, role),
			})
		}
	}

	return out
}

// RoleRefusal is one role for which an omitted candidate would be refused
// right now, computed by the same resolveCandidate call bind makes.
type RoleRefusal struct {
	Role    string
	Text    string   // the words after "would refuse:" in relevo config
	NoOrder bool     // true for the ambiguous case, false for the all-gated case
	Serving []string // tokens serving the role, in Set.ForRole order
	Gated   []string // providers with a gate on a serving token, deduplicated, in Serving order; nil when NoOrder
}

// RoleRefusals lists, in harness.RoleNames() order, every role with at
// least one serving candidate that resolveCandidate would refuse with no
// token named. It is the one source of doctor's policy rows and relevo
// policy's "would refuse" lines (#165).
func RoleRefusals(set *candidate.Set, pol policy.Policy, gates []availability.Gate) []RoleRefusal {
	if set == nil || set.Len() == 0 {
		return nil
	}
	var out []RoleRefusal
	for _, role := range harness.RoleNames() {
		serving := set.ForRole(role)
		if len(serving) == 0 {
			continue
		}
		_, err := resolveCandidate(set, pol, gates, "", role)
		// A roles-missing gate scoped to another role must not make this
		// role's refusal name a provider it never uses (#374 §3.1).
		if r, ok := refusalFromErr(role, serving, gatesForRole(gates, role), err); ok {
			out = append(out, r)
		}
	}
	return out
}

// refusalFromErr maps resolveCandidate's error for role to a RoleRefusal.
// FormatPolicy and RoleRefusals both go through here, so relevo config and
// relevo doctor print the same words for the same state.
func refusalFromErr(role string, serving []candidate.Candidate, gates []availability.Gate, err error) (RoleRefusal, bool) {
	tokens := make([]string, 0, len(serving))
	for _, c := range serving {
		tokens = append(tokens, c.Ref().String())
	}
	switch {
	case errors.Is(err, ErrAmbiguousCandidate):
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("%d candidates serve %s and no order is set", len(serving), role),
			NoOrder: true,
			Serving: tokens,
		}, true
	case errors.Is(err, ErrAllGated):
		var providers []string
		for _, c := range serving {
			if len(skipsFor(gates, c.Ref().String())) > 0 {
				providers = append(providers, c.Ref().Provider)
			}
		}
		return RoleRefusal{
			Role:    role,
			Text:    fmt.Sprintf("every candidate serving %s is gated", role),
			Serving: tokens,
			Gated:   uniqStrings(providers),
		}, true
	}
	return RoleRefusal{}, false
}

// refWidth is the widest configured candidate as it is printed: its short
// name (A1 §4.4), so every policy row pads to the width of what it shows.
func refWidth(set *candidate.Set) int {
	width := 0
	for _, ref := range set.Refs() {
		if n := set.NameOf(ref); len(n) > width {
			width = len(n)
		}
	}
	return width
}

// policyRoleView is one role's rendering inputs for formatPolicyRole. The two
// callers differ only in how they fill it in -- FormatPolicy from
// candidates.json and policy.json, FormatPolicyFor from the registry -- so a
// row can never render one way in one view and another way in the other
// (#374 §3.1).
type policyRoleView struct {
	role    string
	header  string
	rows    []rankedEntry
	serving []candidate.Candidate
	// sole forces the "sole" tag on the single row. FormatPolicy means "one
	// serving candidate"; FormatPolicyFor means "one row".
	sole bool
	// noRows is the line printed when there are no rows at all.
	noRows string
	res    Resolution
	err    error
}

// formatPolicyRole renders one role's block: the header, one row per ranked
// entry with the shared tail (peak cue, gate texts, "<- would pick"), and the
// refusal line. It is the single row renderer both FormatPolicy and
// FormatPolicyFor call, so the two can never drift. The gates are filtered to
// the role here, so a gate scoped to another role never shows on this role's
// rows or in its refusal.
func formatPolicyRole(sb *strings.Builder, v policyRoleView, set *candidate.Set, width int, gates []availability.Gate, hist availability.History, now time.Time, loc *time.Location) {
	sb.WriteString(v.header + "\n")
	if len(v.rows) == 0 {
		if v.noRows != "" {
			sb.WriteString(v.noRows + "\n")
		}
		return
	}

	roleGates := gatesForRole(gates, v.role)
	byToken := make(map[string][]availability.Gate)
	for _, g := range roleGates {
		byToken[g.Token] = append(byToken[g.Token], g)
	}

	for i, r := range v.rows {
		tag := string(r.How)
		if v.sole {
			tag = "sole"
		}
		tok := r.Candidate.Ref().String()

		var tailParts []string
		if pt := peakText(hist, r.Candidate.Ref().Provider, now, loc); pt != "" {
			tailParts = append(tailParts, pt)
		}
		var gateTexts []string
		for _, g := range byToken[tok] {
			gateTexts = append(gateTexts, availability.GateKindText(g.Kind)+" "+availability.GateUntilText(g.Until))
		}
		tailParts = append(tailParts, uniqStrings(gateTexts)...)
		tail := strings.Join(tailParts, "; ")

		// This combination cannot occur: a gated or off row is never picked.
		if !r.Off && v.err == nil && tok == v.res.Token() {
			if tail != "" {
				tail += "  "
			}
			tail += "<- would pick"
		}

		// The status column is the tag, padded to 8. An off entry reads the
		// plain word "off", padded like the other status words, and is never
		// marked as the pick (A2 §4.4, round 2 R1: no escape codes).
		tagField := fmt.Sprintf("%-8s", tag)
		if r.Off {
			tagField = fmt.Sprintf("%-8s", "off")
		}
		row := fmt.Sprintf("  %d  %-*s  %s  %s", i+1, width, set.NameOf(tok), tagField, tail)
		sb.WriteString(strings.TrimRight(row, " ") + "\n")
	}

	if r, ok := refusalFromErr(v.role, v.serving, roleGates, v.err); ok {
		sb.WriteString("  would refuse: " + r.Text + "\n")
	}
}

// FormatPolicy renders, per role, what resolveCandidate would do right now
// and why -- computed by calling it, so the marker here can never disagree
// with what bind actually picks (spec §4.7). It is a listing, not a check:
// `relevo config` prints this and always exits 0.
func FormatPolicy(set *candidate.Set, pol policy.Policy, gates []availability.Gate, hist availability.History, now time.Time, loc *time.Location) string {
	if set == nil || set.Len() == 0 {
		return "no candidates configured; set one with relevo config set candidates (see README \"Candidates\")\n"
	}

	width := refWidth(set)

	var sb strings.Builder
	for _, role := range harness.RoleNames() {
		serving := set.ForRole(role)
		ordered := len(pol.OrderFor(role)) > 0

		header := role
		if ordered {
			header += "  (order set in config policy)"
		} else {
			header += "  (no order set)"
		}

		v := policyRoleView{
			role:    role,
			header:  header,
			serving: serving,
			sole:    len(serving) == 1,
			noRows:  "  no candidate serves this actor",
		}
		if len(serving) > 0 {
			if ordered {
				v.rows = rankedList(set, pol, role)
			} else {
				for _, c := range serving {
					v.rows = append(v.rows, rankedEntry{Candidate: c})
				}
			}
			v.res, v.err = resolveCandidate(set, pol, gates, "", role)
		}
		formatPolicyRole(&sb, v, set, width, gates, hist, now, loc)
	}

	if warnings := PolicyWarnings(set, pol); len(warnings) > 0 {
		sb.WriteString("\nwarnings\n")
		for _, w := range warnings {
			sb.WriteString("  " + w.Text + "\n")
		}
	}

	if s := formatHistory(hist, loc); s != "" {
		sb.WriteString("\n" + s)
	}

	if len(pol.Order) == 0 {
		sb.WriteString("no policy configured; set one with relevo config set policy (see README \"Policy\")\n")
	}

	return sb.String()
}

// FormatPolicyFor is FormatPolicy with the roles read from reg (#374 §3.1). In
// legacy mode it returns FormatPolicy's bytes exactly; in file mode the roles,
// their candidate lists and their refusal come from roles.json, the header
// names the file, a role with nothing to rank says so, and the "no policy
// configured" line never prints -- roles.json is the policy.
func FormatPolicyFor(reg *roles.Registry, set *candidate.Set, pol policy.Policy, gates []availability.Gate, hist availability.History, now time.Time, loc *time.Location) string {
	if !reg.FileMode() {
		return FormatPolicy(set, pol, gates, hist, now, loc)
	}
	if set == nil || set.Len() == 0 {
		return "no candidates configured; set one with relevo config set candidates (see README \"Candidates\")\n"
	}

	width := refWidth(set)

	var sb strings.Builder
	for _, role := range reg.Names() {
		rows := rankedRole(reg, set, role)
		serving := make([]candidate.Candidate, 0, len(rows))
		for _, r := range rows {
			serving = append(serving, r.Candidate)
		}

		v := policyRoleView{
			role:    role,
			header:  role + "  (" + roleSectionText(reg) + ")",
			rows:    rows,
			serving: serving,
			sole:    len(rows) == 1,
			noRows:  fmt.Sprintf("  no candidate listed in %s %s.candidates", roleSectionText(reg), role),
		}
		if len(rows) > 0 {
			v.res, v.err = resolveRole(reg, set, gates, "", role)
		}
		formatPolicyRole(&sb, v, set, width, gates, hist, now, loc)
	}

	if warnings := PolicyWarningsFor(reg, set, pol); len(warnings) > 0 {
		sb.WriteString("\nwarnings\n")
		for _, w := range warnings {
			sb.WriteString("  " + w.Text + "\n")
		}
	}

	if s := formatHistory(hist, loc); s != "" {
		sb.WriteString("\n" + s)
	}

	return sb.String()
}

// PolicyWarningsFor is PolicyWarnings with the role assignments read from reg
// (#374 §3.1). In legacy mode it delegates, so the findings are unchanged; in
// file mode the candidates list is the assignment, so there is no
// "serves but not listed" finding -- a token that is not configured, or whose
// kind has no definition for the role, is the whole story.
func PolicyWarningsFor(reg *roles.Registry, set *candidate.Set, pol policy.Policy) []PolicyWarning {
	if !reg.FileMode() {
		return PolicyWarnings(set, pol)
	}
	if set == nil {
		// No candidates.json: nothing is configured, so every listed token
		// gets its warning rather than a panic.
		set = &candidate.Set{}
	}

	var out []PolicyWarning
	for _, role := range reg.Names() {
		info, _ := reg.Role(role)
		for i, entry := range info.Candidates {
			// An entry may be a candidate name or a canonical token (A1
			// §4.2); a resolving one is shown as its short name, an
			// unresolved one stays raw (A1 §4.4).
			c, err := set.Resolve(entry)
			if err != nil {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: entry,
					Text:  fmt.Sprintf("%s %s.candidates[%d] %q is not a configured candidate", roleSectionText(reg), role, i, entry),
				})
				continue
			}
			if _, err := reg.Spec(role, c.Ref().Harness); err != nil {
				out = append(out, PolicyWarning{
					Role:  role,
					Index: i,
					Token: entry,
					Text:  fmt.Sprintf("%s %s.candidates[%d] %q: %s has no definition for %s", roleSectionText(reg), role, i, c.Name, role, c.Ref().Harness),
				})
			}
		}
	}
	return out
}

// RoleRefusalsFor is RoleRefusals with the roles, their candidates and their
// gating read from reg (#374 §3.1). In legacy mode it delegates, so the rows
// are unchanged; in file mode it walks reg.Names() and each role's ranked
// candidates.
func RoleRefusalsFor(reg *roles.Registry, set *candidate.Set, pol policy.Policy, gates []availability.Gate) []RoleRefusal {
	if !reg.FileMode() {
		return RoleRefusals(set, pol, gates)
	}
	if set == nil || set.Len() == 0 {
		return nil
	}

	var out []RoleRefusal
	for _, role := range reg.Names() {
		ranked := rankedRole(reg, set, role)
		if len(ranked) == 0 {
			continue
		}
		serving := make([]candidate.Candidate, 0, len(ranked))
		for _, r := range ranked {
			serving = append(serving, r.Candidate)
		}
		_, err := resolveRole(reg, set, gates, "", role)
		if r, ok := refusalFromErr(role, serving, gatesForRole(gates, role), err); ok {
			out = append(out, r)
		}
	}
	return out
}

// LegacyRoleFieldWarnings names every legacy field roles.json makes
// irrelevant, one string per finding, in role order (#374 §3.1). It is pure,
// and nil in legacy mode: without a roles.json those fields are the source,
// not stale copies of it.
func LegacyRoleFieldWarnings(reg *roles.Registry, set *candidate.Set, pol policy.Policy) []string {
	if !reg.FileMode() {
		return nil
	}
	if set == nil {
		set = &candidate.Set{}
	}

	var out []string
	for _, ref := range set.Refs() {
		parsed, _ := candidate.ParseRef(ref)
		c, err := set.Lookup(parsed)
		if err != nil {
			continue
		}
		if len(c.Roles) > 0 {
			out = append(out, fmt.Sprintf("candidates: %s: roles is ignored; config roles assigns candidates to roles", ref))
		}
	}
	for _, ref := range set.Refs() {
		parsed, _ := candidate.ParseRef(ref)
		c, err := set.Lookup(parsed)
		if err != nil {
			continue
		}
		if c.Tier != "" {
			out = append(out, fmt.Sprintf("candidates: %s: tier is ignored; set the role's tier in config roles", ref))
		}
	}
	if len(pol.Order) > 0 {
		out = append(out, "policy: order is ignored; config roles <role>.candidates orders them")
	}
	if len(pol.Tier) > 0 {
		out = append(out, "policy: tier is ignored; set the role's tier in config roles")
	}
	return out
}

// peakText is a candidate row's cue that its provider was recently
// rate-limited: "limited <n>x around <HH>:00 (30d)" for the local hour
// around now, or "" when the window around now saw none (spec §4.2).
// History never changes a pick -- resolveCandidate is untouched -- this is
// display only.
func peakText(hist availability.History, provider string, now time.Time, loc *time.Location) string {
	c := availability.HourCounts(hist, provider, availability.RateLimited, loc)
	h := now.In(loc).Hour()
	n := c[(h+23)%24] + c[h] + c[(h+1)%24]
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("limited %dx around %02d:00 (30d)", n, h)
}

// formatHistory renders the "history (30d, local hours)" block: one row per
// (provider, kind) with at least one event in hist, providers sorted and
// RateLimited before SpawnFailed before Cleared within a provider (spec
// §4.2). A provider whose Cleared events carry a Since also gets a row in
// the "blocked for" summary under the grid (#302). It returns "" when hist
// has no events, so a fresh install's `relevo config` prints nothing extra.
func formatHistory(hist availability.History, loc *time.Location) string {
	if len(hist.Events) == 0 {
		return ""
	}

	type pair struct {
		provider string
		kind     availability.Kind
	}
	seen := make(map[pair]bool)
	providerSeen := make(map[string]bool)
	var providers []string
	for _, e := range hist.Events {
		seen[pair{e.Provider, e.Kind}] = true
		if !providerSeen[e.Provider] {
			providerSeen[e.Provider] = true
			providers = append(providers, e.Provider)
		}
	}
	sort.Strings(providers)

	var sb strings.Builder
	sb.WriteString("history (30d, local hours)\n")

	var labels [24]string
	for h := range labels {
		labels[h] = fmt.Sprintf("%02d", h)
	}
	sb.WriteString(strings.Repeat(" ", 27) + strings.Join(labels[:], " ") + "\n")

	kinds := []availability.Kind{availability.RateLimited, availability.SpawnFailed, availability.Cleared}
	for _, p := range providers {
		for _, k := range kinds {
			if !seen[pair{p, k}] {
				continue
			}
			counts := availability.HourCounts(hist, p, k, loc)
			row := fmt.Sprintf("  %-10s %-13s", p, availability.GateKindText(k))
			for _, n := range counts {
				cell := "."
				if n != 0 {
					cell = strconv.Itoa(n)
				}
				row += fmt.Sprintf(" %2s", cell)
			}
			sb.WriteString(row + "\n")
		}
	}

	var blocked strings.Builder
	for _, p := range providers {
		durations := availability.BlockedDurations(hist, p)
		if len(durations) == 0 {
			continue
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

		// Even n takes the lower middle, so the median is always one of the
		// durations actually observed -- never an average of two.
		median := durations[len(durations)/2]
		if len(durations)%2 == 0 {
			median = durations[len(durations)/2-1]
		}
		longest := durations[len(durations)-1]

		count := fmt.Sprintf("%d clears", len(durations))
		if len(durations) == 1 {
			count = "1 clear"
		}
		blocked.WriteString(fmt.Sprintf("  %-10s %s  median %s  longest %s\n", p, count, blockedText(median), blockedText(longest)))
	}
	if blocked.Len() > 0 {
		sb.WriteString("blocked for (30d, gates cleared by hand)\n")
		sb.WriteString(blocked.String())
	}

	return sb.String()
}

// blockedText renders a blocked duration compactly, flooring:
//
//	d < 1h   -> "<m>m"          45m -> "45m"; 0 -> "0m"
//	d < 24h  -> "<h>h<mm>m"     5h2m -> "5h02m"
//	else     -> "<d>d<hh>h"     52h -> "2d04h"; 72h -> "3d00h"
func blockedText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", d/time.Hour, (d%time.Hour)/time.Minute)
	default:
		return fmt.Sprintf("%dd%02dh", d/(24*time.Hour), (d%(24*time.Hour))/time.Hour)
	}
}
