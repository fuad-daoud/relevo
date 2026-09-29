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
