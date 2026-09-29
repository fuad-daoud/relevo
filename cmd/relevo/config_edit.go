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
)

// configGet prints the JSON value at a path, indented. A missing section or
// key exits 1 with `relevo: <path>: not set`.
func configGet(args []string) error {
	fs := flag.NewFlagSet("relevo config get", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config get <section>[.<key>...]")
		return exitCodeErr{code: 2}
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: not set", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	body, present, err := rt.Config.Body(sec)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%s: not set", path)
	}
	value, ok := jsonAt(body, keys)
	if !ok {
		return fmt.Errorf("%s: not set", path)
	}
	return printJSON(os.Stdout, value)
}

// configSet sets a JSON value at a path, creating intermediate objects. A
// value that is not valid JSON is stored as a JSON string. A path through a
// non-object exits 1. With no key the whole section is replaced.
func configSet(args []string) error {
	fs := flag.NewFlagSet("relevo config set", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: relevo config set <section>[.<key>...] <json>")
		return exitCodeErr{code: 2}
	}
	path, raw := rest[0], rest[1]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: unknown section", path)
	}
	value := rawJSONOrString(raw)

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if len(keys) == 0 {
		_, err := rt.Config.As("cli", "config set "+path).Put(sec, value)
		return err
	}

	body := []byte("{}")
	if stored, present, err := rt.Config.Body(sec); err != nil {
		return err
	} else if present {
		body = stored
	}
	updated, err := setJSON(body, keys, value)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	_, err = rt.Config.As("cli", "config set "+path).Put(sec, updated)
	return err
}

// configUnset removes a key, or, with no key, deletes the whole section. A
// path through a non-object, or a key that is not set, exits 1.
func configUnset(args []string) error {
	fs := flag.NewFlagSet("relevo config unset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: relevo config unset <section>[.<key>...]")
		return exitCodeErr{code: 2}
	}
	path := rest[0]

	sec, keys, ok := configPath(path)
	if !ok {
		return fmt.Errorf("%s: unknown section", path)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return rt.Config.As("cli", "config unset "+path).Delete(sec)
	}

	body, present, err := rt.Config.Body(sec)
	if err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("%s: not set", path)
	}
	updated, removed, err := unsetJSON(body, keys)
	if err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	if !removed {
		return fmt.Errorf("%s: not set", path)
	}
	_, err = rt.Config.As("cli", "config unset "+path).Put(sec, updated)
	return err
}

// configEdit opens the whole-config document in $VISUAL, else $EDITOR, else
// vi, and stores what the editor leaves (§4.2). A file left empty aborts, an
// unchanged file is a no-op, and an invalid document goes round again with the
// editor so the user can fix it.
func configEdit(args []string) error {
	fs := flag.NewFlagSet("relevo config edit", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelpShown) {
			return err
		}
		return exitCodeErr{code: 2}
	}
	if len(fs.Args()) != 0 {
		fmt.Fprintln(os.Stderr, "usage: relevo config edit")
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

	tmp, err := os.CreateTemp("", "relevo-config-*.json")
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

		warnings, err := rt.Config.As("cli", "config edit").PutDoc(parsed)
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

// runEditor runs $VISUAL, else $EDITOR, else vi (split on spaces) with path
// appended, its standard streams attached. It returns the editor's exit code.
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

// configPath splits <section>[.<key>...] into its section and key path. ok is
// false for an empty path or a section name relevo does not know.
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

// configSectionKnown reports whether sec names a stored section.
func configSectionKnown(sec config.Section) bool {
	for _, s := range config.Sections {
		if s == sec {
			return true
		}
	}
	return false
}

// jsonAt walks keys into a JSON body, returning the raw value there. A key
// missing, or a body that is not an object at some step, is ok false.
func jsonAt(body []byte, keys []string) (json.RawMessage, bool) {
	cur := json.RawMessage(body)
	for _, key := range keys {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(cur, &obj); err != nil {
			return nil, false
		}
		next, ok := obj[key]
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// setJSON returns body with value stored at keys, creating intermediate
// objects. A path that runs through a non-object is an error.
func setJSON(body []byte, keys []string, value json.RawMessage) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, errors.New("not an object")
	}
	if root == nil {
		return nil, errors.New("not an object")
	}
	if err := setAt(root, keys, value); err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func setAt(obj map[string]json.RawMessage, keys []string, value json.RawMessage) error {
	key := keys[0]
	if len(keys) == 1 {
		obj[key] = value
		return nil
	}

	child := map[string]json.RawMessage{}
	if existing, ok := obj[key]; ok {
		if err := json.Unmarshal(existing, &child); err != nil || child == nil {
			return errors.New("not an object")
		}
	}
	if err := setAt(child, keys[1:], value); err != nil {
		return err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return err
	}
	obj[key] = encoded
	return nil
}

// unsetJSON returns body with the key at keys removed, and removed false when
// the path is missing. A path that runs through a non-object is an error.
func unsetJSON(body []byte, keys []string) (out []byte, removed bool, err error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, false, errors.New("not an object")
	}
	if root == nil {
		return nil, false, errors.New("not an object")
	}
	removed, err = delAt(root, keys)
	if err != nil {
		return nil, false, err
	}
	if !removed {
		return nil, false, nil
	}
	out, err = json.Marshal(root)
	return out, true, err
}

func delAt(obj map[string]json.RawMessage, keys []string) (bool, error) {
	key := keys[0]
	if len(keys) == 1 {
		if _, ok := obj[key]; !ok {
			return false, nil
		}
		delete(obj, key)
		return true, nil
	}

	existing, ok := obj[key]
	if !ok {
		return false, nil
	}
	var child map[string]json.RawMessage
	if err := json.Unmarshal(existing, &child); err != nil || child == nil {
		return false, errors.New("not an object")
	}
	removed, err := delAt(child, keys[1:])
	if err != nil || !removed {
		return false, err
	}
	encoded, err := json.Marshal(child)
	if err != nil {
		return false, err
	}
	obj[key] = encoded
	return true, nil
}

// rawJSONOrString is the value `config set` stores: raw is kept when it is
// valid JSON, and otherwise treated as a JSON string.
func rawJSONOrString(raw string) json.RawMessage {
	if json.Valid([]byte(raw)) {
		return json.RawMessage(raw)
	}
	encoded, _ := json.Marshal(raw)
	return encoded
}

// printJSON writes raw as indented JSON on its own line.
func printJSON(w io.Writer, raw json.RawMessage) error {
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return err
	}
	out.WriteByte('\n')
	_, err := w.Write(out.Bytes())
	return err
}
