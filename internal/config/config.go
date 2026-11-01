// Package config owns relevo's configuration and secrets in the machine database.
package config

import (
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/board/theme"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Section names one config_doc row. Hooks, Agents, Actors and Accounts are stored
// as a JSON body.
type Section string

const (
	Candidates Section = "candidates"
	Agents     Section = "agents"
	Actors     Section = "actors"
	Accounts   Section = "accounts"
	Policy     Section = "policy"
	Roles      Section = "roles"
	Prices     Section = "prices"
	Servers    Section = "servers"
	Hooks      Section = "hooks"
	Workflows  Section = "workflows"
	Repos      Section = "repos"
	Themes     Section = "themes"
	Sync       Section = "sync"
)

// Sections is the order an import checks and stores the sections, and the order
// EncodeDoc and DiffDocs render them.
var Sections = []Section{Candidates, Agents, Actors, Accounts, Policy, Roles, Prices, Servers, Hooks, Workflows, Repos, Themes, Sync}

// sectionFile maps a section to the file it is imported from.
var sectionFile = map[Section]string{
	Candidates: "candidates.json",
	Policy:     "policy.json",
	Roles:      "roles.json",
	Prices:     "prices.json",
	Servers:    "servers.json",
}

func FileName(sec Section) string { return sectionFile[sec] }

const (
	SecretClientKey = "client.key"
	SecretTypesafe  = "typesafe"
)

const (
	clientKeyFile   = "client.key"
	typesafeKeyFile = "typesafe.key"
	clientPubFile   = "client.pub"
	aliasesFile     = "aliases.json"
)

// Files returns the file names a config dir may hold and ImportFiles consumes, without
// the hooks directory.
func Files() []string {
	files := make([]string, 0, len(Sections)+2)
	for _, sec := range Sections {
		if name := FileName(sec); name != "" {
			files = append(files, name)
		}
	}
	return append(files, clientKeyFile, typesafeKeyFile)
}

type HooksMap map[string][][]string

// Loaded is one consistent read of every section and both secrets. An absent section
// reads as its missing file would: an empty non-nil collection for candidates and
// servers, the zero values otherwise.
type Loaded struct {
	Candidates *candidate.Set
	Policy     policy.Policy
	RolesFile  *roles.File
	Registry   *roles.Registry
	Prices     usage.Prices
	Servers    remote.Servers
	Hooks      HooksMap
	Workflows  map[string]StoredWorkflow
	// Repos is the per-repository settings section, keyed by the repository's
	// identity (the sha256 of its root commit). An absent section is an empty
	// map: a machine that never used the feature reads no entry.
	Repos map[string]RepoEntry
	// Themes is the board palettes section, keyed by theme name. It is seeded
	// once per database from the release's own data, so a fresh install reads
	// the two built-ins from the section like any other palette. An absent
	// section is an empty map.
	Themes    map[string]theme.Theme
	ClientKey []byte
	Typesafe  string
	Warnings  []string
	Version   int64
	Agents    map[string]roles.AgentEntry
	Actors    map[string]roles.Actor
	Accounts  account.Set
}

// Store reads and writes the config sections and secrets of one database.
type Store struct {
	db      *db.DB
	source  string
	message string
	now     func() time.Time
	// unstamped marks a store whose writes are not a person's edit: an item
	// that came from a peer (the export state already stands for it), the
	// seed, a migration. Such a write records no write stamp.
	unstamped bool
	// files is where the hooks section's embedded scripts are written, and
	// root is the config root every root-relative name is measured against.
	// Both are empty on a store that carries the config but writes no disk.
	files FileApplier
	root  string
	// rerender re-renders an agent whose item arrived from a peer. It is nil on
	// a store that has not been bound to one.
	rerender AgentRerenderer
}

// Open binds a config store to d's machine-local file, or to d itself on a handle
// that carries none: sections and secrets live in the local file, so binding here
// keeps a shared handle from reading its config out of the shared file. The machine
// fields move runs here so the first opener reaches it; a failure is left to the
// caller's own write, since a store that could not write cannot have moved anything.
// The hooks argv move does not run here: with no config root it could only record
// the work without doing it, so WithFiles runs it once a root is bound.
func Open(d *db.DB) *Store {
	s := &Store{db: d.LocalOrSelf(), now: time.Now}
	_, _ = s.EnsureLocalLayer()
	return s
}

// Load reads every section and both secrets. An unparsable stored body is an error:
// it cannot happen after a validated Put.
func (s *Store) Load() (Loaded, error) {
	doc, err := s.mergedDoc()
	if err != nil {
		return Loaded{}, err
	}
	L, err := decodeDoc(doc)
	if err != nil {
		return Loaded{}, err
	}
	if err := s.loadSecrets(&L); err != nil {
		return Loaded{}, err
	}

	v, err := s.db.ConfigVersion()
	if err != nil {
		return Loaded{}, err
	}
	L.Version = v
	return L, nil
}

func loadCandidates(doc Doc, L *Loaded) ([]byte, bool, error) {
	body, ok := doc[Candidates]
	if !ok {
		// An absent section is today's missing file: an empty set. Parsing an
		// empty array is exactly candidate.Load's missing-file result.
		set, _, err := candidate.Parse(FileName(Candidates), []byte("[]"))
		if err != nil {
			return nil, false, err
		}
		L.Candidates = set
		return nil, false, nil
	}
	set, warnings, err := candidate.Parse(FileName(Candidates), body)
	if err != nil {
		return nil, false, err
	}
	L.Candidates = set
	L.Warnings = append(L.Warnings, warnings...)
	return body, true, nil
}

func loadPolicy(doc Doc, L *Loaded) ([]byte, bool, error) {
	body, ok := doc[Policy]
	if !ok {
		return nil, false, nil
	}
	pol, warnings, err := policy.Parse(FileName(Policy), body)
	if err != nil {
		return nil, false, err
	}
	L.Policy = pol
	L.Warnings = append(L.Warnings, warnings...)
	return body, true, nil
}

func loadRoles(doc Doc, L *Loaded) (bool, error) {
	body, ok := doc[Roles]
	if !ok {
		return false, nil
	}
	f, warnings, err := roles.Parse(FileName(Roles), body)
	if err != nil {
		return false, err
	}
	L.RolesFile = f
	L.Warnings = append(L.Warnings, warnings...)
	return true, nil
}

func loadAgents(doc Doc, L *Loaded) (map[string]roles.AgentEntry, error) {
	body, ok := doc[Agents]
	if !ok {
		return nil, nil
	}
	a, warnings, err := roles.ParseAgents(body)
	if err != nil {
		return nil, err
	}
	L.Agents = a
	L.Warnings = append(L.Warnings, warnings...)
	return a, nil
}

// loadActors parses the actors section and, when present, rebuilds the roles file
// from it: actors win and the roles keys stop being read.
func loadActors(doc Doc, L *Loaded, agents map[string]roles.AgentEntry, rolesPresent bool, candBody []byte, candOK bool, polBody []byte, polOK bool) error {
	body, ok := doc[Actors]
	if !ok {
		return nil
	}
	a, warnings, err := roles.ParseActors(body)
	if err != nil {
		return err
	}
	L.Actors = a
	L.Warnings = append(L.Warnings, warnings...)

	rf, warnings, err := roles.FromActors(agents, a)
	if err != nil {
		return err
	}
	L.RolesFile = rf
	L.Warnings = append(L.Warnings, warnings...)
	if rolesPresent {
		L.Warnings = append(L.Warnings, "config: actors is set, so the roles section is ignored")
	}
	L.Warnings = append(L.Warnings, ignoredLegacyWarnings(candBody, candOK, polBody, polOK)...)
	return nil
}

func loadPrices(doc Doc, L *Loaded) error {
	body, ok := doc[Prices]
	if !ok {
		L.Prices = usage.DefaultPrices()
		return nil
	}
	p, err := usage.ParsePrices(body)
	if err != nil {
		return err
	}
	L.Prices = p
	return nil
}

func loadServers(doc Doc, L *Loaded) error {
	body, ok := doc[Servers]
	if !ok {
		L.Servers = remote.Servers{}
		return nil
	}
	srv, err := remote.ParseServers(body)
	if err != nil {
		return err
	}
	L.Servers = srv
	return nil
}

// loadAccounts parses the accounts section, warning about a group no candidate of
// that harness uses. An absent section is an empty pool.
func loadAccounts(doc Doc, L *Loaded) error {
	body, ok := doc[Accounts]
	if !ok {
		return nil
	}
	set, warnings, err := account.Parse(string(Accounts), body, candidateGroups(L.Candidates))
	if err != nil {
		return err
	}
	L.Accounts = set
	L.Warnings = append(L.Warnings, warnings...)
	return nil
}

// candidateGroups maps each harness kind to the quota groups its candidates
// use, which is what tells a stale group in the accounts section apart from a
// live one.
func candidateGroups(set *candidate.Set) map[account.Kind][]string {
	if set == nil {
		return nil
	}
	groups := make(map[account.Kind][]string)
	seen := make(map[account.Kind]map[string]bool)
	for _, token := range set.Refs() {
		ref, err := candidate.ParseRef(token)
		if err != nil {
			continue
		}
		kind := account.Kind(ref.Harness)
		if seen[kind] == nil {
			seen[kind] = make(map[string]bool)
		}
		if seen[kind][ref.Provider] {
			continue
		}
		seen[kind][ref.Provider] = true
		groups[kind] = append(groups[kind], ref.Provider)
	}
	return groups
}

// opencodeGroups names every quota group an opencode account covers, for the
// policy check that refuses round-robin.
func opencodeGroups(set account.Set) []string {
	var out []string
	seen := make(map[string]bool)
	for _, a := range set {
		if a.Harness != account.OpenCode {
			continue
		}
		for _, g := range a.Groups {
			if seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
	}
	return out
}

// loadHooks reads the hooks section into the dispatcher's argv map: shared rows under
// their stored root-relative names, machine rows under their absolute names. Embedded
// scripts are not read here; they are the applier's business at write time.
func loadHooks(doc Doc, L *Loaded) error {
	body, ok := doc[Hooks]
	if !ok {
		L.Hooks = HooksMap{}
		return nil
	}
	hooks, err := ParseHookDoc(body)
	if err != nil {
		return err
	}
	L.Hooks = HooksMapOf(hooks)
	return nil
}

// Validate parses body for sec, returning its warnings; an unknown section is
// an error.
func Validate(sec Section, body []byte) ([]string, error) {
	switch sec {
	case Candidates:
		_, warnings, err := candidate.Parse(FileName(sec), body)
		return warnings, err
	case Policy:
		_, warnings, err := policy.Parse(FileName(sec), body)
		return warnings, err
	case Roles:
		_, warnings, err := roles.Parse(FileName(sec), body)
		return warnings, err
	case Agents:
		_, warnings, err := roles.ParseAgents(body)
		return warnings, err
	case Actors:
		_, warnings, err := roles.ParseActors(body)
		return warnings, err
	case Accounts:
		_, warnings, err := account.Parse(string(Accounts), body, nil)
		return warnings, err
	case Prices:
		_, err := usage.ParsePrices(body)
		return nil, err
	case Servers:
		_, err := remote.ParseServers(body)
		return nil, err
	case Hooks:
		hooks, err := ParseHookDoc(body)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", Hooks, err)
		}
		// The path rules need a config root to be relative to, and a body
		// handed to Validate on its own has none. The caps and the shape are
		// checked here; the per-name refusals happen in ValidateHookDoc, which
		// the store calls with its root attached.
		if err := ValidateHookDoc(hooks); err != nil {
			return nil, fmt.Errorf("%s: %w", Hooks, err)
		}
		return nil, nil
	case Workflows:
		_, err := parseWorkflows(body)
		return nil, err
	case Repos:
		_, err := ParseRepos(body)
		return nil, err
	case Themes:
		_, err := ParseThemes(body)
		return nil, err
	case Sync:
		_, err := sync.ParseSettings(body)
		return nil, err
	default:
		return nil, fmt.Errorf("unknown config section %q", sec)
	}
}

// SecretNames returns every stored secret's name, sorted, never a value.
