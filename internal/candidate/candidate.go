// Package candidate loads the harness/provider/model triples relevo may start,
// and nothing else: which one to start is the caller's choice.
package candidate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

var ErrBadRef = errors.New("bad candidate reference")

var ErrUnknownCandidate = errors.New("unknown candidate")

type Ref struct {
	Harness  string
	Provider string
	Model    string
}

func ParseRef(s string) (Ref, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Ref{}, fmt.Errorf("%q: want harness/provider/model: %w", s, ErrBadRef)
	}
	return Ref{
		Harness:  parts[0],
		Provider: parts[1],
		Model:    parts[2],
	}, nil
}

func (r Ref) String() string {
	return r.Harness + "/" + r.Provider + "/" + r.Model
}

type Candidate struct {
	// Name is unique among candidates, at most 24 characters and never
	// containing "/"; it is optional in stored JSON and derived at parse time
	// when missing.
	Name          string   `json:"name,omitempty"`
	Harness       string   `json:"harness"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Roles         []string `json:"roles,omitempty"`
	Tree          string   `json:"tree,omitempty"`
	ExtraArgs     []string `json:"extra_args,omitempty"`
	LimitPatterns []string `json:"limit_patterns,omitempty"`
	// DialogPatterns is no longer used: pane dialogs were deleted. The field
	// stays decodable so existing candidates.json files still load.
	DialogPatterns []string `json:"dialog_patterns,omitempty"`
	// Tier is the candidate's default permission tier; "" means the role default
	// from policy, else harness.
	Tier string `json:"tier,omitempty"`
	// DenialPatterns replace the harness's default denial regexes for this candidate.
	DenialPatterns []string `json:"denial_patterns,omitempty"`

	// Plan marks a subscription lane: the round's cost is a quota draw, and
	// printers say "plan", never "$0" and never "free".
	Plan bool `json:"plan,omitempty"`
}

func (c Candidate) Ref() Ref {
	return Ref{
		Harness:  c.Harness,
		Provider: c.Provider,
		Model:    c.Model,
	}
}

func (c Candidate) Serves(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Set is an immutable collection of validated candidates keyed by canonical ref.
type Set struct {
	byRef  map[string]Candidate
	byName map[string]string
}

func newSet() *Set {
	return &Set{
		byRef:  make(map[string]Candidate),
		byName: make(map[string]string),
	}
}

func (s *Set) Lookup(ref Ref) (Candidate, error) {
	c, ok := s.byRef[ref.String()]
	if !ok {
		return Candidate{}, fmt.Errorf("candidate %q not found (configured: %s): %w", ref.String(), strings.Join(s.Names(), ", "), ErrUnknownCandidate)
	}
	return c, nil
}

// Resolve takes a name or a harness/provider/model token to a candidate.
func (s *Set) Resolve(str string) (Candidate, error) {
	if s == nil {
		return Candidate{}, fmt.Errorf("unknown candidate %q (no candidates configured): %w", str, ErrUnknownCandidate)
	}
	if strings.Contains(str, "/") {
		ref, err := ParseRef(str)
		if err != nil {
			return Candidate{}, err
		}
		return s.Lookup(ref)
	}
	if key, ok := s.byName[str]; ok {
		return s.byRef[key], nil
	}
	return Candidate{}, fmt.Errorf("unknown candidate %q (known: %s): %w", str, strings.Join(s.Names(), ", "), ErrUnknownCandidate)
}

// NameOf returns token unchanged when the set does not hold it, nil included.
func (s *Set) NameOf(token string) string {
	if s == nil {
		return token
	}
	c, ok := s.byRef[token]
	if !ok {
		return token
	}
	return c.Name
}

// NameFor is like NameOf but reports whether the set holds token. A caller uses
// it to leave a name field unset rather than carry a token in it.
func (s *Set) NameFor(token string) (string, bool) {
	if s == nil {
		return "", false
	}
	c, ok := s.byRef[token]
	if !ok {
		return "", false
	}
	return c.Name, true
}

func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, 0, len(s.byName))
	for n := range s.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (s *Set) ForRole(role string) []Candidate {
	var matches []Candidate
	for _, c := range s.byRef {
		if c.Serves(role) {
			matches = append(matches, c)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Ref().String() < matches[j].Ref().String()
	})
	return matches
}

func (s *Set) Refs() []string {
	refs := make([]string, 0, len(s.byRef))
	for k := range s.byRef {
		refs = append(refs, k)
	}
	sort.Strings(refs)
	return refs
}

func (s *Set) Len() int {
	return len(s.byRef)
}

// Providers returns the distinct providers the configured candidates use,
// sorted; a nil *Set returns nil.
func (s *Set) Providers() []string {
	if s == nil {
		return nil
	}

	seen := make(map[string]bool, len(s.byRef))
	var out []string
	for _, c := range s.byRef {
		if seen[c.Provider] {
			continue
		}
		seen[c.Provider] = true
		out = append(out, c.Provider)
	}
	sort.Strings(out)
	return out
}

// Load reads and validates candidate definitions from a JSON file, discarding
// the warnings. A missing file is zero candidates, not an error, because relevo
// ships none; a present file that does not validate fails startup.
func Load(path string) (*Set, error) {
	set, _, err := LoadWithWarnings(path)
	return set, err
}

// LoadWithWarnings returns a warning for every candidate this relevo drops
// because its harness or one of its roles is unknown, so a newer relevo's
// candidate does not stop this one. Only those two are skipped; every other
// validation failure still fails the load.
func LoadWithWarnings(path string) (*Set, []string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return newSet(), nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read candidates %s: %w", path, err)
	}
	return Parse(path, raw)
}

// Parse validates candidate definitions from data, naming them by name in every
// message. Every entry gets a name: an explicit one kept when valid, else the
// deterministic one DeriveNames computes. A skipped entry still takes part in
// DeriveNames, so the other names do not shift when it is fixed.
func Parse(name string, data []byte) (*Set, []string, error) {
	var entries []Candidate
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, nil, fmt.Errorf("decode candidates %s: %w", name, err)
	}

	set := newSet()
	seen := make(map[string]int)
	seenNames := make(map[string]int)
	base := filepath.Base(name)
	var warnings []string
	names := DeriveNames(entries)
	providers := providerNames(entries)

	for i, c := range entries {
		if c.Name != "" && !IsName(c.Name) {
			return nil, nil, fmt.Errorf("candidates %s: candidate %d: name %q: want ^[a-z0-9][a-z0-9.-]{0,23}$", name, i, c.Name)
		}
		c.Name = names[i]

		warn, err := validateCandidate(name, base, i, c)
		if err != nil {
			return nil, nil, err
		}
		if warn != "" {
			warnings = append(warnings, warn)
			continue
		}
		if err := registerCandidate(name, i, c, set, seen, seenNames, providers); err != nil {
			return nil, nil, err
		}
	}

	return set, warnings, nil
}

func providerNames(entries []Candidate) map[string]bool {
	providers := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Provider != "" {
			providers[e.Provider] = true
		}
	}
	return providers
}

// validateCandidate checks one entry before it joins the set. A non-empty
// warning means the entry is skipped, not that the load fails; field checks that
// must fail the load come first, so their errors report ahead of a skip.
func validateCandidate(name, base string, i int, c Candidate) (string, error) {
	if err := checkRequired(name, i, c); err != nil {
		return "", err
	}
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return fmt.Sprintf("%s: %s: unknown harness %q (skipped)", base, c.Ref().String(), c.Harness), nil
	}
	warn, err := checkRoles(name, base, i, c, h)
	if warn != "" || err != nil {
		return warn, err
	}
	return "", checkRest(name, i, c)
}

func checkRequired(name string, i int, c Candidate) error {
	if c.Harness == "" || c.Provider == "" || c.Model == "" {
		return fmt.Errorf("candidates %s: candidate %d: harness, provider and model are required", name, i)
	}
	if strings.Contains(c.Provider, "/") {
		return fmt.Errorf("candidates %s: candidate %d: provider must be a single segment", name, i)
	}
	return nil
}

func checkRoles(name, base string, i int, c Candidate, h harness.Harness) (string, error) {
	for _, r := range c.Roles {
		if _, ok := harness.RoleByName(r); !ok {
			return fmt.Sprintf("%s: %s: unknown role %q (skipped)", base, c.Ref().String(), r), nil
		}
		if !h.CanServe(r) {
			return "", fmt.Errorf("candidates %s: candidate %d: harness %q has no definition for role %q", name, i, c.Harness, r)
		}
	}
	return "", nil
}

// checkRest validates the remaining fields, in the order their errors are reported.
func checkRest(name string, i int, c Candidate) error {
	if c.Tree != "" && c.Tree != "binding" && c.Tree != "none" {
		return fmt.Errorf("candidates %s: candidate %d: tree must be \"binding\" or \"none\"", name, i)
	}
	if err := compilePatterns(name, i, "limit_patterns", c.LimitPatterns); err != nil {
		return err
	}
	if err := compilePatterns(name, i, "dialog_patterns", c.DialogPatterns); err != nil {
		return err
	}
	if c.Tier != "" {
		if _, err := harness.ParseTier(c.Tier); err != nil {
			return fmt.Errorf("candidates %s: candidate %d: tier: %w", name, i, err)
		}
	}
	return compilePatterns(name, i, "denial_patterns", c.DenialPatterns)
}

func compilePatterns(name string, i int, field string, patterns []string) error {
	for j, pat := range patterns {
		if _, err := regexp.Compile(pat); err != nil {
			return fmt.Errorf("candidates %s: candidate %d: %s[%d]: %w", name, i, field, j, err)
		}
	}
	return nil
}

func registerCandidate(name string, i int, c Candidate, set *Set, seen, seenNames map[string]int, providers map[string]bool) error {
	key := c.Ref().String()
	if first, exists := seen[key]; exists {
		return fmt.Errorf("candidates %s: candidate %d: duplicate candidate %s at index %d and %d", name, i, key, first, i)
	}
	seen[key] = i
	if first, exists := seenNames[c.Name]; exists {
		return fmt.Errorf("candidates %s: candidate %d: duplicate name %q at index %d and %d", name, i, c.Name, first, i)
	}
	seenNames[c.Name] = i
	if providers[c.Name] {
		return fmt.Errorf("candidates %s: candidate %d: name %q is also a provider name", name, i, c.Name)
	}
	set.byRef[key] = c
	set.byName[c.Name] = key
	return nil
}
