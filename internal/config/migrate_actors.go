package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// MigrateToActors rewrites a pre-actors stored config -- a roles section, or
// the legacy policy.order/policy.tier and candidate roles/tier keys -- as an
// actors section plus, when a role needs one, an agents section, recorded as
// one revision whose source is "migration". It reports whether it wrote.
//
// It computes the whole new document and validates every section before
// opening the transaction, so a failure writes nothing and the old config
// keeps working.
func (s *Store) MigrateToActors() (bool, error) {
	doc, err := s.currentDoc()
	if err != nil {
		return false, err
	}
	if _, ok := doc[Actors]; ok {
		return false, nil
	}
	if !legacyRolesPresent(doc) {
		return false, nil
	}

	set, err := candidateSetFromDoc(doc)
	if err != nil {
		return false, err
	}

	out, notes, err := migrateDoc(doc, set)
	if err != nil {
		return false, err
	}

	for _, sec := range Sections {
		body, ok := out[sec]
		if !ok {
			continue
		}
		if _, err := Validate(sec, body); err != nil {
			return false, err
		}
	}

	message := "roles → actors"
	if len(notes) > 0 {
		message += "; " + strings.Join(notes, "; ")
	}

	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		for _, sec := range []Section{Candidates, Agents, Actors, Policy} {
			body, ok := out[sec]
			if !ok {
				continue
			}
			if err := t.ConfigPut(string(sec), body, now); err != nil {
				return err
			}
		}
		if err := t.ConfigDelete(string(Roles)); err != nil {
			return err
		}
		return s.As("migration", message).record(t, before, nil)
	}); err != nil {
		return false, err
	}
	return true, nil
}

func legacyRolesPresent(doc Doc) bool {
	if _, ok := doc[Roles]; ok {
		return true
	}
	if body, ok := doc[Policy]; ok && jsonHasAnyKey(body, "order", "tier") {
		return true
	}
	if body, ok := doc[Candidates]; ok && candidatesHaveAnyKey(body, "roles", "tier") {
		return true
	}
	return false
}

func candidateSetFromDoc(doc Doc) (*candidate.Set, error) {
	body, ok := doc[Candidates]
	if !ok {
		body = []byte("[]")
	}
	set, _, err := candidate.Parse(FileName(Candidates), body)
	if err != nil {
		return nil, err
	}
	return set, nil
}

func ignoredLegacyWarnings(candBody []byte, candOK bool, polBody []byte, polOK bool) []string {
	var out []string
	if polOK && jsonHasAnyKey(polBody, "order") {
		out = append(out, "config: policy.order is ignored; actors decide (relevo config log shows the migration)")
	}
	if polOK && jsonHasAnyKey(polBody, "tier") {
		out = append(out, "config: policy.tier is ignored; actors decide (relevo config log shows the migration)")
	}
	if !candOK {
		return out
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(candBody, &rows); err != nil {
		return out
	}
	for i, row := range rows {
		if _, ok := row["roles"]; ok {
			out = append(out, fmt.Sprintf("config: candidates[%d].roles is ignored; actors decide (relevo config log shows the migration)", i))
		}
		if _, ok := row["tier"]; ok {
			out = append(out, fmt.Sprintf("config: candidates[%d].tier is ignored; actors decide (relevo config log shows the migration)", i))
		}
	}
	return out
}

type migration struct {
	agents    map[string]roles.AgentEntry
	actors    map[string]roles.Actor
	roleTiers map[string]string
	notes     []string
}

func migrateDoc(doc Doc, set *candidate.Set) (Doc, []string, error) {
	m, err := buildMigration(doc, set)
	if err != nil {
		return nil, nil, err
	}
	out, err := migratedSections(doc, set, m)
	if err != nil {
		return nil, nil, err
	}
	return out, m.notes, nil
}

func buildMigration(doc Doc, set *candidate.Set) (*migration, error) {
	pol, rf, err := legacySections(doc)
	if err != nil {
		return nil, err
	}
	reg, err := roles.Build(rf, set, pol)
	if err != nil {
		return nil, err
	}
	old, err := legacyRows(rf, set, pol)
	if err != nil {
		return nil, err
	}

	m := &migration{
		agents:    map[string]roles.AgentEntry{},
		actors:    map[string]roles.Actor{},
		roleTiers: map[string]string{},
	}
	// Existing agents survive the migration; a new native agent whose name
	// collides with one takes the <role>-agent fallback.
	if body, ok := doc[Agents]; ok {
		existing, _, err := roles.ParseAgents(body)
		if err != nil {
			return nil, err
		}
		for name, entry := range existing {
			m.agents[name] = entry
		}
	}

	for _, name := range reg.Names() {
		role, _ := reg.Role(name)
		a, agentName, notes := actorForRole(name, role, old.Rows[name], set, rf, m.agents)
		m.notes = append(m.notes, notes...)
		if agentName == "" {
			continue
		}
		if t, ok := reg.RoleTier(name); ok {
			a.Tier = string(t)
			m.roleTiers[name] = string(t)
		}
		m.actors[name] = a
	}
	return m, nil
}

func legacySections(doc Doc) (policy.Policy, *roles.File, error) {
	pol := policy.Policy{}
	if body, ok := doc[Policy]; ok {
		p, _, err := policy.Parse(FileName(Policy), body)
		if err != nil {
			return policy.Policy{}, nil, err
		}
		pol = p
	}
	var rf *roles.File
	if body, ok := doc[Roles]; ok {
		f, _, err := roles.Parse(FileName(Roles), body)
		if err != nil {
			return policy.Policy{}, nil, err
		}
		rf = f
	}
	return pol, rf, nil
}

func legacyRows(rf *roles.File, set *candidate.Set, pol policy.Policy) (*roles.File, error) {
	if rf != nil {
		return rf, nil
	}
	f, _, err := roles.FromLegacy(set, pol)
	if err != nil {
		return nil, err
	}
	return f, nil
}

func actorForRole(name string, role roles.Role, row roles.Row, set *candidate.Set, rf *roles.File, agentsOut map[string]roles.AgentEntry) (roles.Actor, string, []string) {
	agentName, entry, created := agentForRole(role, agentsOut)
	if agentName == "" {
		return roles.Actor{}, "", []string{fmt.Sprintf("actor %s: no definitions; skipped", name)}
	}

	var notes []string
	if created {
		agentsOut[agentName] = entry
		notes = append(notes, fmt.Sprintf("agent %s: from role %s's definitions", agentName, name))
	}

	a := roles.Actor{Agent: agentName}
	seen := make(map[string]bool)
	for _, raw := range row.Candidates {
		display := raw
		if _, err := set.Resolve(raw); err == nil {
			display = set.NameOf(raw)
		} else {
			notes = append(notes, fmt.Sprintf("actor %s: candidate %q is not configured; kept", name, raw))
		}
		if seen[display] {
			// Two raw entries resolving to one candidate: file mode dedupes
			// them on the ranked token, so the actor names the candidate once.
			continue
		}
		seen[display] = true
		a.Candidates = append(a.Candidates, roles.Entry{Candidate: display, Off: containsString(row.Off, raw)})
	}
	if rf != nil && row.Check != nil {
		a.Check = row.Check
	}
	return a, agentName, notes
}

func migratedSections(doc Doc, set *candidate.Set, m *migration) (Doc, error) {
	out := make(Doc, len(doc)+2)
	for sec, body := range doc {
		out[sec] = body
	}

	actorsJSON, err := roles.EncodeActors(m.actors)
	if err != nil {
		return nil, err
	}
	out[Actors] = actorsJSON
	delete(out, Roles)

	if len(m.agents) > 0 {
		agentsJSON, err := roles.EncodeAgents(m.agents)
		if err != nil {
			return nil, err
		}
		out[Agents] = agentsJSON
	}

	if body, ok := doc[Policy]; ok {
		updated, err := stripObjectKeys(body, Policy, "order", "tier")
		if err != nil {
			return nil, err
		}
		out[Policy] = updated
	}

	if body, ok := doc[Candidates]; ok {
		updated, candNotes, err := stripCandidateKeys(body, set, m.roleTiers)
		if err != nil {
			return nil, err
		}
		out[Candidates] = updated
		m.notes = append(m.notes, candNotes...)
	}
	return out, nil
}

func agentForRole(role roles.Role, agentsOut map[string]roles.AgentEntry) (string, roles.AgentEntry, bool) {
	if shipped, ok := shippedAgentFor(role); ok {
		return shipped, roles.AgentEntry{}, false
	}
	if len(role.Definitions) == 0 {
		return "", roles.AgentEntry{}, false
	}

	name := nativeAgentName(role)
	if _, shipped := roles.Shipped(name); shipped {
		name = role.Name + "-agent"
	}
	if _, exists := agentsOut[name]; exists {
		name = role.Name + "-agent"
	}

	native := make(map[string]roles.DefRow, len(role.Definitions))
	for kind, d := range role.Definitions {
		native[kind] = roles.DefRow{
			Agent:    d.Agent,
			Requires: append([]string(nil), d.Requires...),
		}
	}
	entry := roles.AgentEntry{Shape: shapeWord(role.Shape), Native: native}
	return name, entry, true
}

func shippedAgentFor(role roles.Role) (string, bool) {
	spec, ok := harness.RoleByName(role.Name)
	if !ok {
		return "", false
	}
	var wants []string
	if len(spec.Definitions) > 1 {
		wants = spec.Definitions[1:]
	}
	for _, h := range harness.All() {
		d, ok := role.Definitions[h.Kind]
		if !ok {
			return "", false
		}
		if d.Agent != spec.Definition || !sameStrings(d.Requires, wants) {
			return "", false
		}
	}
	return spec.Definition, true
}

// nativeAgentName falls back to <role>-agent when the kinds disagree.
func nativeAgentName(role roles.Role) string {
	claude, ok := role.Definitions["claude"]
	if !ok {
		return role.Name + "-agent"
	}
	for kind, d := range role.Definitions {
		if kind == "claude" {
			continue
		}
		if d.Agent != claude.Agent || !sameStrings(d.Requires, claude.Requires) {
			return role.Name + "-agent"
		}
	}
	return claude.Agent
}

func shapeWord(shape harness.RoleShape) string {
	if shape == harness.ShapeBuilder {
		return "writer"
	}
	return "reader"
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func jsonHasAnyKey(body []byte, keys ...string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return false
	}
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

func candidatesHaveAnyKey(body []byte, keys ...string) bool {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return false
	}
	for _, row := range rows {
		for _, k := range keys {
			if _, ok := row[k]; ok {
				return true
			}
		}
	}
	return false
}

func stripObjectKeys(body []byte, sec Section, keys ...string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("%s: %w", sec, err)
	}
	for _, k := range keys {
		delete(obj, k)
	}
	return encodeSection(obj)
}

func stripCandidateKeys(body []byte, set *candidate.Set, roleTiers map[string]string) ([]byte, []string, error) {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", Candidates, err)
	}

	var notes []string
	for _, row := range rows {
		tier := rawString(row["tier"])
		rolesOf := rawStrings(row["roles"])
		delete(row, "roles")
		delete(row, "tier")

		if tier == "" || !tierDropped(tier, rolesOf, roleTiers) {
			continue
		}
		notes = append(notes, fmt.Sprintf("candidate %s: tier %s dropped; set it on the actor", candidateLabel(row, set), tier))
	}

	encoded, err := encodeSection(rows)
	if err != nil {
		return nil, nil, err
	}
	return encoded, notes, nil
}

func tierDropped(tier string, rolesOf []string, roleTiers map[string]string) bool {
	if len(rolesOf) == 0 {
		return true
	}
	for _, r := range rolesOf {
		if roleTiers[r] == tier {
			return false
		}
	}
	return true
}

func candidateLabel(row map[string]json.RawMessage, set *candidate.Set) string {
	if name := rawString(row["name"]); name != "" {
		return name
	}
	ref := strings.Join([]string{
		rawString(row["harness"]),
		rawString(row["provider"]),
		rawString(row["model"]),
	}, "/")
	if set != nil {
		if c, err := set.Resolve(ref); err == nil {
			return c.Name
		}
	}
	if ref == "//" {
		return "?"
	}
	return ref
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func rawStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func encodeSection(v any) ([]byte, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
