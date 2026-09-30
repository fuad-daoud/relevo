// Package config is the one home of relevo's configuration and secrets: they
// live in the machine database and are imported once from the files under
// ~/.config/relevo.
package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Section names one config_doc row. Hooks, Agents, Actors and Accounts have no
// file: they are stored as a JSON body.
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
)

// Sections is the order an import checks and stores the sections, and the
// order EncodeDoc and DiffDocs render them.
var Sections = []Section{Candidates, Agents, Actors, Accounts, Policy, Roles, Prices, Servers, Hooks}

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

// Files returns the file names a config dir may hold and ImportFiles consumes;
// the hooks directory is not in the list.
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

// Loaded is one consistent read of every section and both secrets. An absent
// section reproduces the corresponding missing file: an empty non-nil
// collection for candidates and servers, the zero values otherwise.
type Loaded struct {
	Candidates *candidate.Set
	Policy     policy.Policy
	RolesFile  *roles.File
	Registry   *roles.Registry
	Prices     usage.Prices
	Servers    remote.Servers
	Hooks      HooksMap
	ClientKey  []byte
	Typesafe   string
	Warnings   []string
	Version    int64
	Agents     map[string]roles.AgentEntry
	Actors     map[string]roles.Actor
	Accounts   account.Set
}

// Store reads and writes the config sections and secrets of one database.
type Store struct {
	db      *db.DB
	source  string
	message string
	now     func() time.Time
}

func Open(d *db.DB) *Store { return &Store{db: d, now: time.Now} }

// Load reads every section and both secrets. A stored body that does not parse
// is an error: it cannot happen after a validated Put.
func (s *Store) Load() (Loaded, error) {
	doc, err := s.currentDoc()
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

// loadActors parses the actors section and, when present, rebuilds the roles
// file from it: actors win and the pre-actors keys stop being read.
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

// loadAccounts parses the accounts section, warning about a group no candidate
// of that harness uses. An absent section is an empty pool.
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

func loadHooks(doc Doc, L *Loaded) error {
	body, ok := doc[Hooks]
	if !ok {
		L.Hooks = HooksMap{}
		return nil
	}
	if err := json.Unmarshal(body, &L.Hooks); err != nil {
		return fmt.Errorf("%s: %w", Hooks, err)
	}
	if L.Hooks == nil {
		L.Hooks = HooksMap{}
	}
	return nil
}

func (s *Store) loadSecrets(L *Loaded) error {
	key, ok, err := s.db.SecretGet(SecretClientKey)
	if err != nil {
		return err
	}
	if ok {
		L.ClientKey = key
	}
	ts, ok, err := s.db.SecretGet(SecretTypesafe)
	if err != nil {
		return err
	}
	if ok {
		L.Typesafe = string(ts)
	}
	return nil
}

func (s *Store) Body(sec Section) ([]byte, bool, error) {
	return s.db.ConfigGet(string(sec))
}

// Has reports whether the section has a stored body.
func (s *Store) Has(sec Section) (bool, error) {
	_, ok, err := s.Body(sec)
	return ok, err
}

func (s *Store) Secret(name string) ([]byte, bool, error) {
	return s.db.SecretGet(name)
}

func (s *Store) Version() (int64, error) { return s.db.ConfigVersion() }

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
		var h HooksMap
		if err := json.Unmarshal(body, &h); err != nil {
			return nil, fmt.Errorf("%s: %w", Hooks, err)
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown config section %q", sec)
	}
}

// Put validates body and stores it as sec in one transaction; a refused body
// writes nothing. A write the next Load would refuse is refused here.
func (s *Store) Put(sec Section, body []byte) ([]string, error) {
	if sec == Candidates {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, err
		}
		body = filled
	}
	warnings, err := Validate(sec, body)
	if err != nil {
		return nil, err
	}
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		prospective := copyDoc(before.doc)
		prospective[sec] = body
		if err := validateProspective(prospective); err != nil {
			return err
		}
		if err := t.ConfigPut(string(sec), body, s.now().UTC()); err != nil {
			return err
		}
		return s.record(t, before, nil)
	}); err != nil {
		return nil, err
	}
	return warnings, nil
}

// Delete removes sec's stored body, if any, in one transaction. The version
// bumps even when sec was absent: it is a change counter, not a section count.
// A removal the next Load would refuse is refused here.
func (s *Store) Delete(sec Section) error {
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := validateProspective(docWithout(before.doc, sec)); err != nil {
			return err
		}
		if err := t.ConfigDelete(string(sec)); err != nil {
			return err
		}
		return s.record(t, before, nil)
	})
}

// PutDoc stores every section in doc in one transaction, validating every body
// first; an unknown section or the first invalid body aborts with nothing
// written. Sections not named in doc are untouched. A write the next Load
// would refuse is refused here.
func (s *Store) PutDoc(doc map[Section]json.RawMessage) ([]string, error) {
	if err := checkDocSections(doc); err != nil {
		return nil, err
	}

	if body, ok := doc[Candidates]; ok {
		filled, _, err := fillCandidateNames(body)
		if err != nil {
			return nil, err
		}
		doc[Candidates] = filled
	}

	var warnings []string
	for _, sec := range Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		w, err := Validate(sec, body)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, w...)
	}

	now := s.now().UTC()
	if err := s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		prospective := copyDoc(before.doc)
		for _, sec := range Sections {
			body, ok := doc[sec]
			if !ok {
				continue
			}
			prospective[sec] = body
		}
		if err := validateProspective(prospective); err != nil {
			return err
		}
		for _, sec := range Sections {
			body, ok := doc[sec]
			if !ok {
				continue
			}
			if err := t.ConfigPut(string(sec), body, now); err != nil {
				return err
			}
		}
		return s.record(t, before, nil)
	}); err != nil {
		return nil, err
	}
	return warnings, nil
}

func checkDocSections(doc map[Section]json.RawMessage) error {
	var unknown []string
	for sec := range doc {
		if !known(sec) {
			unknown = append(unknown, string(sec))
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unknown config section %q", unknown[0])
}

func known(sec Section) bool {
	for _, s := range Sections {
		if s == sec {
			return true
		}
	}
	return false
}

// PutSecret stores value under name, validating the client key with
// remote.ParsePrivate first so a malformed PEM never reaches the database. A
// client key is re-marshalled before it is stored, so whichever accepted label
// the caller handed in -- the current one or the pre-rename one -- exactly one
// label, the current one, survives a re-store.
func (s *Store) PutSecret(name string, value []byte) error {
	if name == SecretClientKey {
		kp, err := remote.ParsePrivate(value)
		if err != nil {
			return err
		}
		canonical, err := remote.MarshalPrivate(kp)
		if err != nil {
			return err
		}
		value = canonical
	}
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if err := t.SecretPut(name, value, s.now().UTC()); err != nil {
			return err
		}
		return s.record(t, before, []Change{{Path: "secret." + name, Op: "set"}})
	})
}

// SecretDelete removes name's stored value, if any; an unstored name writes
// nothing.
func (s *Store) SecretDelete(name string) error {
	return s.db.Tx(func(t *db.Tx) error {
		before, err := readSnapshot(t)
		if err != nil {
			return err
		}
		if _, ok, err := t.SecretGet(name); err != nil {
			return err
		} else if !ok {
			return nil
		}
		if err := t.SecretDelete(name); err != nil {
			return err
		}
		return s.record(t, before, []Change{{Path: "secret." + name, Op: "remove"}})
	})
}

// SecretNames returns every stored secret's name, sorted, never a value.
func (s *Store) SecretNames() ([]string, error) { return s.db.SecretNames() }
