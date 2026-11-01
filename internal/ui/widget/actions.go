package widget

import (
	"context"
	"os/exec"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// Actions is the cockpit's write seam: one method per key that
// changes something, each one a thin call into the same internal/relevo
// function the CLI verb calls. A nil Actions (serve ui) hides every action
// key and makes one do nothing.
type Actions interface {
	Stop(ctx context.Context, key string) Result
	Done(ctx context.Context, key string) Result
	Unbind(ctx context.Context, key string) Result // always archives
	Gate(ctx context.Context, subject string, forDur time.Duration, reason string) Result
	Ungate(ctx context.Context, subject string) Result
	Shell(key string) (*exec.Cmd, error) // a shell in the binding's tree

	// The acts that need a file (send, retry) or a new binding
	// (bind).
	Send(ctx context.Context, key, planFile string) Result
	Bind(ctx context.Context, in BindInput) Result
	Retry(ctx context.Context, key, candidate string) Result
	Candidates(role string) []string // names, in the role's order
	Chains(ctx context.Context) (relevo.ChainsDoc, error)
	// ChainTrace is one chain's ordered trace rows, read the same way for a
	// local and a server chain: a server chain's rows come from the server.
	ChainTrace(ctx context.Context, name string) (relevo.ChainTraceDoc, error)

	// The config views: the stored config, one validated edit
	// applied and reloaded, and one candidate probed.
	ConfigDoc() (relevo.ConfigDoc, error)                        // the stored config, freshly read
	ApplyConfig(ctx context.Context, e relevo.ConfigEdit) Result // write one edit, then reload this adapter's runtime
	// ServerProbes checks every configured server's reachability and this
	// client's enrollment on it, the same relevo.ProbeServers `relevo config
	// server list` runs. No config store, or a nil adapter, is an empty
	// answer rather than a failure.
	ServerProbes(ctx context.Context) []relevo.ServerProbe
	// The audit view: revisions, their changes in human words, and the roll back.
	ConfigLog() ([]db.RevisionRow, error)                 // every revision, newest first, no snapshots
	ConfigChanges(rev int64) ([]relevo.ChangeLine, error) // one revision's changes, in human words
	RollbackPreview(rev int64) ([]relevo.ChangeLine, error)
	Rollback(ctx context.Context, rev int64) Result
	Probe(ctx context.Context, name string) Result // probe one candidate (spawns its harness)

	// The agents view: definition files, one reset to the copy relevo ships,
	// and the editor.
	AgentFiles(agent string) ([]harness.AgentFile, error)          // the dry-run install state of one agent
	ResetAgentFile(ctx context.Context, kind, agent string) Result // overwrite one definition file
	AgentEditor(path string) (*exec.Cmd, error)                    // the user's editor on one file

	// The round view's artifacts tab: the pager, browser or
	// editor for one artifact. Like Shell and AgentEditor it returns the
	// command without running it, so the cockpit can run the pager and the
	// editor under tea.ExecProcess and start the browser detached.
	OpenArtifact(path, kind string) (*exec.Cmd, error)

	// The workflows view: every workflow this machine can start a chain
	// with, one workflow's source (or, for a shipped one, its definition as
	// JSON), one workflow's step graph, and the two writes that add from a
	// file and remove a saved one. Each write's Result.Refresh reloads this
	// adapter's runtime, the way ApplyConfig's does.
	Workflows() ([]relevo.WorkflowSummary, error)
	WorkflowSource(name string) (string, bool, error)
	WorkflowGraph(name string) ([]relevo.GraphRow, error)
	WorkflowAdd(ctx context.Context, path string, replace bool) Result
	WorkflowRemove(ctx context.Context, name string) Result
	// The edit loop's three reads and its write. WorkflowDefinition is the
	// parsed definition WorkflowEditRound validates the edited source
	// against, and WorkflowActors the actors it may name, both read the way the
	// CLI's edit loop reads them.
	WorkflowDefinition(name string) (workflow.Definition, error)
	WorkflowActors() map[string]workflow.ActorInfo
	WorkflowSave(ctx context.Context, name, source string, def workflow.Definition) Result

	// The sync view (S5). SyncSnapshot is every value the view renders that no
	// key changes. It is read off the update loop and holds no handle, so the
	// view can draw it with the network blackholed.
	//
	// The token is reported only as present or absent. Its value never leaves
	// the function that read it: a snapshot that could render a credential is a
	// snapshot that can leak one into a screen or a log.
	SyncSnapshot() (SyncSnapshot, error)
	// SyncPush and SyncPull are S3's two calls, each bounded the way the CLI
	// verb bounds it.
	SyncPush(ctx context.Context) Result
	SyncPull(ctx context.Context) Result
	// SyncTest asks the remote for its stats without changing a byte here.
	SyncTest(ctx context.Context) Result
	// SyncDisable is S4's turn-off, run whole and in the order its contract
	// fixes.
	SyncDisable(ctx context.Context) Result
	// SyncEditor is the user's editor on the sync section, like AgentEditor
	// and Shell return the command rather than running it.
	SyncEditor() (*exec.Cmd, error)
}

// BindInput is one b key's answers: the new binding's name, the
// candidate it should run ("" means resolve the policy pick), and its feature
// label.
type BindInput struct{ Name, Candidate, Feature string }

// Result is what one action did. Text is what the CLI would print on success
// (StopText, DoneText, ...), multi-line allowed; Err is the failure the CLI
// would have returned; Refresh asks the shell to refetch status now.
type Result struct {
	Text    string
	Err     error
	Refresh bool
}
