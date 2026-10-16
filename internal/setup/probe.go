package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// Runner is the seat one probe command runs through: the command's stdout,
// its exit code, and a non-nil err only when it could not run at all.
type Runner interface {
	Run(name string, args ...string) (stdout []byte, exit int, err error)
}

// SetupEnv is both seams setup reads: the agent-install files management the
// install step walks, plus the runner the probe commands run through.
type SetupEnv interface {
	harness.InstallEnv
	Runner
}

// probeDeadline bounds one probe command: a hung CLI must not stall setup.
const probeDeadline = 10 * time.Second

var errProbeTimeout = errors.New("probe command timed out")

// runnerEnv is an install env with the OS runner added. The deadline sits in
// the exec context, so a hung CLI's process dies instead of lingering.
type runnerEnv struct{ harness.InstallEnv }

// Run execs the command under the probe deadline; a hang surfaces as
// errProbeTimeout, and a non-zero exit reports with its code and no error.
func (runnerEnv) Run(name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeDeadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if ctx.Err() != nil {
		return out, -1, errProbeTimeout
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return out, ee.ExitCode(), nil
	}
	return out, 0, err
}

// HarnessStatus is one probed harness: its kind, whether it is on PATH and,
// when it is, whether its CLI is logged in and which models it offers. Login
// carries the login command to name wherever the status is printed; it is
// empty for a harness not on PATH. Efforts and DefaultEffort are what a CLI
// reports per model -- only codex does; the others' efforts are known.
type HarnessStatus struct {
	Harness       string
	OnPath        bool
	LoggedIn      bool
	Models        []string
	Login         string
	Efforts       map[string][]string
	DefaultEffort map[string]string
}

// Inventory is everything a probe produced: the wizard harnesses' statuses
// plus the notes the front end prints as they are. Only the three harnesses
// the wizard can configure get status entries.
type Inventory struct {
	Harnesses []HarnessStatus
	Notes     []string
}

// loginCommands are the login texts the statuses and the not_available
// message name for each wizard harness. agy has no login verb, so its text
// names the run instead.
var loginCommands = map[string]string{
	"claude": "claude auth login",
	"codex":  "codex login",
	"agy":    "run agy once to sign in",
}

// opencodeNote is what opencode's presence earns; a follow-up adds it to the
// wizard.
const opencodeNote = "opencode: not covered by setup yet; configure it with relevo config"

// claudeAliases are the only candidate models claude ever names, because
// claude has no model-list command and the aliases always resolve.
var claudeAliases = []string{"sonnet", "opus", "haiku"}

// Probe runs each harness's status and model commands through the env's
// runner and reports what it found. opencode on PATH becomes a note, and a
// harness not on PATH gets no status at all.
func Probe(env SetupEnv) Inventory {
	var inv Inventory
	var kinds []string
	for _, h := range harness.All() {
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		if h.Kind == "opencode" {
			inv.Notes = append(inv.Notes, opencodeNote)
			continue
		}
		kinds = append(kinds, h.Kind)
	}
	// Each CLI takes seconds to answer, so the harnesses probe side by side;
	// the statuses keep harness order.
	inv.Harnesses = make([]HarnessStatus, len(kinds))
	var wg sync.WaitGroup
	for i, kind := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv.Harnesses[i] = probeHarness(env, kind)
		}()
	}
	wg.Wait()
	return inv
}

// probeTimeoutFor is how long one probe command may take. An env may name a
// shorter value so a test can bound its own clock on a blocking fake.
func probeTimeoutFor(env SetupEnv) time.Duration {
	if t, ok := env.(interface{ ProbeTimeout() time.Duration }); ok {
		return t.ProbeTimeout()
	}
	return probeDeadline
}

// runProbe runs one command through env.Run under the probe timeout. The
// guard keeps a runner that blocks instead of timing out -- a wrapper on a
// remote call, say -- from stalling Probe too.
func runProbe(env SetupEnv, name string, args ...string) (stdout []byte, exit int, err error) {
	type outcome struct {
		out  []byte
		exit int
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		out, exit, err := env.Run(name, args...)
		ch <- outcome{out, exit, err}
	}()
	select {
	case o := <-ch:
		return o.out, o.exit, o.err
	case <-time.After(probeTimeoutFor(env)):
		return nil, -1, errProbeTimeout
	}
}

// probeHarness fills one wizard harness's status from its own commands.
func probeHarness(env SetupEnv, kind string) HarnessStatus {
	s := HarnessStatus{Harness: kind, OnPath: true, Login: loginCommands[kind]}
	switch kind {
	case "claude":
		s.LoggedIn, s.Models = probeClaude(env)
	case "codex":
		var offer []codexEntry
		s.LoggedIn, offer = probeCodex(env)
		s.Models, s.Efforts, s.DefaultEffort = codexOffer(offer)
	case "agy":
		s.LoggedIn, s.Models = probeAgy(env)
	}
	return s
}

// probeClaude asks `claude auth status`: logged in is its JSON's loggedIn
// flag. Anything else -- a bad exit, or stdout that does not parse -- is not
// logged in.
func probeClaude(env SetupEnv) (bool, []string) {
	out, exit, err := runProbe(env, "claude", "auth", "status")
	if err != nil || exit != 0 {
		return false, nil
	}
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if json.Unmarshal(out, &status) != nil || !status.LoggedIn {
		return false, nil
	}
	return true, claudeAliases
}

type codexEntry struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
	Priority   int    `json:"priority"`
	Default    string `json:"default_reasoning_level"`
	Levels     []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
}

// probeCodex asks `codex login status` -- exit 0 is logged in -- and then
// `codex debug models` for the offer.
func probeCodex(env SetupEnv) (bool, []codexEntry) {
	_, exit, err := runProbe(env, "codex", "login", "status")
	if err != nil || exit != 0 {
		return false, nil
	}
	out, exit, err := runProbe(env, "codex", "debug", "models")
	if err != nil || exit != 0 {
		return true, nil
	}
	return true, codexModels(out)
}

// codexOffer splits the listed entries into the status fields: the models in
// order, and each one's efforts and default where codex reports them.
func codexOffer(listed []codexEntry) ([]string, map[string][]string, map[string]string) {
	if len(listed) == 0 {
		return nil, nil, nil
	}
	models := make([]string, len(listed))
	efforts := map[string][]string{}
	defaults := map[string]string{}
	for i, e := range listed {
		models[i] = e.Slug
		for _, l := range e.Levels {
			if l.Effort != "" {
				efforts[e.Slug] = append(efforts[e.Slug], l.Effort)
			}
		}
		if e.Default != "" {
			defaults[e.Slug] = e.Default
		}
	}
	return models, efforts, defaults
}

// codexModels keeps the "list" entries and orders them by ascending
// priority. Stdout malformed, or a models command that says nothing, is no
// offer rather than a failed login.
func codexModels(stdout []byte) []codexEntry {
	var entries []codexEntry
	if json.Unmarshal(stdout, &entries) != nil {
		var wrapped struct {
			Models []codexEntry `json:"models"`
		}
		if json.Unmarshal(stdout, &wrapped) != nil {
			return nil
		}
		entries = wrapped.Models
	}
	var listed []codexEntry
	for _, e := range entries {
		if e.Visibility == "list" && e.Slug != "" {
			listed = append(listed, e)
		}
	}
	sort.Slice(listed, func(i, j int) bool { return listed[i].Priority < listed[j].Priority })
	return listed
}

// probeAgy asks `agy models`: exit 0 is the login signal, because agy needs
// no login verb, and the lines' first tab-separated fields are the offer.
// A failure is asked once more: the command fetches over the network, and a
// one-off failure would tell a logged-in user to sign in.
func probeAgy(env SetupEnv) (bool, []string) {
	for try := 0; try < 2; try++ {
		out, exit, err := runProbe(env, "agy", "models")
		if err == nil && exit == 0 {
			return true, agyModels(out)
		}
	}
	return false, nil
}

// agyModels keeps each line's first tab-separated field, skipping the
// "Fetching ..." progress line and blanks.
func agyModels(stdout []byte) []string {
	var models []string
	for _, line := range strings.Split(string(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Fetching") {
			continue
		}
		if i := strings.IndexByte(line, '\t'); i >= 0 {
			line = line[:i]
		}
		models = append(models, line)
	}
	return models
}
