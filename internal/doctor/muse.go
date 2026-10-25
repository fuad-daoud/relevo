package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/fuad-daoud/relevo/internal/release"
)

// Muse plugin rows. The single source of truth is `muse plugins inspect
// relevo --json` through the injected reader: a zero exit returns an object
// with record.enabled, record.version and a runtime_capabilities array whose
// hook and mcp_server entries read trusted_enabled once approved; a non-zero
// exit returns {"error":{"code":"unknown-plugin",...}} while still on muse.
// The on-disk settings.json cannot answer this because its
// runtime_capabilities stores only trusted definition hashes.

// MuseInspectFn probes muse's plugin state, shaped exactly like Env.Command
// so cmd/relevo can hand it the Env method itself.
type MuseInspectFn func(ctx context.Context, bin string, args ...string) ([]byte, error)

// Muse's plugin fixes, mirroring the Claude rows' install/enable/update hints
// and adding the approve hint muse requires for hooks and MCP servers.
const (
	musePluginName       = "relevo"
	musePluginInstallFix = "muse plugins marketplace add relevo https://github.com/fuad-daoud/relevo.git && muse plugins install relevo@relevo"
	musePluginEnableFix  = "muse plugins enable relevo"
	musePluginApproveFix = "muse plugins approve relevo"
	musePluginUpdateFix  = "muse plugins marketplace update relevo && muse plugins update relevo && muse plugins approve relevo"

	museStatusTrusted = "trusted_enabled"
)

// museCap is one runtime capability of the inspected plugin: what it is and
// how far muse's review of it has come.
type museCap struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

// musePluginState is what one `muse plugins inspect relevo --json` probe
// proves about the relevo plugin. museErr names the error code the answer
// carried ("" when the answer was a plain installed object).
type musePluginState struct {
	museErr string
	enabled bool
	version string
	caps    []museCap
}

// musePluginChecks reads the plugin state once and reports the three rows
// that mirror the Claude plugin rows. Nothing the probe cannot establish is
// a FAIL: a spawn error or unparseable output leaves every row `not checked`
// OK.
func musePluginChecks(in MasterMindCheckInput) []Check {
	state, err := inspectMusePlugin(in.MuseInspect)
	if err != nil {
		notChecked := "not checked (" + museInspectCommandText + ": " + err.Error() + ")"
		return []Check{
			museUncheckedRow("muse plugin", notChecked),
			museUncheckedRow("muse plugin hook", notChecked),
			museUncheckedRow("muse plugin version", notChecked),
		}
	}
	if state.museErr == "unknown-plugin" {
		return []Check{
			{Name: "muse plugin", Severity: SevFail, Detail: "the relevo plugin is not installed in muse", Fix: musePluginInstallFix},
			museUncheckedRow("muse plugin hook", "not checked ("+museInspectCommandText+": relevo plugin not installed)"),
			museUncheckedRow("muse plugin version", "not checked ("+museInspectCommandText+": relevo plugin not installed)"),
		}
	}
	if state.museErr != "" {
		notChecked := "not checked (" + museInspectCommandText + ": error code " + state.museErr + ")"
		return []Check{
			museUncheckedRow("muse plugin", notChecked),
			museUncheckedRow("muse plugin hook", notChecked),
			museUncheckedRow("muse plugin version", notChecked),
		}
	}
	if !state.enabled {
		return []Check{
			{Name: "muse plugin", Severity: SevFail, Detail: "relevo@relevo is not enabled in muse", Fix: musePluginEnableFix},
			museUncheckedRow("muse plugin hook", "not checked ("+museInspectCommandText+": relevo plugin disabled)"),
			museUncheckedRow("muse plugin version", "not checked ("+museInspectCommandText+": relevo plugin disabled)"),
		}
	}
	return []Check{
		{Name: "muse plugin", Severity: SevOK, Detail: "relevo@relevo enabled in muse"},
		museApprovalRow(state),
		museVersionRow(state, in.Running),
	}
}

// museUncheckedRow is a row for a fact relevo could not establish: always
// OK, never FAIL.
func museUncheckedRow(name, detail string) Check {
	return Check{Name: name, Severity: SevOK, Detail: detail}
}

// museApprovalRow is the hooks/MCP row: FAIL with the approve fix when any
// hook or mcp_server capability sits at another status, naming them. A
// disabled plugin reports no capabilities, which the enable row fails.
func museApprovalRow(state musePluginState) Check {
	var seen []string
	for _, c := range state.caps {
		if (c.Kind == "hook" || c.Kind == "mcp_server") && c.Status != museStatusTrusted {
			seen = append(seen, c.Kind+": "+c.Status)
		}
	}
	if len(seen) > 0 {
		return Check{Name: "muse plugin hook", Severity: SevFail,
			Detail: "muse hook/MCP capabilities not approved: " + strings.Join(seen, ", "),
			Fix:    musePluginApproveFix}
	}
	return Check{Name: "muse plugin hook", Severity: SevOK,
		Detail: "muse hooks and MCP active (all capabilities " + museStatusTrusted + ")"}
}

// museVersionRow compares the installed plugin's version with the running
// binary's, advisory only like the Claude version row.
func museVersionRow(state musePluginState, running string) Check {
	const name = "muse plugin version"

	if running == "" || state.version == "" {
		return museUncheckedRow(name, "not checked")
	}
	plugin, pok := release.ParseVersion(state.version)
	rv, rok := release.ParseVersion(running)
	if !pok || !rok {
		return museUncheckedRow(name, "not checked (relevo is "+running+")")
	}
	// Major.Minor.Patch only, so v0.8.0-15-gd664545 matches 0.8.0.
	if plugin.Major == rv.Major && plugin.Minor == rv.Minor && plugin.Patch == rv.Patch {
		return Check{Name: name, Severity: SevOK, Detail: "plugin " + state.version + " matches relevo"}
	}
	return Check{Name: name, Severity: SevWarn,
		Detail: "plugin " + state.version + ", relevo " + running,
		Fix:    musePluginUpdateFix}
}

// museInspectCommandText is the probe, named in every "not checked" detail
// so a reader can rerun the exact command.
const museInspectCommandText = "muse plugins inspect relevo --json"

// errNoMuseProbe says the caller gave no reader at all, which reads as an
// unchecked row rather than a guessed one.
var errNoMuseProbe = errors.New("no muse inspect reader")

// inspectMusePlugin runs the injected inspect command and decodes both
// shapes: muse answers a missing plugin with exit 1 and an error object, and
// any other command error or unparseable body is not a fact relevo can
// state, so it comes back as an error the rows render as `not checked`.
func inspectMusePlugin(inspect MuseInspectFn) (musePluginState, error) {
	if inspect == nil {
		return musePluginState{}, errNoMuseProbe
	}
	out, err := inspect(context.Background(), "muse", "plugins", "inspect", musePluginName, "--json")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return musePluginState{}, err
		}
	}

	var doc struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		Record *struct {
			Enabled bool   `json:"enabled"`
			Version string `json:"version"`
		} `json:"record"`
		RuntimeCapabilities []museCapability `json:"runtime_capabilities"`
	}
	if json.Unmarshal(out, &doc) != nil {
		return musePluginState{}, fmt.Errorf("%s: unparseable output", museInspectCommandText)
	}

	state := musePluginState{}
	if doc.Error != nil {
		state.museErr = doc.Error.Code
		return state, nil
	}
	if doc.Record == nil {
		return musePluginState{}, fmt.Errorf("%s: installed answer with no record", museInspectCommandText)
	}
	state.enabled = doc.Record.Enabled
	state.version = doc.Record.Version
	for _, c := range doc.RuntimeCapabilities {
		state.caps = append(state.caps, museCap{Kind: c.Candidate.Kind, Status: c.Status})
	}
	return state, nil
}

// museCapability mirrors one runtime_capabilities entry of the inspect
// answer: the candidate's kind sits nested, the status beside it.
type museCapability struct {
	Candidate struct {
		Kind string `json:"kind"`
	} `json:"candidate"`
	Status string `json:"status"`
}
