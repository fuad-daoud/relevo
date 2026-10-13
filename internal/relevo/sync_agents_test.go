package relevo

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/agentsrc"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// These tests drive an agents change through the real sync pipeline on the
// in-memory log, with the applier the pipeline binds reading a temporary root:
// nothing here binds a document by hand, so a construction site that forgets to
// bind the config document still fails.
//
// The homes move with the machine, so each one sees only its own disk. That is
// what makes the assertions about a file landing mean anything: on b's attempt
// the harness home is b's, and the file a's capture carried arrives there.

// agentEnv is the per-machine filesystem the applier, the capture and the
// render resolve against: a home, a config root and a state root, kept per
// machine tag so pointing at a machine twice is the same machine twice. The
// state root matters as much as the home -- it holds the role manifest, so a
// render that found a fresh one would keep a definition the manifest cannot
// vouch for rather than refresh it.
type agentEnv struct {
	t     *testing.T
	roots map[string]string
}

func newAgentEnv(t *testing.T) *agentEnv {
	t.Helper()
	return &agentEnv{t: t, roots: map[string]string{}}
}

// point makes tag this process's home, config root and state root, and returns
// its home.
func (e *agentEnv) point(tag string) string {
	e.t.Helper()
	home, held := e.roots[tag]
	if !held {
		home = e.t.TempDir()
		e.roots[tag] = home
	}
	e.t.Setenv("HOME", home)
	e.t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	e.t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	return home
}

// writeHomeFile puts a file at a home-relative path, the way a person's editor
// or a harness would.
func writeHomeFile(t *testing.T, home, rel, body string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

const nativeAgentBody = `{"shape":"writer","native":{"claude":{"agent":"my-native"}}}`

// TestAgentsApplyRerendersThatAgent drives one machine's agents section to a
// peer over the steady pipeline: the native agent's harness file travels
// inside the item and is written under the peer's own home, and the source
// agent's rendered definition is written there by the re-render that follows
// the apply.
func TestAgentsApplyRerendersThatAgent(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	env := newAgentEnv(t)
	homeA := env.point("a")
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`}`)
	a.set(t, config.Agents, `{"my-executor":{"source":`+quoteJSON(customSource("my-executor", "claude"))+
		`},"my-native":`+nativeAgentBody+`}`)
	writeHomeFile(t, homeA, ".claude/agents/my-native.md", "a's native definition\n")
	a.attempt(t)

	homeB := env.point("b")
	b.attempt(t)

	// The native agent's file landed under b's own home, with a's bytes.
	landed := filepath.Join(homeB, ".claude", "agents", "my-native.md")
	data, err := os.ReadFile(landed)
	if err != nil {
		t.Fatalf("read %s: %v", landed, err)
	}
	if string(data) != "a's native definition\n" {
		t.Errorf("the landed definition = %q, want a's", data)
	}

	// The source agent's rendered definition landed too, and holds what the
	// renderer makes of the source rather than anything a's disk held.
	rendered := filepath.Join(homeB, ".claude", "agents", "my-executor.md")
	data, err = os.ReadFile(rendered)
	if err != nil {
		t.Fatalf("read %s: %v", rendered, err)
	}
	src, err := agentsrc.Parse([]byte(customSource("my-executor", "claude")))
	if err != nil {
		t.Fatalf("agentsrc.Parse: %v", err)
	}
	want, err := agentsrc.Render(src, "claude")
	if err != nil {
		t.Fatalf("agentsrc.Render: %v", err)
	}
	if string(data) != string(want) {
		t.Errorf("the rendered definition = %q, want %q", data, want)
	}
}

// TestAgentsApplyKeepsAnEditedDefinition pins the hand edit the re-render must
// not overwrite: b already has a native definition of its own, and a's copy
// leaves it alone while the item itself still lands.
func TestAgentsApplyKeepsAnEditedDefinition(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	env := newAgentEnv(t)
	homeA := env.point("a")
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`}`)
	writeHomeFile(t, homeA, ".claude/agents/my-native.md", "a's definition\n")
	a.attempt(t)

	homeB := env.point("b")
	writeHomeFile(t, homeB, ".claude/agents/my-native.md", "what b's person wrote\n")
	b.attempt(t)

	data, err := os.ReadFile(filepath.Join(homeB, ".claude", "agents", "my-native.md"))
	if err != nil {
		t.Fatalf("read the definition: %v", err)
	}
	if string(data) != "what b's person wrote\n" {
		t.Errorf("the definition = %q, want b's own kept", data)
	}
	// The item still landed, so a peer's other agents travel and the next
	// capture exports b's own copy rather than a's.
	loaded, err := b.cfg.Load()
	if err != nil {
		t.Fatalf("Load on B: %v", err)
	}
	if _, ok := loaded.Agents["my-native"]; !ok {
		t.Errorf("B's agents = %v, want the item to have landed", loaded.Agents)
	}
}

// TestHandEditedAgentFileExportsOnTheNextAttempt pins the capture every
// attempt runs: a native definition a person edits on disk, with no config
// write behind it, reaches the peer on the next attempt.
func TestHandEditedAgentFileExportsOnTheNextAttempt(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	env := newAgentEnv(t)
	homeA := env.point("a")
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`}`)
	writeHomeFile(t, homeA, ".claude/agents/my-native.md", "first\n")
	a.attempt(t)
	homeB := env.point("b")
	b.attempt(t)

	landed := filepath.Join(homeB, ".claude", "agents", "my-native.md")
	if data, err := os.ReadFile(landed); err != nil || string(data) != "first\n" {
		t.Fatalf("b's definition = %q, %v; want a's first version", data, err)
	}

	env.point("a")
	writeHomeFile(t, homeA, ".claude/agents/my-native.md", "edited\n")
	a.attempt(t)
	env.point("b")
	b.attempt(t)

	if data, err := os.ReadFile(landed); err != nil || string(data) != "edited\n" {
		t.Errorf("b's definition = %q, %v; want a's hand edit to have exported", data, err)
	}
}

// TestSourceAgentChangeRendersWithoutTouchingNativeFiles pins that a source
// agent's text change re-renders its own definition and leaves every native
// file on the machine exactly as it was.
func TestSourceAgentChangeRendersWithoutTouchingNativeFiles(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)

	env := newAgentEnv(t)
	homeA := env.point("a")
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`,"my-executor":{"source":`+
		quoteJSON(customSource("my-executor", "claude"))+`}}`)
	writeHomeFile(t, homeA, ".claude/agents/my-native.md", "a's native definition\n")
	a.attempt(t)

	homeB := env.point("b")
	b.attempt(t)
	nativeBefore, err := os.ReadFile(filepath.Join(homeB, ".claude", "agents", "my-native.md"))
	if err != nil {
		t.Fatalf("read the native definition: %v", err)
	}

	// The source changes on a, and only the source changes.
	env.point("a")
	a.set(t, config.Agents, `{"my-native":`+nativeAgentBody+`,"my-executor":{"source":`+
		quoteJSON(customSource("my-executor", "claude")+"A second instruction.\n")+`}}`)
	a.attempt(t)

	env.point("b")
	b.attempt(t)

	rendered := filepath.Join(homeB, ".claude", "agents", "my-executor.md")
	data, err := os.ReadFile(rendered)
	if err != nil {
		t.Fatalf("read %s: %v", rendered, err)
	}
	if !containsText(string(data), "A second instruction.") {
		t.Errorf("the rendered definition = %q, want the new source's line", data)
	}
	nativeAfter, err := os.ReadFile(filepath.Join(homeB, ".claude", "agents", "my-native.md"))
	if err != nil {
		t.Fatalf("read the native definition: %v", err)
	}
	if string(nativeAfter) != string(nativeBefore) {
		t.Errorf("the native definition changed from %q to %q on a source-only change",
			nativeBefore, nativeAfter)
	}
}

// TestRenderedAgentEditIsKept pins the other half of "keeps hand edits": a
// rendered definition b's person edited survives the re-render that follows
// an arriving source change, because the re-render runs without Force. With
// Force the arrival would silently overwrite the machine's own file, which is
// the one outcome the whole rule exists to prevent.
func TestRenderedAgentEditIsKept(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	env := newAgentEnv(t)

	env.point("a")
	a.set(t, config.Agents, `{"my-executor":{"source":`+quoteJSON(customSource("my-executor", "claude"))+`}}`)
	a.attempt(t)

	homeB := env.point("b")
	b.attempt(t)
	rendered := filepath.Join(homeB, ".claude", "agents", "my-executor.md")
	writeHomeFile(t, homeB, ".claude/agents/my-executor.md", "what b's person wrote\n")

	// The source changes on a, so a re-render is queued on b.
	env.point("a")
	a.set(t, config.Agents, `{"my-executor":{"source":`+
		quoteJSON(customSource("my-executor", "claude")+"A second instruction.\n")+`}}`)
	a.attempt(t)
	env.point("b")
	b.attempt(t)

	data, err := os.ReadFile(rendered)
	if err != nil {
		t.Fatalf("read %s: %v", rendered, err)
	}
	if string(data) != "what b's person wrote\n" {
		t.Errorf("the rendered definition = %q, want b's own kept", data)
	}
}

// TestPendingRendersArePerMachine pins the queue's key: two machines in one
// process must not read each other's queued agents, or a render would run for
// an agent the other machine applied.
func TestPendingRendersArePerMachine(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	env := newAgentEnv(t)
	env.point("a")
	queueRender(a.mdb)("only-a")
	if got := takeRenders(b.mdb); got != nil {
		t.Errorf("B took %v, want nothing: the queue is per machine", got)
	}
	if got := takeRenders(a.mdb); len(got) != 1 || got[0] != "only-a" {
		t.Errorf("A took %v, want [only-a]", got)
	}
	if got := takeRenders(a.mdb); got != nil {
		t.Errorf("A took %v a second time, want nothing", got)
	}
}

// TestAgentsEntryIsCarriedPerItem pins that an agents item travels under its
// own key, so two agents converge on one entry each.
func TestAgentsEntryIsCarriedPerItem(t *testing.T) {
	log := synclog.NewMemTransport("x")
	a := newCfgMachine(t, log, cfgSyncKey, true)
	b := newCfgMachine(t, log, cfgSyncKey, true)
	env := newAgentEnv(t)
	env.point("a")
	a.set(t, config.Agents, `{"alpha-agent":`+nativeAgentBody+`,"beta-agent":`+nativeAgentBody+`}`)
	a.attempt(t)

	env.point("b")
	b.attempt(t)

	loaded, err := b.cfg.Load()
	if err != nil {
		t.Fatalf("Load on B: %v", err)
	}
	for _, name := range []string{"alpha-agent", "beta-agent"} {
		entry, ok := loaded.Agents[name]
		if !ok {
			t.Errorf("B has no %s; its agents are %v", name, loaded.Agents)
			continue
		}
		if len(entry.Native) != 1 || entry.Native["claude"].Agent != "my-native" {
			t.Errorf("B's %s = %+v, want the claude native row", name, entry)
		}
	}
}

// quoteJSON renders one string as a JSON string, for a body a test builds by
// concatenation.
func quoteJSON(s string) string {
	body, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(body)
}

// containsText reports whether text holds want.
func containsText(text, want string) bool { return strings.Contains(text, want) }
