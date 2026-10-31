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

// The `config repo` verbs: a thin CLI over the repos config section, one
// repository's settings keyed by the repository's identity. The verbs read and
// write through the existing config machinery -- the same store, the same
// revision log, the same shared config doc -- so a `config repo set` and a
// `config set repos.<id>.gate` are the same write seen two ways.

// repoEntryKeys is the key vocabulary one entry takes. Anything else is a
// usage refusal naming these four.
var repoEntryKeys = []string{"name", "gate", "board_theme", "trimpath"}

// repoKeyKnown reports whether key names a field a repos entry carries.
func repoKeyKnown(key string) bool {
	for _, k := range repoEntryKeys {
		if k == key {
			return true
		}
	}
	return false
}

// configRepoUsage is the whole `config repo` surface. The trimpath paragraph
// is the one longer bit of prose the surface carries, because the flag's cost
// is not visible from the word "on" alone: paths a test may compare
// disappear with it.
const configRepoUsage = `usage: relevo config repo [--repo <path>] [--json]
       relevo config repo set <name|gate|board_theme|trimpath> <value> [--repo <path>] [--json]
       relevo config repo unset <name|gate|board_theme|trimpath> [--repo <path>] [--json]
       relevo config repo list [--json]

trimpath on|off (unset means on): under -trimpath, file paths in panics and
runtime.Caller become module-relative, so tests that compare absolute source
paths can break; set trimpath off opts this repository out, and unset
restores the default.`

// cmdConfigRepo dispatches the `config repo` verbs. A bare invocation, or one
// whose first argument is a flag, shows the working directory's own settings.
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

// configRepoFlagValues holds the pointers the show/set/unset verbs share: the
// repository path the settings are read for, and the --json document switch.
type configRepoFlagValues struct {
	repo   *string
	asJSON *bool
}

// configRepoShowFlagSet defines show's flags on fs and returns what they parse
// into, so the registry's parity test finds exactly one installer per verb.
func configRepoShowFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoSetFlagSet defines set's flags on fs and returns what they parse
// into.
func configRepoSetFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoUnsetFlagSet defines unset's flags on fs and returns what they
// parse into.
func configRepoUnsetFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	return newConfigRepoFlagValues(fs)
}

// configRepoListFlagSet defines list's flags on fs and returns what they parse
// into. List takes no --repo: it lists every repository the section carries.
func configRepoListFlagSet(fs *flag.FlagSet) *configRepoFlagValues {
	v := &configRepoFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the stored entries as a JSON document")
	return v
}

// newConfigRepoFlagValues declares --repo and --json on fs.
func newConfigRepoFlagValues(fs *flag.FlagSet) *configRepoFlagValues {
	v := &configRepoFlagValues{}
	v.repo = fs.String("repo", "", "read the settings of this repository path instead of the working directory")
	v.asJSON = fs.Bool("json", false, "print the settings as a JSON document")
	return v
}

// ConfigRepoDoc is `config repo --json`: the repository's identity and, when
// the section carries one for it, the stored entry's fields. An absent field is
// absent from the document, exactly as it is from the entry.
type ConfigRepoDoc struct {
	Repo       string `json:"repo"`
	Name       string `json:"name,omitempty"`
	Gate       string `json:"gate,omitempty"`
	BoardTheme string `json:"board_theme,omitempty"`
	Trimpath   string `json:"trimpath,omitempty"`
}

// ConfigRepoListDoc is `config repo list --json`: one row per repository the
// section carries, in identity order. Repos is never nil, so an empty answer
// marshals [] rather than null.
type ConfigRepoListDoc struct {
	Repos []ConfigRepoDoc `json:"repos"`
}

// configRepoShow prints the working directory's -- or --repo's -- own settings.
// A directory that is not a repository is a usage refusal: there is no
// identity to read settings for.
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
	doc := ConfigRepoDoc{Repo: key, Name: entry.Name, Gate: entry.Gate, BoardTheme: entry.BoardTheme, Trimpath: entry.Trimpath}
	if *v.asJSON {
		return printDoc(doc)
	}
	fmt.Printf("repo %s\n", key)
	for _, line := range configRepoLines(entry) {
		fmt.Println(line)
	}
	return nil
}

// configRepoSet stores one field of the working directory's -- or --repo's --
// own entry. A field set to "" is stored as absent, and an entry left with no
// field at all is not stored at all, which is the section's own encoding.
func configRepoSet(args []string) error {
	fs := flag.NewFlagSet("relevo config repo set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoSetFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fail(codeUsage, "config repo set wants <key> <value>; the keys are name, gate, board_theme and trimpath")
	}
	key, raw := rest[0], rest[1]
	if !repoKeyKnown(key) {
		return fail(codeUsage, "config repo set: unknown key %q; a repos entry takes name, gate, board_theme and trimpath", key)
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
	switch key {
	case "name":
		entry.Name = repoValueString(raw)
	case "gate":
		entry.Gate = repoValueString(raw)
	case "board_theme":
		entry.BoardTheme = repoValueString(raw)
	case "trimpath":
		entry.Trimpath = repoValueString(raw)
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

// clearRepoKey clears one field of a repos entry, reporting whether it was
// set: an unset field is a refusal, and the caller drops an emptied entry.
func clearRepoKey(entry config.RepoEntry, key string) (config.RepoEntry, bool) {
	wasSet := false
	switch key {
	case "name":
		wasSet = entry.Name != ""
		entry.Name = ""
	case "gate":
		wasSet = entry.Gate != ""
		entry.Gate = ""
	case "board_theme":
		wasSet = entry.BoardTheme != ""
		entry.BoardTheme = ""
	case "trimpath":
		wasSet = entry.Trimpath != ""
		entry.Trimpath = ""
	}
	return entry, wasSet
}

// configRepoUnset clears one field of the working directory's -- or --repo's --
// own entry. A field that is not set is a refusal, the same answer
// `config unset` gives; an entry the clear leaves empty is dropped.
func configRepoUnset(args []string) error {
	fs := flag.NewFlagSet("relevo config repo unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configRepoUnsetFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config repo unset wants <key>; the keys are name, gate, board_theme and trimpath")
	}
	key := rest[0]
	if !repoKeyKnown(key) {
		return fail(codeUsage, "config repo unset: unknown key %q; a repos entry takes name, gate, board_theme and trimpath", key)
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
	entry, ok := entries[id]
	if !ok {
		return fail(codeConfigInvalid, "repos.%s.%s: not set", id, key)
	}
	entry, wasSet := clearRepoKey(entry, key)
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

// configRepoList prints one row per repository the section carries, in
// identity order.
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
		rows = append(rows, ConfigRepoDoc{Repo: id, Name: entry.Name, Gate: entry.Gate, BoardTheme: entry.BoardTheme, Trimpath: entry.Trimpath})
	}
	if *v.asJSON {
		return printDoc(ConfigRepoListDoc{Repos: rows})
	}
	if len(rows) == 0 {
		fmt.Println("no repositories have settings")
		return nil
	}
	for _, row := range rows {
		entry := config.RepoEntry{Name: row.Name, Gate: row.Gate, BoardTheme: row.BoardTheme, Trimpath: row.Trimpath}
		fmt.Printf("%s  %s\n", row.Repo, strings.Join(configRepoLines(entry), "  "))
	}
	return nil
}

// configRepoLines renders one entry's set fields, one "key value" word pair
// per line, in the entry's own field order. The trimpath line always renders
// through its effective value, so an entry that stores nothing for it still
// shows what the repository's Go trees build with.
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
	return lines
}

// repoDirOf is the directory the settings are read for: --repo's path, or the
// working directory.
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

// repoKeyOf is the identity dir's settings are keyed on, or ok=false when git
// cannot identify the tree.
func repoKeyOf(ctx context.Context, rt relevo.Runtime, dir string) (string, bool, error) {
	return relevo.RepoKey(ctx, rt.Git, dir)
}

// errNotARepository marks a directory git cannot give an identity for. The
// `config repo` verbs refuse it as usage, while the gate paths read it as "no
// settings" -- the two answers the seed names for the same fact.
var errNotARepository = fmt.Errorf("not a git repository (a repository with no commits has no identity either)")

// storedRepos reads the whole stored repos section, parsed.
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

// reposEntry is one identity's stored entry, and whether the section carries
// one for it.
func reposEntry(rt relevo.Runtime, key string) (config.RepoEntry, bool, error) {
	entries, err := storedRepos(rt)
	if err != nil {
		return config.RepoEntry{}, false, err
	}
	entry, ok := entries[key]
	return entry, ok, nil
}

// repoValueString is the string a `config repo set` value stores: a JSON
// string is decoded, and anything else is the literal text as typed, so
// `config repo set gate "make check"` and `config repo set gate make check`
// store the same command.
func repoValueString(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return s
	}
	return raw
}
