package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/serve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// outcomeError classifies a failure out of one of the administrivia write
// paths into the frame's catalog code. It is the admin counterpart to
// writeError: the same "never invent a code" rule, the sentinels the
// mastermind and serve packages export are the only distinctions they make,
// and anything else is internal. An error that is already coded passes
// through untouched.
//
// The classified cause is attached with failWrap, so a caller that probes a
// sentinel (errors.Is(err, mastermind.ErrInUse)) still finds it under the
// code.
func outcomeError(err error) error {
	if err == nil {
		return nil
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return err
	}
	// The help and usage-printed sentinels are control flow, not failures to
	// classify: they must reach report exactly as they are.
	if errors.Is(err, errHelpShown) || errors.Is(err, errUsagePrinted) {
		return err
	}
	switch {
	case errors.Is(err, mastermind.ErrNotFound):
		return failWrap(codeMastermindNotFound, err, "%v", err)
	case errors.Is(err, mastermind.ErrNameTaken):
		return failWrap(codeConflict, err, "%v", err)
	case errors.Is(err, mastermind.ErrInUse):
		return failWrap(codeConflict, err, "%v", err)
	case errors.Is(err, mastermind.ErrInvalid):
		return failWrap(codeUsage, err, "%v", err)
	case errors.Is(err, serve.ErrNoSuchClient):
		return failWrap(codeClientNotFound, err, "%v", err)
	case errors.Is(err, serve.ErrAlreadyEnrolled):
		return failWrap(codeConflict, err, "%v", err)
	case errors.Is(err, serve.ErrTLSExists):
		return failWrap(codeConflict, err, "%v", err)
	case errors.Is(err, store.ErrNotFound):
		return failWrap(codeBindingNotFound, err, "%v", err)
	case errors.Is(err, client.ErrUnreachable):
		return failWrap(codeRemoteUnreachable, err, "%v", err)
	case errors.Is(err, release.ErrOffline):
		return failWrap(codeRemoteUnreachable, err, "%v", err)
	case errors.Is(err, config.ErrInvalidValue):
		// A refused value is the caller's input, not a failure of the store, so
		// it earns the config code rather than internal.
		return failWrap(codeConfigInvalid, err, "%v", err)
	default:
		return fail(codeInternal, "%v", err)
	}
}

// ConfigWriteDoc is `config set`/`config unset --json`: the section the path
// named, the path verbatim, and the configuration version the write left
// behind. version is the store's own counter, not a config revision number.
type ConfigWriteDoc struct {
	Section string `json:"section"`
	Path    string `json:"path"`
	Version int64  `json:"version"`
}

// configWriteDocOf is the config-write document. section is the path's first
// segment; the human line prints nothing, so the document is the whole answer.
func configWriteDocOf(path, section string, version int64) ConfigWriteDoc {
	return ConfigWriteDoc{Section: section, Path: path, Version: version}
}

// ConfigImportDoc is `config import`/`config init --json`: the sections the
// write touched, in config.Sections order, the store's warnings, and the
// configuration version. Sections and Warnings are never nil, so an empty
// answer marshals [] rather than null.
type ConfigImportDoc struct {
	Sections []string `json:"sections"`
	Warnings []string `json:"warnings"`
	Version  int64    `json:"version"`
}

// configImportDocOf is the import/init document. init's warnings are
// document-only: its human lines never printed them.
func configImportDocOf(sections, warnings []string, version int64) ConfigImportDoc {
	if sections == nil {
		sections = []string{}
	}
	if warnings == nil {
		warnings = []string{}
	}
	return ConfigImportDoc{Sections: sections, Warnings: warnings, Version: version}
}

// ConfigAgentInstall is one installed agent definition: the harness kind, the
// role, the path written (or would be), the outcome word, and the failure
// text when the outcome is "error".
type ConfigAgentInstall struct {
	Kind    string `json:"kind"`
	Role    string `json:"role"`
	Path    string `json:"path"`
	Outcome string `json:"outcome"`
	Error   string `json:"error,omitempty"`
}

// ConfigAgentsDoc is `config agents --json`: one row per definition the run
// considered. Installed is never nil.
type ConfigAgentsDoc struct {
	Installed []ConfigAgentInstall `json:"installed"`
}

// configAgentsDocOf maps the harness install results to the document. The same
// results feed the human lines, so the two can never disagree.
func configAgentsDocOf(results []harness.InstallResult) ConfigAgentsDoc {
	installed := make([]ConfigAgentInstall, 0, len(results))
	for _, r := range results {
		installed = append(installed, ConfigAgentInstall{
			Kind:    r.Kind,
			Role:    r.Role,
			Path:    r.Path,
			Outcome: string(r.Outcome),
			Error:   r.Err,
		})
	}
	return ConfigAgentsDoc{Installed: installed}
}

// ConfigServerAddDoc is `config server add --json`.
type ConfigServerAddDoc struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// configServerAddDocOf is the add document.
func configServerAddDocOf(name, url string) ConfigServerAddDoc {
	return ConfigServerAddDoc{Name: name, URL: url}
}

// ConfigServerRmDoc is `config server rm --json`.
type ConfigServerRmDoc struct {
	Name    string `json:"name"`
	Removed bool   `json:"removed"`
}

// configServerRmDocOf is the remove document.
func configServerRmDocOf(name string) ConfigServerRmDoc {
	return ConfigServerRmDoc{Name: name, Removed: true}
}

// ConfigSecretDoc is `config secret set`/`config secret rm --json`: the name
// only, never the value.
type ConfigSecretDoc struct {
	Name string `json:"name"`
}

// configSecretDocOf is the secret document.
func configSecretDocOf(name string) ConfigSecretDoc {
	return ConfigSecretDoc{Name: name}
}

// MasterMindDoc is the mastermind write verbs' document. state is the word the
// verb reached: created/reattached/moved for init and enable, forgotten/
// no_record/disabled for disable, reset for reset, forgotten for forget. repo
// is the repository the answer applied to, empty when the answer was the
// session's own.
type MasterMindDoc struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
	Repo  string `json:"repo"`
}

// mastermindDocOf is the mastermind document for a record the verb has; id and
// name are empty when the verb acted without one.
func mastermindDocOf(id, name, state, repo string) MasterMindDoc {
	return MasterMindDoc{ID: id, Name: name, State: state, Repo: repo}
}

// MasterMindRenameDoc is `mastermind rename --json`: the record's id, the name
// it carries now, and the name it carried before.
type MasterMindRenameDoc struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	OldName string `json:"old_name"`
}

// mastermindRenameDocOf is the rename document.
func mastermindRenameDocOf(id, oldName, newName string) MasterMindRenameDoc {
	return MasterMindRenameDoc{ID: id, Name: newName, OldName: oldName}
}

// ServeInitDoc is `serve init --json`: the state root initialised, the
// certificate fingerprint, and whether this run created it (created is false
// when the root was already initialised, which is what tells the fresh run
// from the re-run).
type ServeInitDoc struct {
	Root        string `json:"root"`
	Fingerprint string `json:"fingerprint"`
	Created     bool   `json:"created"`
}

// serveInitDocOf is the serve-init document.
func serveInitDocOf(root, fingerprint string, created bool) ServeInitDoc {
	return ServeInitDoc{Root: root, Fingerprint: fingerprint, Created: created}
}

// ServeEnrollDoc is `serve enroll --json`.
type ServeEnrollDoc struct {
	Label string `json:"label"`
	ID    string `json:"id"`
}

// serveEnrollDocOf is the enroll document.
func serveEnrollDocOf(label, id string) ServeEnrollDoc {
	return ServeEnrollDoc{Label: label, ID: id}
}

// ServeRevokeDoc is `serve revoke --json`: the id the verb revoked. The human
// default printed nothing, so this document is the verb's only success output.
type ServeRevokeDoc struct {
	ID string `json:"id"`
}

// serveRevokeDocOf is the revoke document.
func serveRevokeDocOf(id string) ServeRevokeDoc {
	return ServeRevokeDoc{ID: id}
}

// ServeGCResult is one `serve gc --json` row: the owner, its label, the
// binding name, when it was last seen as RFC3339, and whether it was archived
// (false on a dry run).
type ServeGCResult struct {
	Owner    string `json:"owner"`
	Label    string `json:"label"`
	Name     string `json:"name"`
	LastSeen string `json:"last_seen"`
	Archived bool   `json:"archived"`
}

// ServeGCDoc is `serve gc --json`. Results is never nil.
type ServeGCDoc struct {
	DryRun  bool            `json:"dry_run"`
	Results []ServeGCResult `json:"results"`
}

// serveGCDocOf is the gc document: the same rows the human lines print.
func serveGCDocOf(dryRun bool, results []serve.GCAbandonedResult) ServeGCDoc {
	rows := make([]ServeGCResult, 0, len(results))
	for _, r := range results {
		rows = append(rows, ServeGCResult{
			Owner:    string(r.Owner),
			Label:    r.Label,
			Name:     r.Name,
			LastSeen: r.LastSeen.UTC().Format(time.RFC3339),
			Archived: r.Archive,
		})
	}
	return ServeGCDoc{DryRun: dryRun, Results: rows}
}

// ServeUnbindDoc is `serve unbind --json`: the owner the binding belonged to,
// then the five fields unbindDocOf already carries for the client-side verb.
type ServeUnbindDoc struct {
	Owner string `json:"owner"`
	UnbindDoc
}

// serveUnbindDocOf is the serve-unbind document; the embedded half is the very
// builder `relevo unbind` uses, so the two renderings cannot drift.
func serveUnbindDocOf(owner, name string, res relevo.UnbindResult) ServeUnbindDoc {
	return ServeUnbindDoc{Owner: owner, UnbindDoc: unbindDocOf(name, res)}
}

// updateDoc is `update`/`update --check --json`: the running version, the
// executable the run resolved, the action word, the target (omitted when the
// decision names none), whether the binary was actually replaced, and the
// decision's one sentence (for the go-install action, the command itself).
//
// kind is not part of the document: --check's human block is rendered from
// this document, and the block names the detected kind beside the version.
type updateDoc struct {
	Running string `json:"running"`
	Exe     string `json:"exe"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Updated bool   `json:"updated"`
	Message string `json:"message"`

	kind release.Kind
}

// updateDocOf is the update document, shared by the plain verb and --check so
// the two shapes cannot disagree.
func updateDocOf(running, exe string, kind release.Kind, dec release.UpdateDecision, updated bool) updateDoc {
	return updateDoc{
		Running: running,
		Exe:     exe,
		Action:  dec.Action.String(),
		Target:  dec.Target,
		Updated: updated,
		Message: dec.Message,
		kind:    kind,
	}
}

// renderUpdateCheck writes `update --check`'s human block from the one
// document, so the block and the JSON cannot disagree.
func renderUpdateCheck(doc updateDoc) {
	fmt.Printf("running  %s (%s)\n", doc.Running, doc.kind)
	fmt.Printf("exe      %s\n", doc.Exe)
	fmt.Printf("action   %s\n", doc.Action)
	fmt.Printf("         %s\n", doc.Message)
}

// outcomeDocFixtures is the literal input for every admin golden: one row per
// document the admin write verbs emit. The builders are pure, so the shape is
// pinned without a state directory, a harness or a network.
func outcomeDocFixtures() []struct {
	golden string
	doc    any
} {
	lastSeen := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	return []struct {
		golden string
		doc    any
	}{
		{"config-set-json", configWriteDocOf("policy.order.builder", "policy", 7)},
		{"config-unset-json", configWriteDocOf("actors", "actors", 8)},
		{"config-import-json", configImportDocOf(
			[]string{"candidates", "policy"}, []string{"prices[0].in is deprecated"}, 9)},
		{"config-init-json", configImportDocOf(
			[]string{"candidates", "actors", "policy"}, nil, 10)},
		{"config-agents-json", configAgentsDocOf([]harness.InstallResult{
			{Kind: "claude", Role: "plan-executor", Path: "~/.claude/agents/plan-executor.md", Outcome: harness.OutcomeWrote},
			{Kind: "opencode", Role: "researcher", Path: "~/.config/opencode/agent/researcher.md", Outcome: harness.OutcomeError, Err: "permission denied"},
		})},
		{"config-server-add-json", configServerAddDocOf("zen", "https://zen.example.test:7777")},
		{"config-server-rm-json", configServerRmDocOf("zen")},
		{"config-secret-set-json", configSecretDocOf("typesafe")},
		{"config-secret-rm-json", configSecretDocOf("client.key")},
		{"mastermind-init-json", mastermindDocOf("pl_aaaaaaaabbbb", "architect-1", "created", "https://github.com/acme/webshop")},
		{"mastermind-enable-json", mastermindDocOf("pl_aaaaaaaabbbb", "architect-1", "reattached", "https://github.com/acme/webshop")},
		{"mastermind-disable-json", mastermindDocOf("pl_aaaaaaaabbbb", "architect-1", "forgotten", "")},
		{"mastermind-reset-json", mastermindDocOf("", "", "reset", "https://github.com/acme/webshop")},
		{"mastermind-rename-json", mastermindRenameDocOf("pl_aaaaaaaabbbb", "architect-1", "reviewer-2")},
		{"mastermind-forget-json", mastermindDocOf("pl_aaaaaaaabbbb", "architect-1", "forgotten", "")},
		{"serve-init-json", serveInitDocOf("/s/serve", "sha256:0f1e2d3c4b5a69788796a5b4c3d2e1f0", true)},
		{"serve-enroll-json", serveEnrollDocOf("alice", "SHA256:2b3c4d5e6f70")},
		{"serve-revoke-json", serveRevokeDocOf("SHA256:2b3c4d5e6f70")},
		{"serve-gc-json", serveGCDocOf(false, []serve.GCAbandonedResult{{
			Owner:    "SHA256:2b3c4d5e6f70",
			Label:    "alice",
			Name:     "webshop",
			LastSeen: lastSeen,
			Archive:  true,
		}})},
		{"serve-unbind-json", serveUnbindDocOf("alice", "webshop", relevo.UnbindResult{
			Archived:        true,
			WorktreeRemoved: "/s/work/webshop",
			ProcessStopped:  4242,
		})},
		{"update-json", updateDocOf("v0.13.0", "/usr/local/bin/relevo", release.KindRelease,
			release.UpdateDecision{Action: release.UpdateReplace, Target: "v0.14.0", Message: "relevo v0.13.0 -> v0.14.0"}, true)},
		{"update-check-json", updateDocOf("(devel)", "/home/u/go/bin/relevo", release.KindGoInstall,
			release.UpdateDecision{Action: release.UpdatePrintGoInstall, Target: "latest", Message: "go install github.com/fuad-daoud/relevo/cmd/relevo@latest"}, false)},
	}
}
