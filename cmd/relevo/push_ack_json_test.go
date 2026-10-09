package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/store"
)

// pushAckMasterMind is the MasterMind id push_ack_env registers and seeds. It
// has the shape delivery.KVClaims.Live requires, since that is the store the
// CLI's runtime builds.
const pushAckMasterMind = "pl_aaaaaaaabbbb"

// pushAckEnv points the state and config roots at fresh temp directories and
// registers the MasterMind the ack belongs to, returning its id. Everything a
// `push --ack` needs beyond the entry itself lives under these roots.
func pushAckEnv(t *testing.T) string {
	t.Helper()
	docsEnv(t)
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	reg := mastermindRegistryAt(t, os.Getenv("XDG_STATE_HOME"))
	rec, err := reg.Create(mastermind.Record{
		ID: pushAckMasterMind, Name: "architect-1", HarnessKind: "claude",
		SessionID: "sess-1", CWD: filepath.Join(root, "mastermind-1"),
	})
	if err != nil {
		t.Fatalf("Create mastermind: %v", err)
	}
	return rec.ID
}

// seedPushAckEntry saves one binding owned by mastermindID carrying a single
// admitted to-mastermind entry at seq 1, and writes the live push claim an ack
// requires. The claim names this test process, so Live answers without any
// holder process being spawned.
func seedPushAckEntry(t *testing.T, name, mastermindID string) {
	t.Helper()
	root, err := store.DefaultRoot()
	if err != nil {
		t.Fatalf("DefaultRoot: %v", err)
	}
	s := store.New(root)
	if err := s.Save(store.Binding{
		Name:         name,
		CWD:          filepath.Join(root, "work", name),
		State:        store.StateActive,
		MasterMindID: mastermindID,
	}); err != nil {
		t.Fatalf("Save %s: %v", name, err)
	}
	if err := s.AppendLog(name, store.LogEntry{
		TS:        time.Now().UTC(),
		Round:     1,
		Direction: store.DirToMasterMind,
		Kind:      store.KindReport,
	}); err != nil {
		t.Fatalf("Append %s: %v", name, err)
	}
	if err := s.AdmitIndex(name, 0); err != nil {
		t.Fatalf("AdmitIndex %s: %v", name, err)
	}

	// The claim is what makes the ack's liveness check pass. It is written
	// straight to the kv rows the CLI's own ClaimStore reads, so no holder
	// process is needed.
	rt, err := newRuntime()
	if err != nil {
		t.Fatalf("newRuntime: %v", err)
	}
	now := rt.Now()
	if err := rt.Channels.Write(delivery.Claim{
		MasterMind: mastermindID,
		PID:        os.Getpid(),
		StartedAt:  now,
		SeenAt:     now,
	}, now); err != nil {
		t.Fatalf("write claim: %v", err)
	}
}

// TestPushAckJSONFreshAndIdempotent is the whole success contract in one test:
// the first `--ack --json` prints one document with already_confirmed false,
// and the retry of the very same ack prints already_confirmed true. The second
// field is the only difference between the two documents.
func TestPushAckJSONFreshAndIdempotent(t *testing.T) {
	id := pushAckEnv(t)
	seedPushAckEntry(t, "webshop", id)

	fresh := decodePushAckDoc(t, runDocs(t, "push", "--mastermind", id, "--ack", "webshop", "1", "--json"))
	if fresh.Binding != "webshop" || fresh.Seq != 1 {
		t.Errorf("document = %+v, want binding webshop seq 1", fresh)
	}
	if fresh.Route != "push" {
		t.Errorf("route = %q, want push", fresh.Route)
	}
	if fresh.AlreadyConfirmed {
		t.Error("already_confirmed = true on the first ack, want false")
	}

	retry := decodePushAckDoc(t, runDocs(t, "push", "--mastermind", id, "--ack", "webshop", "1", "--json"))
	if !retry.AlreadyConfirmed {
		t.Error("already_confirmed = false on the retry, want true")
	}
	if retry.Binding != fresh.Binding || retry.Seq != fresh.Seq || retry.Route != fresh.Route {
		t.Errorf("retry document = %+v, want every field but already_confirmed to match %+v", retry, fresh)
	}
}

// decodePushAckDoc unmarshals one ack document, failing the test if stdout is
// not exactly one JSON object.
func decodePushAckDoc(t *testing.T, stdout []byte) PushAckDoc {
	t.Helper()
	if len(stdout) == 0 || stdout[0] != '{' || !json.Valid(stdout) {
		t.Fatalf("stdout = %q, want exactly one JSON object", stdout)
	}
	var doc PushAckDoc
	if err := json.Unmarshal(stdout, &doc); err != nil {
		t.Fatalf("unmarshal %q: %v", stdout, err)
	}
	return doc
}

// TestPushAckWithoutJSONStaysSilent: the human default is unchanged by --json.
// An ack prints nothing and succeeds, which is what the holder has always
// relied on.
func TestPushAckWithoutJSONStaysSilent(t *testing.T) {
	id := pushAckEnv(t)
	seedPushAckEntry(t, "webshop", id)

	stdout, stderr, err := captureOutput(t, func() error {
		return run([]string{"push", "--mastermind", id, "--ack", "webshop", "1"})
	})
	if err != nil {
		t.Fatalf("push --ack: %v (stderr: %s)", err, stderr)
	}
	if len(stdout) != 0 {
		t.Errorf("stdout = %q, want silence without --json", stdout)
	}
}

// TestPushAckErrorUsesTheJSONEnvelope: a refused ack is data under --json. The
// envelope is report's, so this pins that push's own refusals reach it -- an
// unknown seq is the cheapest one to provoke without a live claim.
func TestPushAckErrorUsesTheJSONEnvelope(t *testing.T) {
	id := pushAckEnv(t)
	seedPushAckEntry(t, "webshop", id)

	err := run([]string{"push", "--mastermind", id, "--ack", "webshop", "99", "--json"})
	if err == nil {
		t.Fatal("push --ack of an unknown seq succeeded, want a refusal")
	}

	var buf bytes.Buffer
	if code := report(&buf, err, true); code != catalog[codePushSeqNotFound].exit {
		t.Errorf("exit = %d, want %d for %s", code, catalog[codePushSeqNotFound].exit, codePushSeqNotFound)
	}

	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(buf.Bytes(), &env); jerr != nil {
		t.Fatalf("envelope %q: %v", buf.String(), jerr)
	}
	if env.Error.Code != string(codePushSeqNotFound) {
		t.Errorf("code = %q, want %q", env.Error.Code, codePushSeqNotFound)
	}
	if env.Error.Message == "" {
		t.Error("message is empty")
	}
}

// TestPushLongFormAcceptsJSON: --json is accepted on the long form too. The
// stream is already NDJSON, so the flag changes nothing about it; what the test
// pins is that the flag is not a usage error there, which is the failure the
// MasterMind's sandbox run found.
func TestPushLongFormAcceptsJSON(t *testing.T) {
	v, rest, err := parsePushArgs(t, []string{"--json", "--mastermind", pushAckMasterMind})
	if err != nil {
		t.Fatalf("parseFlags(--json) = %v, want the flag accepted on the long form", err)
	}
	if !*v.asJSON {
		t.Error("asJSON = false, want the flag to parse")
	}
	// The long form is still the holder: --json must not turn it into an ack.
	if _, _, acking, ackErr := pushAckArgs(v, rest); acking || ackErr != nil {
		t.Errorf("pushAckArgs = acking %v err %v, want the long form", acking, ackErr)
	}
}

// TestPushJSONFlagIsOnTheSurface: the flag reaches the registry row and the
// installer together, so `relevo help --json` and `relevo push -h` cannot
// disagree about it.
func TestPushJSONFlagIsOnTheSurface(t *testing.T) {
	e, ok := registryEntry("push")
	if !ok {
		t.Fatal("no registry entry for push")
	}
	var found bool
	for _, f := range e.Flags {
		if f == "--json" {
			found = true
		}
	}
	if !found {
		t.Errorf("push registry flags %v do not list --json", e.Flags)
	}
	if !strings.Contains(e.Args, "--json") {
		t.Errorf("push registry args %q do not mention --json", e.Args)
	}
	if e.Output == "" {
		t.Error("push registry row names no output document")
	}
}

// TestEveryRegistryVerbTakesJSON is the plan's guard: the guide tells every
// agent that every verb takes --json, so the registry must say so too. It is
// pure -- it reads the table and spawns nothing.
//
// The exemptions below are the verbs whose job is to own a stream or a
// terminal: a server, a daemon, a TUI and the parent verbs that only dispatch
// cannot print a document instead of what they print. They are named here
// rather than skipped silently, so a verb added to this list is a decision a
// reader can see.
func TestEveryRegistryVerbTakesJSON(t *testing.T) {
	// Verbs that are a stream or a terminal, not a document-producing command,
	// plus the parent verbs that only dispatch to children which do.
	exempt := map[string]string{
		"board":                "opens a whiteboard; `board comments`, `board text` take --json",
		"board annotate":       "appends a text element to a scene; no machine read-back",
		"board comment":        "appends a comment; `board comments` takes --json",
		"board promote":        "renames the live board; the scene has no document",
		"board url":            "prints a URL, which is already the machine answer",
		"config edit":          "opens an editor on the document; `config export --json` is the read",
		"config rollback":      "prints the revisions it would restore; not yet converted",
		"config secret":        "parent verb; `config secret set`/`rm`/`list` take --json",
		"config server":        "parent verb; `config server add`/`rm`/`list`/`key` take --json",
		"config workflow":      "parent verb; its children take --json",
		"config workflow add":  "adds a workflow file; `config workflow show --json` is the read",
		"config workflow edit": "edits a workflow file; `config workflow show --json` is the read",
		"config workflow rm":   "removes a workflow file; `config workflow show --json` is the read",
		"daemon":               "is the long-running reconciler",
		"mastermind":           "parent verb; its children take --json",
		"mastermind notice":    "prints the notice text itself",
		"mcp":                  "is an MCP server speaking JSON-RPC over stdio",
		"serve":                "is the remote-builder server (listener + daemon)",
		"serve ui":             "is the web UI",
		"ui":                   "is the cockpit TUI",
	}

	for _, e := range registry {
		if _, skip := exempt[e.Name]; skip {
			continue
		}
		var found bool
		for _, f := range e.Flags {
			if f == "--json" {
				found = true
			}
		}
		if !found {
			t.Errorf("verb %q does not take --json, but the guide promises every verb does", e.Name)
		}
	}
}
