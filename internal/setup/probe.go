package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// Runner is the seam one probe command runs through.
type Runner interface {
	Run(name string, args ...string) (stdout []byte, exit int, err error)
}

// SetupEnv is both seams setup reads: the agent-install files and the
// runner the probe commands run through.
type SetupEnv interface {
	harness.InstallEnv
	Runner
}

// probeDeadline bounds one probe command so a hung CLI cannot stall setup.
const probeDeadline = 10 * time.Second

var errProbeTimeout = errors.New("probe command timed out")

// runnerEnv is an install env with the OS runner added; the deadline sits in
// the exec context, so a hung CLI's process dies instead of lingering.
type runnerEnv struct{ harness.InstallEnv }

// Run execs the command under the probe deadline; a hang surfaces as
// errProbeTimeout.
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
// is empty for a harness not on PATH; Efforts and DefaultEffort are set
// where the status carries them. Catalog is opencode's every runnable
// "provider/model", and its Models the ones picked from it.
type HarnessStatus struct {
	Harness       string
	OnPath        bool
	LoggedIn      bool
	Models        []string
	Login         string
	Efforts       map[string][]string
	DefaultEffort map[string]string
	Catalog       []string
}

// Inventory is everything a probe produced: the statuses of the harnesses
// the wizard can configure plus the notes the front end prints as they are.
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
	"muse":   "muse login",
	"agy":    "run agy once to sign in",
}

// claudeAliases are the only candidate models claude ever names: claude has
// no model-list command and the aliases always resolve.
var claudeAliases = []string{"sonnet", "opus", "haiku"}

// museModels is muse's whole fixed catalog, in default order: muse names no
// model-list command either, so the first entry is the offer's default pick.
var museModels = []string{"muse-spark-1.3", "muse-spark-1.2", "muse-wonder-1.0"}

var museEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

const museDefaultEffort = "high"

// Probe runs each harness's status and model commands through the env's
// runner and reports what it found.
func Probe(env SetupEnv) Inventory {
	var inv Inventory
	var kinds []string
	for _, h := range harness.All() {
		if _, err := env.LookPath(h.Binary); err != nil {
			continue
		}
		kinds = append(kinds, h.Kind)
	}
	// Each CLI takes seconds to answer, so the harnesses probe side by side.
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

// probeTimeoutFor is how long one probe command may take; an env may name a
// shorter value so a test can bound its own clock.
func probeTimeoutFor(env SetupEnv) time.Duration {
	if t, ok := env.(interface{ ProbeTimeout() time.Duration }); ok {
		return t.ProbeTimeout()
	}
	return probeDeadline
}

// getenvFor is how a probe reads one environment variable; an env may inject
// its own values so a test need not touch the process environment.
func getenvFor(env SetupEnv) func(string) string {
	if g, ok := env.(interface{ Getenv(string) string }); ok {
		return g.Getenv
	}
	return os.Getenv
}

// runProbe runs one command through env.Run under the probe timeout; a runner
// that blocks instead of timing out must not stall Probe either.
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

func probeHarness(env SetupEnv, kind string) HarnessStatus {
	s := HarnessStatus{Harness: kind, OnPath: true, Login: loginCommands[kind]}
	switch kind {
	case "claude":
		s.LoggedIn, s.Models = probeClaude(env)
	case "codex":
		var offer []codexEntry
		s.LoggedIn, offer = probeCodex(env)
		s.Models, s.Efforts, s.DefaultEffort = codexOffer(offer)
	case "muse":
		if s.LoggedIn = probeMuse(env); s.LoggedIn {
			s.Models, s.Efforts, s.DefaultEffort = museOffer()
		}
	case "agy":
		s.LoggedIn, s.Models = probeAgy(env)
	case "opencode":
		catalog := probeOpencode(env)
		s.LoggedIn, s.Catalog, s.Login = true, catalog, ""
	}
	return s
}

// probeClaude asks `claude auth status`; anything but its JSON's loggedIn
// flag is not logged in.
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

// probeCodex asks `codex login status`, then `codex debug models` for the offer.
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

// codexOffer splits the listed entries into the status fields.
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

// codexModels keeps the "list" entries, ordered by ascending priority.
// Malformed stdout is no offer rather than a failed login.
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

// museAuthPath is where muse keeps its login file.
func museAuthPath(env SetupEnv) (string, error) {
	if xdg := getenvFor(env)("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "muse", "auth.json"), nil
	}
	return env.HomePath(".config/muse/auth.json")
}

// probeMuse reports whether muse can run a session: META_API_KEY set wins
// outright, else the auth file's presence decides, any read failure but
// not-exist reading as logged in. No muse command runs: this is a local
// check only.
func probeMuse(env SetupEnv) bool {
	if getenvFor(env)("META_API_KEY") != "" {
		return true
	}
	path, err := museAuthPath(env)
	if err != nil {
		return false
	}
	_, err = env.ReadFile(path)
	return !errors.Is(err, fs.ErrNotExist)
}

// museOffer splits muse's fixed catalog into the status fields.
func museOffer() ([]string, map[string][]string, map[string]string) {
	efforts := make(map[string][]string, len(museModels))
	defaults := make(map[string]string, len(museModels))
	for _, m := range museModels {
		efforts[m] = museEfforts
		defaults[m] = museDefaultEffort
	}
	return museModels, efforts, defaults
}

// probeAgy asks `agy models`: exit 0 is the login signal, and the lines'
// first tab-separated fields are the offer. A failure is asked once more:
// the command fetches over the network, and a one-off failure would tell a
// logged-in user to sign in.
func probeAgy(env SetupEnv) (bool, []string) {
	for try := 0; try < 2; try++ {
		out, exit, err := runProbe(env, "agy", "models")
		if err == nil && exit == 0 {
			return true, agyModels(out)
		}
	}
	return false, nil
}

// agyModels keeps each line's first tab-separated field, skipping blanks and
// the "Fetching ..." progress line.
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
