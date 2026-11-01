package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/tmpdir"
)

type configGetFlagValues struct {
	asJSON *bool
}

// configGetFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configGetFlagSet(fs *flag.FlagSet) *configGetFlagValues {
	v := &configGetFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the value at the path")
	return v
}

// configGet prints the JSON value at a path; a missing section or key is
// config_path_not_set.
func configGet(args []string) error {
	fs := flag.NewFlagSet("relevo config get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configGetFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config get wants one path: <section>[.<key>...]")
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fail(codeConfigPathNotSet, "%s: not set", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	body, present, err := rt.Config.Body(sec)
	if err != nil {
		return fail(codeInternal, "%v", err)
	}
	if !present {
		return fail(codeConfigPathNotSet, "%s: not set", path)
	}
	value, ok := jsonAt(body, keys)
	if !ok {
		return fail(codeConfigPathNotSet, "%s: not set", path)
	}
	return printJSON(os.Stdout, value)
}

type configSetFlagValues struct {
	asJSON *bool
	force  *bool
	local  *bool
}

// configSetFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configSetFlagSet(fs *flag.FlagSet) *configSetFlagValues {
	v := &configSetFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	v.force = fs.Bool("force", false, "replace candidates even when an actor still names a dropped one")
	v.local = fs.Bool("local", false, "store the value on this machine only, never in the shared config")
	return v
}

func configSet(args []string) error {
	return outcomeError(configSetRun(args))
}

func configSetRun(args []string) error {
	fs := flag.NewFlagSet("relevo config set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configSetFlagSet(fs)
	asJSON, force := v.asJSON, v.force
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fail(codeUsage, "config set wants <section>[.<key>...] <json>")
	}
	path, raw := rest[0], rest[1]

	sec, keys, ok := configPath(path)
	if !ok {
		return fail(codeConfigInvalid, "%s: unknown section", path)
	}
	value := rawJSONOrString(raw)

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		if err := guardUnknownFields(rt, sec, nil, value); err != nil {
			return err
		}
		if err := guardCandidatesDrop(rt, sec, value, *force); err != nil {
			return err
		}
		if err := writeConfigSection(rt.Config.As("cli", "config set "+path), sec, value, *v.local); err != nil {
			return err
		}
	} else {
		body := []byte("{}")
		if stored, present, err := rt.Config.Body(sec); err != nil {
			return err
		} else if present {
			body = stored
		}
		updated, err := setJSON(body, keys, value)
		if err != nil {
			return fail(codeConfigInvalid, "%s: %v", path, err)
		}
		if err := guardUnknownFields(rt, sec, body, updated); err != nil {
			return err
		}
		if err := guardCandidatesDrop(rt, sec, updated, *force); err != nil {
			return err
		}
		if err := writeConfigSection(rt.Config.As("cli", "config set "+path), sec, updated, *v.local); err != nil {
			return err
		}
	}

	version, err := rt.Config.Version()
	if err != nil {
		return err
	}
	if *asJSON {
		return printDoc(configWriteDocOf(path, string(sec), version))
	}
	return nil
}

// configUnset removes a key, or the whole section with no key; a missing key
// is config_invalid.
type configUnsetFlagValues struct {
	asJSON *bool
	force  *bool
	local  *bool
}

func configUnsetFlagSet(fs *flag.FlagSet) *configUnsetFlagValues {
	v := &configUnsetFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the write produced")
	v.force = fs.Bool("force", false, "drop the candidates section even when an actor still names one of them")
	v.local = fs.Bool("local", false, "remove a value on this machine only, never in the shared config")
	return v
}

func configUnset(args []string) error {
	return outcomeError(configUnsetRun(args))
}

func configUnsetRun(args []string) error {
	fs := flag.NewFlagSet("relevo config unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configUnsetFlagSet(fs)
	asJSON, force, local := v.asJSON, v.force, v.local
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config unset wants <section>[.<key>...]")
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fail(codeConfigInvalid, "%s: unknown section", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		// Dropping the whole section drops every actor reference, so it takes
		// the same guard as a replace; a key-level unset runs through the
		// array, which is not an object.
		if err := guardCandidatesDrop(rt, sec, nil, *force); err != nil {
			return err
		}
		if *local {
			if err := rt.Config.PutLocal(config.Doc{sec: json.RawMessage("null")}); err != nil {
				return err
			}
		} else if err := rt.Config.As("cli", "config unset "+path).Delete(sec); err != nil {
			return err
		}
	} else {
		body, present, err := rt.Config.Body(sec)
		if err != nil {
			return err
		}
		if !present {
			return fail(codeConfigInvalid, "%s: not set", path)
		}
		updated, removed, err := unsetJSON(body, keys)
		if err != nil {
			return fail(codeConfigInvalid, "%s: %v", path, err)
		}
		if !removed {
			return fail(codeConfigInvalid, "%s: not set", path)
		}
		if err := writeConfigSection(rt.Config.As("cli", "config unset "+path), sec, updated, *local); err != nil {
			return err
		}
	}

	version, err := rt.Config.Version()
	if err != nil {
		return err
	}
	if *asJSON {
		return printDoc(configWriteDocOf(path, string(sec), version))
	}
	return nil
}

// configEdit opens the whole-config document in $VISUAL, else $EDITOR, else
// vi. An invalid document reopens the editor so the user can fix it.
type configEditFlagValues struct {
	local *bool
}

func configEditFlagSet(fs *flag.FlagSet) *configEditFlagValues {
	v := &configEditFlagValues{}
	v.local = fs.Bool("local", false, "store the edits on this machine only, never in the shared config")
	return v
}

func configEdit(args []string) error {
	fs := flag.NewFlagSet("relevo config edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configEditFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config edit [--local]")
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	doc, err := exportDoc(rt)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		doc = []byte("{}\n")
	}

	root := ""
	if rt.Store != nil {
		root = rt.Store.Root()
	}
	tmp, err := tmpdir.CreateTemp(root, "relevo-config-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	for {
		if err := os.WriteFile(tmpPath, doc, 0o600); err != nil {
			return err
		}

		code, err := runEditor(tmpPath)
		if err != nil {
			return err
		}
		if code != 0 {
			fmt.Fprintf(os.Stderr, "relevo config edit: editor exited %d; nothing changed\n", code)
			return exitCodeErr{code: 1}
		}

		edited, err := os.ReadFile(tmpPath)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(edited)) == "" {
			fmt.Println("aborted; nothing changed")
			return nil
		}
		if bytes.Equal(edited, doc) {
			fmt.Println("no changes")
			return nil
		}

		parsed, perr := decodeDoc(edited)
		if perr == nil {
			perr = validateDoc(parsed)
		}
		if perr != nil {
			fmt.Fprintf(os.Stderr, "relevo config edit: %v\n", perr)
			doc = edited
			continue
		}

		warnings, err := putEditedDoc(rt.Config.As("cli", "config edit"), parsed, *v.local)
		if err != nil {
			return err
		}
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "relevo: "+w)
		}
		v, err := rt.Config.Version()
		if err != nil {
			return err
		}
		var rev int64
		if rows, err := rt.Config.Log(1); err == nil && len(rows) > 0 {
			rev = rows[0].Rev
		}
		fmt.Printf("saved (config version %d, revision #%d)\n", v, rev)
		return nil
	}
}

// configWriter is the part of a config store a verb writes through, so the
// flag routing is testable without a database.
type configWriter interface {
	Put(config.Section, []byte) ([]string, error)
	PutDoc(config.Doc) ([]string, error)
	PutLocal(config.Doc) error
}

// writeConfigSection stores one section body; --local changes where the write
// lands, not what the caller edited.
func writeConfigSection(w configWriter, sec config.Section, body []byte, local bool) error {
	if local {
		return w.PutLocal(config.Doc{sec: json.RawMessage(body)})
	}
	_, err := w.Put(sec, body)
	return err
}

// putEditedDoc stores the edited document; --local changes where the changes
// land, not what the user saw.
func putEditedDoc(w configWriter, parsed map[config.Section]json.RawMessage, local bool) ([]string, error) {
	if local {
		return nil, w.PutLocal(parsed)
	}
	return w.PutDoc(parsed)
}

// runEditor runs $VISUAL, else $EDITOR, else vi (split on spaces) with path
// appended.
func runEditor(path string) (int, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	argv := append(strings.Fields(editor), path)

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, err
	}
	return 0, nil
}

// configPath splits a path into its section and key path; ok is false for an
// empty path or an unknown section.
func configPath(path string) (config.Section, []string, bool) {
	if path == "" {
		return "", nil, false
	}
	parts := strings.Split(path, ".")
	sec := config.Section(parts[0])
	if !configSectionKnown(sec) {
		return "", nil, false
	}
	return sec, parts[1:], true
}

func configSectionKnown(sec config.Section) bool {
	for _, s := range config.Sections {
		if s == sec {
			return true
		}
	}
	return false
}

// guardCandidatesDrop refuses a write that newly breaks an actor's candidate
// reference. The refusal names the first drop only, so the message stays one
// line; --force overrules it for scripts.
func guardCandidatesDrop(rt relevo.Runtime, sec config.Section, body []byte, force bool) error {
	if sec != config.Candidates || force {
		return nil
	}
	oldDoc, err := storedCandidatesDoc(rt)
	if err != nil {
		return err
	}
	newDoc := config.Doc{}
	for name, stored := range oldDoc {
		newDoc[name] = stored
	}
	if body == nil {
		delete(newDoc, config.Candidates)
	} else {
		newDoc[config.Candidates] = body
	}
	drops := config.DroppedActorCandidates(oldDoc, newDoc)
	if len(drops) == 0 {
		return nil
	}
	return fail(codeConflict, "%s; pass --force to drop it", drops[0])
}

// storedCandidatesDoc reads the stored candidates and actors bodies; an absent
// section is left out.
func storedCandidatesDoc(rt relevo.Runtime) (config.Doc, error) {
	doc := config.Doc{}
	for _, sec := range []config.Section{config.Candidates, config.Actors} {
		body, present, err := rt.Config.Body(sec)
		if err != nil {
			return nil, err
		}
		if present {
			doc[sec] = body
		}
	}
	return doc, nil
}

// rawJSONOrString is the value `config set` stores: valid JSON is kept,
// anything else becomes a JSON string.
func rawJSONOrString(raw string) json.RawMessage {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	encoded, _ := json.Marshal(raw)
	return encoded
}

func printJSON(w io.Writer, raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := w.Write(out.Bytes())
	return err
}
