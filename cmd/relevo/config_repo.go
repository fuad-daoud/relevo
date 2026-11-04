package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// The `config repo` verbs are a thin CLI over the repos section: a
// `config repo set` and a `config set repos.<id>.gate` are the same write seen
// two ways.

// repoEntryKeys is the key vocabulary one entry takes; anything else is a
// usage refusal.
var repoEntryKeys = []string{"name", "gate", "board_theme", "trimpath", "builder_actor", "reviewer_actor", "planner_actor", "cleanup", "cleanup_after"}

func repoKeyKnown(key string) bool {
	for _, k := range repoEntryKeys {
		if k == key {
			return true
		}
	}
	return false
}

// repoEntryField points at the entry field one CLI key names, so the set and
// unset verbs share the key vocabulary instead of each switching on it. The
// caller checks the key with repoKeyKnown first, so the result is never nil.
func repoEntryField(entry *config.RepoEntry, key string) *string {
	switch key {
	case "name":
		return &entry.Name
	case "gate":
		return &entry.Gate
	case "board_theme":
		return &entry.BoardTheme
	case "trimpath":
		return &entry.Trimpath
	case "builder_actor":
		return &entry.BuilderActor
	case "reviewer_actor":
		return &entry.ReviewerActor
	case "planner_actor":
		return &entry.PlannerActor
	case "cleanup":
		return &entry.Cleanup
	case "cleanup_after":
		return &entry.CleanupAfter
	}
	return nil
}

// configRepoUsage is the whole `config repo` surface. The trimpath paragraph
// stays long because the flag's cost is not visible from the word "on" alone.
const configRepoUsage = `usage: relevo config repo [--repo <path>] [--json]
       relevo config repo set <name|gate|board_theme|trimpath|builder_actor|reviewer_actor|planner_actor|cleanup|cleanup_after> <value> [--repo <path>] [--json]
       relevo config repo unset <name|gate|board_theme|trimpath|builder_actor|reviewer_actor|planner_actor|cleanup|cleanup_after> [--repo <path>] [--json]
       relevo config repo list [--json]

trimpath on|off (unset means on): under -trimpath, file paths in panics and
runtime.Caller become module-relative, so tests that compare absolute source
paths can break; set trimpath off opts this repository out, and unset
restores the default.`

// cmdConfigRepo dispatches the `config repo` verbs; a bare invocation shows
// the working directory's own settings.
func cmdConfigRepo(args []string) error {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(os.Stderr, configRepoUsage)
		return nil
	}
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return configRepoShow(args)
	}
	switch args[0] {
	case "set":
		return configRepoSet(args[1:])
	case "unset":
		return configRepoUnset(args[1:])
	case "list":
		return configRepoList(args[1:])
	case "help":
		fmt.Fprintln(os.Stderr, configRepoUsage)
		return nil
	default:
		return fail(codeUsage, "relevo config repo: unknown command %q", args[0])
	}
}

type configRepoFlagValues struct {
	repo   *string
	asJSON *bool
}

// configRepoShowFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configRepoShowFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoSetFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configRepoSetFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoUnsetFlagSet exists so the registry's parity test finds exactly
// one installer per verb.
func configRepoUnsetFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoListFlagSet exists so the registry's parity test finds exactly one
// installer per verb. List takes no --repo.
func configRepoListFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	v := &configRepoFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the stored entries as a JSON document")
	return v
}

func newConfigRepoFlagValues(fs *flag.FlagSet) *configRepoFlagValues {
	v := &configRepoFlagValues{}
	v.repo = fs.String("repo", "", "read the settings of this repository path instead of the working directory")
	v.asJSON = fs.Bool("json", false, "print the settings as a JSON document")
	return v
}

// ConfigRepoDoc is `config repo --json`: the repository's identity and the
// stored entry's fields.
type ConfigRepoDoc struct {
	Repo       string `json:"repo"`
	Name       string `json:"name,omitempty"`
	Gate       string `json:"gate,omitempty"`
	BoardTheme string `json:"board_theme,omitempty"`
	Trimpath   string `json:"trimpath,omitempty"`
	// The chain members' actors, unset meaning the policy default.
	BuilderActor  string `json:"builder_actor,omitempty"`
	ReviewerActor string `json:"reviewer_actor,omitempty"`
	PlannerActor  string `json:"planner_actor,omitempty"`
	// The chain's cleanup command, unset meaning none, and when it runs,
	// unset meaning chain.
	Cleanup      string `json:"cleanup,omitempty"`
	CleanupAfter string `json:"cleanup_after,omitempty"`
}

// ConfigRepoListDoc is `config repo list --json`: one row per repository in
// identity order. Repos is never nil, so an empty answer marshals [] rather
// than null.
type ConfigRepoListDoc struct {
	Repos []ConfigRepoDoc `json:"repos"`
}

// configRepoShow prints the working directory's, or --repo's, own settings. A
// directory that is not a repository is a usage refusal.
func configRepoShow(args []string) error {
	fs := flag.NewFlagSet("relevo config repo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoShowFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "config repo takes no arguments, got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	dir, err := repoDirOf(*v.repo)
	if err != nil {
		return err
	}
	key, ok, err := repoKeyOf(context.Background(), rt, dir)
	if err != nil {
		return writeError(err)
	}
	if !ok {
		return fail(codeUsage, "%s: %v", dir, errNotARepository)
	}
	entry, present, err := reposEntry(rt, key)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "%v", err)
	}
	if !present {
		if *v.asJSON {
			return printDoc(ConfigRepoDoc{Repo: key})
		}
		fmt.Println("no settings for this repository")
		return nil
	}
	doc := ConfigRepoDoc{Repo: key, Name: entry.Name, Gate: entry.Gate, BoardTheme: entry.BoardTheme, Trimpath: entry.Trimpath, BuilderActor: entry.BuilderActor, ReviewerActor: entry.ReviewerActor, PlannerActor: entry.PlannerActor, Cleanup: entry.Cleanup, CleanupAfter: entry.CleanupAfter}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Printf("repo %s\n", key)
	for _, line := range configRepoLines(entry) {
		fmt.Println(line)
	}
	return nil
}

// configRepoSet stores one field of the working directory's, or --repo's, own
// entry. A field set to "" is stored as absent, and an entry left with no
// field at all is not stored, which is the section's own encoding.
func configRepoSet(args []string) error {
	fs := flag.NewFlagSet("relevo config repo set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoSetFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fail(codeUsage, "config repo set wants <key> <value>; the keys are name, gate, board_theme, trimpath, builder_actor, reviewer_actor, planner_actor, cleanup and cleanup_after")
	}
	key, raw := rest[0], rest[1]
	if !repoKeyKnown(key) {
		return fail(codeUsage, "config repo set: unknown key %q; a repos entry takes name, gate, board_theme, trimpath, builder_actor, reviewer_actor, planner_actor, cleanup and cleanup_after", key)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	dir, err := repoDirOf(*v.repo)
	if err != nil {
		return err
	}
	id, ok, err := repoKeyOf(context.Background(), rt, dir)
	if err != nil {
		return writeError(err)
	}
	if !ok {
		return fail(codeUsage, "%s: %v", dir, errNotARepository)
	}

	entries, err := storedRepos(rt)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "%v", err)
	}
	entry := entries[id]
	*repoEntryField(&entry, key) = repoValueString(raw)
	if entry == (config.RepoEntry{}) {
		delete(entries, id)
	} else {
		entries[id] = entry
	}
	updated, err := config.EncodeRepos(entries)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "%v", err)
	}
	if _, err := rt.Config.As("cli", "config repo set "+key).Put(config.Repos, updated); err != nil {
		return outcomeError(err)
	}

	version, err := rt.Config.Version()
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(configWriteDocOf("repos."+id+"."+key, string(config.Repos), version))
	}
	return nil
}

// configRepoUnset clears one field of the working directory's, or --repo's,
// own entry. An unset field is a refusal, the same answer `config unset`
// gives; an entry the clear leaves empty is dropped.
func configRepoUnset(args []string) error {
	fs := flag.NewFlagSet("relevo config repo unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoUnsetFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config repo unset wants <key>; the keys are name, gate, board_theme, trimpath, builder_actor, reviewer_actor, planner_actor, cleanup and cleanup_after")
	}
	key := rest[0]
	if !repoKeyKnown(key) {
		return fail(codeUsage, "config repo unset: unknown key %q; a repos entry takes name, gate, board_theme, trimpath, builder_actor, reviewer_actor, planner_actor, cleanup and cleanup_after", key)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	target, err := lookupConfigRepoUnsetTarget(rt, *v.repo)
	if err != nil {
		return err
	}
	entries, id := target.entries, target.id
	entry, ok := entries[id]
	if !ok {
		return fail(codeConfigInvalid, "repos.%s.%s: not set", id, key)
	}
	field := repoEntryField(&entry, key)
	wasSet := *field != ""
	*field = ""
	if !wasSet {
		return fail(codeConfigInvalid, "repos.%s.%s: not set", id, key)
	}
	if entry == (config.RepoEntry{}) {
		delete(entries, id)
	} else {
		entries[id] = entry
	}
	updated, err := config.EncodeRepos(entries)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "%v", err)
	}
	if _, err := rt.Config.As("cli", "config repo unset "+key).Put(config.Repos, updated); err != nil {
		return outcomeError(err)
	}

	version, err := rt.Config.Version()
	if err != nil {
		return err
	}
	if *v.asJSON {
		return printDoc(configWriteDocOf("repos."+id+"."+key, string(config.Repos), version))
	}
	return nil
}

// configRepoUnsetTarget is the entry one `config repo unset` clears: the
// stored entries and the id they are keyed on.
type configRepoUnsetTarget struct {
	entries map[string]config.RepoEntry
	id      string
}

// lookupConfigRepoUnsetTarget resolves the directory's settings id and loads
// the entries holding it; the clear-and-store step reports the unset itself.
func lookupConfigRepoUnsetTarget(rt relevo.Runtime, repoFlag string) (configRepoUnsetTarget, error) {
	var target configRepoUnsetTarget
	dir, err := repoDirOf(repoFlag)
	if err != nil {
		return target, err
	}
	id, ok, err := repoKeyOf(context.Background(), rt, dir)
	if err != nil {
		return target, writeError(err)
	}
	if !ok {
		return target, fail(codeUsage, "%s: %v", dir, errNotARepository)
	}
	entries, err := storedRepos(rt)
	if err != nil {
		return target, failWrap(codeConfigInvalid, err, "%v", err)
	}
	target.entries, target.id = entries, id
	return target, nil
}

func configRepoList(args []string) error {
	fs := flag.NewFlagSet("relevo config repo list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoListFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "config repo list takes no arguments, got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	entries, err := storedRepos(rt)
	if err != nil {
		return failWrap(codeConfigInvalid, err, "%v", err)
	}
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	rows := make([]ConfigRepoDoc, 0, len(ids))
	for _, id := range ids {
		entry := entries[id]
		rows = append(rows, ConfigRepoDoc{Repo: id, Name: entry.Name, Gate: entry.Gate, BoardTheme: entry.BoardTheme, Trimpath: entry.Trimpath, BuilderActor: entry.BuilderActor, ReviewerActor: entry.ReviewerActor, PlannerActor: entry.PlannerActor, Cleanup: entry.Cleanup, CleanupAfter: entry.CleanupAfter})
	}
	if *v.asJSON {
		return printDoc(ConfigRepoListDoc{Repos: rows})
	}
	if len(rows) == 0 {
		fmt.Println("no repositories have settings")
		return nil
	}
	for _, row := range rows {
		entry := config.RepoEntry{Name: row.Name, Gate: row.Gate, BoardTheme: row.BoardTheme, Trimpath: row.Trimpath, BuilderActor: row.BuilderActor, ReviewerActor: row.ReviewerActor, PlannerActor: row.PlannerActor, Cleanup: row.Cleanup, CleanupAfter: row.CleanupAfter}
		fmt.Printf("%s  %s\n", row.Repo, strings.Join(configRepoLines(entry), "  "))
	}
	return nil
}

// configRepoLines renders one entry's set fields. The trimpath line renders
// through its effective value, so an entry storing nothing still shows what
// the Go trees build with.
func configRepoLines(entry config.RepoEntry) []string {
	var lines []string
	if entry.Name != "" {
		lines = append(lines, "name "+entry.Name)
	}
	if entry.Gate != "" {
		lines = append(lines, "gate "+entry.Gate)
	}
	if entry.BoardTheme != "" {
		lines = append(lines, "board_theme "+entry.BoardTheme)
	}
	value := entry.Trimpath
	if value == "" {
		value = "on (default)"
	}
	lines = append(lines, "trimpath "+value)
	if entry.BuilderActor != "" {
		lines = append(lines, "builder_actor "+entry.BuilderActor)
	}
	if entry.ReviewerActor != "" {
		lines = append(lines, "reviewer_actor "+entry.ReviewerActor)
	}
	if entry.PlannerActor != "" {
		lines = append(lines, "planner_actor "+entry.PlannerActor)
	}
	if entry.Cleanup != "" {
		lines = append(lines, "cleanup "+entry.Cleanup)
	}
	if entry.CleanupAfter != "" {
		lines = append(lines, "cleanup_after "+entry.CleanupAfter)
	}
	return lines
}

func repoDirOf(repo string) (string, error) {
	if repo != "" {
		return repo, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return dir, nil
}

// repoKeyOf is the identity dir's settings are keyed on; ok is false when git
// cannot identify the tree.
func repoKeyOf(ctx context.Context, rt relevo.Runtime, dir string) (string, bool, error) {
	return relevo.RepoKey(ctx, rt.Git, dir)
}

// errNotARepository marks a directory git cannot give an identity for. The
// `config repo` verbs refuse it as usage, while the gate paths read it as "no
// settings".
var errNotARepository = fmt.Errorf("not a git repository (a repository with no commits has no identity either)")

func storedRepos(rt relevo.Runtime) (map[string]config.RepoEntry, error) {
	body, present, err := rt.Config.Body(config.Repos)
	if err != nil {
		return nil, err
	}
	if !present {
		return map[string]config.RepoEntry{}, nil
	}
	return config.ParseRepos(body)
}

func reposEntry(rt relevo.Runtime, key string) (config.RepoEntry, bool, error) {
	entries, err := storedRepos(rt)
	if err != nil {
		return config.RepoEntry{}, false, err
	}
	entry, ok := entries[key]
	return entry, ok, nil
}

// repoValueString is the string a `config repo set` value stores: a JSON
// string is decoded, and anything else is the literal text as typed.
func repoValueString(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return raw
}
