package main

import (
	"encoding/json"
	"flag"
	"io"
	"testing"

	"github.com/fuad-daoud/relevo/internal/config"
)

// The --local routing. The rule itself -- which fields are local, and that a
// local write never reaches the shared layer -- is a rule of the config package
// and is tested there. What a test here has to pin is the part that lives in
// this package: the flag is declared, parsed, and carried to the write that
// decides where the edit lands. No test here opens a database or runs a verb.

// recordingWriter is a config store that records which route a write took.
type recordingWriter struct {
	shared []config.Section
	local  []config.Section
}

func (w *recordingWriter) Put(sec config.Section, _ []byte) ([]string, error) {
	w.shared = append(w.shared, sec)
	return nil, nil
}

func (w *recordingWriter) PutDoc(doc config.Doc) ([]string, error) {
	for _, sec := range config.Sections {
		if _, ok := doc[sec]; ok {
			w.shared = append(w.shared, sec)
		}
	}
	return nil, nil
}

func (w *recordingWriter) PutLocal(doc config.Doc) error {
	for _, sec := range config.Sections {
		if _, ok := doc[sec]; ok {
			w.local = append(w.local, sec)
		}
	}
	return nil
}

func TestLocalFlagSendsAnyEditLocal(t *testing.T) {
	t.Parallel()

	edited := json.RawMessage(`{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/tmp/dir"}`)

	// Every section, not just the machine-field ones: --local is not a
	// shortcut for "a machine field", it is "this machine only", and a caller
	// who asks for it gets it whatever the path was.
	for _, sec := range config.Sections {
		if sec == config.Sync {
			continue
		}
		var local recordingWriter
		if err := writeConfigSection(&local, sec, edited, true); err != nil {
			t.Fatalf("writeConfigSection(%s, local): %v", sec, err)
		}
		if len(local.local) != 1 || local.local[0] != sec {
			t.Errorf("writeConfigSection(%s, local) = local %v, shared %v: want the edit local", sec, local.local, local.shared)
		}
		if len(local.shared) != 0 {
			t.Errorf("writeConfigSection(%s, local) wrote the shared layer %v", sec, local.shared)
		}
	}

	// Without the flag the same edit is a shared write, which is what every
	// existing caller has been doing and must keep doing.
	var shared recordingWriter
	if err := writeConfigSection(&shared, config.Accounts, edited, false); err != nil {
		t.Fatalf("writeConfigSection(accounts, shared): %v", err)
	}
	if len(shared.shared) != 1 || shared.shared[0] != config.Accounts {
		t.Errorf("writeConfigSection(accounts, shared) = shared %v, local %v: want the shared write", shared.shared, shared.local)
	}

	// The editor's whole-document form routes the same way.
	parsed := config.Doc{config.Policy: json.RawMessage(`{"serve":{"max_builders":2}}`)}
	var editedLocal recordingWriter
	if _, err := putEditedDoc(&editedLocal, parsed, true); err != nil {
		t.Fatalf("putEditedDoc(local): %v", err)
	}
	if len(editedLocal.local) != 1 || editedLocal.local[0] != config.Policy {
		t.Errorf("putEditedDoc(local) = local %v, shared %v", editedLocal.local, editedLocal.shared)
	}
}

func TestLocalFlagIsDeclaredOnEveryEditingVerb(t *testing.T) {
	t.Parallel()

	verbs := map[string]func(*flag.FlagSet) *bool{
		"config set":   func(fs *flag.FlagSet) *bool { return configSetFlagSet(fs).local },
		"config unset": func(fs *flag.FlagSet) *bool { return configUnsetFlagSet(fs).local },
		"config edit":  func(fs *flag.FlagSet) *bool { return configEditFlagSet(fs).local },
	}
	for name, field := range verbs {
		fs := flag.NewFlagSet(name, flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		local := field(fs)
		if local == nil {
			t.Fatalf("%s does not declare --local", name)
		}
		if got := fs.Lookup("local").Value.String(); got != "false" {
			t.Errorf("%s: --local defaults to %s, want false", name, got)
		}
		if err := fs.Parse([]string{"--local"}); err != nil {
			t.Fatalf("%s --local: %v", name, err)
		}
		if !*local {
			t.Errorf("%s --local did not parse", name)
		}
	}
}
