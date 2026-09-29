package main

import (
	"encoding/json"
	"os"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/view"
)

// printDoc writes v to stdout as one indented JSON document. Every --json
// branch this package converted funnels through it, so the machine shape is
// one object (or array) per run and never a bare value.
func printDoc(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// versionDoc is `relevo version --json`: the one version string the human
// line carries, under a name an agent can read.
type versionDoc struct {
	Version string `json:"version"`
}

// DoctorDoc is `relevo doctor --json`: the run's verdict inputs, its failure
// and warning counts, and every check in render order. It is the machine twin
// of renderReport, so a caller can branch on severity instead of on columns.
type DoctorDoc struct {
	UsableBuilder  bool          `json:"usable_builder"`
	NoCandidates   bool          `json:"no_candidates"`
	BuilderRefusal string        `json:"builder_refusal,omitempty"`
	Failures       int           `json:"failures"`
	Warnings       int           `json:"warnings"`
	Checks         []DoctorCheck `json:"checks"`
}

// DoctorCheck is one row of DoctorDoc: the same four facts renderReport
// prints, with severity as its word and the probe-failure flag kept.
type DoctorCheck struct {
	Group       string `json:"group,omitempty"`
	Name        string `json:"name"`
	Severity    string `json:"severity"`
	Detail      string `json:"detail"`
	Fix         string `json:"fix,omitempty"`
	ProbeFailed bool   `json:"probe_failed,omitempty"`
}

// doctorDocOf maps a doctor.Report to its document. It is pure, so the shape
// is tested without probing a machine.
func doctorDocOf(rep doctor.Report) DoctorDoc {
	checks := make([]DoctorCheck, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		checks = append(checks, DoctorCheck{
			Group:       c.Group,
			Name:        c.Name,
			Severity:    c.Severity.String(),
			Detail:      c.Detail,
			Fix:         c.Fix,
			ProbeFailed: c.ProbeFailed,
		})
	}
	return DoctorDoc{
		UsableBuilder:  rep.UsableBuilder,
		NoCandidates:   rep.NoCandidates,
		BuilderRefusal: rep.BuilderRefusal,
		Failures:       rep.Failures(),
		Warnings:       rep.Warnings(),
		Checks:         checks,
	}
}

// ConfigView is `relevo config --json`: the actors (or the legacy roles), the
// current pick per role, and the configured candidates.
type ConfigView struct {
	Actors     map[string]roles.Actor `json:"actors,omitempty"`
	Roles      []string               `json:"roles,omitempty"`
	Pick       []ConfigPick           `json:"pick"`
	Candidates []ConfigCandidate      `json:"candidates"`
}

// ConfigPick is one role's row: the candidate relevo would pick now, and the
// refusal words when a candidate would be refused.
type ConfigPick struct {
	Role      string `json:"role"`
	Candidate string `json:"candidate,omitempty"`
	Why       string `json:"why,omitempty"`
}

// ConfigCandidate is one configured candidate: its canonical token, its short
// name, and the roles that serve it.
type ConfigCandidate struct {
	Ref      string   `json:"ref"`
	Name     string   `json:"name"`
	Harness  string   `json:"harness"`
	Provider string   `json:"provider"`
	Model    string   `json:"model"`
	Roles    []string `json:"roles"`
}

// configViewOf builds the config document. Actors come straight from the
// stored section; the legacy form falls back to the registry's role names.
// pick is the first on-and-ungated entry per role, and why carries the
// refusal text when the role would refuse.
func configViewOf(L config.Loaded, reg *roles.Registry, set *candidate.Set, gates []availability.Gate, refusals []relevo.RoleRefusal) ConfigView {
	out := ConfigView{}

	var names []string
	if len(L.Actors) > 0 {
		out.Actors = L.Actors
		names = make([]string, 0, len(L.Actors))
		for name := range L.Actors {
			names = append(names, name)
		}
		sort.Strings(names)
	} else if reg != nil {
		names = reg.Names()
		out.Roles = names
	}

	why := make(map[string]string, len(refusals))
	for _, r := range refusals {
		why[r.Role] = r.Text
	}

	picks := make([]ConfigPick, 0, len(names))
	for _, name := range names {
		picks = append(picks, ConfigPick{
			Role:      name,
			Candidate: pickCandidate(L.Actors[name], set, gates),
			Why:       why[name],
		})
	}
	out.Pick = picks

	cands := make([]ConfigCandidate, 0)
	if set != nil {
		for _, ref := range set.Refs() {
			parsed, err := candidate.ParseRef(ref)
			if err != nil {
				continue
			}
			c, err := set.Lookup(parsed)
			if err != nil {
				continue
			}
			served := view.CandidateRoles(reg, set, ref)
			if served == nil {
				served = []string{}
			}
			cands = append(cands, ConfigCandidate{
				Ref:      ref,
				Name:     set.NameOf(ref),
				Harness:  c.Harness,
				Provider: c.Provider,
				Model:    c.Model,
				Roles:    served,
			})
		}
	}
	out.Candidates = cands
	return out
}

// pickCandidate is the ui's next-pick rule: the first entry, in order, that
// is on and whose token carries no gate. "" when none does.
func pickCandidate(a roles.Actor, set *candidate.Set, gates []availability.Gate) string {
	for _, e := range a.Candidates {
		if e.Off {
			continue
		}
		name := e.Candidate
		if set != nil {
			name = set.NameOf(e.Candidate)
		}
		if name == "" {
			continue
		}
		if gateOnToken(gates, e.Candidate) {
			continue
		}
		return name
	}
	return ""
}

// gateOnToken reports whether any gate names token.
func gateOnToken(gates []availability.Gate, token string) bool {
	for i := range gates {
		if gates[i].Token == token {
			return true
		}
	}
	return false
}

// ServerRow is one `config server list --json` row. The probe carries more
// than the human table shows, so the optional columns are dropped when unset.
type ServerRow struct {
	Name        string               `json:"name"`
	URL         string               `json:"url"`
	State       string               `json:"state"`
	Label       string               `json:"label,omitempty"`
	Detail      string               `json:"detail,omitempty"`
	TierAware   bool                 `json:"tier_aware,omitempty"`
	BuilderTier string               `json:"builder_tier,omitempty"`
	MaxTier     string               `json:"max_tier,omitempty"`
	QueueAware  bool                 `json:"queue_aware,omitempty"`
	Builders    *remote.BuildersView `json:"builders,omitempty"`
}

// serverRowsOf maps the same probes the human table renders. The slice is
// never nil, so an empty list marshals [] rather than null.
func serverRowsOf(probes []relevo.ServerProbe) []ServerRow {
	rows := make([]ServerRow, 0, len(probes))
	for _, p := range probes {
		rows = append(rows, ServerRow{
			Name:        p.Name,
			URL:         p.URL,
			State:       p.State,
			Label:       p.Label,
			Detail:      p.Detail,
			TierAware:   p.TierAware,
			BuilderTier: p.BuilderTier,
			MaxTier:     p.MaxTier,
			QueueAware:  p.QueueAware,
			Builders:    p.Builders,
		})
	}
	return rows
}

// serverKeyDoc is `config server key --json`: the client id and the
// enrollment line a server admin needs.
type serverKeyDoc struct {
	ID         string `json:"id"`
	EnrollLine string `json:"enroll_line"`
}

// fingerprintDoc is `serve fingerprint --json`.
type fingerprintDoc struct {
	Fingerprint string `json:"fingerprint"`
}

// GateRow is one gate ledger row. A zero Until is omitted, so an open-ended
// gate never claims an expiry.
type GateRow struct {
	Token   string `json:"token"`
	Name    string `json:"name,omitempty"`
	Kind    string `json:"kind"`
	Since   string `json:"since"`
	Until   string `json:"until,omitempty"`
	Note    string `json:"note,omitempty"`
	Source  string `json:"source,omitempty"`
	Binding string `json:"binding,omitempty"`
	Role    string `json:"role,omitempty"`
}

// gateRowsOf maps one ledger's gates. The slice is never nil, so an empty
// ledger marshals [] rather than null.
func gateRowsOf(gates []availability.Gate) []GateRow {
	rows := make([]GateRow, 0, len(gates))
	for _, g := range gates {
		row := GateRow{
			Token:   g.Token,
			Name:    g.Name,
			Kind:    string(g.Kind),
			Since:   g.Since.UTC().Format(time.RFC3339),
			Note:    g.Note,
			Source:  g.Source,
			Binding: g.Binding,
			Role:    g.Role,
		}
		if !g.Until.IsZero() {
			row.Until = g.Until.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	return rows
}
