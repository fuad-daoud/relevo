package main

import "flag"

// verbEntry is one row of the verb registry: what an agent needs to drive a
// verb without reading the usage text. Every field is filled by hand; the
// tests keep the table and the dispatchers in step.
type verbEntry struct {
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Args    string   `json:"args"`
	Flags   []string `json:"flags"`
	Output  string   `json:"output"`
	Exit    []int    `json:"exit"`
	Errors  []string `json:"errors"`
}

// registry is the whole dispatched surface, one entry per verb, in name order
// and with each entry's flags in name order. output is filled only where the
// verb prints a JSON document today; errors lists the catalog codes the verb
// can emit today, which is usage wherever parseFlags can fire.
var registry = []verbEntry{
	{
		Name:    "bind",
		Summary: "bind this MasterMind to a runner over a worktree or an existing tree",
		Args:    "[NAME]",
		Flags: []string{
			"--actor", "--allow-yolo", "--base", "--branch", "--candidate", "--cwd",
			"--feature", "--gate", "--mastermind", "--name", "--no-feature", "--no-gate",
			"--rebind", "--regate", "--resume", "--server", "--ticket", "--tier",
			"--timeout", "--worktree",
		},
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "config",
		Summary: "show the actors, the current pick and the candidates",
		Args:    "[--probe [token...]]",
		Flags:   []string{"--probe"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config agents",
		Summary: "write the starter agent definitions into the harness config",
		Args:    "[--kind <agy|claude|opencode>] [--agent <name>] [--force] [--dry-run]",
		Flags:   []string{"--agent", "--dry-run", "--force", "--kind"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config edit",
		Summary: "edit the configuration document in $EDITOR",
		Args:    "",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config export",
		Summary: "print the whole configuration document",
		Args:    "",
		Flags:   []string{},
		Output:  "json:config document",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config get",
		Summary: "print the JSON value at a path in the configuration",
		Args:    "<section>[.<key>...]",
		Flags:   []string{},
		Output:  "json:the value at the path",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config import",
		Summary: "import a configuration document from a file or stdin",
		Args:    "<file|->",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config init",
		Summary: "write the starter configuration, candidates, policy and actors",
		Args:    "[--force] [--no-agents]",
		Flags:   []string{"--force", "--no-agents"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config log",
		Summary: "list the configuration revisions, or show one",
		Args:    "[-n N] [--rev N] [--json]",
		Flags:   []string{"--json", "--n", "--rev"},
		Output:  "json:one configuration revision and its changes",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config rollback",
		Summary: "restore an earlier configuration revision",
		Args:    "<rev> [--yes] [-m <message>]",
		Flags:   []string{"--m", "--yes"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config secret",
		Summary: "store or forget the typesafe and client.key secrets",
		Args:    "set|rm|list",
		Flags:   []string{},
		Exit:    []int{0, 2},
		Errors:  []string{},
	},
	{
		Name:    "config secret list",
		Summary: "print the stored secret names, never their values",
		Args:    "",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config secret rm",
		Summary: "remove a stored secret",
		Args:    "<typesafe|client.key>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config secret set",
		Summary: "store a secret read from stdin",
		Args:    "<typesafe|client.key>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config server",
		Summary: "this machine's remote-builder identity and its server list",
		Args:    "add|rm|list|key",
		Flags:   []string{},
		Exit:    []int{0, 2},
		Errors:  []string{},
	},
	{
		Name:    "config server add",
		Summary: "record a remote-builder server",
		Args:    "<name> <url> (--fingerprint F | --ca system | --insecure)",
		Flags:   []string{"--ca", "--fingerprint", "--insecure"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config server key",
		Summary: "print this client's id and the enrolment line a server admin needs",
		Args:    "[--enroll-line]",
		Flags:   []string{"--enroll-line"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config server list",
		Summary: "print one row per configured server",
		Args:    "",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config server rm",
		Summary: "forget a configured server",
		Args:    "<name>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config set",
		Summary: "set a configuration section or key to a JSON value",
		Args:    "<section>[.<key>...] <json>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "config unset",
		Summary: "remove a configuration section or key",
		Args:    "<section>[.<key>...]",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "daemon",
		Summary: "run the long-running reconciler",
		Args:    "[--interval D] [--check]",
		Flags:   []string{"--check", "--interval", "--preflight"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "doctor",
		Summary: "preflight check: plugin, daemon, harness binaries, roles, release",
		Args:    "",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "done",
		Summary: "mark a binding done; relaying stops",
		Args:    "[NAME]",
		Flags:   []string{"--name", "--pick"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "gate",
		Summary: "list this machine's gates, gate a provider, or clear one",
		Args:    "[<token>] [--for D] [--reason S] | --clear <provider|token> | --serve",
		Flags:   []string{"--clear", "--for", "--reason", "--serve", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "help",
		Summary: "print the usage text, or the registry document with --json",
		Args:    "[--json [<verb>]]",
		Flags:   []string{"--json"},
		Output:  "json:registry document",
		Exit:    []int{0, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "history",
		Summary: "round history as JSON",
		Args:    "[--binding B] [--since D] [--limit N] [-q QUERY] [--json]",
		Flags: []string{
			"--binding", "--by", "--feature", "--here", "--json", "--limit",
			"--mastermind", "--q", "--rows", "--since", "--ticket",
		},
		Output: "json:[]RoundRow",
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "mastermind",
		Summary: "register this MasterMind, and list, rename or forget records",
		Args:    "init|notice|enable|disable|reset|guide|list|rename|forget",
		Flags:   []string{},
		Exit:    []int{0, 2},
		Errors:  []string{},
	},
	{
		Name:    "mastermind disable",
		Summary: "withdraw consent for this session or repository",
		Args:    "[--repo] [--kind K --session S]",
		Flags:   []string{"--kind", "--repo", "--session"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind enable",
		Summary: "consent this session or repository to relevo",
		Args:    "[--repo] [--kind K --session S]",
		Flags:   []string{"--kind", "--repo", "--session"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind forget",
		Summary: "forget a MasterMind record",
		Args:    "<id|name>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind guide",
		Summary: "print the MasterMind guide, or this session's consent document",
		Args:    "[--cwd DIR] [--kind K --session S] [--json]",
		Flags:   []string{"--cwd", "--json", "--kind", "--session"},
		Output:  "json:the consent document {\"state\",\"text\",\"repo\",\"id\",\"name\"}",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind init",
		Summary: "register this MasterMind, or re-attach an existing record",
		Args:    "[--name N] [--kind K --session S] [--hook claude]",
		Flags:   []string{"--hook", "--kind", "--name", "--session"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind list",
		Summary: "list the MasterMind records",
		Args:    "[--json]",
		Flags:   []string{"--json"},
		Output:  "json:[]a record per MasterMind",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind notice",
		Summary: "the UserPromptSubmit hook that reports a status change",
		Args:    "--hook claude",
		Flags:   []string{"--hook"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind rename",
		Summary: "rename a MasterMind record",
		Args:    "<id|name> <new-name>",
		Flags:   []string{},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mastermind reset",
		Summary: "forget this session's or repository's consent answer",
		Args:    "[--kind K --session S]",
		Flags:   []string{"--kind", "--session"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "mcp",
		Summary: "run an MCP server over stdio for a Claude Code MasterMind pane",
		Args:    "[--mode channel|tools|auto]",
		Flags:   []string{"--interval", "--kind", "--mastermind", "--mode"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "send",
		Summary: "stage a plan file as the current round and start the runner",
		Args:    "--file <plan> [NAME]",
		Flags: []string{
			"--allow-yolo", "--candidate", "--dry-run", "--file", "--force",
			"--name", "--no-verify", "--regate", "--tier", "--verify",
		},
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "serve",
		Summary: "run the remote-builder server (listener + daemon)",
		Args:    "[--listen :7777] [--state <dir>] [--interval 2s]",
		Flags: []string{
			"--insecure-http", "--interval", "--listen", "--max-builders",
			"--max-bundle-bytes", "--state",
		},
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "serve clients",
		Summary: "list the enrolled clients",
		Args:    "[--state <dir>]",
		Flags:   []string{"--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve enroll",
		Summary: "enroll a client's public key",
		Args:    "--label <label> --key \"<ed25519 line>\" [--state <dir>]",
		Flags:   []string{"--key", "--label", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve fingerprint",
		Summary: "print the server certificate's fingerprint",
		Args:    "[--state <dir>]",
		Flags:   []string{"--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve gc",
		Summary: "unbind bindings abandoned for longer than a threshold",
		Args:    "--abandoned <duration> [--dry-run] [--state <dir>]",
		Flags:   []string{"--abandoned", "--dry-run", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve init",
		Summary: "initialise the serve state root and its certificate",
		Args:    "[--host <name>]... [--state <dir>]",
		Flags:   []string{"--host", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve revoke",
		Summary: "revoke an enrolled client",
		Args:    "<id> [--state <dir>]",
		Flags:   []string{"--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve status",
		Summary: "print the census as JSON",
		Args:    "[--json] [--state <dir>]",
		Flags:   []string{"--json", "--state"},
		Output:  "json:census",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve ui",
		Summary: "watch the server's queue in a terminal",
		Args:    "[--state <dir>] [--interval 2s]",
		Flags:   []string{"--interval", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "serve unbind",
		Summary: "unbind one of an owner's bindings on the server host",
		Args:    "--owner <label|id> <name> [--state <dir>] [--force]",
		Flags:   []string{"--force", "--owner", "--state"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "show",
		Summary: "one round's plan, report, diff, drift, gate, findings, log or transcript",
		Args:    "[NAME] [--round N] [--diff [--stat|--anchors]] [--log [--follow --after N]] [--json]",
		Flags: []string{
			"--after", "--anchors", "--artifact", "--artifacts", "--diff", "--drift",
			"--findings", "--follow", "--gate", "--json", "--log", "--output",
			"--owner", "--prompt", "--report", "--round", "--stat", "--state",
			"--transcript",
		},
		Output: "json:ShowResult; --log prints NDJSON events",
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "status",
		Summary: "one row per binding: round, state, live pane status, what is pending",
		Args:    "[--all] [--name NAME] [--line] [--json]",
		Flags:   []string{"--all", "--json", "--line", "--name"},
		Output:  "json:view.Report; --line: json:view.StatusLineDoc",
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "stop",
		Summary: "kill the runner process and close its round unless a report is on disk",
		Args:    "[NAME]",
		Flags:   []string{"--name"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "ui",
		Summary: "the cockpit: :fleet, :rounds [query], :round <binding> [N]",
		Args:    "[:view [args]]",
		Flags:   []string{"--interval"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "unbind",
		Summary: "forget a binding, deleting or archiving its directory",
		Args:    "[NAME] [--done] [--delete] [--dry-run]",
		Flags: []string{
			"--all-masterminds", "--archive", "--delete", "--done", "--dry-run",
			"--mastermind", "--name", "--pick", "--sweep",
		},
		Exit:   []int{0, 1, 2},
		Errors: []string{"usage"},
	},
	{
		Name:    "update",
		Summary: "replace this release binary with the latest release, checksum-verified",
		Args:    "[--check] [--to vX.Y.Z] [--release]",
		Flags:   []string{"--check", "--release", "--to"},
		Exit:    []int{0, 1, 2},
		Errors:  []string{"usage"},
	},
	{
		Name:    "version",
		Summary: "print the relevo version",
		Args:    "",
		Flags:   []string{},
		Exit:    []int{0},
		Errors:  []string{},
	},
	{
		Name:    "wait",
		Summary: "block until a round closes or needs you, then print the pending report",
		Args:    "[NAME] [--any] [--round N] [--peek] [--timeout D]",
		Flags:   []string{"--any", "--name", "--peek", "--round", "--timeout"},
		Exit:    []int{0, 2, 3, 4, 5, 124},
		Errors:  []string{"usage"},
	},
}

// installer adapts a flag installer that hands back the values it parsed into
// to the plain func(*flag.FlagSet) shape the parity test walks this map with.
// Installers that already declare-and-return-nothing are used as they are.
func installer[V any](f func(*flag.FlagSet) V) func(*flag.FlagSet) {
	return func(fs *flag.FlagSet) { f(fs) }
}

// verbFlagSets maps every registry entry to the one function that declares its
// flags, so a test can walk the surface the registry describes. The verbs with
// no flags still map to their own installer: the parity rule is one installer
// per entry, both ways.
var verbFlagSets = map[string]func(*flag.FlagSet){
	"bind":               installer(bindFlagSet),
	"config":             installer(configShowFlagSet),
	"config agents":      installer(agentFlagSet),
	"config edit":        configEditFlagSet,
	"config export":      configExportFlagSet,
	"config get":         configGetFlagSet,
	"config import":      configImportFlagSet,
	"config init":        installer(initFlagSet),
	"config log":         installer(configLogFlagSet),
	"config rollback":    installer(configRollbackFlagSet),
	"config secret":      configSecretFlagSet,
	"config secret list": configSecretListFlagSet,
	"config secret rm":   configSecretRmFlagSet,
	"config secret set":  configSecretSetFlagSet,
	"config server":      configServerFlagSet,
	"config server add":  installer(clientAddServerFlagSet),
	"config server key":  installer(configServerKeyFlagSet),
	"config server list": serversFlagSet,
	"config server rm":   clientRmServerFlagSet,
	"config set":         configSetFlagSet,
	"config unset":       configUnsetFlagSet,
	"daemon":             installer(daemonFlagSet),
	"doctor":             doctorFlagSet,
	"done":               installer(doneFlagSet),
	"gate":               installer(gateFlagSet),
	"help":               installer(helpFlagSet),
	"history":            installer(historyFlagSet),
	"mastermind":         mastermindFlagSet,
	"mastermind disable": installer(mastermindDisableFlagSet),
	"mastermind enable":  installer(mastermindEnableFlagSet),
	"mastermind forget":  mastermindForgetFlagSet,
	"mastermind guide":   installer(mastermindGuideFlagSet),
	"mastermind init":    installer(mastermindInitFlagSet),
	"mastermind list":    installer(mastermindListFlagSet),
	"mastermind notice":  installer(mastermindNoticeFlagSet),
	"mastermind rename":  mastermindRenameFlagSet,
	"mastermind reset":   installer(mastermindResetFlagSet),
	"mcp":                installer(mcpFlagSet),
	"send":               installer(sendFlagSet),
	"serve":              serveRunFlagSet,
	"serve clients":      serveClientsFlagSet,
	"serve enroll":       installer(serveEnrollFlagSet),
	"serve fingerprint":  serveFingerprintFlagSet,
	"serve gc":           installer(serveGCFlagSet),
	"serve init":         installer(serveInitFlagSet),
	"serve revoke":       serveRevokeFlagSet,
	"serve status":       serveStatusFlagSet,
	"serve ui":           installer(serveUIFlagSet),
	"serve unbind":       installer(serveUnbindFlagSet),
	"show":               installer(showFlagSet),
	"status":             installer(statusFlagSet),
	"stop":               installer(stopFlagSet),
	"ui":                 installer(uiFlagSet),
	"unbind":             installer(unbindFlagSet),
	"update":             installer(updateFlagSet),
	"version":            versionFlagSet,
	"wait":               installer(waitFlagSet),
}

// versionFlagSet declares version's flags: none today, and none planned while
// the version line stays the answer. It exists so `help --json` can describe
// every verb, and the parity test demands exactly one installer per entry.
func versionFlagSet(*flag.FlagSet) {}

// registryEntry finds one entry by exact name.
func registryEntry(name string) (verbEntry, bool) {
	for _, e := range registry {
		if e.Name == name {
			return e, true
		}
	}
	return verbEntry{}, false
}

// registryDocument is the machine description of the surface: the registry
// itself plus the error catalog, which is the same table the frame renders
// failures from.
func registryDocument() registryDoc {
	return registryDoc{
		Tool:    "relevo",
		Version: 1,
		Build:   buildVersion(),
		Verbs:   registry,
		Errors:  catalogDocument(),
	}
}

// registryDoc is the shape of the whole `help --json` document.
type registryDoc struct {
	Tool    string                `json:"tool"`
	Version int                   `json:"version"`
	Build   string                `json:"build"`
	Verbs   []verbEntry           `json:"verbs"`
	Errors  map[string]catalogDoc `json:"errors"`
}

// catalogDoc is one catalog row in the document: the exit a code earns and
// the command that usually comes next, empty where there is none.
type catalogDoc struct {
	Exit int    `json:"exit"`
	Next string `json:"next"`
}

// catalogDocument renders the catalog as the document's errors object.
func catalogDocument() map[string]catalogDoc {
	doc := make(map[string]catalogDoc, len(catalog))
	for code, entry := range catalog {
		doc[string(code)] = catalogDoc{Exit: entry.exit, Next: entry.next}
	}
	return doc
}
