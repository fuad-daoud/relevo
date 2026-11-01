package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

type configExportFlagValues struct {
	asJSON *bool
}

// configExportFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configExportFlagSet(fs *flag.FlagSet) *configExportFlagValues {
	v := &configExportFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the configuration document")
	return v
}

// configExport writes the whole-config document to stdout in config.Sections
// order.
func configExport(args []string) error {
	fs := flag.NewFlagSet("relevo config export", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configExportFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return fail(codeUsage, "config export takes no arguments, got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	out, err := exportDoc(rt)
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	_, err = os.Stdout.Write(out)
	return err
}

type configImportFlagValues struct {
	asJSON *bool
	force  *bool
}

// configImportFlagSet exists so the registry's parity test finds exactly one
// installer per verb.
func configImportFlagSet(fs *flag.FlagSet) *configImportFlagValues {
	v := &configImportFlagValues{}
	v.asJSON = fs.Bool("json", false, "print the document the import produced")
	v.force = fs.Bool("force", false, "import candidates even when an actor still names a dropped one")
	return v
}

func configImport(args []string) error {
	return outcomeError(configImportRun(args))
}

func configImportRun(args []string) error {
	fs := flag.NewFlagSet("relevo config import", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := configImportFlagSet(fs)
	asJSON, force := v.asJSON, v.force
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 1 {
		return fail(codeUsage, "config import wants <file|->")
	}

	var data []byte
	var err error
	if rest[0] == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(rest[0])
	}
	if err != nil {
		return err
	}

	doc, err := decodeDoc(data)
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	if body, ok := doc[config.Candidates]; ok {
		if err := guardImportCandidatesDrop(rt, body, *force); err != nil {
			return err
		}
	}
	warnings, err := rt.Config.As("cli", "config import "+rest[0]).PutDoc(doc)
	if err != nil {
		return fail(codeConfigInvalid, "%v", err)
	}
	// Warnings stay on stderr; under --json the same list also rides in the
	// document.
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "relevo: "+w)
	}

	version, err := rt.Config.Version()
	if err != nil {
		return err
	}
	if *asJSON {
		return printDoc(configImportDocOf(importSections(doc), warnings, version))
	}
	return nil
}

// guardImportCandidatesDrop refuses an import whose candidates drop a candidate
// an actor names. The document's own actors section wins over the stored one,
// because PutDoc stores both in one revision.
func guardImportCandidatesDrop(rt relevo.Runtime, candidates json.RawMessage, force bool) error {
	if force {
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
	newDoc[config.Candidates] = candidates
	drops := config.DroppedActorCandidates(oldDoc, newDoc)
	if len(drops) == 0 {
		return nil
	}
	return fail(codeConflict, "%s; pass --force to drop it", drops[0])
}

// importSections names the sections a document carries in config.Sections
// order, so the list reads the same on every run.
func importSections(doc map[config.Section]json.RawMessage) []string {
	out := make([]string, 0, len(doc))
	for _, sec := range config.Sections {
		if _, ok := doc[sec]; ok {
			out = append(out, string(sec))
		}
	}
	return out
}

func exportDoc(rt relevo.Runtime) ([]byte, error) {
	doc := make(map[config.Section]json.RawMessage)
	for _, sec := range config.Sections {
		body, present, err := rt.Config.Body(sec)
		if err != nil {
			return nil, err
		}
		if present {
			doc[sec] = body
		}
	}
	return config.EncodeDoc(doc)
}

// decodeDoc parses a whole-config document into sections; the top level must
// be a JSON object.
func decodeDoc(data []byte) (map[config.Section]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("config document is not a JSON object")
	}
	doc := make(map[config.Section]json.RawMessage, len(raw))
	for name, body := range raw {
		doc[config.Section(name)] = body
	}
	return doc, nil
}

// validateDoc validates every section in doc, refusing unknown section names.
func validateDoc(doc map[config.Section]json.RawMessage) error {
	for sec := range doc {
		if !configSectionKnown(sec) {
			return fmt.Errorf("unknown config section %q", sec)
		}
	}
	for _, sec := range config.Sections {
		body, ok := doc[sec]
		if !ok {
			continue
		}
		if _, err := config.Validate(sec, body); err != nil {
			return err
		}
	}
	return nil
}

// stdinStat is the terminal test's only seam: production stats os.Stdin, and a
// test can replace it.
var stdinStat = os.Stdin.Stat

func stdinIsTerminal() bool {
	info, err := stdinStat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
