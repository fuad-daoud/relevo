package relevo

import (
	"log/slog"
	"sort"
	"sync"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The re-render that follows an arriving agents item. A native agent's
// definition lands as a file write, because nothing renders it from anything; a
// source agent's definition is derived from its text and has to be rendered
// again the moment the text changes. Both go through the same call, without
// Force, which is what keeps a definition a person edited: the no-Force outcome
// is keep-differs, the same one refreshRoles gets for the definitions it does
// not force either.
//
// The render reads the config the apply wrote, so it cannot run inside the
// importer's transaction -- it would read the document as it was before. The
// apply therefore queues the agent and the pipeline drains the queue once the
// transaction has committed.

// pendingRenders is the agents one machine database has applied and not yet
// re-rendered, keyed by that handle so two machines in one process -- which is
// what the memfake tests are -- never read each other's work.
var pendingRenders = struct {
	mu    sync.Mutex
	byDoc map[*db.DB]map[string]bool
}{byDoc: map[*db.DB]map[string]bool{}}

// queueRender is the re-render hook a config store bound by sharedDocs carries:
// it records the agent and returns, because the render belongs after the
// commit the apply runs in.
func queueRender(shared *db.DB) func(string) {
	return func(agent string) {
		pendingRenders.mu.Lock()
		defer pendingRenders.mu.Unlock()
		set := pendingRenders.byDoc[shared]
		if set == nil {
			set = map[string]bool{}
			pendingRenders.byDoc[shared] = set
		}
		set[agent] = true
	}
}

// takeRenders takes the agents queued for shared and leaves the queue empty, so
// a render is done once however many items named the same agent.
func takeRenders(shared *db.DB) []string {
	pendingRenders.mu.Lock()
	defer pendingRenders.mu.Unlock()
	set := pendingRenders.byDoc[shared]
	if len(set) == 0 {
		return nil
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	delete(pendingRenders.byDoc, shared)
	return names
}

// agentInstallEnv is the InstallEnv the re-render installs through. It is a
// variable so a test can point the render at a temp home: the render writes
// into the user's home, and a test that cannot say which home is not a test
// this package can run. Production never replaces it.
var agentInstallEnv = AgentInstallEnv

// renderPendingAgents re-renders every agent an apply queued for this machine,
// in name order, and drops the queue whatever it manages. It never fails the
// import it follows: a definition that could not be rendered is one warning,
// exactly as refreshRoles treats one, and the entry stays applied either way.
func renderPendingAgents(shared *db.DB) {
	names := takeRenders(shared)
	if len(names) == 0 {
		return
	}
	cfg := machineConfig(shared)
	env, err := agentInstallEnv()
	if err != nil {
		slog.Warn("agent definitions not re-rendered", "err", err)
		return
	}
	for _, name := range names {
		// No Force: an agent definition a person edited is kept, and doctor
		// names it. Forcing it here would make every arrival a silent
		// overwrite of the machine's own file.
		results, err := InstallCustomAgents(cfg, env, harness.InstallOptions{Role: name})
		if err != nil {
			slog.Warn("agent definition not re-rendered", "agent", name, "err", err)
			continue
		}
		for _, r := range results {
			switch r.Outcome {
			case harness.OutcomeWrote, harness.OutcomeUpdated:
				slog.Info("agent definition re-rendered", "kind", r.Kind, "agent", name, "path", r.Path)
			case harness.OutcomeKeptDiffers:
				slog.Info("agent definition was edited and kept", "kind", r.Kind, "agent", name, "path", r.Path)
			case harness.OutcomeError:
				slog.Warn("agent definition not re-rendered", "kind", r.Kind, "agent", name, "err", r.Err)
			}
		}
	}
}

func init() {
	relevosync.SetFileSettle(renderPendingAgents)
}
